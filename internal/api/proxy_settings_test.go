package api_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/mirivlad/barrymore/internal/runner"
)

func preserveWorkerProxyEnv(t *testing.T) {
	t.Helper()
	previous, hadPrevious := os.LookupEnv(runner.WorkerProxyEnv)
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv(runner.WorkerProxyEnv, previous)
		} else {
			_ = os.Unsetenv(runner.WorkerProxyEnv)
		}
		runner.CloseWorkerProxyRelays()
	})
}

func TestWorkerProxySettingAppliesWithoutRestart(t *testing.T) {
	preserveWorkerProxyEnv(t)
	_ = os.Unsetenv(runner.WorkerProxyEnv)

	s := newServer(t)

	badCode, _ := s.do(http.MethodPost, "/api/v1/settings/worker-proxy", map[string]any{
		"proxy": "ftp://127.0.0.1:21",
	})
	if badCode != http.StatusBadRequest {
		t.Fatalf("неподдерживаемая схема → %d, ожидалось 400", badCode)
	}

	out := s.mustDo(http.MethodPost, "/api/v1/settings/worker-proxy", map[string]any{
		"proxy": "http://127.0.0.1:12334/",
	}, http.StatusOK)
	if out["worker_proxy"] != "http://127.0.0.1:12334" {
		t.Fatalf("прокси не нормализован: %v", out)
	}
	if got := os.Getenv(runner.WorkerProxyEnv); got != "http://127.0.0.1:12334" {
		t.Fatalf("effective policy = %q", got)
	}

	effective := s.mustDo(http.MethodGet, "/api/v1/settings/worker-proxy", nil, http.StatusOK)
	if effective["worker_proxy"] != "http://127.0.0.1:12334" || effective["overridden"] != false {
		t.Fatalf("GET не показывает действующую политику: %v", effective)
	}

	settings := s.mustDo(http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	saved, _ := settings["settings"].(map[string]any)
	if saved["worker_proxy"] != "http://127.0.0.1:12334" {
		t.Fatalf("настройка не переживёт рестарт: %v", saved)
	}

	out = s.mustDo(http.MethodPost, "/api/v1/settings/worker-proxy", map[string]any{
		"proxy": "",
	}, http.StatusOK)
	if enabled, _ := out["enabled"].(bool); enabled {
		t.Fatalf("прокси не отключён: %v", out)
	}
	if _, ok := os.LookupEnv(runner.WorkerProxyEnv); ok {
		t.Fatal("effective proxy остался после отключения")
	}
}

func TestWorkerProxyGetShowsCommandLineOverride(t *testing.T) {
	preserveWorkerProxyEnv(t)
	if err := os.Setenv(runner.WorkerProxyEnv, "http://127.0.0.1:43111"); err != nil {
		t.Fatal(err)
	}

	// Новый App имеет пустой settings.json, но effective policy уже задана так,
	// как это бывает после запуска barrymored с -worker-proxy. UI обязан
	// показывать реальность, а не пустое сохранённое значение.
	s := newServer(t)
	out := s.mustDo(http.MethodGet, "/api/v1/settings/worker-proxy", nil, http.StatusOK)
	if out["worker_proxy"] != "http://127.0.0.1:43111" {
		t.Fatalf("effective proxy потерян: %v", out)
	}
	if overridden, _ := out["overridden"].(bool); !overridden {
		t.Fatalf("CLI override не отмечен: %v", out)
	}
}
