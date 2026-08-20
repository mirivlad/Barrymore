package experience_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func newService(t *testing.T) (*experience.Service, *projection.Registry) {
	t.Helper()
	db := testsupport.OpenDB(t)
	clk := testsupport.Clock()
	svc := experience.New(db, event.NewJournal(db, clk), clk)
	reg := projection.NewRegistry()
	svc.Projections(reg)
	return svc, reg
}

func TestEpisodeProcedureFeedbackRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenDB(t)
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)
	svc := experience.New(db, journal, clk)
	reg := projection.NewRegistry()
	svc.Projections(reg)

	ep, err := svc.Begin(ctx, experience.StartRequest{
		Goal:           "узнать, какой llama-server отвечает на 18080",
		Scope:          "local-model",
		InitialContext: json.RawMessage(`{"endpoint":"127.0.0.1:18080"}`),
	}, event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != experience.EpisodeOpen {
		t.Fatalf("не open: %+v", ep)
	}

	src, err := svc.AddSource(ctx, ep.ID, experience.Source{
		Kind: "runtime", Locator: "/api/v1/local-model",
		Evidence: "endpoint отвечает; managed=false", Confidence: 1,
	}, event.Actor{Type: event.ActorRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if src.EpisodeID != ep.ID {
		t.Fatalf("источник потерял эпизод: %+v", src)
	}

	proc, err := svc.SaveProcedure(ctx, experience.Procedure{
		Intent:               "verify-local-model-owner",
		Title:                "Проверить, чей процесс обслуживает локальную модель",
		SourceEpisodeID:      ep.ID,
		Steps:                []experience.Step{{Capability: "runtime.local_model.inspect", Args: json.RawMessage(`{}`)}},
		RequiredCapabilities: []string{"runtime.local_model.inspect"},
		ExpectedResult:       "известны serving и managed",
		Verification:         []string{"endpoint отвечает", "process identity сопоставлена"},
	}, event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	if proc.Status != experience.ProcedureActive || proc.RiskClass != "read_only" {
		t.Fatalf("неверные defaults процедуры: %+v", proc)
	}

	artifact, err := svc.AddArtifact(ctx, ep.ID, experience.Artifact{
		Name: "llama-server.log", Path: "/tmp/episode/llama-server.log", Size: 128, Checksum: "sha256:test",
	}, event.Actor{Type: event.ActorRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "file" {
		t.Fatalf("kind артефакта не подставлен: %+v", artifact)
	}

	ep, err = svc.Complete(ctx, ep.ID, experience.CompleteRequest{
		Outcome:      experience.OutcomeSuccess,
		Result:       "порт занят внешним llama-server",
		Verification: json.RawMessage(`{"managed":false}`),
	}, event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != experience.EpisodeCompleted || ep.FinishedAt == nil {
		t.Fatalf("эпизод не завершён: %+v", ep)
	}

	if _, err := svc.RecordFeedback(ctx, ep.ID, "neutral", "", event.Actor{Type: event.ActorPerson}); err == nil {
		t.Fatal("neutral не должен превращаться в запись: отсутствие оценки хранится отсутствием")
	}
	fb, err := svc.RecordFeedback(ctx, ep.ID, experience.FeedbackLike, "правильно заметил чужой процесс", event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatal(err)
	}
	if fb.Value != experience.FeedbackLike {
		t.Fatalf("оценка потеряна: %+v", fb)
	}

	sources, err := svc.Sources(ctx, ep.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("источники: %v %+v", err, sources)
	}
	gotProc, err := svc.Procedure(ctx, proc.ID)
	if err != nil || gotProc.Intent != proc.Intent || len(gotProc.Steps) != 1 {
		t.Fatalf("процедура: %v %+v", err, gotProc)
	}
	feedback, err := svc.Feedback(ctx, ep.ID)
	if err != nil || len(feedback) != 1 {
		t.Fatalf("feedback: %v %+v", err, feedback)
	}
	artifacts, err := svc.Artifacts(ctx, ep.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Checksum != "sha256:test" {
		t.Fatalf("artifacts: %v %+v", err, artifacts)
	}
	hits, err := svc.Search(ctx, "чей процесс обслуживает", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("FTS не нашёл сохранённую процедуру: %v %+v", err, hits)
	}

	// Опыт обязан выводиться из журнала так же, как остальные доменные проекции.
	if err := projection.Rebuild(ctx, db, journal, reg); err != nil {
		t.Fatalf("rebuild experience: %v", err)
	}
	rebuilt, err := svc.Episode(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Outcome != experience.OutcomeSuccess || rebuilt.Result != ep.Result {
		t.Fatalf("после rebuild эпизод изменился: %+v", rebuilt)
	}
	feedback, err = svc.Feedback(ctx, ep.ID)
	if err != nil || len(feedback) != 1 || feedback[0].Value != experience.FeedbackLike {
		t.Fatalf("feedback не пережил rebuild: %v %+v", err, feedback)
	}
	hits, err = svc.Search(ctx, "чей процесс обслуживает", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("FTS не пережил rebuild: %v %+v", err, hits)
	}
}

func TestProcedureRejectsArbitraryOrBrokenStep(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.SaveProcedure(ctx, experience.Procedure{
		Intent: "bad", Title: "bad",
		Steps: []experience.Step{{Capability: "", Args: json.RawMessage(`{}`)}},
	}, event.Actor{Type: event.ActorBarrymore}); err == nil {
		t.Fatal("шаг без capability принят")
	}
	if _, err := svc.SaveProcedure(ctx, experience.Procedure{
		Intent: "bad-json", Title: "bad-json",
		Steps: []experience.Step{{Capability: "fs.read", Args: json.RawMessage(`{сломано`)}},
	}, event.Actor{Type: event.ActorBarrymore}); err == nil {
		t.Fatal("невалидные args приняты молча")
	}
}

func TestFeedbackBeforeCompletionIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	ep, err := svc.Begin(ctx, experience.StartRequest{Goal: "тест"}, event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordFeedback(ctx, ep.ID, experience.FeedbackDislike, "рано", event.Actor{Type: event.ActorPerson}); err == nil {
		t.Fatal("незавершённый эпизод получил итоговую оценку")
	}
}
