package runner

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/worker"
)

func TestEveryNetworkWorkerUsesPolicyGuard(t *testing.T) {
	t.Setenv(WorkerProxyEnv, "")
	plan := worker.RunPlan{
		Argv:    []string{"/bin/true"},
		Sandbox: worker.Sandbox{Network: true},
	}
	argv, profile, err := buildCommand(Capabilities{}, plan, commandOptions{})
	if err != nil {
		t.Fatalf("direct network command: %v", err)
	}
	if !slices.Contains(argv, internalWorkerNetworkGuardMode) {
		t.Fatalf("сетевой worker запущен без generation guard: %#v", argv)
	}
	if !profile.Network || profile.ProxyOnly {
		t.Fatalf("неверный direct profile: %+v", profile)
	}
}

func TestNetworkWorkerCannotBuildDuringPolicyChange(t *testing.T) {
	t.Setenv(WorkerProxyEnv, "")
	end, err := BeginWorkerNetworkPolicyChange()
	if err != nil {
		t.Fatal(err)
	}
	defer end()

	plan := worker.RunPlan{
		Argv:    []string{"/bin/true"},
		Sandbox: worker.Sandbox{Network: true},
	}
	_, _, err = buildCommand(Capabilities{}, plan, commandOptions{})
	if !errors.Is(err, ErrNetworkPolicyChanging) {
		t.Fatalf("во время смены policy получили %v, ожидался ErrNetworkPolicyChanging", err)
	}
}

func TestNetworkGuardRefusesStaleCommandBeforeWorkerStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runWorkerNetworkGuard([]string{path, "old", "--", "/bin/true"})
	if code != 125 {
		t.Fatalf("stale guard exit = %d, want 125", code)
	}
}

func TestNetworkGuardStopsRunningWorkerWhenPolicyChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		done <- runWorkerNetworkGuard([]string{
			path, "one", "--", "/bin/sh", "-c", "while :; do sleep 1; done",
		})
	}()

	// Даём child действительно стартовать, затем меняем policy так же, как API.
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(path, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		// Код зависит от того, успел ли shell обработать TERM до SIGKILL; важен
		// наблюдаемый факт, что guard не оставил worker жить на старой policy.
	case <-time.After(2 * time.Second):
		t.Fatal("worker пережил смену сетевой policy")
	}
}
