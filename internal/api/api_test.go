package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/api"
	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/worker"
)

// Сквозная проверка собранного приложения: тот же путь, которым ходит браузер.
// Домены покрыты по отдельности, но связка «маршрут → сервис → база» до сих
// пор проверялась только руками.
type server struct {
	t    *testing.T
	http *httptest.Server
	app  *app.App
}

func newServer(t *testing.T) *server {
	t.Helper()
	dataRoot := t.TempDir()
	a, err := app.New(context.Background(), app.Config{
		DataRoot:     dataRoot,
		Addr:         "127.0.0.1:0",
		ModelPolicy:  worker.FreeOnly(),
		MemoryPolicy: memory.DefaultPolicy(),
		Logger:       testsupport.Logger(t),
	})
	if err != nil {
		t.Fatalf("приложение не собралось: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	ts := httptest.NewServer(api.NewServer(a).Handler())
	t.Cleanup(ts.Close)
	return &server{t: t, http: ts, app: a}
}

func (s *server) do(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.http.URL+path, reader)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Client().Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			s.t.Fatalf("%s %s вернул не JSON: %s", method, path, data)
		}
	}
	return resp.StatusCode, out
}

func (s *server) mustDo(method, path string, body any, want int) map[string]any {
	s.t.Helper()
	code, out := s.do(method, path, body)
	if code != want {
		s.t.Fatalf("%s %s → %d, ожидалось %d: %v", method, path, code, want, out)
	}
	return out
}

func TestHealthAndState(t *testing.T) {
	s := newServer(t)

	if code, _ := s.do(http.MethodGet, "/healthz", nil); code != http.StatusOK {
		t.Fatalf("healthz → %d", code)
	}

	st := s.mustDo(http.MethodGet, "/api/v1/system/state", nil, http.StatusOK)
	for _, key := range []string{
		"journal_head", "isolation", "workspace_roots", "startup_notes",
		"conversation", "local_model", "model_policy", "memory_policy",
	} {
		if _, ok := st[key]; !ok {
			t.Errorf("в состоянии нет %q: владелец не увидит часть картины", key)
		}
	}

	// Ограничения запуска перечисляются, а не замалчиваются.
	notes, _ := st["startup_notes"].([]any)
	if len(notes) == 0 {
		t.Fatal("без рабочих каталогов и провайдера ограничения обязаны быть названы")
	}
}

// Разговорный слой не настроен — и это видно, а не притворяется работающим.
func TestConversationWithoutProviderIsHonest(t *testing.T) {
	s := newServer(t)
	out := s.mustDo(http.MethodGet, "/api/v1/conversations", nil, http.StatusOK)
	prov, _ := out["provider"].(map[string]any)
	if prov["status"] != "not_configured" {
		t.Fatalf("состояние провайдера %v, ожидалось not_configured", prov["status"])
	}
}

func TestThreadLifecycle(t *testing.T) {
	s := newServer(t)

	th := s.mustDo(http.MethodPost, "/api/v1/threads", map[string]any{
		"title": "Проверка API", "kind": "project", "origin": "сквозной тест",
	}, http.StatusCreated)
	id, _ := th["id"].(string)
	if id == "" {
		t.Fatalf("нить создана без идентификатора: %v", th)
	}

	s.mustDo(http.MethodPost, "/api/v1/threads/"+id+"/positions", map[string]any{
		"owner": "person", "statement": "Так будет лучше", "confidence": 0.9,
		"basis": "опыт",
	}, http.StatusCreated)
	s.mustDo(http.MethodPost, "/api/v1/threads/"+id+"/questions", map[string]any{
		"question": "А если нет?",
	}, http.StatusCreated)

	detail := s.mustDo(http.MethodGet, "/api/v1/threads/"+id, nil, http.StatusOK)
	d, _ := detail["thread"].(map[string]any)
	if len(d["positions"].([]any)) != 1 {
		t.Fatalf("позиции не сохранились: %v", d["positions"])
	}
	if len(d["questions"].([]any)) != 1 {
		t.Fatalf("вопросы не сохранились: %v", d["questions"])
	}

	// История нити — не роскошь: без неё нельзя понять, как всё пришло к этому.
	tl := s.mustDo(http.MethodGet, "/api/v1/threads/"+id+"/timeline", nil, http.StatusOK)
	if len(tl["items"].([]any)) < 3 {
		t.Fatalf("в истории нити %v событий, ожидалось не меньше трёх", len(tl["items"].([]any)))
	}

	if code, _ := s.do(http.MethodGet, "/api/v1/threads/thr_нет", nil); code != http.StatusNotFound {
		t.Fatalf("несуществующая нить → %d, ожидалось 404", code)
	}
}

