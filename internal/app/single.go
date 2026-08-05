package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockfile — исключительная блокировка каталога данных.
//
// Два Бэрримора на одном каталоге — не безобидное дублирование. SQLite в
// режиме WAL пустит обоих, и дальше они разойдутся тихо: два предиктивных
// контура выдадут по реакции на одно расхождение, два источника поводов —
// по обращению на один факт, и оба будут считать сервер модели своим.
// Владелец увидит систему, которая спорит сама с собой, и не поймёт почему.
//
// Поэтому второй запуск отказывается, называя причину.
type lockfile struct {
	f *os.File
}

// lockDataRoot берёт каталог данных в исключительное пользование.
func lockDataRoot(dataRoot string) (*lockfile, error) {
	path := filepath.Join(dataRoot, "barrymore.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("замок каталога данных: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf(
			"каталог данных %s уже занят другим Бэрримором. Двое на одном журнале "+
				"начнут дублировать реакции и обращения, поэтому второй не запускается. "+
				"Остановите работающий экземпляр или укажите другой -data-root", dataRoot)
	}
	// Номер процесса записывается для человека, а не для программы: искать
	// «кто же его держит» через lsof — занятие не для владельца.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return &lockfile{f: f}, nil
}

// release снимает блокировку.
func (l *lockfile) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
