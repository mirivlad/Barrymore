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
	ID                        string  `json:"id"`
	ConversationID            string  `json:"conversation_id"`
	ThreadID                  string  `json:"thread_id,omitempty"`
	Role                      string  `json:"role"`
	Content                   string  `json:"content"`
	Provider                  string  `json:"provider,omitempty"`
	Model                     string  `json:"model,omitempty"`
	PromptTokens              int     `json:"prompt_tokens,omitempty"`
	OutputTokens              int     `json:"output_tokens,omitempty"`
	LatencyMS                 int64   `json:"latency_ms,omitempty"`
	PromptMS                  float64 `json:"prompt_ms,omitempty"`
	GenerationMS              float64 `json:"generation_ms,omitempty"`
	PromptTokensPerSecond     float64 `json:"prompt_tokens_per_second,omitempty"`
	GenerationTokensPerSecond float64 `json:"generation_tokens_per_second,omitempty"`
	TurnLatencyMS             int64   `json:"turn_latency_ms,omitempty"`
	// EpisodeID связывает финальную реплику с единицей опыта, которую можно
	// явно оценить. Старые сообщения до появления Episode остаются без связи.
	EpisodeID string `json:"episode_id,omitempty"`
	// Feedback — последняя явная оценка владельца для Episode. Это read-model
	// поле: в событие сообщения оно не обязано попадать и не является фактом.
	Feedback string `json:"feedback,omitempty"`
	// RetrievalTrace показывает, что именно было подано модели.
	// Без него нельзя понять, почему она ответила так, а не иначе.
	RetrievalTrace []string  `json:"retrieval_trace,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Proposal — то, что модель вернула сверх текста ответа.
//
// Это предложения, а не изменения состояния. Единственное исключение по
// времени — Research: он исполняется runtime до показа финального ответа и
// только через зарегистрированную read-only capability.
type Proposal struct {
	Reply string `json:"reply"`
	// Research — какое наблюдение нужно сделать, прежде чем отвечать. Пустой
	// CapabilityID означает, что имеющихся данных достаточно.
	Research ResearchProposal `json:"research"`
	// ThreadMatch — к какой нити относится разговор.
	ThreadMatch *ThreadMatch `json:"thread_match,omitempty"`
	// ThreadState — каноническое состояние нити после этого хода.
	ThreadState *StateProposal `json:"thread_state,omitempty"`
	// ThreadPosition — как Бэрримор формулирует свою позицию по нити.
	ThreadPosition   *PositionProposal   `json:"thread_position,omitempty"`
	MemoryCandidates []MemoryProposal    `json:"memory_candidates"`
	WorkOrders       []WorkOrderProposal `json:"work_order_proposals"`
	// OwnActions — то, что Бэрримор берётся сделать сам после ответа/с ведома
	// владельца. Research сюда не относится: исследование закрывает пробел в
	// знаниях до финального ответа.
	OwnActions    []OwnActionProposal `json:"own_actions"`
	OpenQuestions []string            `json:"open_questions"`
}

// ResearchProposal — один следующий шаг исследования.
//
// Модель выбирает только capability из показанного runtime каталога. Args —
// данные для её типизированного контракта, а не команда, shell или URL,
// исполняемый напрямую из текста модели.
type ResearchProposal struct {
	CapabilityID string          `json:"capability_id"`
	Args         json.RawMessage `json:"args"`
	Why          string          `json:"why"`
}

// OwnActionProposal — предложение применить собственное умение.
//
// ADR 0019: умение выбирается из показанного списка, каталог проверяется
// политикой. Ни команды, ни аргументов сверх каталога модель не задаёт —
// шаги умения заданы самим умением.
type OwnActionProposal struct {
	SkillID string `json:"skill_id"`
	Target  string `json:"target"`
	Why     string `json:"why"`
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

// StateProposal — предложенное каноническое состояние нити после финального
// ответа. Промежуточные исследовательские предложения не применяются.
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
type MemoryProposal struct {
	Type        string  `json:"type"`
	Content     string  `json:"content"`
	Reason      string  `json:"reason"`
	Sensitivity string  `json:"sensitivity"`
	Confidence  float64 `json:"confidence"`
}

// WorkOrderProposal — предложение поручить работу исполнителю.
type WorkOrderProposal struct {
	Title              string   `json:"title"`
	Goal               string   `json:"goal"`
	Why                string   `json:"why"`
	WorkspaceHint      string   `json:"workspace_hint,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	NeedsWrite         bool     `json:"needs_write,omitempty"`
}

// Turn — итог одного хода разговора.
type Turn struct {
	UserMessage      Message             `json:"user_message"`
	Reply            Message             `json:"reply"`
	Proposal         Proposal            `json:"proposal"`
	MemoryCandidates []MemoryCandidateID `json:"memory_candidates"`
	// EpisodeID — единица опыта завершённого ответа. Она есть и когда Research
	// не потребовался: тогда технический outcome остаётся unknown, пока не было
	// объективной проверки; пользовательский feedback хранится отдельно.
	EpisodeID  string        `json:"episode_id,omitempty"`
	Thread     ThreadOutcome `json:"thread"`
	OwnActions []OwnAction   `json:"own_actions,omitempty"`
}

