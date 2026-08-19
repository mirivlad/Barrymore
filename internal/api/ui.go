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
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeProblem(w, http.StatusMethodNotAllowed, "метод не поддерживается",
					"для настройки прокси используйте POST")
				return
			}
			s.setWorkerProxy(w, r)
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}
