package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/thread"
)

// ErrNoProvider возвращается, когда разговорный слой не настроен.
//
// Это честное отключённое состояние, а не заглушка с выдуманным ответом
// (12_BOOTSTRAP_PROMPT: honest disabled state, не mock response).
var ErrNoProvider = errors.New("разговорный слой не настроен: провайдер модели недоступен")

// ErrNotFound возвращается, когда разговор не найден.
var ErrNotFound = errors.New("разговор не найден")

// Service ведёт разговор.
type Service struct {
	db       *store.DB
	journal  *event.Journal
	clock    clock.Clock
	provider model.Provider
	threads  *thread.Service
	memory   *memory.Service
	rt       *runtime.Runtime
	identity Identity
	log      *slog.Logger

	// maxHistory ограничивает, сколько прошлых реплик уходит в модель.
	maxHistory int
	// maxTokens ограничивает объём ответа.
	maxTokens int
}

// Config — параметры сервиса.
type Config struct {
	DB         *store.DB
	Journal    *event.Journal
	Clock      clock.Clock
	Provider   model.Provider
	Threads    *thread.Service
	Memory     *memory.Service
	Runtime    *runtime.Runtime
	Identity   Identity
	Logger     *slog.Logger
	MaxHistory int
	MaxTokens  int
}

// New создаёт сервис разговора.
func New(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxHistory <= 0 {
		cfg.MaxHistory = 12
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1600
	}
	if cfg.Identity.Name == "" {
		cfg.Identity = DefaultIdentity()
	}
	return &Service{
		db: cfg.DB, journal: cfg.Journal, clock: cfg.Clock, provider: cfg.Provider,
		threads: cfg.Threads, memory: cfg.Memory, rt: cfg.Runtime,
		identity: cfg.Identity, log: cfg.Logger,
		maxHistory: cfg.MaxHistory, maxTokens: cfg.MaxTokens,
	}
}

// Available сообщает, доступен ли разговорный слой.
func (s *Service) Available() bool { return s.provider != nil }

// ProviderStatus возвращает наблюдаемое состояние провайдера.
func (s *Service) ProviderStatus(ctx context.Context) model.Status {
	if s.provider == nil {
		return model.Status{
			Status: model.StatusNotConfig, ObservedAt: s.clock.Now(),
			Reason: "провайдер модели не задан; Бэрримор не разговаривает, " +
				"но нити, штат, поручения и предиктивный контур работают",
		}
	}
	return s.provider.Probe(ctx)
}

// Start создаёт разговор.
func (s *Service) Start(ctx context.Context, threadID, title string) (Conversation, error) {
	if threadID != "" {
		if _, err := s.threads.Get(ctx, threadID); err != nil {
			return Conversation{}, err
		}
	}
	now := s.clock.Now()
	c := Conversation{
		ID: ids.New("conv"), Title: title, ThreadID: threadID,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: c.ID, ExpectedRevision: 0,
			EventType: EvConversationStarted,
			Actor:     event.Actor{Type: event.ActorPerson}, Payload: c,
		}); err != nil {
			return err
		}
		return applyConversation(ctx, tx, c)
	})
	if err != nil {
		return Conversation{}, err
	}
	return c, nil
}

