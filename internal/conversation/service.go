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
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/learning"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/research"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/skill"
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
	db         *store.DB
	journal    *event.Journal
	clock      clock.Clock
	provider   model.Provider
	threads    *thread.Service
	memory     *memory.Service
	experience *experience.Service
	research   *research.Registry
	rt         *runtime.Runtime
	skills     SkillCatalog
	practices  PracticeCatalog
	identity   Identity
	log        *slog.Logger

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
	Experience *experience.Service
	Research   *research.Registry
	Runtime    *runtime.Runtime
	Skills     SkillCatalog
	Practices  PracticeCatalog
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
	if cfg.Experience == nil && cfg.DB != nil && cfg.Journal != nil {
		cfg.Experience = experience.New(cfg.DB, cfg.Journal, cfg.Clock)
	}
	if cfg.Research == nil {
		cfg.Research = research.New()
		if err := research.RegisterProviderInspector(cfg.Research, cfg.Provider, cfg.Clock); err != nil {
			cfg.Logger.Error("research capability провайдера не зарегистрирована", "error", err)
		}
	}
	return &Service{
		db: cfg.DB, journal: cfg.Journal, clock: cfg.Clock, provider: cfg.Provider,
		threads: cfg.Threads, memory: cfg.Memory, experience: cfg.Experience,
		research: cfg.Research, rt: cfg.Runtime,
		skills: cfg.Skills, practices: cfg.Practices,
		identity: cfg.Identity, log: cfg.Logger,
		maxHistory: cfg.MaxHistory, maxTokens: cfg.MaxTokens,
	}
}

// Experience возвращает долговечный опыт, которым владеет разговорный контур.
// API использует этот же экземпляр для feedback: второй сервис поверх той же
// базы был бы работоспособен, но скрывал бы реальную границу владения.
func (s *Service) Experience() *experience.Service { return s.experience }

// Research возвращает каталог безопасных read-only исследований.
func (s *Service) Research() *research.Registry { return s.research }

// SkillCatalog перечисляет то, что Бэрримор умеет сам.
//
// Разговор берёт список отсюда и показывает его модели. Придуманное умение
// runtime отвергает — граница полномочий совпадает с тем, что система
// показала сама (тот же порядок, что и с нитями, ADR 0018).
type SkillCatalog interface {
	Live() []skill.Skill
	// Ambient — то, что видно о машине без всякого умения.
	Ambient(ctx context.Context) []skill.Fact
}

