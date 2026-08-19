package api

import (
	"net/http"
	"os"
	"sync"

	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/runner"
)

// workerProxyChangeMu сериализует изменение глобальной сетевой политики.
// Два одновременных клика «сохранить» не должны по очереди останавливать штат
// и оставлять файл настроек в состоянии, не совпадающем с effective policy.
var workerProxyChangeMu sync.Mutex

// setWorkerProxy меняет глобальный маршрут внешнего персонала немедленно.
//
// Семантика намеренно сильнее обычной настройки окружения: прежде чем новый
// маршрут станет действующим, все текущие внешние worker-процессы должны быть
// остановлены. После успешного ответа нет смеси старого proxy, нового proxy и
// direct egress среди работающего персонала.
func (s *Server) setWorkerProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Proxy string `json:"proxy"`
	}
	if !decode(w, r, &body) {
		return
	}

	next, err := runner.NormalizeWorkerProxy(body.Proxy)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "прокси не принят", err.Error())
		return
	}

	workerProxyChangeMu.Lock()
	defer workerProxyChangeMu.Unlock()

	current := os.Getenv(runner.WorkerProxyEnv)
	if current == next {
		// Даже если effective policy уже такая (например, пришла флагом при
		// запуске), сохраняем решение владельца, чтобы оно пережило рестарт.
		if _, err := s.app.Settings.Update(func(cur app.Settings) app.Settings {
			cur.WorkerProxy = next
			return cur
		}); err != nil {
			writeProblem(w, http.StatusInternalServerError, "прокси не сохранён", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"worker_proxy": next,
			"enabled":      next != "",
			"stopped_runs": 0,
			"note":         "сетевая политика персонала уже действовала; настройка сохранена",
		})
		return
	}

	stopped, err := s.app.Delegation.StopForNetworkPolicyChange(
		r.Context(), "изменена сетевая политика персонала")
	if err != nil {
		writeProblem(w, http.StatusConflict, "прокси не переключён", err.Error())
		return
	}

	// Старый маршрут уничтожается до публикации нового. После этого даже
	// оставшийся в файловой системе старый socket path не должен принимать
	// соединения.
	runner.CloseWorkerProxyRelays()

	previous, hadPrevious := os.LookupEnv(runner.WorkerProxyEnv)
	if next == "" {
		_ = os.Unsetenv(runner.WorkerProxyEnv)
	} else if err := os.Setenv(runner.WorkerProxyEnv, next); err != nil {
		writeProblem(w, http.StatusInternalServerError, "прокси не переключён", err.Error())
		return
	}

	if _, err := s.app.Settings.Update(func(cur app.Settings) app.Settings {
		cur.WorkerProxy = next
		return cur
	}); err != nil {
		// Файл — источник выбора после рестарта. Если его не удалось сохранить,
		// runtime policy откатывается, чтобы не возникло расхождения «сейчас одно,
		// после рестарта другое». Работники уже остановлены — это безопасно.
		if hadPrevious {
			_ = os.Setenv(runner.WorkerProxyEnv, previous)
		} else {
			_ = os.Unsetenv(runner.WorkerProxyEnv)
		}
		writeProblem(w, http.StatusInternalServerError, "прокси не сохранён", err.Error())
		return
	}

	note := "прокси внешнего персонала включён; новые worker-процессы смогут выйти только через него"
	if next == "" {
		note = "прокси внешнего персонала отключён; новые worker-процессы используют обычную сетевую политику"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"worker_proxy": next,
		"enabled":      next != "",
		"stopped_runs": stopped,
		"note":         note,
	})
}