// OwnAction — проверенное предложение применить умение.
type OwnAction struct {
	SkillID  string `json:"skill_id"`
	Title    string `json:"title"`
	Question string `json:"question"`
	Target   string `json:"target,omitempty"`
	Why      string `json:"why,omitempty"`
	Refused  string `json:"refused,omitempty"`
}

// ThreadOutcome — судьба нити после хода.
type ThreadOutcome struct {
	ThreadID string             `json:"thread_id,omitempty"`
	Title    string             `json:"title,omitempty"`
	Attached bool               `json:"attached,omitempty"`
	Why      string             `json:"why,omitempty"`
	Proposed *NewThreadProposal `json:"proposed,omitempty"`
	Refused  string             `json:"refused,omitempty"`
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
	Auto    bool   `json:"auto"`
	Reason  string `json:"reason"`
	ItemID  string `json:"item_id,omitempty"`
}

// Типы событий разговора.
const (
	EvConversationStarted = "conversation.started"
	EvMessageRecorded     = "conversation.message.recorded"
	EvProposalReceived    = "conversation.proposal.received"
	EvThreadAttached      = "conversation.thread.attached"
	EvThreadDetached      = "conversation.thread.detached"
	EvTurnQueued          = "conversation.turn.queued"
	EvTurnStarted         = "conversation.turn.started"
	EvTurnStageChanged    = "conversation.turn.stage.changed"
	EvTurnCompleted       = "conversation.turn.completed"
	EvTurnFailed          = "conversation.turn.failed"
	EvTurnInterrupted     = "conversation.turn.interrupted"
)

type proposalPayload struct {
	MessageID string   `json:"message_id"`
	Proposal  Proposal `json:"proposal"`
}

type threadLinkPayload struct {
	ConversationID string    `json:"conversation_id"`
	ThreadID       string    `json:"thread_id,omitempty"`
	Previous       string    `json:"previous,omitempty"`
	Why            string    `json:"why,omitempty"`
	At             time.Time `json:"at"`
}

const StreamType = "conversation"

var ProjectionTables = []string{"conversations", "messages", "conversation_turn_runs"}

// ResponseSchema — схема, к которой принуждается ответ модели.
func ResponseSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["reply", "research", "thread_match", "thread_state", "memory_candidates",
               "own_actions", "work_order_proposals", "open_questions"],
  "properties": {
    "reply": {
      "type": "string",
      "description": "Финальный ответ владельцу. Если research.capability_id не пуст, это промежуточный черновик и он владельцу не показывается; допустима пустая строка."
    },
    "research": {
      "type": "object",
      "additionalProperties": false,
      "required": ["capability_id", "args", "why"],
      "properties": {
        "capability_id": {
          "type": "string",
          "description": "Следующая read-only capability из раздела исследовательских возможностей. Пустая строка означает: достаточно данных, можно дать финальный ответ."
        },
        "args": {
          "type": "object",
          "description": "Аргументы типизированной capability. Для capability без аргументов — пустой объект."
        },
        "why": {"type": "string", "description": "Какой пробел в знаниях закроет это наблюдение."}
      }
    },
    "thread_match": {
      "type": "object",
      "additionalProperties": false,
      "required": ["thread_id", "new_thread_title", "new_thread_kind", "why"],
      "properties": {
        "thread_id": {"type": "string"},
        "new_thread_title": {"type": "string"},
        "new_thread_kind": {
          "type": "string",
          "enum": ["", "project", "idea", "problem", "decision", "conversation", "research", "waiting", "personal"]
        },
        "why": {"type": "string"}
      }
    },
    "thread_state": {
      "type": "object",
      "additionalProperties": false,
      "required": ["goal", "situation", "next_step", "obstacles", "waiting"],
      "properties": {
        "goal": {"type": "string"},
        "situation": {"type": "string"},
        "next_step": {"type": "string"},
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
    "own_actions": {
      "type": "array",
      "description": "Действия/умения после финального ответа. Не используй их вместо research для получения недостающего read-only evidence.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["skill_id", "target", "why"],
        "properties": {
          "skill_id": {"type": "string"},
          "target": {"type": "string"},
          "why": {"type": "string"}
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
          "title": {"type": "string"},
          "goal": {"type": "string"},
          "why": {"type": "string"},
          "workspace_hint": {"type": "string"},
          "acceptance_criteria": {"type": "array", "items": {"type": "string"}},
          "needs_write": {"type": "boolean"}
        }
      }
    },
    "open_questions": {"type": "array", "items": {"type": "string"}}
  }
}`)
}
