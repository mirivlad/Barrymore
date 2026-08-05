package api

import (
	"net/http"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/delegation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/thread"
)

// Здесь живёт сквозной путь: разговор → нить → поручение → подтверждение.
//
// Смысл этих маршрутов в том, чего они не требуют. Ни один не принимает
// текста, который владелец уже видел: цель, причина, критерии и название нити
// берутся из журнала — из того, что Бэрримор действительно сказал. Владелец
// подтверждает, а не перепечатывает.

// getConversation возвращает разговор вместе с нитью, к которой он отнесён.
//
// Одним запросом: главный экран не должен собирать состояние по кускам,
// а владелец — ждать, пока оно сойдётся.
func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	conv, err := s.app.Talk.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := map[string]any{"conversation": conv}
	if conv.ThreadID != "" {
		d, err := s.app.Threads.Detail(r.Context(), conv.ThreadID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		out["thread"] = d

		orders, err := s.app.Delegation.List(r.Context(), "", 100)
		if err == nil {
			related := []delegation.WorkOrder{}
			for _, o := range orders {
				if o.ThreadID == conv.ThreadID {
					related = append(related, o)
				}
			}
			out["orders"] = related
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// lastProposal возвращает последнее предложение Бэрримора в разговоре.
//
// Нужен после перезагрузки страницы: предложение живёт в журнале, а не в
// браузере, и терять из-за обновления вкладки готовое к одному нажатию
// поручение было бы обидно и незачем.
func (s *Server) lastProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.app.Talk.ProposalFor(r.Context(), id, "")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	msgID, err := s.app.Talk.LastProposalMessage(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p, "message_id": msgID})
}

// setConversationThread связывает разговор с нитью или снимает связь.
//
// Бэрримор связывает сам, значит, иногда ошибается. Ручка отмены — не
// украшение: без неё автоматическая связь превращается в необратимую.
func (s *Server) setConversationThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ThreadID string `json:"thread_id"`
		Why      string `json:"why"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	actor := event.Actor{Type: event.ActorPerson}

	var err error
	if body.ThreadID == "" {
		err = s.app.Talk.Detach(r.Context(), id, body.Why, actor)
	} else {
		err = s.app.Talk.Attach(r.Context(), id, body.ThreadID, body.Why, actor)
	}
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.getConversation(w, r)
}

// startThreadFromTalk заводит нить по предложению Бэрримора.
//
// Название, вид и состояние приходят из журнала. Владелец нажимает одну
// кнопку — и это единственное, что от него требуется.
func (s *Server) startThreadFromTalk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
		// Title позволяет поправить название, не отказываясь от предложения.
		Title string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")

	proposal, err := s.app.Talk.ProposalFor(r.Context(), id, body.MessageID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	// Название владельца достаточно само по себе. Требовать, чтобы Бэрримор
	// сперва предложил нить, значило бы запирать владельца, когда тот ошибся:
	// сослался на несуществующую нить и предложить новую забыл.
	p := conversation.NewThreadProposal{Title: body.Title}
	if m := proposal.ThreadMatch; m != nil {
		if p.Title == "" {
			p.Title = m.NewThreadTitle
		}
		p.Kind, p.Why = m.NewThreadKind, m.Why
	}
	if p.Title == "" {
		writeProblem(w, http.StatusConflict, "нить не предлагалась",
			"в этом ходу Бэрримор не предлагал заводить нить; назовите её сами")
		return
	}
	// Состояние нити Бэрримор уже сформулировал — оно годится и для нити,
	// которую владелец назвал по-своему.
	if proposal.ThreadState != nil {
		p.State = *proposal.ThreadState
	}

	th, err := s.app.Talk.StartThread(r.Context(), id, p)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, th)
}

// orderFromTalk оформляет поручение из предложения Бэрримора.
//
// Ничего не запускает: возвращается то же, что и у ручной формы, — поручение
// и запрос подтверждения. Разница только в том, что владельцу не пришлось
// переносить в форму то, что уже сказано.
func (s *Server) orderFromTalk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
		Index     int    `json:"index"`
		// WorkspaceRoot и AllowWrite позволяют поправить существенное, не
		// отказываясь от остального. nil означает «как предложено».
		WorkspaceRoot *string `json:"workspace_root"`
		AllowWrite    *bool   `json:"allow_write"`
		WorkerID      string  `json:"worker_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")

	conv, err := s.app.Talk.Get(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if conv.ThreadID == "" {
		writeProblem(w, http.StatusConflict, "поручение вне нити",
			"разговор пока не отнесён ни к одной нити; поручение без нити повиснет "+
				"без истории и результата")
		return
	}

	proposal, err := s.app.Talk.ProposalFor(r.Context(), id, body.MessageID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if body.Index < 0 || body.Index >= len(proposal.WorkOrders) {
		writeProblem(w, http.StatusNotFound, "предложение не найдено",
			"в этом ходу Бэрримор столько поручений не предлагал")
		return
	}
	wo := proposal.WorkOrders[body.Index]

	root := wo.WorkspaceHint
	if body.WorkspaceRoot != nil {
		root = *body.WorkspaceRoot
	}
	// Политика проверяется до формирования поручения: отказ должен быть
	// понятным, а не всплывать при запуске процесса.
	if err := s.app.Policy.AllowWorkspace(root); err != nil {
		writeProblem(w, http.StatusForbidden, "рабочий каталог не разрешён", err.Error())
		return
	}

	// Умолчание — только чтение. Предложение модели включить запись остаётся
	// предложением: разрешение даёт подтверждение владельца, и в нём запись
	// названа прямо.
	allowWrite := wo.NeedsWrite
	if body.AllowWrite != nil {
		allowWrite = *body.AllowWrite
	}

	p, err := s.app.Delegation.Propose(r.Context(), delegation.ProposeRequest{
		ThreadID: conv.ThreadID, Title: wo.Title, Goal: wo.Goal, Why: wo.Why,
		WorkspaceRoot: root, WorkerID: body.WorkerID,
		AcceptanceCriteria: wo.AcceptanceCriteria,
		AuditOnly:          !allowWrite,
		Actor:              event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "поручение не сформировано", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// setCanon позволяет владельцу поправить состояние нити.
//
// Бэрримор ведёт нить сам, но последнее слово о том, что в ней написано,
// остаётся за владельцем — иначе автоматическое ведение стало бы навязанным.
func (s *Server) setCanon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Goal      *string   `json:"goal"`
		Situation *string   `json:"situation"`
		NextStep  *string   `json:"next_step"`
		Obstacles *[]string `json:"obstacles"`
		Waiting   *[]string `json:"waiting"`
	}
	if !decode(w, r, &body) {
		return
	}
	th, err := s.app.Threads.SetCanon(r.Context(), r.PathValue("id"),
		thread.CanonPatch{
			Goal: body.Goal, Situation: body.Situation, NextStep: body.NextStep,
			Obstacles: body.Obstacles, Waiting: body.Waiting,
		},
		thread.CanonFromPerson, "правка владельца", event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, th)
}

// undoCanon возвращает предыдущее состояние нити.
func (s *Server) undoCanon(w http.ResponseWriter, r *http.Request) {
	th, err := s.app.Threads.UndoCanon(r.Context(), r.PathValue("id"),
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, th)
}