// PracticeCatalog отдаёт то, чему научил опыт.
//
// Без него список способов выглядит ровным перечнем, и Бэрримор всякий раз
// выбирает как в первый: что однажды не сработало, ничем не отличается от
// того, что работает годами.
type PracticeCatalog interface {
	Practices(ctx context.Context) ([]learning.Practice, error)
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

	sections, trace, offered, err := s.buildContext(ctx, conv)
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

	// Нить определяется до всего остального: позиция, открытые вопросы и
	// состояние принадлежат ей, и без неё им негде оказаться.
	turn.Thread = s.settleThread(ctx, &conv, proposal, offered)

	// Собственные умения проверяются по показанному списку — тем же правилом,
	// что и нити. Придуманное умение не отбрасывается молча: владелец видит
	// отказ и его причину.
	turn.OwnActions = s.settleOwnActions(proposal.OwnActions)

	// Предложения превращаются в видимых кандидатов. Ничего не записывается
	// в память молча (00_PRODUCT_VISION §9.2).
	for _, mc := range proposal.MemoryCandidates {
		res, err := s.memory.Propose(ctx, memory.ProposeRequest{
			Type: mc.Type, Content: mc.Content, Reason: mc.Reason,
			Sensitivity: mc.Sensitivity, Confidence: mc.Confidence,
			ProposedBy: memory.ProposedByBarrymore, ThreadID: conv.ThreadID,
			ConversationID: conv.ID, MessageID: replyMsg.ID,
		})
		if err != nil {
			s.log.Error("кандидат в память не создан", "conversation", conv.ID, "error", err)
			continue
		}
		entry := MemoryCandidateID{
			ID: res.Candidate.ID, Type: res.Candidate.Type,
			Content: res.Candidate.Content, Auto: res.Auto, Reason: res.Reason,
		}
		if res.Item != nil {
			entry.ItemID = res.Item.ID
		}
		turn.MemoryCandidates = append(turn.MemoryCandidates, entry)
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

	// Каноническое состояние нити ведёт Бэрримор, а не владелец: если бы
	// его надо было заполнять руками, нить снова стала бы карточкой проекта.
	if proposal.ThreadState != nil && conv.ThreadID != "" && !proposal.ThreadState.Empty() {
		if _, err := s.threads.SetCanon(ctx, conv.ThreadID,
			patchFromProposal(*proposal.ThreadState),
			thread.CanonFromTalk, "по итогам разговора",
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			s.log.Error("состояние нити не записано", "thread", conv.ThreadID, "error", err)
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

// Report записывает в разговор то, что Бэрримор сделал сам.
//
// Это не ответ модели: реплика появляется без единого обращения к провайдеру,
// потому что смотреть Бэрримор умеет сам. Модель, пересказывающая увиденное,
// добавила бы к факту только задержку и возможность его исказить.
func (s *Service) Report(ctx context.Context, conversationID, text string) (Message, error) {
	conv, err := s.Get(ctx, conversationID)
	if err != nil {
		return Message{}, err
	}
	return s.record(ctx, conv, Message{
		ConversationID: conv.ID, ThreadID: conv.ThreadID,
		Role: RoleBarrymore, Content: text,
	}, event.Actor{Type: event.ActorBarrymore})
}

// settleOwnActions оставляет только те умения, которые Бэрримор действительно
// умеет, и объясняет отказ по остальным.
func (s *Service) settleOwnActions(props []OwnActionProposal) []OwnAction {
	if len(props) == 0 {
		return nil
	}
	live := map[string]skill.Skill{}
	if s.skills != nil {
		for _, sk := range s.skills.Live() {
			live[sk.ID] = sk
		}
	}

	var out []OwnAction
	seen := map[string]bool{}
	for _, p := range props {
		id := strings.TrimSpace(p.SkillID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		sk, ok := live[id]
		if !ok {
			s.log.Warn("предложено несуществующее умение", "skill", id)
			out = append(out, OwnAction{
				SkillID: id,
				Refused: fmt.Sprintf("умения %q у меня нет — я могу только то, "+
					"что перечислено в разделе умений", id),
			})
			continue
		}
		out = append(out, OwnAction{
			SkillID: sk.ID, Title: sk.Title, Question: sk.Question,
			Target: strings.TrimSpace(p.Target), Why: strings.TrimSpace(p.Why),
		})
	}
	return out
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
			Payload: proposalPayload{MessageID: messageID, Proposal: p},
		})
		return err
	})
	return err
}

// LastProposalMessage возвращает реплику, к которой относится последнее
// предложение. Без неё восстановленное предложение не на что сослаться.
func (s *Service) LastProposalMessage(ctx context.Context, conversationID string) (string, error) {
	envs, err := s.journal.Stream(ctx, StreamType, conversationID)
	if err != nil {
		return "", err
	}
	out := ""
	for _, env := range envs {
		if env.EventType != EvProposalReceived {
			continue
		}
		var p proposalPayload
		if err := env.Decode(&p); err != nil {
			return "", err
		}
		out = p.MessageID
	}
	return out, nil
}

// ProposalFor возвращает предложение, которое Бэрримор сделал в этом ходу.
//
// Читается из журнала, а не принимается от клиента. Разница принципиальна:
// владелец подтверждает то, что Бэрримор действительно сказал, а не то, что
// браузер прислал обратно под видом сказанного.
//
// Пустой messageID означает «последнее предложение разговора».
func (s *Service) ProposalFor(ctx context.Context, conversationID, messageID string) (Proposal, error) {
	envs, err := s.journal.Stream(ctx, StreamType, conversationID)
	if err != nil {
		return Proposal{}, err
	}
	var found *Proposal
	for _, env := range envs {
		if env.EventType != EvProposalReceived {
			continue
		}
		var p proposalPayload
		if err := env.Decode(&p); err != nil {
			return Proposal{}, err
		}
		if messageID != "" && p.MessageID != messageID {
			continue
		}
		proposal := p.Proposal
		found = &proposal
	}
	if found == nil {
		return Proposal{}, fmt.Errorf("%w: предложение в разговоре %s", ErrNotFound, conversationID)
	}
	return *found, nil
}

// buildContext собирает разделы контекста, след извлечения и список нитей,
// среди которых модели позволено выбирать.
//
// Для каждого элемента сохраняется retrieval trace: владелец должен видеть,
// что именно было подано модели (04_MEMORY_AND_CONTINUITY §7).
//
// Возвращаемый список нитей — не украшение ответа, а граница полномочий:
// связать разговор можно только с тем, что было показано. Сослаться на нить,
// которой не предлагали, значит сослаться на догадку.
func (s *Service) buildContext(ctx context.Context, conv Conversation) (
	[]ContextSection, []string, map[string]string, error) {

	var sections []ContextSection
	var trace []string
	offered := map[string]string{}

	if conv.ThreadID != "" {
		d, err := s.threads.Detail(ctx, conv.ThreadID)
		if err != nil {
			return nil, nil, nil, err
		}
		offered[d.Thread.ID] = d.Thread.Title
		var b strings.Builder
		b.WriteString("Название: " + d.Thread.Title + "\n")
		b.WriteString("Состояние: " + d.Thread.State + "\n")
		if d.Thread.Origin != "" {
			b.WriteString("Происхождение: " + d.Thread.Origin + "\n")
		}
		b.WriteString(describeCanon(d.Thread.Canon))
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
	} else {
		// Разговор ещё ни к чему не отнесён. Показываем живые нити, чтобы
		// Бэрримор мог узнать среди них ту, о которой идёт речь, — иначе он
		// вынужден предлагать новую всякий раз, и владелец получает
		// десять нитей об одном и том же.
		open, err := s.threads.List(ctx, thread.ListFilter{
			States: []string{thread.StateActive, thread.StateMaturing,
				thread.StateWaiting, thread.StateBlocked},
			Limit: 25,
		})
		if err != nil {
			return nil, nil, nil, err
		}
		var b strings.Builder
		for _, t := range open {
			offered[t.ID] = t.Title
			b.WriteString("- " + t.ID + " — " + t.Title)
			if t.Canon.Goal != "" {
				b.WriteString("; цель: " + t.Canon.Goal)
			}
			if t.Canon.Situation != "" {
				b.WriteString("; где остановились: " + t.Canon.Situation)
			}
			b.WriteString("\n")
		}
		if len(open) == 0 {
			// Пустоту приходится называть вслух. Молчание о том, что нитей нет,
			// модель читает как отсутствие раздела, а не как отсутствие нитей,
			// и выдумывает идентификатор вместо того, чтобы предложить новую.
			// Проверено живьём: первая же реплика на чистой базе.
			b.WriteString("Ни одной нити пока нет. Значит, thread_id обязан быть " +
				"пустым: сослаться не на что.\n")
		}
		sections = append(sections, ContextSection{
			Title: "Нити, которые уже есть", Body: b.String(),
		})
		trace = append(trace, fmt.Sprintf("нитей предложено к сопоставлению: %d", len(open)))
	}

	// Окружение подаётся до всего остального и на каждом ходу.
	//
	// Это ответ на вопрос «что, теперь на каждый вопрос писать умение?».
	// Нет: дешёвые факты о машине, не требующие цели, наблюдает сам runtime
	// и кладёт сюда. Модель не просит их, не ждёт их и не выдумывает —
	// она их просто знает, как знает текущее время.
	if s.skills != nil {
		if facts := s.skills.Ambient(ctx); len(facts) > 0 {
			var b strings.Builder
			for _, f := range facts {
				b.WriteString("- " + f.Text + "\n")
			}
			sections = append(sections, ContextSection{
				Title: "Что ты видишь прямо сейчас", Body: b.String(),
			})
			trace = append(trace, fmt.Sprintf("окружение: %d наблюдений", len(facts)))
		}
	}

	// Собственные умения показываются наравне с нитями и по той же причине:
	// выбрать можно только из показанного. Без этого раздела Бэрримор не знает,
	// что способен посмотреть сам, и зовёт исполнителя даже за `git worktree list`.
	if s.skills != nil {
		live := s.skills.Live()
		var b strings.Builder
		for _, sk := range live {
			b.WriteString("- " + sk.ID + " — " + sk.Title + "; отвечает на вопрос: " + sk.Question)
			if sk.NeedsTarget {
				b.WriteString("; нужен каталог")
			}
			b.WriteString("\n")
		}
		if len(live) == 0 {
			b.WriteString("Умений пока нет: всё, что нужно сделать, придётся поручать.\n")
		}
		sections = append(sections, ContextSection{Title: "Что ты умеешь сам", Body: b.String()})
		trace = append(trace, fmt.Sprintf("собственных умений предложено: %d", len(live)))
	}

	// Опыт показывается рядом со способами и меняет выбор: способ с записью
	// «три неудачи подряд» и способ с записью «семь раз без осечек» не должны
	// выглядеть одинаково.
	if s.practices != nil {
		ps, err := s.practices.Practices(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(ps) > 0 {
			var b strings.Builder
			for _, p := range ps {
				b.WriteString("- " + nonEmptyText(p.Title, p.Ref) + ": " + p.Record() + "\n")
			}
			sections = append(sections, ContextSection{
				Title: "Чему научил опыт", Body: b.String(),
			})
			trace = append(trace, fmt.Sprintf("опыт по способам работы: %d записей", len(ps)))
		}
	}

	items, err := s.memory.Active(ctx, 40)
	if err != nil {
		return nil, nil, nil, err
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
			return nil, nil, nil, err
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

	return sections, trace, offered, nil
}

func nonEmptyText(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// describeCanon переводит каноническое состояние нити в текст для модели.
func describeCanon(c thread.Canon) string {
	var b strings.Builder
	if c.Goal != "" {
		b.WriteString("Цель: " + c.Goal + "\n")
	}
	if c.Situation != "" {
		b.WriteString("Где остановились: " + c.Situation + "\n")
	}
	for _, o := range c.Obstacles {
		b.WriteString("Мешает: " + o + "\n")
	}
	for _, w := range c.Waiting {
		b.WriteString("Ждём: " + w + "\n")
	}
	if c.NextStep != "" {
		b.WriteString("Следующий шаг: " + c.NextStep + "\n")
	}
	return b.String()
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
	reg.On(EvThreadAttached, projectThreadLink)
	reg.On(EvThreadDetached, projectThreadLink)
	// Предложение — запись аудита: состояние меняют кандидаты и позиции,
	// у которых собственные события.
	reg.OnAudit(EvProposalReceived)
	if s.experience != nil {
		s.experience.Projections(reg)
	}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
