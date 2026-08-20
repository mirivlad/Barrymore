package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/api"
	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/worker"
)

// Здесь проверяется главный сценарий продукта целиком: владелец пишет о деле,
// а нить и поручение появляются сами. Если этот тест проходит, а вкладки
// «Нити» и «Поручения» ни разу не понадобились — срез сделан.

// scriptedProvider отдаёт заранее заданный ответ по контракту.
type scriptedProvider struct{ reply string }

func (p *scriptedProvider) ID() string       { return "scripted" }
func (p *scriptedProvider) Describe() string { return "подготовленный ответ" }
func (p *scriptedProvider) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (p *scriptedProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: p.reply, Model: "тестовая"}, nil
}

// talkAdapter — исполнитель, которого достаточно, чтобы поручение сформировалось.
type talkAdapter struct{}

func (talkAdapter) Descriptor() worker.Descriptor {
	return worker.Descriptor{
		ID: "fake", DisplayName: "Тестовый исполнитель",
		Executables: []string{"sh"}, DefaultTrust: worker.TrustWorkspaceRead,
		Class: worker.ClassRoutine, Runnable: true,
		DeclaredCapabilities: []string{worker.CapRepositoryAudit},
	}
}

func (talkAdapter) Discover(context.Context) (worker.Installation, bool, error) {
	return worker.Installation{
		ExecutablePath: "/bin/sh", Version: "test-1",
		AuthState: worker.AuthConfigured, AuthDetail: "тестовая учётная запись",
	}, true, nil
}

func (talkAdapter) Availability(context.Context, worker.Installation) (worker.Availability, error) {
	until := time.Now().Add(time.Hour)
	return worker.Availability{
		Status: worker.StatusLikelyAvailable, Confidence: 0.6,
		ObservedAt: time.Now(), ValidUntil: &until, Source: "test",
	}, nil
}

func (talkAdapter) Models(context.Context, worker.Installation) ([]worker.Model, error) {
	return []worker.Model{{
		Ref: "free-model", Provider: "test", CostTier: worker.CostFree,
		Source: "test", ObservedAt: time.Now(),
	}}, nil
}

func (talkAdapter) Plan(context.Context, worker.Installation, worker.RunRequest) (worker.RunPlan, error) {
	return worker.RunPlan{Argv: []string{"/bin/sh", "-c", "true"}}, nil
}

func (talkAdapter) ParseLine([]byte) (worker.RunEvent, bool) { return worker.RunEvent{}, false }
func (talkAdapter) Collect(context.Context, string) error    { return nil }

