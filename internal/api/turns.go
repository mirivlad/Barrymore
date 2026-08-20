package api

import (
	"errors"
	"net/http"

	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/conversation"
)

type turnResponse struct {
	conversation.TurnRun
	Progress *conversation.TurnProgress `json:"progress,omitempty"`
}

// sendMessage accepts the owner message and returns before deliberation starts.
// App, not the HTTP request, owns the resulting execution lifetime.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	run, err := s.app.BeginTurn(r.Context(), r.PathValue("id"), body.Text)
	if err != nil {
		s.writeTurnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"turn_id": run.ID,
		"status":  run.Status,
	})
}

func (s *Server) getTurn(w http.ResponseWriter, r *http.Request) {
	run, err := s.app.Talk.TurnRun(r.Context(), r.PathValue("id"), r.PathValue("turn_id"))
	if err != nil {
		s.writeTurnError(w, err)
		return
	}
	s.writeTurn(w, run)
}

func (s *Server) activeTurn(w http.ResponseWriter, r *http.Request) {
	run, err := s.app.Talk.ActiveTurn(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeTurnError(w, err)
		return
	}
	s.writeTurn(w, run)
}

func (s *Server) writeTurn(w http.ResponseWriter, run conversation.TurnRun) {
	response := turnResponse{TurnRun: run}
	if progress, ok := s.app.Talk.Progress().Latest(run.ID); ok {
		response.Progress = &progress
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) writeTurnError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversation.ErrTurnActive):
		writeProblem(w, http.StatusConflict, "ход уже выполняется", err.Error())
	case errors.Is(err, conversation.ErrNoActiveTurn), errors.Is(err, conversation.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "ход не найден", err.Error())
	case errors.Is(err, conversation.ErrNoProvider):
		writeProblem(w, http.StatusServiceUnavailable, "Бэрримор не разговаривает", err.Error())
	case errors.Is(err, app.ErrShuttingDown):
		writeProblem(w, http.StatusServiceUnavailable, "Бэрримор завершает работу", err.Error())
	default:
		writeProblem(w, http.StatusBadRequest, "ход не принят", err.Error())
	}
}
