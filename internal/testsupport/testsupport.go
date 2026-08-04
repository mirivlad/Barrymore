// Package testsupport собирает общую обвязку тестов: временная база,
// управляемые часы и логгер, пишущий в вывод теста.
package testsupport

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/store"
)

// Epoch — фиксированный момент, от которого отсчитывают время все тесты.
var Epoch = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// Logger возвращает логгер, отправляющий записи в t.Log.
func Logger(t testing.TB) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// Discard возвращает молчащий логгер.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}

// OpenDB создаёт временную базу с применёнными миграциями.
func OpenDB(t testing.TB) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{
		Path:   filepath.Join(t.TempDir(), "barrymore.db"),
		Logger: Logger(t),
	})
	if err != nil {
		t.Fatalf("открытие тестовой базы: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// OpenDBAt создаёт базу по конкретному пути (для проверок рестарта).
func OpenDBAt(t testing.TB, path string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: path, Logger: Logger(t)})
	if err != nil {
		t.Fatalf("открытие базы %q: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Clock возвращает управляемые часы, стоящие на Epoch.
func Clock() *clock.Fake { return clock.NewFake(Epoch) }