// talkReply собирает ответ модели с предложением нити и поручения.
func talkReply(workspace string) string {
	out := map[string]any{
		"reply": "Похоже, дело в незакрытом worktree. Предлагаю посмотреть.",
		"thread_match": map[string]any{
			"thread_id": "", "new_thread_title": "Rollboard",
			"new_thread_kind": "project", "why": "речь о зависшем worktree Rollboard",
		},
		"thread_state": map[string]any{
			"goal":      "разобраться с зависшим worktree Rollboard",
			"situation": "владелец не может продолжить работу: worktree занят",
			"next_step": "выяснить, чем занят каталог",
			"obstacles": []string{"неизвестно, какой процесс держит каталог"},
			"waiting":   []string{},
		},
		"memory_candidates": []any{},
		"work_order_proposals": []any{map[string]any{
			"title": "Аудит Rollboard", "goal": "выяснить состояние worktree",
			"why": "владелец не может продолжить работу", "workspace_hint": workspace,
			"acceptance_criteria": []string{"названо, чем занят каталог"},
			"needs_write":         false,
		}},
		"open_questions": []string{},
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// Полный путь: разговор → предложенная нить → поручение → подтверждение.
func TestReceptionCarriesTheWholePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("тест\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := talkServerAt(t, dir, talkReply(dir))

	// 1. Владелец просто пишет. Нить не выбирается.
	conv := s.mustDo(http.MethodPost, "/api/v1/conversations",
		map[string]any{"thread_id": "", "title": ""}, http.StatusCreated)
	convID, _ := conv["id"].(string)

	turn := sendAndWait(t, s, convID, "У меня Rollboard завис в worktree")

	// 2. Бэрримор сам предлагает нить — с готовым названием и состоянием.
	th, _ := turn["thread"].(map[string]any)
	proposed, _ := th["proposed"].(map[string]any)
	if proposed == nil {
		t.Fatalf("нить не предложена: %v", th)
	}
	if proposed["title"] != "Rollboard" {
		t.Fatalf("название нити %v", proposed["title"])
	}
	reply, _ := turn["reply"].(map[string]any)
	messageID, _ := reply["id"].(string)

	// 3. Одно нажатие — нить есть, вместе с состоянием.
	created := s.mustDo(http.MethodPost, "/api/v1/conversations/"+convID+"/threads",
		map[string]any{"message_id": messageID}, http.StatusCreated)
	canon, _ := created["canon"].(map[string]any)
	if canon["goal"] == "" || canon["situation"] == "" || canon["next_step"] == "" {
		t.Fatalf("нить заведена без состояния: %v", canon)
	}

	// 4. Главный экран показывает нить рядом с разговором.
	state := s.mustDo(http.MethodGet, "/api/v1/conversations/"+convID, nil, http.StatusOK)
	if _, ok := state["thread"]; !ok {
		t.Fatal("разговор не показывает нить: владельцу пришлось бы идти на вкладку")
	}

	// 5. Поручение оформляется из сказанного, без повторного ввода.
	order := s.mustDo(http.MethodPost, "/api/v1/conversations/"+convID+"/work-orders",
		map[string]any{"message_id": messageID, "index": 0}, http.StatusCreated)
	wo, _ := order["order"].(map[string]any)
	if wo["goal"] != "выяснить состояние worktree" {
		t.Fatalf("цель поручения не перенесена: %v", wo["goal"])
	}
	if wo["why"] == "" {
		t.Fatal("причина потерялась по дороге — а именно её труднее всего восстановить")
	}
	if wo["workspace_root"] != dir {
		t.Fatalf("каталог не перенесён: %v", wo["workspace_root"])
	}
	if wo["audit_only"] != true {
		t.Fatal("поручение без явного разрешения обязано быть только на чтение")
	}
	criteria, _ := wo["acceptance_criteria"].([]any)
	if len(criteria) == 0 {
		t.Fatal("критерии приёмки не перенесены")
	}

	// 6. Подтверждение — существенное решение, и оно названо прямо.
	appr, _ := order["approval"].(map[string]any)
	summary, _ := appr["summary"].(string)
	if !strings.Contains(summary, dir) {
		t.Fatalf("в подтверждении не назван каталог: %q", summary)
	}
	apprID, _ := appr["id"].(string)
	s.mustDo(http.MethodPost, "/api/v1/approvals/"+apprID+"/grant",
		map[string]any{"decided_by": "владелец"}, http.StatusOK)
}

// Ошибку связывания владелец должен уметь поправить.
func TestOwnerCanDetachThreadFromConversation(t *testing.T) {
	dir := t.TempDir()
	s := talkServerAt(t, dir, talkReply(dir))

	conv := s.mustDo(http.MethodPost, "/api/v1/conversations",
		map[string]any{}, http.StatusCreated)
	convID, _ := conv["id"].(string)
	turn := sendAndWait(t, s, convID, "Rollboard завис")
	reply, _ := turn["reply"].(map[string]any)
	messageID, _ := reply["id"].(string)
	s.mustDo(http.MethodPost, "/api/v1/conversations/"+convID+"/threads",
		map[string]any{"message_id": messageID}, http.StatusCreated)

	out := s.mustDo(http.MethodPost, "/api/v1/conversations/"+convID+"/thread",
		map[string]any{"thread_id": "", "why": "не про это"}, http.StatusOK)
	if _, ok := out["thread"]; ok {
		t.Fatal("связь с нитью не снята")
	}
}

// Поручение без нити не создаётся: оно повисло бы без истории и результата.
func TestOrderFromTalkNeedsThread(t *testing.T) {
	dir := t.TempDir()
	s := talkServerAt(t, dir, talkReply(dir))

	conv := s.mustDo(http.MethodPost, "/api/v1/conversations",
		map[string]any{}, http.StatusCreated)
	convID, _ := conv["id"].(string)
	_ = sendAndWait(t, s, convID, "Rollboard завис")

	code, out := s.do(http.MethodPost, "/api/v1/conversations/"+convID+"/work-orders",
		map[string]any{"index": 0})
	if code != http.StatusConflict {
		t.Fatalf("код %d, ожидался 409: %v", code, out)
	}
}

func sendAndWait(t *testing.T, s *server, conversationID, text string) map[string]any {
	t.Helper()
	accepted := s.mustDo(http.MethodPost, "/api/v1/conversations/"+conversationID+"/messages",
		map[string]any{"text": text}, http.StatusAccepted)
	turnID, _ := accepted["turn_id"].(string)
	completed := waitForTurn(t, s, conversationID, turnID)
	if completed["status"] != "completed" {
		t.Fatalf("turn=%v", completed)
	}
	result, _ := completed["result"].(map[string]any)
	return result
}

// talkServerAt поднимает приложение с разрешённым каталогом и провайдером.
func talkServerAt(t *testing.T, workspaceRoot, reply string) *server {
	t.Helper()
	a, err := app.New(context.Background(), app.Config{
		DataRoot:       t.TempDir(),
		Addr:           "127.0.0.1:0",
		WorkspaceRoots: []string{workspaceRoot},
		ModelPolicy:    worker.FreeOnly(),
		MemoryPolicy:   memory.DefaultPolicy(),
		Provider:       &scriptedProvider{reply: reply},
		Logger:         testsupport.Logger(t),
	})
	if err != nil {
		t.Fatalf("приложение не собралось: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	if err := a.Registry.Register(talkAdapter{}); err != nil {
		t.Fatalf("регистрация исполнителя: %v", err)
	}
	if _, err := a.Registry.Discover(context.Background(),
		event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("обнаружение исполнителей: %v", err)
	}

	ts := httptest.NewServer(api.NewServer(a).Handler())
	t.Cleanup(ts.Close)
	return &server{t: t, http: ts, app: a}
}