// Поручение по неразрешённому каталогу отклоняется до всякого запуска,
// и причина понятна.
func TestWorkOrderRefusedOutsideAllowedRoots(t *testing.T) {
	s := newServer(t)
	th := s.mustDo(http.MethodPost, "/api/v1/threads", map[string]any{
		"title": "Нить", "kind": "project",
	}, http.StatusCreated)

	code, out := s.do(http.MethodPost, "/api/v1/work-orders", map[string]any{
		"thread_id": th["id"], "title": "Аудит", "goal": "посмотреть",
		"why": "проверка", "workspace_root": t.TempDir(),
	})
	if code != http.StatusForbidden {
		t.Fatalf("поручение вне разрешённых каталогов → %d, ожидалось 403", code)
	}
	detail, _ := out["detail"].(string)
	if !strings.Contains(detail, "не задан ни один разрешённый") {
		t.Fatalf("причина отказа неясна: %q", detail)
	}
}

// Разрешение каталога действует немедленно, без перезапуска.
func TestWorkspaceRootTakesEffectImmediately(t *testing.T) {
	s := newServer(t)
	root := t.TempDir()
	th := s.mustDo(http.MethodPost, "/api/v1/threads", map[string]any{
		"title": "Нить", "kind": "project",
	}, http.StatusCreated)

	if code, _ := s.do(http.MethodPost, "/api/v1/work-orders", map[string]any{
		"thread_id": th["id"], "title": "Аудит", "goal": "посмотреть",
		"why": "проверка", "workspace_root": root,
	}); code != http.StatusForbidden {
		t.Fatalf("до разрешения ожидался 403, получено %d", code)
	}

	s.mustDo(http.MethodPost, "/api/v1/settings/workspace-roots",
		map[string]any{"path": root}, http.StatusOK)

	// Теперь политика пропускает. Дальше выбор исполнителя может не удаться —
	// в тестовой среде их нет, — но отказ уже не про каталог.
	code, out := s.do(http.MethodPost, "/api/v1/work-orders", map[string]any{
		"thread_id": th["id"], "title": "Аудит", "goal": "посмотреть",
		"why": "проверка", "workspace_root": root,
	})
	if code == http.StatusForbidden {
		t.Fatalf("после разрешения каталог всё ещё отклоняется: %v", out)
	}

	// И выбор сохранён на диске: перезапуск не должен его забыть.
	saved, err := app.LoadSettings(s.app.Config.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.WorkspaceRoots) != 1 {
		t.Fatalf("разрешение не сохранено: %v", saved.WorkspaceRoots)
	}
}

// Корень файловой системы разрешать нельзя, и об этом говорится словами.
func TestDangerousRootIsRefused(t *testing.T) {
	s := newServer(t)
	code, out := s.do(http.MethodPost, "/api/v1/settings/workspace-roots",
		map[string]any{"path": "/"})
	if code != http.StatusBadRequest {
		t.Fatalf("корень файловой системы → %d, ожидалось 400", code)
	}
	if detail, _ := out["detail"].(string); !strings.Contains(detail, "отсутствие") {
		t.Fatalf("отказ не объяснён: %q", detail)
	}
}

