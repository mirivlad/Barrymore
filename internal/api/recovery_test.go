package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPPanicBecomesProblemResponse(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := withCommonHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), log)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/conv/messages", strings.NewReader(`{}`))
	res := httptest.NewRecorder()

	// The panic must not escape to net/http where it would close the socket and
	// make Firefox report only NetworkError.
	h.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", res.Code, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content-type=%q, want problem+json", ct)
	}
	if !strings.Contains(res.Body.String(), "подробности записаны") {
		t.Fatalf("problem body does not explain where diagnostics are: %s", res.Body.String())
	}
}
