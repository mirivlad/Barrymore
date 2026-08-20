package conversation_test

import (
	"context"
	"testing"

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

	turn, err := h.talk.Send(context.Background(), c.ID, "Ответь без инструментов")
	if err != nil {
		t.Fatal(err)
	}
	if turn.EpisodeID == "" {
		t.Fatal("финальный ответ без Research остался без Episode")
	}
	ep, err := h.talk.Experience().Episode(context.Background(), turn.EpisodeID)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != experience.EpisodeCompleted || ep.Outcome != experience.OutcomeUnknown {
		t.Fatalf("непроверенный ответ получил неверный технический исход: %+v", ep)
	}

	msgs, err := h.talk.Messages(context.Background(), c.ID, 10)
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
}
