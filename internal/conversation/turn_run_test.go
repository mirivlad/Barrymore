package conversation_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/thread"
)

type blockingStreamingProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (p *blockingStreamingProvider) ID() string       { return "streaming" }
func (p *blockingStreamingProvider) Describe() string { return "streaming test provider" }
func (p *blockingStreamingProvider) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (p *blockingStreamingProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("non-streaming fallback used")
}
func (p *blockingStreamingProvider) CompleteStream(ctx context.Context, _ model.Request,
	onProgress func(model.Progress)) (model.Response, error) {
	onProgress(model.Progress{OutputUnits: 8, Elapsed: 2 * time.Second})
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	return model.Response{
		Content: `{
			"reply":"Готово из stream.",
			"research":{"capability_id":"","args":{},"why":"данных достаточно"},
			"thread_match":null,
			"thread_state":null,
			"memory_candidates":[],
			"own_actions":[],
			"work_order_proposals":[],
			"open_questions":[]
		}`,
		Model: "stream-model", PromptTokens: 12, CompletionTokens: 4,
		PromptDuration: 100 * time.Millisecond, GenerationDuration: 500 * time.Millisecond,
		PromptTokensPerSecond: 120, GenerationTokensPerSecond: 8,
	}, nil
}

type finalProvider struct{}

func (finalProvider) ID() string       { return "final" }
func (finalProvider) Describe() string { return "final test provider" }
func (finalProvider) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (finalProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: `{
		"reply":"Готово.",
		"research":{"capability_id":"","args":{},"why":"данных достаточно"},
		"thread_match":null,
		"thread_state":null,
		"memory_candidates":[],
		"own_actions":[],
		"work_order_proposals":[],
		"open_questions":[]
	}`, Model: "test", PromptTokens: 10, CompletionTokens: 3}, nil
}

func newTurnRunTalk(t *testing.T) (*conversation.Service, *store.DB, *event.Journal, context.Context) {
	t.Helper()
	ctx := context.Background()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)
	threads := thread.NewService(db, journal, clk)
	talk := conversation.New(conversation.Config{
		DB: db, Journal: journal, Clock: clk, Provider: finalProvider{},
		Threads: threads, Logger: testsupport.Logger(t),
	})
	return talk, db, journal, ctx
}

func TestBeginTurnPersistsOwnerMessageAndRejectsConcurrentTurn(t *testing.T) {
	talk, _, _, ctx := newTurnRunTalk(t)
	conv, err := talk.Start(ctx, "", "turn run")
	if err != nil {
		t.Fatal(err)
	}

	run, err := talk.BeginTurn(ctx, conv.ID, "Что происходит?")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.Status != conversation.TurnQueued || run.Stage != conversation.StageQueued {
		t.Fatalf("queued turn=%+v", run)
	}
	if run.UserMessageID == "" || run.ConversationID != conv.ID {
		t.Fatalf("turn не связан с репликой: %+v", run)
	}

	msgs, err := talk.Messages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != run.UserMessageID || msgs[0].Content != "Что происходит?" {
		t.Fatalf("реплика и turn не записаны атомарно: messages=%+v turn=%+v", msgs, run)
	}

	active, err := talk.ActiveTurn(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != run.ID {
		t.Fatalf("active turn=%s, want %s", active.ID, run.ID)
	}

	_, err = talk.BeginTurn(ctx, conv.ID, "Вторая реплика")
	if !errors.Is(err, conversation.ErrTurnActive) {
		t.Fatalf("concurrent error=%v, want ErrTurnActive", err)
	}
	msgs, err = talk.Messages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("отвергнутая реплика попала в историю: %+v", msgs)
	}
}