// Владелец может прямо сказать «запомни это», посмотреть память и удалить.
func TestMemoryDirectWriteAndForget(t *testing.T) {
	s := newServer(t)

	item := s.mustDo(http.MethodPost, "/api/v1/memories", map[string]any{
		"type": "preference", "content": "Работает по вечерам",
	}, http.StatusCreated)
	id, _ := item["id"].(string)
	if id == "" {
		// Ответ может быть обёрнут; ищем идентификатор внутри.
		if inner, ok := item["item"].(map[string]any); ok {
			id, _ = inner["id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("запись создана без идентификатора: %v", item)
	}

	list := s.mustDo(http.MethodGet, "/api/v1/memories", nil, http.StatusOK)
	if len(list["items"].([]any)) != 1 {
		t.Fatalf("записей %v, ожидалась одна", len(list["items"].([]any)))
	}

	out := s.mustDo(http.MethodDelete, "/api/v1/memories/"+id, nil, http.StatusOK)
	// Про надгробие говорится прямо, а не делается вид, что данных не было.
	if note, _ := out["note"].(string); !strings.Contains(note, "журнал") {
		t.Fatalf("удаление не объясняет, что остаётся в журнале: %q", note)
	}

	// В обычном списке удалённого нет: строка «[удалено владельцем]» выглядела
	// бы так, будто удаление не сработало.
	after := s.mustDo(http.MethodGet, "/api/v1/memories", nil, http.StatusOK)
	if len(after["items"].([]any)) != 0 {
		t.Fatalf("удалённая запись осталась в списке памяти: %v", after["items"])
	}

	// Но надгробие существует и доступно по запросу: делать вид, будто записи
	// никогда не было, Бэрримор не станет.
	graves := s.mustDo(http.MethodGet, "/api/v1/memories?forgotten=true", nil, http.StatusOK)
	if len(graves["forgotten"].([]any)) != 1 {
		t.Fatalf("надгробие потеряно: %v", graves["forgotten"])
	}
}

// Выбор модели, которой нет, отклоняется — и работающая не трогается.
func TestSelectingAbsentModelIsRefused(t *testing.T) {
	s := newServer(t)
	code, out := s.do(http.MethodPost, "/api/v1/local-model/select",
		map[string]any{"path": filepath.Join(t.TempDir(), "нет.gguf")})
	if code != http.StatusConflict {
		t.Fatalf("несуществующая модель → %d, ожидалось 409", code)
	}
	if detail, _ := out["detail"].(string); detail == "" {
		t.Fatal("отказ без причины")
	}
}

// Настройки отвечают тем, что действительно задано, и называют, что требует
// перезапуска: дать поменять то, что не поменяется, — обман.
func TestSettingsNameWhatNeedsRestart(t *testing.T) {
	s := newServer(t)
	out := s.mustDo(http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	need, _ := out["restart_required"].([]any)
	if len(need) == 0 {
		t.Fatal("не сказано, что требует перезапуска")
	}
	for _, v := range need {
		if v == "разрешённые рабочие каталоги" {
			t.Fatal("каталоги меняются на ходу; называть их требующими перезапуска — неправда")
		}
	}
	path, _ := out["path"].(string)
	if path == "" {
		t.Fatal("владельцу не показан путь к файлу настроек")
	}
}

// Поток событий продолжается с указанного места (ADR 0010).
func TestStreamResumesFromLastEventID(t *testing.T) {
	s := newServer(t)
	s.mustDo(http.MethodPost, "/api/v1/threads", map[string]any{
		"title": "Первая", "kind": "project",
	}, http.StatusCreated)

	req, err := http.NewRequest(http.MethodGet, s.http.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := s.http.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("поток отдан как %q", ct)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if n == 0 || !bytes.Contains(buf[:n], []byte("thread.created")) {
		t.Fatalf("прошлое событие не переиграно: %q", buf[:n])
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	s := newServer(t)
	resp, err := s.http.Client().Get(s.http.URL + "/api/v1/чего-нибудь")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("неизвестный маршрут → %d", resp.StatusCode)
	}
}

// Страница отдаётся с заголовками, закрывающими её от встраивания.
func TestUIIsServedWithProtectiveHeaders(t *testing.T) {
	s := newServer(t)
	resp, err := s.http.Client().Get(s.http.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("страница → %d", resp.StatusCode)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, ожидалось %q", header, got, want)
		}
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Бэрримор")) {
		t.Fatal("отдана не та страница")
	}
}

// Данные не должны утекать между запусками: каталог данных задан явно.
func TestDataStaysInsideDataRoot(t *testing.T) {
	s := newServer(t)
	s.mustDo(http.MethodPost, "/api/v1/threads", map[string]any{
		"title": "Нить", "kind": "project",
	}, http.StatusCreated)

	if _, err := os.Stat(filepath.Join(s.app.Config.DataRoot, "barrymore.db")); err != nil {
		t.Fatalf("база не в каталоге данных: %v", err)
	}
}
