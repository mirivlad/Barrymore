package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/api"
	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/worker"
)

type blockingTurnProvider struct {
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

type panicTurnProvider struct{}

func (panicTurnProvider) ID() string       { return "panic" }
func (panicTurnProvider) Describe() string { return "panic test provider" }
func (panicTurnProvider) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (panicTurnProvider) Complete(context.Context, model.Request) (model.Response, error) {
	panic("provider exploded")
}

func (p *blockingTurnProvider) ID() string       { return "blocking" }
func (p *blockingTurnProvider) Describe() string { return "blocking test provider" }
func (p *blockingTurnProvider) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (p *blockingTurnProvider) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return model.Response{Content: `{
			"reply":"Готово асинхронно.",
			"research":{"capability_id":"","args":{}},
			"thread_match":null,
			"thread_state":null,
			"memory_candidates":[],
			"own_actions":[],
			"work_order_proposals":[],
			"open_questions":[]
		}`, Model: "blocking", PromptTokens: 3, CompletionTokens: 2}, nil
	case <-ctx.Done():
		p.cancelled <- struct{}{}
		return model.Response{}, ctx.Err()
	}
}

func TestAsyncTurnSurvivesRequestCancellationAndRejectsOnlySameConversation(t *testing.T) {
	provider := &blockingTurnProvider{
		entered: make(chan struct{}, 2), release: make(chan struct{}), cancelled: make(chan struct{}, 2),
	}
	a, err := app.New(context.Background(), app.Config{
		DataRoot: t.TempDir(), ModelPolicy: worker.FreeOnly(),
		MemoryPolicy: memory.DefaultPolicy(), Provider: provider,
		Logger: testsupport.Logger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	ts := httptest.NewServer(api.NewServer(a).Handler())
	t.Cleanup(ts.Close)
	s := &server{t: t, http: ts, app: a}

	firstConversation := s.mustDo(http.MethodPost, "/api/v1/conversations", map[string]any{}, http.StatusCreated)
	firstID := firstConversation["id"].(string)
	secondConversation := s.mustDo(http.MethodPost, "/api/v1/conversations", map[string]any{}, http.StatusCreated)
	secondID := secondConversation["id"].(string)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	firstResponse := make(chan struct {
		status int
		body   map[string]any
	}, 1)
	go func() {
		status, body := doJSONWithContext(t, ts.Client(), requestCtx,
			http.MethodPost, ts.URL+"/api/v1/conversations/"+firstID+"/messages",
			map[string]any{"text": "Долгий вопрос"})
		firstResponse <- struct {
			status int
			body   map[string]any
		}{status: status, body: body}
	}()

	var accepted struct {
		status int
		body   map[string]any
	}
	select {
	case accepted = <-firstResponse:
	case <-time.After(200 * time.Millisecond):
		close(provider.release)
		<-firstResponse
		t.Fatal("POST ждал завершения модели вместо быстрого 202")
	}
	if accepted.status != http.StatusAccepted {
		close(provider.release)
		t.Fatalf("POST status=%d body=%v, want 202", accepted.status, accepted.body)
	}
	firstTurnID, _ := accepted.body["turn_id"].(string)
	if firstTurnID == "" || accepted.body["status"] != "queued" {
		close(provider.release)
		t.Fatalf("accepted=%v", accepted.body)
	}
	cancelRequest()

	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		close(provider.release)
		t.Fatal("background turn did not reach provider")
	}
	select {
	case <-provider.cancelled:
		close(provider.release)
		t.Fatal("request cancellation stopped app-owned turn")
	default:
	}

	active := s.mustDo(http.MethodGet,
		"/api/v1/conversations/"+firstID+"/turns/active", nil, http.StatusOK)
	if active["id"] != firstTurnID || active["status"] != "running" {
		close(provider.release)
		t.Fatalf("active=%v", active)
	}

	if code, out := s.do(http.MethodPost, "/api/v1/conversations/"+firstID+"/messages",
		map[string]any{"text": "Второй вопрос"}); code != http.StatusConflict {
		close(provider.release)
		t.Fatalf("same conversation status=%d body=%v, want 409", code, out)
	}
	secondAccepted := s.mustDo(http.MethodPost, "/api/v1/conversations/"+secondID+"/messages",
		map[string]any{"text": "Параллельный вопрос"}, http.StatusAccepted)
	secondTurnID := secondAccepted["turn_id"].(string)
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		close(provider.release)
		t.Fatal("different conversation did not run concurrently")
	}

	close(provider.release)
	completed := waitForTurn(t, s, firstID, firstTurnID)
	if completed["status"] != "completed" {
		t.Fatalf("completed=%v", completed)
	}
	result, _ := completed["result"].(map[string]any)
	reply, _ := result["reply"].(map[string]any)
	if reply["content"] != "Готово асинхронно." {
		t.Fatalf("result=%v", result)
	}
	waitForTurn(t, s, secondID, secondTurnID)
	messages := s.mustDo(http.MethodGet, "/api/v1/conversations/"+firstID+"/messages", nil, http.StatusOK)
	if len(messages["items"].([]any)) != 2 {
		t.Fatalf("messages=%v", messages)
	}
}

func TestStreamCarriesEphemeralTurnProgressWithoutJournalID(t *testing.T) {
	s := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.http.URL+"/api/v1/stream?from=999999", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	s.app.Talk.Progress().Publish(conversation.TurnProgress{
		TurnID: "trn_live", ConversationID: "conv_live",
		Stage: conversation.StageProviderGeneration, Label: "Формирую ответ",
		OutputTokens: 7, Approximate: true,
	})
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "event: conversation.turn.progress") ||
		!strings.Contains(joined, `"turn_id":"trn_live"`) {
		t.Fatalf("progress event=%q", joined)
	}
	if strings.Contains(joined, "id:") {
		t.Fatalf("ephemeral progress has journal id: %q", joined)
	}
}

func TestAsyncTurnPanicIsRecordedAsFailure(t *testing.T) {
	a, err := app.New(context.Background(), app.Config{
		DataRoot: t.TempDir(), ModelPolicy: worker.FreeOnly(),
		MemoryPolicy: memory.DefaultPolicy(), Provider: panicTurnProvider{},
		Logger: testsupport.Logger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	ts := httptest.NewServer(api.NewServer(a).Handler())
	t.Cleanup(ts.Close)
	s := &server{t: t, http: ts, app: a}

	conv := s.mustDo(http.MethodPost, "/api/v1/conversations", map[string]any{}, http.StatusCreated)
	conversationID := conv["id"].(string)
	accepted := s.mustDo(http.MethodPost, "/api/v1/conversations/"+conversationID+"/messages",
		map[string]any{"text": "Вызови панику"}, http.StatusAccepted)
	failed := waitForTurn(t, s, conversationID, accepted["turn_id"].(string))
	if failed["status"] != "failed" || failed["error_code"] != "turn_panic" {
		t.Fatalf("failed turn=%v", failed)
	}
}

func waitForTurn(t *testing.T, s *server, conversationID, turnID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		turn := s.mustDo(http.MethodGet, "/api/v1/conversations/"+conversationID+"/turns/"+turnID,
			nil, http.StatusOK)
		if status, _ := turn["status"].(string); status == "completed" || status == "failed" {
			return turn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("turn did not reach terminal state")
	return nil
}

func doJSONWithContext(t *testing.T, client *http.Client, ctx context.Context,
	method, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("request failed: %v", err)
		return 0, nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("response is not JSON: %s", data)
	}
	return resp.StatusCode, out
}
