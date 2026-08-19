package runner

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// internalWorkerNetworkGuardMode — приватный режим того же barrymored.
// Каждый внешний worker, которому нужна сеть, живёт дочерним процессом этого
// guard. Так глобальная смена сетевой политики может остановить даже процесс,
// который стартовал одновременно с нажатием «Применить» и ещё не успел
// появиться в проекции поручений.
const internalWorkerNetworkGuardMode = "__worker-network-guard"

var (
	workerNetworkPolicyChanging atomic.Bool
	workerNetworkEpochMu        sync.Mutex
)

// ErrNetworkPolicyChanging не даёт новому сетевому worker стартовать в
// промежутке между остановкой старого персонала и публикацией новой policy.
var ErrNetworkPolicyChanging = errors.New(
	"сетевая политика персонала меняется; новый сетевой worker пока не запускается")

// beginWorkerNetworkPolicyChange закрывает окно гонки смены proxy.
//
// Сначала помечаем policy changing, затем немедленно меняем generation на
// диске. Worker, который уже работает, увидит новую generation и завершится;
// worker, чья команда была собрана по старой generation, откажется запускать
// полезную работу при старте guard. Новая сборка команды в это время получает
// ErrNetworkPolicyChanging.
func beginWorkerNetworkPolicyChange() error {
	if !workerNetworkPolicyChanging.CompareAndSwap(false, true) {
		return ErrNetworkPolicyChanging
	}
	if _, _, err := rotateWorkerNetworkEpoch(); err != nil {
		workerNetworkPolicyChanging.Store(false)
		return err
	}
	return nil
}

func endWorkerNetworkPolicyChange() {
	workerNetworkPolicyChanging.Store(false)
}

// BeginWorkerNetworkPolicyChange экспортируется только для той части runtime,
// которая атомарно меняет глобальную сетевую настройку. Вернувшаяся функция
// обязана быть вызвана через defer.
func BeginWorkerNetworkPolicyChange() (func(), error) {
	if err := beginWorkerNetworkPolicyChange(); err != nil {
		return nil, err
	}
	return endWorkerNetworkPolicyChange, nil
}

func workerNetworkEpochPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("каталог сетевой политики персонала не определён: %w", err)
	}
	dir := filepath.Join(base, "barrymore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("каталог сетевой политики персонала: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("права каталога сетевой политики персонала: %w", err)
	}
	return filepath.Join(dir, "worker-network-policy"), nil
}

// workerNetworkEpochSnapshot возвращает стабильную generation текущей policy.
// Первый запуск создаёт её; последующие только читают.
func workerNetworkEpochSnapshot() (path, epoch string, err error) {
	workerNetworkEpochMu.Lock()
	defer workerNetworkEpochMu.Unlock()

	path, err = workerNetworkEpochPath()
	if err != nil {
		return "", "", err
	}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		epoch = strings.TrimSpace(string(data))
		if epoch != "" {
			return path, epoch, nil
		}
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("чтение generation сетевой политики: %w", readErr)
	}
	return writeWorkerNetworkEpochLocked(path)
}

func rotateWorkerNetworkEpoch() (path, epoch string, err error) {
	workerNetworkEpochMu.Lock()
	defer workerNetworkEpochMu.Unlock()
	path, err = workerNetworkEpochPath()
	if err != nil {
		return "", "", err
	}
	return writeWorkerNetworkEpochLocked(path)
}

func writeWorkerNetworkEpochLocked(path string) (string, string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generation сетевой политики не создана: %w", err)
	}
	epoch := hex.EncodeToString(buf)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(epoch+"\n"), 0o600); err != nil {
		return "", "", fmt.Errorf("generation сетевой политики не записана: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", "", fmt.Errorf("generation сетевой политики не опубликована: %w", err)
	}
	return path, epoch, nil
}

func readWorkerNetworkEpoch(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// runWorkerNetworkGuard запускает полезный worker только пока generation на
// диске совпадает с той, при которой была собрана команда.
//
// Guard не маршрутизирует сеть и не знает адрес proxy. Его единственная роль —
// сделать смену глобальной policy наблюдаемой уже запущенными процессами.
func runWorkerNetworkGuard(args []string) int {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "barrymore worker network guard: не хватает аргументов")
		return 2
	}
	policyPath := args[0]
	expected := args[1]
	sep := -1
	for i := 2; i < len(args); i++ {
		if args[i] == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "barrymore worker network guard: не задан worker после --")
		return 2
	}
	if readWorkerNetworkEpoch(policyPath) != expected {
		fmt.Fprintln(os.Stderr, "barrymore worker network guard: сетевая политика изменилась до запуска")
		return 125
	}

	workerArgv := args[sep+1:]
	cmd := exec.Command(workerArgv[0], workerArgv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	// Своя process group позволяет guard остановить не только CLI, но и его
	// дочерние процессы, если они уже успели появиться.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "barrymore worker network guard: worker не запущен:", err)
		return 126
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return childExitCode(err)
		case <-ticker.C:
			if readWorkerNetworkEpoch(policyPath) == expected {
				continue
			}
			// Policy поменялась. Сначала даём CLI коротко убрать временные файлы,
			// затем гарантированно гасим всю его process group. Новый маршрут этому
			// процессу никогда не выдаётся.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case err := <-done:
				return childExitCode(err)
			case <-time.After(500 * time.Millisecond):
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return childExitCode(<-done)
		}
	}
}

func childExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 126
}
