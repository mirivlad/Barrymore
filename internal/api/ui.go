package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/*
var webFS embed.FS

// ui отдаёт интерфейс оператора и маленькие маршруты, принадлежащие именно
// живой продуктовой поверхности.
//
// Основной versioned API по-прежнему регистрируется в Handler. Небольшие
// front-desk endpoints живут здесь, чтобы этот срез можно было развивать без
// раздувания старого монолитного api.go; контракт URL от этого не меняется.
func (s *Server) ui() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusInternalServerError, "интерфейс недоступен", err.Error())
		})
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/settings/worker-proxy" {
			switch r.Method {
			case http.MethodGet:
				s.getWorkerProxy(w, r)
			case http.MethodPost:
				s.setWorkerProxy(w, r)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeProblem(w, http.StatusMethodNotAllowed, "метод не поддерживается",
					"для настройки прокси используйте GET или POST")
			}
			return
		}

		if r.URL.Path == "/api/v1/desk/ambient" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeProblem(w, http.StatusMethodNotAllowed, "метод не поддерживается",
					"Стол только читает наблюдаемое состояние машины")
				return
			}
			s.deskAmbient(w, r)
			return
		}

		// Explicit feedback belongs to the owner-facing surface. The episode ID
		// is the durable learning unit; message text is never posted back as an
		// authority for what Barrymore said.
		const episodePrefix = "/api/v1/episodes/"
		const feedbackSuffix = "/feedback"
		if strings.HasPrefix(r.URL.Path, episodePrefix) && strings.HasSuffix(r.URL.Path, feedbackSuffix) {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, episodePrefix), feedbackSuffix)
			id = strings.Trim(id, "/")
			if id == "" || strings.Contains(id, "/") {
				writeProblem(w, http.StatusNotFound, "эпизод не найден", "некорректный идентификатор эпизода")
				return
			}
			s.experienceFeedback(w, r, id)
			return
		}

		// Основной app.js большой и давно проверяется E2E. Новые независимые
		// поверхности подключаются маленькими ES-модулями, чтобы изменение одной
		// настройки не требовало переписывать монолитный файл и не увеличивало
		// радиус случайной поломки перед живым запуском.
		if r.URL.Path == "/app.js" && r.Method == http.MethodGet {
			base, err := webFS.ReadFile("web/app.js")
			if err != nil {
				writeProblem(w, http.StatusInternalServerError, "интерфейс недоступен", err.Error())
				return
			}
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(base)
			// Static imports are evaluated before the body of app.js. surface.js
			// therefore installs its hydration guard before loadTalk can restore
			// the previous turn and accidentally open the Desk on entry. feedback.js
			// observes the same message responses without making feedback domain state
			// a browser concern.
			_, _ = w.Write([]byte(
				"\nimport \"/surface.js\";\nimport \"/language.js\";\nimport \"/presence.js\";\nimport \"/desk.js\";\nimport \"/proxy.js\";\nimport \"/feedback.js\";\n"))
			return
		}

		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}
