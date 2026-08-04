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
	// ThreadPosition — как Бэрримор формулирует свою позицию по нити.
	ThreadPosition   *PositionProposal   `json:"thread_position,omitempty"`
	MemoryCandidates []MemoryProposal    `json:"memory_candidates"`
	WorkOrders       []WorkOrderProposal `json:"work_order_proposals"`
	OpenQuestions    []string            `json:"open_questions"`
}

// PositionProposal — предложенная позиция Бэрримора.
type PositionProposal struct {
	Statement  string  `json:"statement"`
	Confidence float64 `json:"confidence"`
	Basis      string  `json:"basis"`
}

// MemoryProposal — предложение запомнить.
type MemoryProposal struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Reason  string `json:"reason"`
}

// WorkOrderProposal — предложение поручить работу исполнителю.
//
// Это только предложение: поручение создаётся отдельным действием владельца,
// проходит выбор исполнителя, политику стоимости и подтверждение.
type WorkOrderProposal struct {
	Goal          string `json:"goal"`
	Why           string `json:"why"`
	WorkspaceHint string `json:"workspace_hint,omitempty"`
}

// Turn — итог одного хода разговора.
type Turn struct {
	UserMessage      Message             `json:"user_message"`
	Reply            Message             `json:"reply"`
	Proposal         Proposal            `json:"proposal"`
	MemoryCandidates []MemoryCandidateID `json:"memory_candidates"`
}

// MemoryCandidateID — созданный кандидат в память.
type MemoryCandidateID struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Типы событий разговора.
const (
	EvConversationStarted = "conversation.started"
	EvMessageRecorded     = "conversation.message.recorded"
	EvProposalReceived    = "conversation.proposal.received"
)

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
  "required": ["reply", "memory_candidates", "work_order_proposals", "open_questions"],
  "properties": {
    "reply": {
      "type": "string",
      "description": "Ответ Бэрримора владельцу на русском языке."
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
        "required": ["type", "content", "reason"],
        "properties": {
          "type": {"type": "string", "enum": ["fact", "preference", "decision", "open_question", "known_failure"]},
          "content": {"type": "string"},
          "reason": {"type": "string"}
        }
      }
    },
    "work_order_proposals": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["goal", "why"],
        "properties": {
          "goal": {"type": "string"},
          "why": {"type": "string"},
          "workspace_hint": {"type": "string"}
        }
      }
    },
    "open_questions": {"type": "array", "items": {"type": "string"}}
  }
}`)
}
