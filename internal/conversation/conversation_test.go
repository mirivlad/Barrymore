package conversation_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/thread"
)

// scriptedProvider отдаёт заранее заданный ответ и запоминает запрос.
type scriptedProvider struct {
	reply   string
	err     error
	lastReq model.Request
	calls   int
}

func (p *scriptedProvider) ID() string       { return "scripted" }
func (p *scriptedProvider) Describe() string { return "подготовленный ответ" }
func (p *scriptedProvider) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (p *scriptedProvider) Complete(_ context.Context, req model.Request) (model.Response, error) {
	p.calls++
	p.lastReq = req
	if p.err != nil {
		return model.Response{}, p.err
	}
	return model.Response{Content: p.reply, Model: "тестовая", PromptTokens: 10, CompletionTokens: 5}, nil
}

type harness struct {
	talk    *conversation.Service
	threads *thread.Service
	mem     *memory.Service
	prov    *scriptedProvider
	db      *store.DB
}

func newHarness(t *testing.T, prov model.Provider, pol memory.Policy) *harness {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	j := event.NewJournal(db, clk)
	rt := runtime.New(runtime.Config{DB: db, Journal: j, Clock: clk, Logger: testsupport.Logger(t)})
	th := thread.NewService(db, j, clk)
	mem := memory.NewService(db, j, clk, pol)

	talk := conversation.New(conversation.Config{
		DB: db, Journal: j, Clock: clk, Provider: prov,
		Threads: th, Memory: mem, Runtime: rt, Logger: testsupport.Logger(t),
	})

	reg := projection.NewRegistry()
	rt.Projections(reg)
	th.Projections(reg)
	mem.Projections(reg)
	talk.Projections(reg)

	h := &harness{talk: talk, threads: th, mem: mem, db: db}
	if p, ok := prov.(*scriptedProvider); ok {
		h.prov = p
	}
	return h
}

func (h *harness) conversation(t *testing.T, threadID string) conversation.Conversation {
	t.Helper()
	c, err := h.talk.Start(context.Background(), threadID, "проверка")
	if err != nil {
		t.Fatalf("разговор не начат: %v", err)
	}
	return c
}

func (h *harness) thread(t *testing.T) thread.Thread {
	t.Helper()
	th, err := h.threads.Create(context.Background(), thread.CreateRequest{
		Title: "Нить для проверки", Kind: thread.KindProject,
		Actor: event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		t.Fatalf("нить не создана: %v", err)
	}
	return th
}

// --- отсутствие провайдера ---

// Без провайдера Бэрримор не разговаривает, но и не притворяется: остальная
// система обязана работать.
func TestSendWithoutProviderIsHonest(t *testing.T) {
	h := newHarness(t, nil, memory.DefaultPolicy())
	if h.talk.Available() {
		t.Fatal("без провайдера разговорный слой не доступен")
	}
	st := h.talk.ProviderStatus(context.Background())
	if st.Status != model.StatusNotConfig {
		t.Fatalf("состояние %q, ожидалось not_configured", st.Status)
	}
	if !strings.Contains(st.Reason, "работают") {
		t.Fatalf("владельцу не сказано, что остальное работает: %q", st.Reason)
	}

	c := h.conversation(t, "")
	_, err := h.talk.Send(context.Background(), c.ID, "привет")
	if !errors.Is(err, conversation.ErrNoProvider) {
		t.Fatalf("ошибка %v, ожидалась ErrNoProvider", err)
	}
}

// --- контракт ответа ---

// Невалидный ответ не должен частично менять состояние: ни ответа, ни
// кандидатов в память появиться не может.
func TestInvalidResponseChangesNothing(t *testing.T) {
	prov := &scriptedProvider{reply: "я просто поболтаю без всякой схемы"}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	_, err := h.talk.Send(context.Background(), c.ID, "привет")
	if err == nil {
		t.Fatal("ответ вне контракта должен быть ошибкой")
	}
	if !strings.Contains(err.Error(), "контракт") {
		t.Fatalf("причина неясна: %v", err)
	}

	msgs, err := h.talk.Messages(context.Background(), c.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == conversation.RoleBarrymore {
			t.Fatal("при невалидном ответе реплика Бэрримора появиться не должна")
		}
	}
	cands, err := h.mem.Candidates(context.Background(), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("кандидатов %d, ожидалось 0", len(cands))
	}
}

// Пустой текст ответа при формально валидном JSON — тоже нарушение контракта:
// молчание, выданное за ответ, хуже честной ошибки.
func TestEmptyReplyIsRejected(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"   ","memory_candidates":[],` +
		`"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	if _, err := h.talk.Send(context.Background(), c.ID, "привет"); err == nil {
		t.Fatal("пустой ответ должен быть отклонён")
	}
}

