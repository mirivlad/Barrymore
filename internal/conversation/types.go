// Package conversation ведёт разговор с владельцем.
//
// Ключевое ограничение (03_SYSTEM_ARCHITECTURE §6): модель возвращает только
// предложения. Она не пишет память, не создаёт поручений и не выполняет
// побочных действий. Runtime валидирует ответ по схеме и превращает
// предложения в видимых кандидатов, которые владелец принимает или отклоняет.
package conversation

import (
	"encoding/json"
	"time"
)

// Роли участников разговора.
const (
	RolePerson    = "person"
	RoleBarrymore = "barrymore"
)

// Conversation — сессия общения. Может затрагивать несколько нитей.
type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message — реплика.
type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	ThreadID       string `json:"thread_id,omitempty"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	PromptTokens   int    `json:"prompt_tokens,omitempty"`
	OutputTokens   int    `json:"output_tokens,omitempty"`
	LatencyMS      int64  `json:"latency_ms,omitempty"`
	// RetrievalTrace показывает, что именно было подано модели.
	// Без него нельзя понять, почему она ответила так, а не иначе.
	RetrievalTrace []string  `json:"retrieval_trace,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Proposal — то, что модель вернула сверх текста ответа.
//
// Это предложения, а не изменения состояния.
type Proposal struct {
	Reply string `json:"reply"`
	// ThreadMatch — к какой нити относится разговор.
	ThreadMatch *ThreadMatch `json:"thread_match,omitempty"`
	// ThreadState — каноническое состояние нити после этого хода.
	ThreadState *StateProposal `json:"thread_state,omitempty"`
	// ThreadPosition — как Бэрримор формулирует свою позицию по нити.
	ThreadPosition   *PositionProposal   `json:"thread_position,omitempty"`
	MemoryCandidates []MemoryProposal    `json:"memory_candidates"`
	WorkOrders       []WorkOrderProposal `json:"work_order_proposals"`
	OpenQuestions    []string            `json:"open_questions"`
}

// ThreadMatch — к какой нити Бэрримор относит разговор.
//
// Либо к уже существующей — тогда заполнен ThreadID, и он обязан быть из
// списка, который runtime сам показал модели. Либо ни к какой из них — тогда
// предлагается завести новую, и решает это владелец.
type ThreadMatch struct {
	ThreadID       string `json:"thread_id"`
	NewThreadTitle string `json:"new_thread_title"`
	NewThreadKind  string `json:"new_thread_kind"`
	Why            string `json:"why"`
}

// StateProposal — предложенное каноническое состояние нити.
//
// Не пересказ разговора: то, что Бэрримор утверждает о нити и за что отвечает.
type StateProposal struct {
	Goal      string   `json:"goal"`
	Situation string   `json:"situation"`
	NextStep  string   `json:"next_step"`
	Obstacles []string `json:"obstacles"`
	Waiting   []string `json:"waiting"`
}

// Empty сообщает, что состояние не сформулировано.
func (s StateProposal) Empty() bool {
	return s.Goal == "" && s.Situation == "" && s.NextStep == "" &&
		len(s.Obstacles) == 0 && len(s.Waiting) == 0
}

// PositionProposal — предложенная позиция Бэрримора.
type PositionProposal struct {
	Statement  string  `json:"statement"`
	Confidence float64 `json:"confidence"`
	Basis      string  `json:"basis"`
}

// MemoryProposal — предложение запомнить.
//
// Чувствительность и уверенность определяет сам Бэрримор: от них зависит,
// запишет ли он сведение сам или вынесет на решение владельца.
type MemoryProposal struct {
	Type        string  `json:"type"`
	Content     string  `json:"content"`
	Reason      string  `json:"reason"`
	Sensitivity string  `json:"sensitivity"`
	Confidence  float64 `json:"confidence"`
}