// Send передаёт реплику владельца и получает ответ.
//
// Порядок соответствует 03_SYSTEM_ARCHITECTURE §6: определить нити, собрать
// подтверждённый контекст, добавить личность и ограничения, вызвать модель,
// проверить структуру, записать сообщение, создать только кандидатов.
func (s *Service) Send(ctx context.Context, conversationID, text string) (Turn, error) {
	if s.provider == nil {
		return Turn{}, ErrNoProvider
	}
	if strings.TrimSpace(text) == "" {
		return Turn{}, fmt.Errorf("пустая реплика")
	}

	conv, err := s.Get(ctx, conversationID)
	if err != nil {
		return Turn{}, err
	}

	userMsg, err := s.record(ctx, conv, Message{
		ConversationID: conv.ID, ThreadID: conv.ThreadID,
		Role: RolePerson, Content: text,
	}, event.Actor{Type: event.ActorPerson})
	if err != nil {
		return Turn{}, err
	}

	sections, trace, err := s.buildContext(ctx, conv)
	if err != nil {
		return Turn{}, err
	}
	history, err := s.history(ctx, conv.ID)
	if err != nil {
		return Turn{}, err
	}

	req := model.Request{
		System:          s.identity.SystemPrompt(sections, s.clock.Now()),
		Messages:        history,
		Schema:          ResponseSchema(),
		SchemaName:      "barrymore_reply",
		MaxTokens:       s.maxTokens,
		Temperature:     0.3,
		DisableThinking: true,
	}

	resp, err := s.provider.Complete(ctx, req)
	if err != nil {
		return Turn{}, fmt.Errorf("модель не ответила: %w", err)
	}

	proposal, err := parseProposal(resp.Content)
	if err != nil {
		// Невалидный ответ не меняет состояние даже частично: реплика владельца
		// уже записана, но ни ответа, ни кандидатов не появляется.
		return Turn{}, fmt.Errorf("ответ модели не соответствует контракту: %w", err)
	}

	replyMsg, err := s.record(ctx, conv, Message{
		ConversationID: conv.ID, ThreadID: conv.ThreadID,
		Role: RoleBarrymore, Content: proposal.Reply,
		Provider: s.provider.ID(), Model: resp.Model,
		PromptTokens: resp.PromptTokens, OutputTokens: resp.CompletionTokens,
		LatencyMS: resp.Latency.Milliseconds(), RetrievalTrace: trace,
	}, event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		return Turn{}, err
	}

	turn := Turn{UserMessage: userMsg, Reply: replyMsg, Proposal: proposal}

	// Предложения превращаются в видимых кандидатов. Ничего не записывается
	// в память молча (00_PRODUCT_VISION §9.2).
	for _, mc := range proposal.MemoryCandidates {
		cand, err := s.memory.Propose(ctx, memory.ProposeRequest{
			Type: mc.Type, Content: mc.Content, Reason: mc.Reason,
			ProposedBy: RoleBarrymore, ThreadID: conv.ThreadID,
			ConversationID: conv.ID, MessageID: replyMsg.ID,
		})
		if err != nil {
			s.log.Error("кандидат в память не создан", "conversation", conv.ID, "error", err)
			continue
		}
		turn.MemoryCandidates = append(turn.MemoryCandidates, MemoryCandidateID{
			ID: cand.ID, Type: cand.Type, Content: cand.Content,
		})
	}

	// Позиция Бэрримора по нити — тоже предложение, но она принадлежит нити
	// и хранится отдельно от позиции владельца.
	if proposal.ThreadPosition != nil && conv.ThreadID != "" {
		if _, err := s.threads.SetPosition(ctx, conv.ThreadID, thread.PositionRequest{
			Owner:      thread.OwnerBarrymore,
			Statement:  proposal.ThreadPosition.Statement,
			Confidence: proposal.ThreadPosition.Confidence,
			Basis:      proposal.ThreadPosition.Basis,
			Actor:      event.Actor{Type: event.ActorBarrymore},
		}); err != nil {
			s.log.Error("позиция по нити не записана", "thread", conv.ThreadID, "error", err)
		}
	}

	if conv.ThreadID != "" {
		if err := s.threads.TouchActivity(ctx, conv.ThreadID); err != nil {
			s.log.Error("активность нити не отмечена", "thread", conv.ThreadID, "error", err)
		}
	}

	// Открытые вопросы фиксируются как вопросы, а не как задачи.
	for _, q := range proposal.OpenQuestions {
		if conv.ThreadID == "" {
			break
		}
		if _, err := s.threads.OpenQuestion(ctx, conv.ThreadID, q, thread.OwnerBarrymore,
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			s.log.Error("открытый вопрос не записан", "thread", conv.ThreadID, "error", err)
		}
	}

	if err := s.recordProposal(ctx, conv, replyMsg.ID, proposal); err != nil {
		return turn, err
	}
	return turn, nil
}

// parseProposal разбирает ответ модели.
func parseProposal(content string) (Proposal, error) {
	trimmed := strings.TrimSpace(content)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	var p Proposal
	if err := json.Unmarshal([]byte(trimmed), &p); err != nil {
		return Proposal{}, fmt.Errorf("не JSON: %v", err)
	}
	if strings.TrimSpace(p.Reply) == "" {
		return Proposal{}, fmt.Errorf("ответ пуст")
	}
	return p, nil
}

func (s *Service) record(ctx context.Context, conv Conversation, m Message, actor event.Actor) (Message, error) {
	m.ID = ids.New(ids.Message)
	m.CreatedAt = s.clock.Now()

	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: conv.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvMessageRecorded, Actor: actor,
			CorrelationID: conv.ThreadID, Payload: m,
		}); err != nil {
			return err
		}
		return applyMessage(ctx, tx, m)
	})
	if err != nil {
		return Message{}, err
	}
	return m, nil
}

