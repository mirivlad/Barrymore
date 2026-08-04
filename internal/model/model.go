// Package model — сменяемый слой рассуждения и речи.
//
// ADR 0004: LLM является deliberative layer, а не центром управления. Личность,
// память и доменная модель принадлежат runtime; смена провайдера их не меняет.
// ADR 0012: локальный провайдер — llama-server с OpenAI-совместимым интерфейсом.
package model

import (
	"context"
	"encoding/json"
	"time"
)

// Роли сообщений.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message — одна реплика в запросе к модели.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request — запрос к модели.
type Request struct {
	System   string
	Messages []Message
	// Schema принуждает модель отвечать строго по схеме. Пустая схема означает
	// свободный текст; для предложений runtime всегда задаёт схему, потому что
	// невалидный ответ не должен частично менять состояние.
	Schema json.RawMessage
	// SchemaName попадает в запрос как имя схемы.
	SchemaName  string
	MaxTokens   int
	Temperature float64
	// DisableThinking просит модель не выдавать скрытые цепочки рассуждений.
	//
	// 03_SYSTEM_ARCHITECTURE §13: скрытые chain-of-thought не требуются, нужны
	// структурированные решения и основания. Для размышляющих моделей это ещё
	// и экономит бюджет ответа, который иначе уходит в рассуждение целиком.
	DisableThinking bool
}

// Response — ответ модели.
type Response struct {
	Content          string        `json:"content"`
	Model            string        `json:"model"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	FinishReason     string        `json:"finish_reason"`
	Latency          time.Duration `json:"latency"`
}

// Статусы провайдера.
const (
	StatusReady       = "ready"
	StatusUnreachable = "unreachable"
	StatusNotConfig   = "not_configured"
	StatusBroken      = "broken"
)

// Status — наблюдаемое состояние провайдера.
//
// Как и у исполнителей, это наблюдение с временем и основанием, а не вечный факт.
type Status struct {
	Status     string        `json:"status"`
	Endpoint   string        `json:"endpoint,omitempty"`
	Model      string        `json:"model,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	ObservedAt time.Time     `json:"observed_at"`
	Latency    time.Duration `json:"latency,omitempty"`
	// SupportsSchema сообщает, умеет ли провайдер принуждать ответ к схеме.
	SupportsSchema bool `json:"supports_schema"`
}

// Ready сообщает, можно ли обращаться к провайдеру.
func (s Status) Ready() bool { return s.Status == StatusReady }

// Provider — конкретный поставщик модели.
type Provider interface {
	// ID различает провайдеров в журнале и снимках.
	ID() string
	// Describe даёт человекочитаемое описание для интерфейса.
	Describe() string
	// Probe проверяет доступность. Не должен расходовать платную квоту.
	Probe(ctx context.Context) Status
	// Complete выполняет запрос.
	Complete(ctx context.Context, req Request) (Response, error)
}
