package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAICompatibleStreamAssemblesPrivateContentAndTelemetry(t *testing.T) {
	var request chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"ornith\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"{\\\"reply\\\":\\\"\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"model\":\"ornith\",\"choices\":[{\"delta\":{\"content\":\"ready\\\"}\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"model\":\"ornith\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7},\"timings\":{\"prompt_ms\":80.5,\"predicted_ms\":528.3,\"prompt_per_second\":136.64,\"predicted_per_second\":13.25}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAICompatible(server.URL, "ornith", "", "test")
	var progress []Progress
	got, err := provider.CompleteStream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "answer"}},
	}, func(snapshot Progress) {
		progress = append(progress, snapshot)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != `{"reply":"ready"}` {
		t.Fatalf("content=%q", got.Content)
	}
	if got.Model != "ornith" || got.PromptTokens != 11 || got.CompletionTokens != 7 {
		t.Fatalf("usage=%+v", got)
	}
	if got.PromptDuration != 80500*time.Microsecond || got.GenerationDuration != 528300*time.Microsecond {
		t.Fatalf("durations=%s/%s", got.PromptDuration, got.GenerationDuration)
	}
	if got.PromptTokensPerSecond != 136.64 || got.GenerationTokensPerSecond != 13.25 {
		t.Fatalf("rates=%+v", got)
	}
	if !request.Stream || request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
		t.Fatalf("stream request=%+v", request)
	}
	if len(progress) != 2 || progress[0].OutputUnits == 0 || progress[1].OutputUnits <= progress[0].OutputUnits {
		t.Fatalf("progress=%+v", progress)
	}
}

func TestOpenAICompatibleStreamWithoutTimings(t *testing.T) {
	server := streamServer(t,
		`{"model":"plain","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`{"model":"plain","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1}}`,
		`[DONE]`,
	)
	defer server.Close()

	got, err := NewOpenAICompatible(server.URL, "plain", "", "test").CompleteStream(
		context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "ok" || got.CompletionTokens != 1 {
		t.Fatalf("response=%+v", got)
	}
	if got.PromptDuration != 0 || got.GenerationDuration != 0 ||
		got.PromptTokensPerSecond != 0 || got.GenerationTokensPerSecond != 0 {
		t.Fatalf("unsupported timings were fabricated: %+v", got)
	}
}

func TestOpenAICompatibleStreamRejectsProviderError(t *testing.T) {
	server := streamServer(t, `{"error":{"message":"out of memory","type":"server_error"}}`, `[DONE]`)
	defer server.Close()

	_, err := NewOpenAICompatible(server.URL, "model", "", "test").CompleteStream(
		context.Background(), Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenAICompatibleStreamRejectsMalformedEvent(t *testing.T) {
	server := streamServer(t, `{not-json}`, `[DONE]`)
	defer server.Close()

	_, err := NewOpenAICompatible(server.URL, "model", "", "test").CompleteStream(
		context.Background(), Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "разбор stream") {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenAICompatibleStreamRequiresDone(t *testing.T) {
	server := streamServer(t, `{"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`)
	defer server.Close()

	_, err := NewOpenAICompatible(server.URL, "model", "", "test").CompleteStream(
		context.Background(), Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "[DONE]") {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenAICompatibleStreamBoundsEventSize(t *testing.T) {
	server := streamServer(t, `{"choices":[{"delta":{"content":"`+strings.Repeat("x", (16<<20)+1)+`"}}]}`, `[DONE]`)
	defer server.Close()

	_, err := NewOpenAICompatible(server.URL, "model", "", "test").CompleteStream(
		context.Background(), Request{}, nil)
	if err == nil {
		t.Fatal("oversized stream event accepted")
	}
}

func streamServer(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
}
