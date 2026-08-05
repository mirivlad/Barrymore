package api

import (
	"net/http"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/learning"
	"github.com/mirivlad/barrymore/internal/skill"
	"github.com/mirivlad/barrymore/internal/thread"
)

// listSkills показывает, что Бэрримор умеет сам.
//
// Снятые с применения умения не прячутся: «этим способом я больше не
// пользуюсь и вот почему» — такое же знание, как и само умение.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	runs, err := s.app.Skills.Runs(r.Context(), 20)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "умения недоступны", err.Error())
		return
	}
	practices, err := s.app.Learning.Practices(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "опыт недоступен", err.Error())
		return
	}
	// Предложения освоить новое выводятся из journal'а применений и потому
	// не хранятся: пока порядок повторяется, предложение есть; перестал
	// повторяться — исчезло само, без чистки списков.
	suggestions, err := s.app.Learning.Suggest(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "предложения недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       s.app.Skills.Skills(),
		"runs":        runs,
		"practices":   practices,
		"suggestions": suggestions,
	})
}

// learnSkill принимает предложенный способ работы.
//
// Умение собирает runtime из шагов уже существующих умений. Владелец
// соглашается — и повторяющийся порядок действий становится одним нажатием.
func (s *Server) learnSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SuggestionID string `json:"suggestion_id"`
		Title        string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}

	suggestions, err := s.app.Learning.Suggest(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "предложения недоступны", err.Error())
		return
	}
	var chosen *learning.Suggestion
	for i := range suggestions {
		if suggestions[i].ID == body.SuggestionID {
			chosen = &suggestions[i]
			break
		}
	}
	if chosen == nil {
		writeProblem(w, http.StatusConflict, "предложение устарело",
			"такой порядок действий больше не повторяется; осваивать нечего")
		return
	}

	known := map[string]skill.Skill{}
	for _, sk := range s.app.Skills.Skills() {
		known[sk.ID] = sk
	}
	composed, err := learning.Compose(*chosen, known)
	if err != nil {
		writeProblem(w, http.StatusConflict, "умение не собрано", err.Error())
		return
	}
	if strings.TrimSpace(body.Title) != "" {
		composed.Title = strings.TrimSpace(body.Title)
	}

	learned, err := s.app.Skills.Learn(r.Context(), composed, event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusConflict, "умение не освоено", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, learned)
}

// applySkill применяет умение и возвращает то, что Бэрримор увидел.
//
// Всё происходит здесь и сейчас: ни внешнего процесса, ни подтверждения,
// ни списания. Подтверждать нечего — умение только читает, и читает лишь то,
// что владелец уже разрешил.
func (s *Server) applySkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target         string `json:"target"`
		ConversationID string `json:"conversation_id"`
		ThreadID       string `json:"thread_id"`
	}
	if !decode(w, r, &body) {
		return
	}

	req := skill.Request{
		SkillID: r.PathValue("id"),
		Target:  strings.TrimSpace(body.Target),
		// Нить берётся из разговора, а не со слов клиента: подсунуть чужую
		// нить браузер не должен.
		ThreadID:       strings.TrimSpace(body.ThreadID),
		ConversationID: strings.TrimSpace(body.ConversationID),
	}
	if req.ConversationID != "" {
		conv, err := s.app.Talk.Get(r.Context(), req.ConversationID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		req.ThreadID = conv.ThreadID
	}

	run, err := s.app.Skills.Apply(r.Context(), req)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	// Увиденное возвращается в разговор своей репликой: владелец спросил
	// в Приёмной и ответ должен получить там же.
	if req.ConversationID != "" {
		if _, err := s.app.Talk.Report(r.Context(), req.ConversationID, run.Report()); err != nil {
			s.log.Error("итог умения не попал в разговор",
				"conversation", req.ConversationID, "error", err)
		}
	}

	// И в нить — но только «где остановились». Цель нити ставили люди,
	// и осмотр каталога не даёт права её переписывать.
	if run.ThreadID != "" && run.Status == skill.StatusDone {
		if _, err := s.app.Threads.SetCanon(r.Context(), run.ThreadID,
			thread.CanonPatch{Situation: &run.Answer},
			thread.CanonFromSkill, "посмотрел сам: "+run.SkillTitle,
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			s.log.Error("состояние нити не обновлено после умения",
				"thread", run.ThreadID, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, run)
}

// retireSkill снимает умение с применения по решению владельца.
func (s *Server) retireSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Why string `json:"why"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.app.Skills.Retire(r.Context(), r.PathValue("id"), body.Why,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "retired"})
}
