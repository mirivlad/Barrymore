package experience_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func TestRecallFindsOldEpisodeAndProcedureByNaturalQuestion(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenDB(t)
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)
	svc := experience.New(db, journal, clk)

	ep, err := svc.Begin(ctx, experience.StartRequest{
		Goal: "Какая разговорная модель сейчас работает у Бэрримора?",
	}, event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddSource(ctx, ep.ID, experience.Source{
		Kind: "runtime", Evidence: "model=Ornith-1.5-9B.gguf", Confidence: 1,
	}, event.Actor{Type: event.ActorRuntime}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Complete(ctx, ep.ID, experience.CompleteRequest{
		Outcome: experience.OutcomeSuccess, Result: "Сейчас ответы формулирует Ornith 1.5 9B",
	}, event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordFeedback(ctx, ep.ID, experience.FeedbackLike, "ответ точный",
		event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	proc, err := svc.SaveProcedure(ctx, experience.Procedure{
		Intent: "research:runtime.provider.inspect",
		Title:  "Узнать, какая разговорная модель работает сейчас",
		Steps: []experience.Step{{
			Capability: "runtime.provider.inspect", Args: json.RawMessage(`{}`),
		}},
		RiskClass: experience.RiskReadOnly,
	}, event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Recall(ctx, "Какая модель у тебя запущена?", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Episodes) == 0 || got.Episodes[0].Episode.ID != ep.ID {
		t.Fatalf("old episode not recalled: %+v", got)
	}
	if len(got.Episodes[0].Feedback) != 1 || got.Episodes[0].Feedback[0].Value != experience.FeedbackLike {
		t.Fatalf("owner feedback lost from recall: %+v", got.Episodes[0])
	}
	foundProcedure := false
	for _, p := range got.Procedures {
		if p.Procedure.ID == proc.ID {
			foundProcedure = true
			break
		}
	}
	if !foundProcedure {
		t.Fatalf("procedure not recalled: %+v", got.Procedures)
	}
}