// Ответ в ограде из тройных кавычек разбирается: некоторые модели добавляют её
// даже при принуждении к схеме, и терять из-за этого целый ход обидно.
func TestFencedJSONIsParsed(t *testing.T) {
	prov := &scriptedProvider{reply: "```json\n" +
		`{"reply":"Здравствуйте.","memory_candidates":[],` +
		`"work_order_proposals":[],"open_questions":[]}` + "\n```"}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	turn, err := h.talk.Send(context.Background(), c.ID, "привет")
	if err != nil {
		t.Fatalf("ответ в ограде не разобран: %v", err)
	}
	if turn.Reply.Content != "Здравствуйте." {
		t.Fatalf("ответ %q", turn.Reply.Content)
	}
}

// Отказ провайдера доходит до владельца как отказ, а не как пустой ответ.
func TestProviderFailureSurfaces(t *testing.T) {
	prov := &scriptedProvider{err: errors.New("соединение отклонено")}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	_, err := h.talk.Send(context.Background(), c.ID, "привет")
	if err == nil {
		t.Fatal("отказ провайдера должен быть ошибкой")
	}
	if !strings.Contains(err.Error(), "соединение отклонено") {
		t.Fatalf("настоящая причина потеряна: %v", err)
	}
}

// --- предложения ---

// Схема должна доходить до провайдера: без неё принуждение не работает,
// и разбор встретил бы свободный текст.
func TestRequestCarriesSchemaAndIdentity(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"Здравствуйте.","memory_candidates":[],` +
		`"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	if _, err := h.talk.Send(context.Background(), c.ID, "привет"); err != nil {
		t.Fatal(err)
	}
	if len(prov.lastReq.Schema) == 0 {
		t.Fatal("схема не передана провайдеру")
	}
	if !prov.lastReq.DisableThinking {
		t.Fatal("размышления не отключены: бюджет ответа уйдёт в них целиком")
	}
	if !strings.Contains(prov.lastReq.System, "Бэрримор") {
		t.Fatal("личность не подана модели")
	}
	if !strings.Contains(prov.lastReq.System, "confidence") {
		t.Fatal("модели не объяснено, что значит уверенность")
	}
}

// Прямо сказанное владельцем записывается само; чувствительное — нет.
func TestMemoryCandidatesFollowPolicy(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"Записал.","memory_candidates":[
		{"type":"preference","content":"Работает по вечерам","reason":"сказано прямо",
		 "sensitivity":"normal","confidence":1.0},
		{"type":"fact","content":"Возможны проблемы со здоровьем","reason":"следует из графика",
		 "sensitivity":"sensitive","confidence":0.9}
	],"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	turn, err := h.talk.Send(context.Background(), c.ID, "я работаю по вечерам")
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.MemoryCandidates) != 2 {
		t.Fatalf("кандидатов %d, ожидалось 2", len(turn.MemoryCandidates))
	}

	auto, asked := 0, 0
	for _, mc := range turn.MemoryCandidates {
		if mc.Auto {
			auto++
			if mc.ItemID == "" {
				t.Fatal("записанный кандидат должен ссылаться на запись")
			}
		} else {
			asked++
			if mc.Reason == "" {
				t.Fatal("владельцу не объяснено, почему у него спрашивают")
			}
		}
	}
	if auto != 1 || asked != 1 {
		t.Fatalf("записано сам: %d, спрошено: %d — ожидалось по одному", auto, asked)
	}
}

// Уверенность, которой модель не назвала, не подменяется серединой: выдать
// отсутствие оценки за оценку — тот же обман.
func TestMissingConfidenceBlocksAutomaticWrite(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"Ага.","memory_candidates":[
		{"type":"fact","content":"Что-то важное","reason":"показалось","sensitivity":"normal"}
	],"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	turn, err := h.talk.Send(context.Background(), c.ID, "привет")
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.MemoryCandidates) != 1 {
		t.Fatalf("кандидатов %d", len(turn.MemoryCandidates))
	}
	if turn.MemoryCandidates[0].Auto {
		t.Fatal("без названной уверенности запись не может быть автоматической")
	}
}

