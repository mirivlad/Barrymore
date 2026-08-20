package conversation_test

import (
	"context"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/memory"
)

func TestDirectAnswerGetsUnknownEpisodeAndDurableReplyLink(t *testing.T) {
	prov := &scriptedProvider{reply: `{
		"reply":"Стабильный ответ без исследования.",
		"research":{"capability_id":"","args":{},"why":""},
		"thread_match":{"thread_id":"","new_thread_title":"","new_thread_kind":"","why":""},
		"thread_state":{"goal":"","situation":"","next_step":"","obstacles":[],"waiting":[]},
		"memory_candidates":[],"own_actions":[],"work_order_proposals":[],"open_questions":[]
	}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")
	ctx := context.Background()

	turn, err := h.talk.Send(ctx, c.ID, "Ответь без инструментов")
	if err != nil {
		t.Fatal(err)
	}
	if turn.EpisodeID == "" {
		t.Fatal("финальный ответ без Research остался без Episode")
	}
	ep, err := h.talk.Experience().Episode(ctx, turn.EpisodeID)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != experience.EpisodeCompleted || ep.Outcome != experience.OutcomeUnknown {
		t.Fatalf("непроверенный ответ получил неверный технический исход: %+v", ep)
	}

	msgs, err := h.talk.Messages(ctx, c.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("сообщений %d, ожидалось 2", len(msgs))
	}
	if msgs[1].EpisodeID != turn.EpisodeID {
		t.Fatalf("реплика потеряла связь с Episode: message=%q turn=%q", msgs[1].EpisodeID, turn.EpisodeID)
	}
	if msgs[1].Feedback != "" {
		t.Fatalf("отсутствие оценки превратилось в сигнал: %q", msgs[1].Feedback)
	}

	if _, err := h.talk.Experience().RecordFeedback(ctx, turn.EpisodeID,
		experience.FeedbackLike, "", event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	msgs, err = h.talk.Messages(ctx, c.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[1].Feedback != experience.FeedbackLike {
		t.Fatalf("последняя явная оценка не дошла в read model реплики: %q", msgs[1].Feedback)
	}
}
