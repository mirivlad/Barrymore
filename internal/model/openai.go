package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// OpenAICompatible — провайдер с интерфейсом OpenAI /v1/chat/completions.
//
// Под этот интерфейс подходят llama-server, локальные серверы и облачные
// endpoint'ы. Бэрримор не различает их в домене: провайдер сменяем.
type OpenAICompatible struct {
	// Endpoint — базовый адрес без /v1.
	Endpoint string
	// Model — имя модели; для llama-server произвольно.
	Model string
	// APIKey передаётся заголовком. Для локального сервера обычно пуст.
	// Значение не логируется и не попадает в снимки.
	APIKey string
	// Label — как показывать провайдера человеку.
	Label string
	// Timeout ограничивает один запрос.
	Timeout time.Duration

	client *http.Client
	now    func() time.Time
}

// NewOpenAICompatible создаёт провайдера.
func NewOpenAICompatible(endpoint, model, apiKey, label string) *OpenAICompatible {
	if label == "" {
		label = endpoint
	}
	return &OpenAICompatible{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Model:    model,
		APIKey:   apiKey,
		Label:    label,
		Timeout:  10 * time.Minute,
		client:   &http.Client{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// ID возвращает идентификатор провайдера.
func (p *OpenAICompatible) ID() string { return "openai-compatible" }

// Describe даёт описание для интерфейса.
func (p *OpenAICompatible) Describe() string {
	if p.Model != "" {
		return fmt.Sprintf("%s (модель %s)", p.Label, p.Model)
	}
	return p.Label
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// Probe спрашивает список моделей — это бесплатно и не грузит модель работой.
func (p *OpenAICompatible) Probe(ctx context.Context) Status {
	st := Status{ObservedAt: p.now(), Endpoint: p.Endpoint, Model: p.Model, SupportsSchema: true}
	if p.Endpoint == "" {
		st.Status = StatusNotConfig
		st.Reason = "адрес провайдера не задан"
		return st
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	started := p.now()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, p.Endpoint+"/v1/models", nil)
	if err != nil {
		st.Status = StatusBroken
		st.Reason = "запрос не построен: " + err.Error()
		return st
	}
	p.auth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		st.Status = StatusUnreachable
		st.Reason = "провайдер не отвечает: " + err.Error()
		return st
	}
	defer resp.Body.Close()
	st.Latency = p.now().Sub(started)

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		st.Status = StatusBroken
		st.Reason = fmt.Sprintf("провайдер ответил %d: %s",
			resp.StatusCode, truncate(string(body), 200))
		return st
	}

	var mr modelsResponse
	if err := json.Unmarshal(body, &mr); err == nil && len(mr.Data) > 0 && st.Model == "" {
		st.Model = mr.Data[0].ID
	}
	st.Status = StatusReady
	st.Reason = "провайдер отвечает"
	return st
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Temperature    float64        `json:"temperature"`
	Stream         bool           `json:"stream"`
	StreamOptions  *streamOptions `json:"stream_options,omitempty"`
	ResponseFormat *respFormat    `json:"response_format,omitempty"`
	// ChatTemplateKwargs понимают llama-server и совместимые серверы.
	// Незнакомый провайдер поле проигнорирует.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type respFormat struct {
	Type       string         `json:"type"`
	JSONSchema *schemaWrapper `json:"json_schema,omitempty"`
}

type schemaWrapper struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			// ReasoningContent отдают размышляющие модели. Оно не является
			// ответом и в домен не попадает.
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type chatStreamResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Timings struct {
		PromptMS           float64 `json:"prompt_ms"`
		PredictedMS        float64 `json:"predicted_ms"`
		PromptPerSecond    float64 `json:"prompt_per_second"`
		PredictedPerSecond float64 `json:"predicted_per_second"`
	} `json:"timings"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete выполняет запрос к модели.
//
// При заданной схеме используется structured output: провайдер принуждает
// ответ к схеме на уровне сэмплера, поэтому разбор не встречает мусора.
func (p *OpenAICompatible) Complete(ctx context.Context, req Request) (Response, error) {
	if p.Endpoint == "" {
		return Response{}, fmt.Errorf("провайдер модели не настроен")
	}

	body := p.chatRequest(req, false)

	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("сериализация запроса к модели: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		p.Endpoint+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("запрос к модели: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.auth(httpReq)

	started := p.now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("провайдер модели не ответил: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return Response{}, fmt.Errorf("чтение ответа модели: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("провайдер модели ответил %d: %s",
			resp.StatusCode, truncate(string(raw), 400))
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Response{}, fmt.Errorf("разбор ответа модели: %w", err)
	}
	if cr.Error != nil {
		return Response{}, fmt.Errorf("провайдер модели вернул ошибку: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return Response{}, fmt.Errorf("провайдер модели вернул пустой ответ")
	}

	content := cr.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" && cr.Choices[0].FinishReason == "length" {
		// Весь бюджет ушёл в рассуждение — ответа нет. Молча вернуть пустую
		// строку значило бы выдать отсутствие ответа за ответ.
		return Response{}, fmt.Errorf(
			"модель израсходовала бюджет ответа на скрытое рассуждение и не дала ответа; " +
				"увеличьте предел токенов или отключите рассуждения")
	}

	return Response{
		Content:          content,
		Model:            cr.Model,
		PromptTokens:     cr.Usage.PromptTokens,
		CompletionTokens: cr.Usage.CompletionTokens,
		FinishReason:     cr.Choices[0].FinishReason,
		Latency:          p.now().Sub(started),
	}, nil
}

// CompleteStream privately assembles an OpenAI-compatible SSE response. The
// callback receives counters only; JSON fragments never cross this boundary.
func (p *OpenAICompatible) CompleteStream(ctx context.Context, req Request,
	onProgress func(Progress)) (Response, error) {
	if p.Endpoint == "" {
		return Response{}, fmt.Errorf("провайдер модели не настроен")
	}

	body := p.chatRequest(req, true)
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("сериализация запроса к модели: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		p.Endpoint+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("запрос к модели: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	p.auth(httpReq)

	started := p.now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("провайдер модели не ответил: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, fmt.Errorf("провайдер модели ответил %d: %s",
			resp.StatusCode, truncate(string(raw), 400))
	}

	const maxResponseBytes = 16 << 20
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
	var content strings.Builder
	outputRunes := 0
	var result Response
	var dataLines []string
	done := false
	consume := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk chatStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("разбор stream ответа модели: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("провайдер модели вернул ошибку: %s", chunk.Error.Message)
		}
		if chunk.Model != "" {
			result.Model = chunk.Model
		}
		for _, choice := range chunk.Choices {
			if content.Len()+len(choice.Delta.Content) > maxResponseBytes {
				return fmt.Errorf("stream ответ модели превышает %d байт", maxResponseBytes)
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				outputRunes += utf8.RuneCountInString(choice.Delta.Content)
				if onProgress != nil {
					onProgress(Progress{
						OutputUnits: (outputRunes + 3) / 4,
						Elapsed:     p.now().Sub(started),
					})
				}
			}
			if choice.FinishReason != nil {
				result.FinishReason = *choice.FinishReason
			}
		}
		if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
			result.PromptTokens = chunk.Usage.PromptTokens
			result.CompletionTokens = chunk.Usage.CompletionTokens
		}
		if chunk.Timings.PromptMS > 0 {
			result.PromptDuration = durationFromMilliseconds(chunk.Timings.PromptMS)
		}
		if chunk.Timings.PredictedMS > 0 {
			result.GenerationDuration = durationFromMilliseconds(chunk.Timings.PredictedMS)
		}
		result.PromptTokensPerSecond = chunk.Timings.PromptPerSecond
		result.GenerationTokensPerSecond = chunk.Timings.PredictedPerSecond
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return Response{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("чтение stream ответа модели: %w", err)
	}
	if err := consume(); err != nil {
		return Response{}, err
	}
	if !done {
		return Response{}, fmt.Errorf("stream ответа модели завершён без [DONE]")
	}
	result.Content = content.String()
	result.Latency = p.now().Sub(started)
	if strings.TrimSpace(result.Content) == "" && result.FinishReason == "length" {
		return Response{}, fmt.Errorf(
			"модель израсходовала бюджет ответа на скрытое рассуждение и не дала ответа; " +
				"увеличьте предел токенов или отключите рассуждения")
	}
	if strings.TrimSpace(result.Content) == "" {
		return Response{}, fmt.Errorf("провайдер модели вернул пустой ответ")
	}
	return result, nil
}

func (p *OpenAICompatible) chatRequest(req Request, stream bool) chatRequest {
	msgs := make([]Message, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, Message{Role: RoleSystem, Content: req.System})
	}
	msgs = append(msgs, req.Messages...)
	body := chatRequest{
		Model: p.Model, Messages: msgs, MaxTokens: req.MaxTokens,
		Temperature: req.Temperature, Stream: stream,
	}
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.DisableThinking {
		body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	if len(req.Schema) > 0 {
		name := req.SchemaName
		if name == "" {
			name = "response"
		}
		body.ResponseFormat = &respFormat{
			Type:       "json_schema",
			JSONSchema: &schemaWrapper{Name: name, Strict: true, Schema: req.Schema},
		}
	}
	return body
}

func durationFromMilliseconds(ms float64) time.Duration {
	return time.Duration(math.Round(ms * float64(time.Millisecond)))
}

// auth добавляет ключ, если он задан. Значение нигде не журналируется.
func (p *OpenAICompatible) auth(r *http.Request) {
	if p.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