// Позиция Бэрримора и открытые вопросы принадлежат нити.
func TestProposalReachesThread(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"Подумал.",
		"thread_position":{"statement":"Стоит начать с аудита","confidence":0.7,
		 "basis":"есть незавершённый worktree"},
		"memory_candidates":[],"work_order_proposals":[],
		"open_questions":["Нужен ли отдельный экран настроек?"]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	th := h.thread(t)
	c := h.conversation(t, th.ID)

	if _, err := h.talk.Send(context.Background(), c.ID, "с чего начать?"); err != nil {
		t.Fatal(err)
	}

	d, err := h.threads.Detail(context.Background(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range d.Positions {
		if p.Owner == thread.OwnerBarrymore && strings.Contains(p.Statement, "аудита") {
			found = true
		}
	}
	if !found {
		t.Fatalf("позиция Бэрримора не попала в нить: %+v", d.Positions)
	}
	if len(d.Questions) != 1 {
		t.Fatalf("открытых вопросов %d, ожидался 1", len(d.Questions))
	}
}

// Без нити позиции и вопросы деться некуда — и это не должно быть ошибкой:
// разговор без нити остаётся полноценным разговором.
func TestProposalWithoutThreadIsNotAnError(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"Хорошо.",
		"thread_position":{"statement":"Что-то","confidence":0.5,"basis":"так вышло"},
		"memory_candidates":[],"work_order_proposals":[],
		"open_questions":["Вопрос в пустоту"]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	if _, err := h.talk.Send(context.Background(), c.ID, "привет"); err != nil {
		t.Fatalf("разговор без нити должен работать: %v", err)
	}
}

// --- запись хода ---

func TestBothMessagesAreRecorded(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"Здравствуйте.","memory_candidates":[],` +
		`"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	if _, err := h.talk.Send(context.Background(), c.ID, "привет"); err != nil {
		t.Fatal(err)
	}
	msgs, err := h.talk.Messages(context.Background(), c.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("сообщений %d, ожидалось 2", len(msgs))
	}
	if msgs[0].Role != conversation.RolePerson || msgs[1].Role != conversation.RoleBarrymore {
		t.Fatalf("порядок реплик нарушен: %s, %s", msgs[0].Role, msgs[1].Role)
	}
	// Владелец должен видеть, что именно подавалось модели.
	if len(msgs[1].RetrievalTrace) == 0 {
		t.Fatal("без следа извлечения нельзя понять, почему ответ такой")
	}
}

// Пустая реплика до модели не доходит: тратить на неё ход бессмысленно.
func TestEmptyInputNeverReachesModel(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"x","memory_candidates":[],` +
		`"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	c := h.conversation(t, "")

	if _, err := h.talk.Send(context.Background(), c.ID, "   "); err == nil {
		t.Fatal("пустая реплика должна быть отклонена")
	}
	if prov.calls != 0 {
		t.Fatalf("модель вызвана %d раз на пустой реплике", prov.calls)
	}
}

func TestUnknownConversationIsNotFound(t *testing.T) {
	prov := &scriptedProvider{reply: `{"reply":"x","memory_candidates":[],` +
		`"work_order_proposals":[],"open_questions":[]}`}
	h := newHarness(t, prov, memory.DefaultPolicy())
	_, err := h.talk.Send(context.Background(), "conv_нет", "привет")
	if !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("ошибка %v, ожидалась ErrNotFound", err)
	}
}

// Промпт не должен противоречить сам себе. «Всё, что ты делаешь, —
// предложения» спорило с правилом памяти ниже, и Бэрримор в разговоре
// утверждал, что не записывает ничего без подтверждения, — хотя записывал.
func TestPromptStatesMemoryRuleOfTheActualPolicy(t *testing.T) {
	cases := []struct {
		mode   string
		expect string
	}{
		{memory.ModeAsk, "Ничего не записывай сам"},
		{memory.ModeAutoSafe, "записываешь сам"},
		{memory.ModeAuto, "записываешь сам"},
	}
	for _, tc := range cases {
		pol, err := memory.ParsePolicy(tc.mode)
		if err != nil {
			t.Fatal(err)
		}
		id := conversation.DefaultIdentity()
		id.MemoryRule = pol.Rule()
		prompt := id.SystemPrompt(nil, time.Now())

		if !strings.Contains(prompt, tc.expect) {
			t.Errorf("режим %s: в промпте нет %q", tc.mode, tc.expect)
		}
		if strings.Contains(prompt, "Ты не выполняешь действий сам") {
			t.Errorf("режим %s: осталось утверждение, противоречащее правилу памяти", tc.mode)
		}
	}
}

// О надзоре за моделью Бэрримор говорит только тогда, когда действительно
// её ведёт: с облачным провайдером это было бы неправдой.
func TestPromptMentionsModelSupervisionOnlyWhenTrue(t *testing.T) {
	id := conversation.DefaultIdentity()
	if strings.Contains(id.SystemPrompt(nil, time.Now()), "сервер модели ты держишь сам") {
		t.Fatal("Бэрримор приписывает себе надзор, которого нет")
	}
	id.KeepsOwnModel = true
	if !strings.Contains(id.SystemPrompt(nil, time.Now()), "сервер модели ты держишь сам") {
		t.Fatal("Бэрримор умалчивает о том, что ведёт модель сам")
	}
}