// WorkOrderProposal — предложение поручить работу исполнителю.
//
// Это только предложение: поручение создаётся отдельным действием владельца,
// проходит выбор исполнителя, политику стоимости и подтверждение.
//
// Заполнено оно целиком — заголовок, цель, причина, каталог, критерии приёмки.
// Половина полей означала бы, что вторую половину придётся печатать заново,
// а именно этого владелец и не должен делать.
type WorkOrderProposal struct {
	Title              string   `json:"title"`
	Goal               string   `json:"goal"`
	Why                string   `json:"why"`
	WorkspaceHint      string   `json:"workspace_hint,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	// NeedsWrite — нужна ли исполнителю правка файлов. Это предложение, а не
	// разрешение: запись включается подтверждением владельца, и в подтверждении
	// она названа прямо.
	NeedsWrite bool `json:"needs_write,omitempty"`
}

// Turn — итог одного хода разговора.
type Turn struct {
	UserMessage      Message             `json:"user_message"`
	Reply            Message             `json:"reply"`
	Proposal         Proposal            `json:"proposal"`
	MemoryCandidates []MemoryCandidateID `json:"memory_candidates"`
	// Thread сообщает, что стало с нитью разговора на этом ходу.
	Thread ThreadOutcome `json:"thread"`
}

// ThreadOutcome — судьба нити после хода.
//
// Разделение намеренное: связать разговор с уже существующей нитью Бэрримор
// может сам — это обратимо и ничего не создаёт. Завести новую нить он только
// предлагает: сущности, появляющиеся без ведома владельца, засоряют систему
// быстрее, чем приносят пользу.
type ThreadOutcome struct {
	// ThreadID — нить разговора после хода, если она есть.
	ThreadID string `json:"thread_id,omitempty"`
	Title    string `json:"title,omitempty"`
	// Attached сообщает, что Бэрримор связал разговор с нитью на этом ходу.
	Attached bool `json:"attached,omitempty"`
	// Why объясняет выбор — и связывание, и предложение.
	Why string `json:"why,omitempty"`
	// Proposed заполнен, когда подходящей нити не нашлось.
	Proposed *NewThreadProposal `json:"proposed,omitempty"`
	// Refused объясняет, почему предложение модели не применено.
	Refused string `json:"refused,omitempty"`
}

// NewThreadProposal — предложение завести нить, готовое к одному нажатию.
type NewThreadProposal struct {
	Title string        `json:"title"`
	Kind  string        `json:"kind"`
	Why   string        `json:"why"`
	State StateProposal `json:"state"`
}

// MemoryCandidateID — созданный кандидат в память.
type MemoryCandidateID struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
	// Auto сообщает, записал ли Бэрримор это сам.
	Auto bool `json:"auto"`
	// Reason объясняет решение.
	Reason string `json:"reason"`
	// ItemID заполнен, если запись уже сделана.
	ItemID string `json:"item_id,omitempty"`
}

// Типы событий разговора.
const (
	EvConversationStarted = "conversation.started"
	EvMessageRecorded     = "conversation.message.recorded"
	EvProposalReceived    = "conversation.proposal.received"
	// EvThreadAttached — разговор отнесён к нити.
	EvThreadAttached = "conversation.thread.attached"
	// EvThreadDetached — связь с нитью снята.
	EvThreadDetached = "conversation.thread.detached"
)

// proposalPayload — запись о том, что Бэрримор предложил в этом ходу.
//
// Она и есть источник правды для последующих действий владельца: поручение
// оформляется из неё, а не из того, что прислал браузер.
type proposalPayload struct {
	MessageID string   `json:"message_id"`
	Proposal  Proposal `json:"proposal"`
}

// threadLinkPayload — событие о связи разговора с нитью.
type threadLinkPayload struct {
	ConversationID string    `json:"conversation_id"`
	ThreadID       string    `json:"thread_id,omitempty"`
	Previous       string    `json:"previous,omitempty"`
	Why            string    `json:"why,omitempty"`
	At             time.Time `json:"at"`
}

// StreamType — тип потока событий разговора.
const StreamType = "conversation"

// ProjectionTables — таблицы проекций разговора.
var ProjectionTables = []string{"conversations", "messages"}

// ResponseSchema — схема, к которой принуждается ответ модели.
//
// ADR 0012: у llama-server это работает на уровне сэмплера, поэтому
// разбор не встречает мусора, а невалидный ответ не может частично
// изменить состояние.
func ResponseSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["reply", "thread_match", "thread_state", "memory_candidates",
               "work_order_proposals", "open_questions"],
  "properties": {
    "reply": {
      "type": "string",
      "description": "Ответ Бэрримора владельцу на русском языке."
    },
    "thread_match": {
      "type": "object",
      "additionalProperties": false,
      "required": ["thread_id", "new_thread_title", "new_thread_kind", "why"],
      "properties": {
        "thread_id": {
          "type": "string",
          "description": "Идентификатор нити из раздела «Нити, которые уже есть», если разговор о ней. Иначе пустая строка. Придумывать идентификатор нельзя."
        },
        "new_thread_title": {
          "type": "string",
          "description": "Короткое название новой нити, если разговор не относится ни к одной из существующих. Иначе пустая строка."
        },
        "new_thread_kind": {
          "type": "string",
          "enum": ["", "project", "idea", "problem", "decision", "conversation", "research", "waiting", "personal"]
        },
        "why": {"type": "string", "description": "Почему именно эта нить."}
      }
    },
    "thread_state": {
      "type": "object",
      "additionalProperties": false,
      "required": ["goal", "situation", "next_step", "obstacles", "waiting"],
      "properties": {
        "goal": {"type": "string", "description": "Чего мы хотим добиться в этой нити."},
        "situation": {"type": "string", "description": "Где остановились прямо сейчас."},
        "next_step": {"type": "string", "description": "Какой шаг следующий."},
        "obstacles": {"type": "array", "items": {"type": "string"}},
        "waiting": {"type": "array", "items": {"type": "string"}}
      }
    },
    "thread_position": {
      "type": "object",
      "additionalProperties": false,
      "required": ["statement", "confidence", "basis"],
      "properties": {
        "statement": {"type": "string"},
        "confidence": {"type": "number", "minimum": 0, "maximum": 1},
        "basis": {"type": "string"}
      }
    },
    "memory_candidates": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["type", "content", "reason", "sensitivity", "confidence"],
        "properties": {
          "type": {"type": "string", "enum": ["fact", "preference", "decision", "open_question", "known_failure"]},
          "content": {"type": "string"},
          "reason": {"type": "string"},
          "sensitivity": {"type": "string", "enum": ["normal", "sensitive", "private"]},
          "confidence": {"type": "number", "minimum": 0, "maximum": 1}
        }
      }
    },
    "work_order_proposals": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "goal", "why", "workspace_hint", "acceptance_criteria", "needs_write"],
        "properties": {
          "title": {"type": "string", "description": "Короткий заголовок поручения."},
          "goal": {"type": "string", "description": "Что именно должен сделать исполнитель."},
          "why": {"type": "string", "description": "Почему это нужно сейчас."},
          "workspace_hint": {"type": "string", "description": "Абсолютный путь к каталогу работы, если он известен из разговора или памяти."},
          "acceptance_criteria": {
            "type": "array",
            "items": {"type": "string"},
            "description": "По каким признакам будет видно, что работа сделана."
          },
          "needs_write": {"type": "boolean", "description": "Нужно ли исполнителю менять файлы. Если достаточно прочитать — false."}
        }
      }
    },
    "open_questions": {"type": "array", "items": {"type": "string"}}
  }
}`)
}
