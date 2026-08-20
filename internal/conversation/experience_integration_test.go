package conversation_test

import (
	"context"
	"testing"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func TestConversationRegistersDurableExperienceProjections(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenDB(t)
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)

	talk := conversation.New(conversation.Config{
		DB: db, Journal: journal, Clock: clk,
	})
	if talk.Experience() == nil {
		t.Fatal("conversation did not create durable experience service")
	}

	reg := projection.NewRegistry()
	talk.Projections(reg)

	ep, err := talk.Experience().Begin(ctx, experience.StartRequest{
		Goal: "узнать текущую разговорную модель",
	}, event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := talk.Experience().Complete(ctx, ep.ID, experience.CompleteRequest{
		Outcome: experience.OutcomeSuccess,
		Result:  "Ornith",
	}, event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}

	if err := projection.Rebuild(ctx, db, journal, reg); err != nil {
		t.Fatalf("global rebuild does not know experience events: %v", err)
	}
	got, err := talk.Experience().Episode(ctx, ep.ID)
	if err != nil {
		t.Fatalf("episode was lost after global rebuild: %v", err)
	}
	if got.Result != "Ornith" || got.Outcome != experience.OutcomeSuccess {
		t.Fatalf("restored wrong episode: %+v", got)
	}
}
