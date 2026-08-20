package conversation_test

import (
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/conversation"
)

func TestProgressBrokerKeepsLatestAndCoalescesSlowSubscriber(t *testing.T) {
	broker := conversation.NewProgressBroker()
	updates, closeSubscription := broker.Subscribe()
	defer closeSubscription()

	first := conversation.TurnProgress{TurnID: "trn_1", ConversationID: "conv_1", Stage: conversation.StageRecall}
	latest := conversation.TurnProgress{
		TurnID: "trn_1", ConversationID: "conv_1", Stage: conversation.StageProviderGeneration,
		OutputTokens: 12, Approximate: true,
	}
	broker.Publish(first)
	broker.Publish(latest)

	select {
	case got := <-updates:
		if got != latest {
			t.Fatalf("subscriber got %+v, want %+v", got, latest)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive latest progress")
	}
	got, ok := broker.Latest("trn_1")
	if !ok || got != latest {
		t.Fatalf("latest=%+v ok=%v", got, ok)
	}
}

func TestProgressBrokerForgetAndUnsubscribe(t *testing.T) {
	broker := conversation.NewProgressBroker()
	updates, closeSubscription := broker.Subscribe()
	closeSubscription()
	closeSubscription()

	broker.Publish(conversation.TurnProgress{TurnID: "trn_1"})
	if _, ok := broker.Latest("trn_1"); !ok {
		t.Fatal("latest progress missing")
	}
	broker.Forget("trn_1")
	if _, ok := broker.Latest("trn_1"); ok {
		t.Fatal("forgotten progress remains")
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("unsubscribed channel remains open")
		}
	case <-time.After(time.Second):
		t.Fatal("unsubscribed channel was not closed")
	}
}
