package api

import (
	"net/http"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
)

// experienceFeedback records the owner's explicit judgement of one completed
// episode. Silence never reaches this endpoint and therefore remains neutral.
// Repeating the same value/note is idempotent so a double tap does not become
// two independent learning signals. Changing the judgement appends a new event:
// history is preserved and the newest explicit signal is the current one.
func (s *Server) experienceFeedback(w http.ResponseWriter, r *http.Request, episodeID string) {
	exp := s.app.Talk.Experience()
	if exp == nil {
		writeProblem(w, http.StatusServiceUnavailable, "опыт недоступен",
			"разговорный контур не подключил долговечный опыт")
		return
	}

	items, err := exp.Feedback(r.Context(), episodeID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	switch r.Method {
	case http.MethodGet:
		var current *experience.Feedback
		if len(items) > 0 {
			last := items[len(items)-1]
			current = &last
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "current": current})
		return

	case http.MethodPost:
		var body struct {
			Value string `json:"value"`
			Note  string `json:"note"`
		}
		if !decode(w, r, &body) {
			return
		}
		body.Value = strings.ToLower(strings.TrimSpace(body.Value))
		body.Note = strings.TrimSpace(body.Note)

		if len(items) > 0 {
			last := items[len(items)-1]
			if last.Value == body.Value && last.Note == body.Note {
				writeJSON(w, http.StatusOK, map[string]any{
					"feedback": last, "current": last, "unchanged": true,
				})
				return
			}
		}

		fb, err := exp.RecordFeedback(r.Context(), episodeID, body.Value, body.Note,
			event.Actor{Type: event.ActorPerson, ID: "owner"})
		if err != nil {
			writeProblem(w, http.StatusConflict, "оценка не записана", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"feedback": fb, "current": fb, "unchanged": false,
		})
		return

	default:
		w.Header().Set("Allow", "GET, POST")
		writeProblem(w, http.StatusMethodNotAllowed, "метод не поддерживается",
			"оценку эпизода можно прочитать или записать")
	}
}
