package memory_test

import (
	"context"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func TestRecallFindsRelevantMemoryAndReturnsFreshness(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenDB(t)
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)
	svc := memory.NewService(db, journal, clk, memory.DefaultPolicy())

	item, err := svc.Remember(ctx, memory.ProposeRequest{
		Type:       memory.TypeFact,
		Content:    "Разговорная модель Бэрримора — Ornith",
		Confidence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified := clk.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE memory_items SET stability = 'realtime', verified_at = ? WHERE id = ?`,
		verified, item.ID); err != nil {
		t.Fatal(err)
	}

	hits, err := svc.Recall(ctx, "Какая модель у тебя сейчас запущена?", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("relevant memory was not recalled")
	}
	if hits[0].ID != item.ID || hits[0].Stability != "realtime" || hits[0].VerifiedAt == nil {
		t.Fatalf("freshness metadata lost: %+v", hits[0])
	}
}

func TestRecallDoesNotDumpUnrelatedRecentMemory(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenDB(t)
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)
	svc := memory.NewService(db, journal, clk, memory.DefaultPolicy())

	if _, err := svc.Remember(ctx, memory.ProposeRequest{
		Type: memory.TypePreference, Content: "Владелец предпочитает тёмную тему",
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := svc.Recall(ctx, "Какая разговорная модель запущена?", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("unrelated memory leaked into query-specific recall: %+v", hits)
	}
}
