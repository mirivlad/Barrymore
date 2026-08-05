package api

import (
	"net/http"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/harness"
)

// studyHarness изучает незнакомый инструмент, названный владельцем.
//
// Ничего не подключает: возвращается то, что Бэрримор увидел, и предложение,
// как с этим обращаться. Решать владельцу.
func (s *Server) studyHarness(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}

	survey, draft, err := s.app.Harness.Study(r.Context(), strings.TrimSpace(body.Name))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "инструмент не изучен", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"survey": survey, "draft": draft})
}

// adoptHarness принимает инструмент в штат по решению владельца.
//
// Предложение проверяется заново, по свежей справке: между изучением и
// решением инструмент мог обновиться, а флаг — исчезнуть.
func (s *Server) adoptHarness(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Draft harness.Draft `json:"draft"`
	}
	if !decode(w, r, &body) {
		return
	}

	survey, _, err := s.app.Harness.Study(r.Context(), body.Draft.Name)
	if err != nil {
		writeProblem(w, http.StatusConflict, "инструмент не подтверждён", err.Error())
		return
	}
	m, err := s.app.Harness.Adopt(r.Context(), body.Draft, survey)
	if err != nil {
		writeProblem(w, http.StatusConflict, "исполнитель не подключён", err.Error())
		return
	}

	// Обнаружение сразу же: подключённый исполнитель должен появиться
	// в штате, а не ждать, пока владелец нажмёт «Обнаружить».
	if _, err := s.app.Registry.Discover(r.Context(),
		event.Actor{Type: event.ActorPerson}); err != nil {
		s.log.Error("обнаружение после подключения не прошло", "worker", m.ID, "error", err)
	}
	writeJSON(w, http.StatusCreated, m)
}
