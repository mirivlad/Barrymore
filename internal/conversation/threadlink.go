package conversation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/thread"
)

// settleThread решает, к какой нити относится разговор.
//
// Разделение полномочий здесь такое же, как везде: модель предлагает, runtime
// применяет по политике.
//
//   - разговор уже привязан — ничего не меняется. Перепривязка на ходу
//     означала бы, что нить может уехать под владельцем;
//   - предложена существующая нить — Бэрримор связывает сам. Это обратимо
//     и ничего не создаёт;
//   - подходящей не нашлось — он предлагает завести новую, но не заводит.
//     Сущности, появляющиеся молча, засоряют систему быстрее, чем помогают.
//
// Идентификатор проверяется по списку, который runtime сам показал модели.
// Выдуманный идентификатор — не «нить не найдена», а попытка сослаться на то,
// чего не предлагали, и она отклоняется с объяснением.
func (s *Service) settleThread(ctx context.Context, conv *Conversation,
	p Proposal, offered map[string]string) ThreadOutcome {

	out := ThreadOutcome{ThreadID: conv.ThreadID}
	if conv.ThreadID != "" {
		out.Title = offered[conv.ThreadID]
		if out.Title == "" {
			if th, err := s.threads.Get(ctx, conv.ThreadID); err == nil {
				out.Title = th.Title
			}
		}
		return out
	}
	if p.ThreadMatch == nil {
		return out
	}
	m := *p.ThreadMatch

	if id := strings.TrimSpace(m.ThreadID); id != "" {
		title, known := offered[id]
		if !known {
			out.Refused = fmt.Sprintf(
				"Бэрримор сослался на нить %s, которой в списке не было — связь не создана", id)
			s.log.Warn("предложена непредложенная нить", "conversation", conv.ID, "thread", id)
			return out
		}
		if err := s.Attach(ctx, conv.ID, id, m.Why,
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			s.log.Error("разговор не связан с нитью", "conversation", conv.ID, "error", err)
			return out
		}
		conv.ThreadID = id
		return ThreadOutcome{ThreadID: id, Title: title, Attached: true, Why: m.Why}
	}

	if title := strings.TrimSpace(m.NewThreadTitle); title != "" {
		proposed := &NewThreadProposal{Title: title, Kind: m.NewThreadKind, Why: m.Why}
		if p.ThreadState != nil {
			proposed.State = *p.ThreadState
		}
		out.Proposed = proposed
		out.Why = m.Why
	}
	return out
}

// Attach относит разговор к нити.
func (s *Service) Attach(ctx context.Context, conversationID, threadID, why string,
	actor event.Actor) error {

	conv, err := s.Get(ctx, conversationID)
	if err != nil {
		return err
	}
	if _, err := s.threads.Get(ctx, threadID); err != nil {
		return err
	}
	if conv.ThreadID == threadID {
		return nil
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	p := threadLinkPayload{
		ConversationID: conversationID, ThreadID: threadID,
		Previous: conv.ThreadID, Why: why, At: s.clock.Now(),
	}
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: conversationID, ExpectedRevision: event.AnyRevision,
			EventType: EvThreadAttached, Actor: actor, CorrelationID: threadID, Payload: p,
		}); err != nil {
			return err
		}
		return applyThreadLink(ctx, tx, p)
	})
	return err
}

// Detach снимает связь разговора с нитью.
//
// Нужен ровно затем, зачем нужна любая отмена: Бэрримор связывает сам, а
// значит, иногда ошибается, и владелец должен уметь это поправить.
func (s *Service) Detach(ctx context.Context, conversationID, why string, actor event.Actor) error {
	conv, err := s.Get(ctx, conversationID)
	if err != nil {
		return err
	}
	if conv.ThreadID == "" {
		return nil
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	p := threadLinkPayload{
		ConversationID: conversationID, Previous: conv.ThreadID, Why: why, At: s.clock.Now(),
	}
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: conversationID, ExpectedRevision: event.AnyRevision,
			EventType: EvThreadDetached, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyThreadLink(ctx, tx, p)
	})
	return err
}

// StartThread заводит нить по предложению Бэрримора и относит к ней разговор.
//
// Название, вид и состояние приходят из предложения целиком: заставлять
// владельца перепечатывать то, что Бэрримор уже сформулировал, — ровно та
// работа, ради избавления от которой нить и заводится.
func (s *Service) StartThread(ctx context.Context, conversationID string,
	p NewThreadProposal) (thread.Thread, error) {

	if strings.TrimSpace(p.Title) == "" {
		return thread.Thread{}, fmt.Errorf("нить без названия")
	}
	kind := p.Kind
	if kind == "" || thread.ValidateKind(kind) != nil {
		kind = thread.KindConversation
	}

	th, err := s.threads.Create(ctx, thread.CreateRequest{
		Title: p.Title, Kind: kind, Origin: p.Why,
		Actor: event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		return thread.Thread{}, err
	}
	if !p.State.Empty() {
		if th, err = s.threads.SetCanon(ctx, th.ID, patchFromProposal(p.State),
			thread.CanonFromTalk, "нить заведена из разговора",
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			return thread.Thread{}, err
		}
	}
	if err := s.Attach(ctx, conversationID, th.ID, p.Why,
		event.Actor{Type: event.ActorPerson}); err != nil {
		return thread.Thread{}, err
	}
	return th, nil
}

// patchFromProposal переводит предложенное состояние в изменение нити.
//
// Пустые поля не затирают прежние: молчание модели о цели не является
// сообщением, что цели больше нет.
func patchFromProposal(p StateProposal) thread.CanonPatch {
	patch := thread.CanonPatch{}
	if v := strings.TrimSpace(p.Goal); v != "" {
		patch.Goal = &v
	}
	if v := strings.TrimSpace(p.Situation); v != "" {
		patch.Situation = &v
	}
	if v := strings.TrimSpace(p.NextStep); v != "" {
		patch.NextStep = &v
	}
	if len(p.Obstacles) > 0 {
		v := p.Obstacles
		patch.Obstacles = &v
	}
	if len(p.Waiting) > 0 {
		v := p.Waiting
		patch.Waiting = &v
	}
	return patch
}
