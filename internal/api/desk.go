package api

import (
	"net/http"

	"github.com/mirivlad/barrymore/internal/skill"
)

// deskAmbient отдаёт только наблюдаемые дешёвые факты о машине для Стола.
// Модель не участвует ни в измерении, ни в форме данных, ни в разметке.
func (s *Server) deskAmbient(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":     "machine",
		"snapshot": skill.SnapshotAmbient(),
	})
}
