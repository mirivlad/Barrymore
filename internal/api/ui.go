package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webFS embed.FS

// ui отдаёт интерфейс оператора.
//
// Это тонкая поверхность для разработки и проверки runtime, а не продуктовый
// интерфейс Бэрримора: он появится отдельным этапом.
func (s *Server) ui() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusInternalServerError, "интерфейс недоступен", err.Error())
		})
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}
