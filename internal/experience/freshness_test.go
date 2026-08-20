package experience_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func TestSourceFreshnessSurvivesProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	clk := testsupport.Clock()
	db := testsupport.OpenDB(t)
	journal := event.NewJournal(db, clk)
	svc := experience.New(db, journal, clk)
	reg := projection.NewRegistry()
	svc.Projections(reg)

	ep, err := svc.Begin(ctx, experience.StartRequest{Goal: "узнать текущую погоду"},
		event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddSource(ctx, ep.ID, experience.Source{
		Kind: "web.weather", Evidence: "сейчас +17", Confidence: 1,
		Stability: experience.StabilityRealtime, ObservedAt: clk.Now(),
	}, event.Actor{Type: event.ActorRuntime}); err != nil {
		t.Fatal(err)
	}

	assertStability := func(stage string) {
		t.Helper()
		sources, err := svc.Sources(ctx, ep.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) != 1 {
			t.Fatalf("%s: источников %d, ожидался 1", stage, len(sources))
		}
		if sources[0].Stability != experience.StabilityRealtime {
			t.Fatalf("%s: freshness потеряна: %+v", stage, sources[0])
		}
	}

	assertStability("до rebuild")
	if err := projection.Rebuild(ctx, db, journal, reg); err != nil {
		t.Fatal(err)
	}
	assertStability("после rebuild")
}

func TestOldSourceEventWithoutFreshnessStillReplays(t *testing.T) {
	ctx := context.Background()
	clk := testsupport.Clock()
	db := testsupport.OpenDB(t)
	journal := event.NewJournal(db, clk)
	svc := experience.New(db, journal, clk)
	reg := projection.NewRegistry()
	svc.Projections(reg)

	ep, err := svc.Begin(ctx, experience.StartRequest{Goal: "старый эпизод"},
		event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}

	// До появления migration 0016 payload Source не содержал stability.
	// Такой журнал обязан оставаться воспроизводимым после обновления.
	_, err = journal.Write(ctx, func(_ *sql.Tx, w *event.TxWriter) error {
		_, err := w.Append(ctx, event.Request{
			StreamType: experience.StreamEpisode,
			StreamID: ep.ID,
			ExpectedRevision: event.AnyRevision,
			EventType: experience.EvSourceRecorded,
			Actor: event.Actor{Type: event.ActorRuntime},
			Payload: map[string]any{
				"id": "src_legacy", "episode_id": ep.ID, "kind": "legacy.read",
				"evidence": "старое наблюдение", "confidence": 1,
				"observed_at": clk.Now(), "created_at": clk.Now(),
			},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := projection.Rebuild(ctx, db, journal, reg); err != nil {
		t.Fatalf("старый event перестал replay-иться: %v", err)
	}
	sources, err := svc.Sources(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Stability != experience.StabilityStable {
		t.Fatalf("legacy source должен получить безопасный default stable: %+v", sources)
	}
}
