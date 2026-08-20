package conversation_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/thread"
)

type researchSequenceProvider struct {
	replies []string
	calls   int
	probes  int
	last    []model.Request
}

func (p *researchSequenceProvider) ID() string       { return "sequence" }
func (p *researchSequenceProvider) Describe() string { return "Ornith local" }
func (p *researchSequenceProvider) Probe(context.Context) model.Status {
	p.probes++
	return model.Status{
		Status: model.StatusReady, Endpoint: "http://127.0.0.1:18080",
		Model: "Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf", Reason: "провайдер отвечает",
		SupportsSchema: true,
	}
}
func (p *researchSequenceProvider) Complete(_ context.Context, req model.Request) (model.Response, error) {
	p.last = append(p.last, req)
	idx := p.calls
	p.calls++
	if idx >= len(p.replies) {
		idx = len(p.replies) - 1
	}
	return model.Response{
		Content: p.replies[idx], Model: "Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf",
		PromptTokens: 10, CompletionTokens: 5,
	}, nil
}

func TestUnknownCurrentStateIsResearchedBeforeFinalReply(t *testing.T) {
	ctx := context.Background()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	journal := event.NewJournal(db, clk)
	rt := runtime.New(runtime.Config{DB: db, Journal: journal, Clock: clk, Logger: testsupport.Logger(t)})
	threads := thread.NewService(db, journal, clk)
	mem := memory.NewService(db, journal, clk, memory.DefaultPolicy())
	prov := &researchSequenceProvider{replies: []string{
		`{"reply":"Проверю, какая модель реально отвечает сейчас.",` +
			`"research":{"capability_id":"runtime.provider.inspect","args":{},"why":"нужно текущее, а не запомненное состояние"},` +
			`"memory_candidates":[],"own_actions":[],"work_order_proposals":[],"open_questions":[]}`,
		`{"reply":"Fresh evidence уже достаточно; повторять probe не нужно.",` +
			`"research":{"capability_id":"runtime.provider.inspect","args":{},"why":"слабая модель повторила уже выполненный шаг"},` +
			`"memory_candidates":[],"own_actions":[],"work_order_proposals":[],"open_questions":[]}`,
		`{"reply":"Сейчас мои ответы формулирует локальная Ornith 1.5 9B.",` +
			`"research":{"capability_id":"","args":{},"why":"evidence уже получено"},` +
			`"memory_candidates":[],"own_actions":[],"work_order_proposals":[],"open_questions":[]}`,
	}}
	talk := conversation.New(conversation.Config{
		DB: db, Journal: journal, Clock: clk, Provider: prov,
		Threads: threads, Memory: mem, Runtime: rt, Logger: testsupport.Logger(t),
	})
	reg := projection.NewRegistry()
	rt.Projections(reg)
	threads.Projections(reg)
	mem.Projections(reg)
	talk.Projections(reg)

	conv, err := talk.Start(ctx, "", "research")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := talk.Send(ctx, conv.ID, "Какая модель у тебя сейчас запущена?")
	if err != nil {
		t.Fatal(err)
	}

	if prov.calls != 3 {
		t.Fatalf("модель вызвана %d раз, ожидалось исследование + ошибочный повтор + финал", prov.calls)
	}
	if prov.probes != 1 {
		t.Fatalf("provider probe вызван %d раз, ожидался один свежий probe", prov.probes)
	}
	if !strings.Contains(turn.Reply.Content, "Ornith") {
		t.Fatalf("финальный ответ не использовал evidence: %q", turn.Reply.Content)
	}
	if turn.EpisodeID == "" {
		t.Fatal("research turn не связан с Episode")
	}
	if len(turn.OwnActions) != 0 || len(turn.Proposal.WorkOrders) != 0 {
		t.Fatalf("research ошибочно превратился в action/work order: %+v", turn)
	}
	if strings.Contains(strings.ToLower(turn.Reply.Content), "git.worktree") {
		t.Fatalf("вернулся старый нерелевантный git workaround: %q", turn.Reply.Content)
	}

	msgs, err := talk.Messages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("промежуточный research draft попал в историю: сообщений=%d", len(msgs))
	}
	if msgs[1].PromptTokens != 30 || msgs[1].OutputTokens != 15 {
		t.Fatalf("стоимость research loop не агрегирована: %+v", msgs[1])
	}

	ep, err := talk.Experience().Episode(ctx, turn.EpisodeID)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != experience.EpisodeCompleted || ep.Outcome != experience.OutcomeSuccess {
		t.Fatalf("episode не завершён успехом: %+v", ep)
	}
	sources, err := talk.Experience().Sources(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || !strings.Contains(sources[0].Evidence, "Ornith") {
		t.Fatalf("fresh evidence не сохранено: %+v", sources)
	}
	procedures, err := talk.Experience().ProceduresByIntent(ctx, "research:runtime.provider.inspect", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(procedures) != 1 || len(procedures[0].Steps) != 1 {
		t.Fatalf("успешный способ не стал procedural memory: %+v", procedures)
	}

	if len(prov.last) != 3 || !strings.Contains(prov.last[1].System, "Новое evidence исследования") {
		t.Fatal("следующий deliberation не получил результат исследования")
	}
	if !strings.Contains(prov.last[2].System, "Новых исследовательских шагов") {
		t.Fatal("после повтора успешного probe модель не была принуждена к финальному ответу")
	}
}
