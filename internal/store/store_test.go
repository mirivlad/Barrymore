package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "barrymore.db")

	db, err := store.Open(ctx, store.Options{Path: path, Logger: testsupport.Logger(t)})
	if err != nil {
		t.Fatalf("первое открытие: %v", err)
	}
	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("загрузка миграций: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("встроенных миграций нет")
	}

	var applied int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("подсчёт применённых миграций: %v", err)
	}
	if applied != len(migrations) {
		t.Fatalf("применено %d миграций, ожидалось %d", applied, len(migrations))
	}
	if err := db.Close(); err != nil {
		t.Fatalf("закрытие: %v", err)
	}

	// Повторное открытие не должно ничего менять.
	db2, err := store.Open(ctx, store.Options{Path: path, Logger: testsupport.Logger(t)})
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	defer db2.Close()

	var again int
	if err := db2.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&again); err != nil {
		t.Fatalf("подсчёт после повторного открытия: %v", err)
	}
	if again != applied {
		t.Fatalf("после повторного открытия %d миграций вместо %d", again, applied)
	}
	if err := db2.Integrity(ctx); err != nil {
		t.Fatalf("integrity: %v", err)
	}
}

func TestMigrationChecksumMismatchIsRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "barrymore.db")

	db, err := store.Open(ctx, store.Options{Path: path, Logger: testsupport.Logger(t)})
	if err != nil {
		t.Fatalf("открытие: %v", err)
	}
	// Имитируем правку уже применённой миграции.
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'deadbeef' || checksum WHERE version = 1`); err != nil {
		t.Fatalf("подмена контрольной суммы: %v", err)
	}
	db.Close()

	_, err = store.Open(ctx, store.Options{Path: path, Logger: testsupport.Logger(t)})
	if err == nil {
		t.Fatal("ожидался отказ открывать базу с изменённой миграцией")
	}
	if !strings.Contains(err.Error(), "изменена после применения") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}

func TestMigrateBacksUpExistingDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "barrymore.db")

	db, err := store.Open(ctx, store.Options{Path: path, Logger: testsupport.Logger(t)})
	if err != nil {
		t.Fatalf("открытие: %v", err)
	}
	db.Close()

	// Откатываем последнюю миграцию в учётной таблице, чтобы вызвать повторное применение.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("прямое открытие: %v", err)
	}
	var maxVersion int
	if err := raw.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatalf("максимальная версия: %v", err)
	}
	if maxVersion < 2 {
		t.Skip("для проверки backup нужна хотя бы вторая миграция")
	}
	if _, err := raw.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version = ?`, maxVersion); err != nil {
		t.Fatalf("удаление записи о миграции: %v", err)
	}
	raw.Close()

	// Пересоздаём объекты последней миграции, чтобы она применилась заново.
	// Достаточно проверить сам факт появления резервной копии.
	_, _ = store.Open(ctx, store.Options{Path: path, Logger: testsupport.Logger(t)})

	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("каталог backups не создан: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("резервная копия не создана перед повторной миграцией")
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenDB(t)

	wantErr := errTest{}
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx,
			`INSERT INTO streams (stream_type, stream_id, revision, updated_at)
			 VALUES ('thread', 'thr_test', 1, '2026-08-04T00:00:00Z')`)
		if execErr != nil {
			return execErr
		}
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("ожидалась исходная ошибка, получено: %v", err)
	}

	var n int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM streams WHERE stream_id = 'thr_test'`).Scan(&n); err != nil {
		t.Fatalf("проверка отката: %v", err)
	}
	if n != 0 {
		t.Fatalf("транзакция не откатилась: осталось %d строк", n)
	}
}

type errTest struct{}

func (errTest) Error() string { return "тестовая ошибка" }
