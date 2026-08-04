// Package store открывает SQLite и даёт транзакционные границы остальному коду.
//
// Разделение пулов: SQLite допускает одного писателя. Отдельный пул записи с
// единственным соединением превращает конкуренцию за запись в очередь внутри
// процесса вместо ошибок SQLITE_BUSY.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB — пара пулов к одному файлу базы.
type DB struct {
	write *sql.DB
	read  *sql.DB
	path  string
}

// Options задаёт параметры открытия.
type Options struct {
	Path string
	// ReadPoolSize — число параллельных читающих соединений.
	ReadPoolSize int
	Logger       *slog.Logger
}

const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)"

// Open открывает базу, применяет миграции и возвращает готовое хранилище.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, errors.New("store: не задан путь к базе")
	}
	if opts.ReadPoolSize <= 0 {
		opts.ReadPoolSize = 4
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return nil, fmt.Errorf("store: каталог базы: %w", err)
	}

	dsn := "file:" + opts.Path + pragmas

	write, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: открытие пула записи: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	if err := write.PingContext(ctx); err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: соединение с базой %q: %w", opts.Path, err)
	}

	if err := Migrate(ctx, write, opts.Path, opts.Logger); err != nil {
		_ = write.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: открытие пула чтения: %w", err)
	}
	read.SetMaxOpenConns(opts.ReadPoolSize)
	read.SetMaxIdleConns(opts.ReadPoolSize)

	return &DB{write: write, read: read, path: opts.Path}, nil
}

// Path возвращает путь к файлу базы.
func (d *DB) Path() string { return d.path }

// Reader — пул для запросов чтения.
func (d *DB) Reader() *sql.DB { return d.read }

// Writer — пул записи. Прямое использование допустимо только для DDL и обслуживания;
// доменные изменения проходят через Tx.
func (d *DB) Writer() *sql.DB { return d.write }

// Close закрывает оба пула.
func (d *DB) Close() error {
	var errs []error
	if err := d.read.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := d.write.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Tx выполняет fn в транзакции записи.
//
// Откат выполняется при ошибке и при панике; паника пробрасывается дальше,
// чтобы дефект не превратился в тихо проглоченную ошибку.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// Integrity выполняет проверку целостности базы.
func (d *DB) Integrity(ctx context.Context) error {
	var result string
	if err := d.read.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("store: integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("store: база повреждена: %s", result)
	}
	return nil
}