func (s *Service) recordProposal(ctx context.Context, conv Conversation, messageID string, p Proposal) error {
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		_, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: conv.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvProposalReceived, Actor: event.Actor{Type: event.ActorBarrymore},
			Payload: map[string]any{"message_id": messageID, "proposal": p},
		})
		return err
	})
	return err
}

// buildContext собирает разделы контекста и след извлечения.
//
// Для каждого элемента сохраняется retrieval trace: владелец должен видеть,
// что именно было подано модели (04_MEMORY_AND_CONTINUITY §7).
func (s *Service) buildContext(ctx context.Context, conv Conversation) ([]ContextSection, []string, error) {
	var sections []ContextSection
	var trace []string

	if conv.ThreadID != "" {
		d, err := s.threads.Detail(ctx, conv.ThreadID)
		if err != nil {
			return nil, nil, err
		}
		var b strings.Builder
		b.WriteString("Название: " + d.Thread.Title + "\n")
		b.WriteString("Состояние: " + d.Thread.State + "\n")
		if d.Thread.Origin != "" {
			b.WriteString("Происхождение: " + d.Thread.Origin + "\n")
		}
		for _, pos := range d.Positions {
			if pos.ValidUntil != nil {
				continue
			}
			who := "Владелец"
			if pos.Owner == thread.OwnerBarrymore {
				who = "Ты сам"
			}
			b.WriteString(fmt.Sprintf("Позиция (%s): %s\n", who, pos.Statement))
		}
		for _, dec := range d.Decisions {
			b.WriteString("Решение: " + dec.Statement + "\n")
		}
		open := 0
		for _, q := range d.Questions {
			if q.Status == thread.QuestionOpen {
				b.WriteString("Открытый вопрос: " + q.Question + "\n")
				open++
			}
		}
		sections = append(sections, ContextSection{Title: "Текущая нить", Body: b.String()})
		trace = append(trace, fmt.Sprintf("нить %s: позиций %d, решений %d, открытых вопросов %d",
			d.Thread.ID, len(d.Positions), len(d.Decisions), open))
	}

	items, err := s.memory.Active(ctx, 40)
	if err != nil {
		return nil, nil, err
	}
	if len(items) > 0 {
		var b strings.Builder
		for _, it := range items {
			b.WriteString("- [" + it.Type + "] " + it.Content + "\n")
		}
		sections = append(sections, ContextSection{Title: "Подтверждённая память", Body: b.String()})
		trace = append(trace, fmt.Sprintf("подтверждённая память: %d записей", len(items)))
	} else {
		trace = append(trace, "подтверждённой памяти пока нет")
	}

	if s.rt != nil {
		open, err := s.rt.Discrepancies(ctx, true, 10)
		if err != nil {
			return nil, nil, err
		}
		if len(open) > 0 {
			var b strings.Builder
			for _, d := range open {
				b.WriteString(fmt.Sprintf("- %s (%s): ожидалось «%s», наблюдалось «%s»\n",
					d.Kind, d.Severity, d.Expected, d.Observed))
			}
			sections = append(sections, ContextSection{
				Title: "Открытые расхождения в работе системы", Body: b.String(),
			})
			trace = append(trace, fmt.Sprintf("открытых расхождений: %d", len(open)))
		}
	}

	return sections, trace, nil
}

// history возвращает последние реплики разговора для передачи модели.
func (s *Service) history(ctx context.Context, conversationID string) ([]model.Message, error) {
	msgs, err := s.Messages(ctx, conversationID, s.maxHistory)
	if err != nil {
		return nil, err
	}
	out := make([]model.Message, 0, len(msgs))
	for _, m := range msgs {
		role := model.RoleUser
		if m.Role == RoleBarrymore {
			role = model.RoleAssistant
		}
		out = append(out, model.Message{Role: role, Content: m.Content})
	}
	return out, nil
}

// Projections регистрирует проекторы разговора.
func (s *Service) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvConversationStarted, projectConversation)
	reg.On(EvMessageRecorded, projectMessage)
	// Предложение — запись аудита: состояние меняют кандидаты и позиции,
	// у которых собственные события.
	reg.OnAudit(EvProposalReceived)
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
