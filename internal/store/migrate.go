package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration — один шаг схемы.
type Migration struct {
	Version int
	Name    string
	SQL     string
	Sum     string // sha256 текста, защищает от правки уже применённой миграции
}

// LoadMigrations читает встроенные миграции и сортирует по версии.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("чтение каталога миграций: %w", err)
	}

	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		idx := strings.IndexByte(name, '_')
		if idx <= 0 {
			return nil, fmt.Errorf("миграция %q: имя должно быть <версия>_<название>.sql", name)
		}
		version, err := strconv.Atoi(name[:idx])
		if err != nil {
			return nil, fmt.Errorf("миграция %q: версия не число: %w", name, err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("версия миграции %d встречается дважды: %s и %s", version, prev, name)
		}
		seen[version] = name

		body, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("чтение %q: %w", name, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Version: version,
			Name:    strings.TrimSuffix(name[idx+1:], ".sql"),
			SQL:     string(body),
			Sum:     hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

const migrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    checksum    TEXT NOT NULL,
    applied_at  TEXT NOT NULL
)`

// Migrate применяет недостающие миграции.
//
// Перед первым изменением уже существующей базы создаётся резервная копия файла:
// docs/11_ENGINEERING_AUTHORITY §5 запрещает необратимую миграцию без backup.
func Migrate(ctx context.Context, db *sql.DB, dbPath string, log *slog.Logger) error {
	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, migrationsTableDDL); err != nil {
		return fmt.Errorf("создание schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	var pending []Migration
	for _, m := range migrations {
		got, ok := applied[m.Version]
		if !ok {
			pending = append(pending, m)
			continue
		}
		if got != m.Sum {
			return fmt.Errorf(
				"миграция %d (%s) изменена после применения: в базе %s, в коде %s; "+
					"применённые миграции править нельзя, добавьте новую",
				m.Version, m.Name, got[:12], m.Sum[:12])
		}
	}
	if len(pending) == 0 {
		return nil
	}

	if len(applied) > 0 && dbPath != "" {
		backup, err := backupDB(ctx, db, dbPath)
		if err != nil {
			return fmt.Errorf("резервная копия перед миграцией: %w", err)
		}
		log.Info("создана резервная копия базы перед миграцией", "path", backup)
	}

	for _, m := range pending {
		if err := applyOne(ctx, db, m); err != nil {
			return err
		}
		log.Info("миграция применена", "version", m.Version, "name", m.Name)
	}
	return nil
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("чтение schema_migrations: %w", err)
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		out[v] = sum
	}
	return out, rows.Err()
}

func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("миграция %d: начало транзакции: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("миграция %d (%s): %w", m.Version, m.Name, err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Sum, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("миграция %d: запись в schema_migrations: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("миграция %d: commit: %w", m.Version, err)
	}
	return nil
}

// backupDB делает согласованную копию через VACUUM INTO.
//
// Простое копирование файла при WAL некорректно: часть зафиксированных данных
// находится в -wal и в копию не попадёт.
func backupDB(ctx context.Context, db *sql.DB, dbPath string) (string, error) {
	dir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s.%s.bak",
		filepath.Base(dbPath), time.Now().UTC().Format("20060102T150405Z")))

	// VACUUM INTO отказывается перезаписывать существующий файл — это нам и нужно.
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return "", fmt.Errorf("VACUUM INTO %q: %w", dst, err)
	}
	return dst, nil
}