func TestExecuteTurnPersistsCompletedResult(t *testing.T) {
	talk, _, _, ctx := newTurnRunTalk(t)
	conv, err := talk.Start(ctx, "", "completed turn")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := talk.BeginTurn(ctx, conv.ID, "Ответь")
	if err != nil {
		t.Fatal(err)
	}

	completed, err := talk.ExecuteTurn(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != conversation.TurnCompleted || completed.FinishedAt == nil {
		t.Fatalf("completed turn=%+v", completed)
	}
	if completed.ReplyMessageID == "" || completed.Result.Reply.ID != completed.ReplyMessageID {
		t.Fatalf("result не связан с reply: %+v", completed)
	}
	if completed.Result.Reply.Content != "Готово." {
		t.Fatalf("reply=%q", completed.Result.Reply.Content)
	}
	if completed.PromptTokens != 10 || completed.OutputTokens != 3 {
		t.Fatalf("usage=%d/%d", completed.PromptTokens, completed.OutputTokens)
	}

	stored, err := talk.TurnRun(ctx, conv.ID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != conversation.TurnCompleted || stored.Result.Reply.Content != "Готово." {
		t.Fatalf("stored turn=%+v", stored)
	}
	if _, err := talk.ActiveTurn(ctx, conv.ID); !errors.Is(err, conversation.ErrNoActiveTurn) {
		t.Fatalf("active after completion=%v", err)
	}

	msgs, err := talk.Messages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[1].ID != completed.ReplyMessageID {
		t.Fatalf("messages=%+v", msgs)
	}
}

func TestInterruptUnfinishedTurnSurvivesProjectionRebuild(t *testing.T) {
	talk, db, journal, ctx := newTurnRunTalk(t)
	conv, err := talk.Start(ctx, "", "interrupted turn")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := talk.BeginTurn(ctx, conv.ID, "Долгий вопрос")
	if err != nil {
		t.Fatal(err)
	}

	count, err := talk.InterruptUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interrupted=%d, want 1", count)
	}
	if count, err = talk.InterruptUnfinished(ctx); err != nil || count != 0 {
		t.Fatalf("повторное восстановление count=%d err=%v", count, err)
	}

	reg := projection.NewRegistry()
	talk.Projections(reg)
	if err := projection.Rebuild(ctx, db, journal, reg); err != nil {
		t.Fatal(err)
	}
	stored, err := talk.TurnRun(ctx, conv.ID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != conversation.TurnInterrupted || stored.FinishedAt == nil {
		t.Fatalf("replayed turn=%+v", stored)
	}
	if _, err := talk.ActiveTurn(ctx, conv.ID); !errors.Is(err, conversation.ErrNoActiveTurn) {
		t.Fatalf("active after recovery=%v", err)
	}
}

func TestExecuteTurnUsesPrivateStreamingProgressAndPersistsExactTelemetry(t *testing.T) {
	provider := &blockingStreamingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	ctx := context.Background()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	clk := testsupport.Clock()
	journal := event.NewJournal(db, clk)
	threads := thread.NewService(db, journal, clk)
	talk := conversation.New(conversation.Config{
		DB: db, Journal: journal, Clock: clk, Provider: provider,
		Threads: threads, Logger: testsupport.Logger(t),
	})
	conv, err := talk.Start(ctx, "", "stream progress")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := talk.BeginTurn(ctx, conv.ID, "Ответь потоково")
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan struct {
		run conversation.TurnRun
		err error
	}, 1)
	go func() {
		run, err := talk.ExecuteTurn(ctx, queued.ID)
		result <- struct {
			run conversation.TurnRun
			err error
		}{run: run, err: err}
	}()

	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("streaming provider was not used")
	}
	progress, ok := talk.Progress().Latest(queued.ID)
	if !ok || progress.Stage != conversation.StageProviderGeneration ||
		progress.OutputTokens != 8 || !progress.Approximate {
		t.Fatalf("live progress=%+v ok=%v", progress, ok)
	}
	close(provider.release)

	var completed conversation.TurnRun
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		completed = got.run
	case <-time.After(time.Second):
		t.Fatal("turn did not complete")
	}
	if completed.PromptTokens != 12 || completed.OutputTokens != 4 ||
		completed.PromptMS != 100 || completed.GenerationMS != 500 ||
		completed.PromptTokensPerSecond != 120 || completed.GenerationTokensPerSecond != 8 {
		t.Fatalf("exact telemetry=%+v", completed)
	}
	if _, ok := talk.Progress().Latest(queued.ID); ok {
		t.Fatal("completed turn kept ephemeral progress")
	}
	messages, err := talk.Messages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	reply := messages[len(messages)-1]
	if reply.PromptMS != 100 || reply.GenerationMS != 500 ||
		reply.PromptTokensPerSecond != 120 || reply.GenerationTokensPerSecond != 8 ||
		reply.TurnLatencyMS != completed.TotalLatencyMS {
		t.Fatalf("reply telemetry=%+v", reply)
	}
}
