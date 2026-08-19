package api_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/mirivlad/barrymore/internal/runner"
)

func TestWorkerProxySettingAppliesWithoutRestart(t *testing.T) {
	previous, hadPrevious := os.LookupEnv(runner.WorkerProxyEnv)
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv(runner.WorkerProxyEnv, previous)
		} else {
			_ = os.Unsetenv(runner.WorkerProxyEnv)
		}
		runner.CloseWorkerProxyRelays()
	})
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
