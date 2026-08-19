package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webFS embed.FS

// ui отдаёт интерфейс оператора и маленькие маршруты, принадлежащие именно
// живой поверхности настроек.
//
// Основной versioned API по-прежнему регистрируется в Handler. Proxy-setting
// перехватывается здесь, потому что этот срез должен применяться без
// перезапуска и одновременно обслуживает форму в embedded UI. После
// укрупнения settings API маршрут можно без изменения контракта перенести в
// общий список Handler.
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
			// the previous turn and accidentally open the Desk on entry.
			_, _ = w.Write([]byte("\nimport \"/surface.js\";\nimport \"/proxy.js\";\n"))
			return
		}

		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}
