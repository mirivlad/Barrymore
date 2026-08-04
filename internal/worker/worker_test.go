package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/worker"
	"github.com/mirivlad/barrymore/internal/worker/codex"
)

func newRegistry(t *testing.T) (*worker.Registry, *clock.Fake) {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDB(t)
	j := event.NewJournal(db, clk)
	rt := runtime.New(runtime.Config{DB: db, Journal: j, Clock: clk, Logger: testsupport.Logger(t)})
	return worker.NewRegistry(db, j, clk, rt), clk
}

// fakeCodex подменяет поиск исполняемого файла и домашний каталог,
// чтобы тест не зависел от того, что установлено на машине.
func fakeCodex(t *testing.T, clk *clock.Fake, withAuth bool) *codex.Adapter {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\necho 'codex-cli 0.146.0'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("создание поддельного codex: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatalf("создание каталога codex: %v", err)
	}
	// Кэш моделей codex: без него у исполнителя нет каталога, и он не
	// может быть выбран — это отдельное проверяемое поведение.
	if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
		[]byte(`{"fetched_at":"2026-08-04T00:00:00Z","models":[{"slug":"gpt-5.6-sol","display_name":"GPT"}]}`),
		0o600); err != nil {
		t.Fatalf("создание кэша моделей: %v", err)
	}
	if withAuth {
		if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
			[]byte(`{"secret":"не должен читаться"}`), 0o600); err != nil {
			t.Fatalf("создание файла учётных данных: %v", err)
		}
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	a := codex.New()
	a.Now = clk.Now
	a.LookPath = func(name string) (string, error) {
		if name == "codex" {
			return bin, nil
		}
		return "", errors.New("не найдено")
	}
	a.HomeDir = func() (string, error) { return home, nil }
	return a
}

func TestDiscoveryRecordsEvidenceWithoutPaidRequest(t *testing.T) {
	// Сценарий D: найти исполняемый файл, получить версию, не выполнять
	// платный запрос, честно показать неизвестную квоту.
	ctx := context.Background()
	reg, clk := newRegistry(t)
	if err := reg.Register(fakeCodex(t, clk, true)); err != nil {
		t.Fatalf("регистрация adapter: %v", err)
	}

	res, err := reg.Discover(ctx, event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatalf("обнаружение: %v", err)
	}
	if len(res.Found) != 1 {
		t.Fatalf("найдено исполнителей %d, ожидался 1", len(res.Found))
	}

	v := res.Found[0]
	if v.Worker.Version == "" {
		t.Fatal("версия не получена")
	}
	if v.Worker.AuthState != worker.AuthConfigured {
		t.Fatalf("состояние учётной записи %q, ожидалось configured", v.Worker.AuthState)
	}
	if v.Availability.Status != worker.StatusLikelyAvailable {
		t.Fatalf("статус доступности %q; без проверки квоты нельзя заявлять available",
			v.Availability.Status)
	}
	if v.Availability.QuotaKnown {
		t.Fatal("система заявила знание о квоте, которой не проверяла")
	}
	if v.Availability.QuotaNote == "" {
		t.Fatal("не объяснено, почему состояние квоты неизвестно")
	}

	// Возможности должны иметь основания разной силы.
	byEvidence := map[string]int{}
	for _, c := range v.Capabilities {
		byEvidence[c.Evidence]++
	}
	if byEvidence[worker.EvidenceDeclared] == 0 {
		t.Fatal("заявленные возможности не записаны")
	}
	if byEvidence[worker.EvidenceProbe] == 0 {
		t.Fatal("подтверждённая опросом возможность не записана")
	}
	for _, c := range v.Capabilities {
		if c.Evidence == worker.EvidenceDeclared && c.Confidence >= 0.5 {
			t.Fatalf("заявленной возможности %s присвоена завышенная уверенность %.2f",
				c.Capability, c.Confidence)
		}
	}
}

func TestMissingAuthBlocksSelection(t *testing.T) {
	ctx := context.Background()
	reg, clk := newRegistry(t)
	if err := reg.Register(fakeCodex(t, clk, false)); err != nil {
		t.Fatalf("регистрация adapter: %v", err)
	}
	if _, err := reg.Discover(ctx, event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("обнаружение: %v", err)
	}

	ranked, err := reg.Rank(ctx, worker.RankRequest{
		RequiredCapabilities: []string{worker.CapRepositoryAudit},
		AuditOnly:            true,
		RequireRunnable:      true,
		ModelPolicy:          worker.PreferFree(),
	})
	if err != nil {
		t.Fatalf("ранжирование: %v", err)
	}
	if len(ranked) != 1 {
		t.Fatalf("кандидатов %d, ожидался 1", len(ranked))
	}
	if !ranked[0].Blocked {
		t.Fatal("исполнитель без настроенной учётной записи не заблокирован")
	}
}

func TestStaleSnapshotLowersScoreInsteadOfInventingAvailability(t *testing.T) {
	// Сценарий R: просроченный снимок не подменяется выдуманной доступностью.
	ctx := context.Background()
	reg, clk := newRegistry(t)
	if err := reg.Register(fakeCodex(t, clk, true)); err != nil {
		t.Fatalf("регистрация adapter: %v", err)
	}
	if _, err := reg.Discover(ctx, event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("обнаружение: %v", err)
	}

	fresh, err := reg.Rank(ctx, worker.RankRequest{
		RequiredCapabilities: []string{worker.CapRepositoryAudit},
		AuditOnly:            true, RequireRunnable: true,
		ModelPolicy: worker.PreferFree(),
	})
	if err != nil {
		t.Fatalf("ранжирование до устаревания: %v", err)
	}
	if fresh[0].Blocked {
		t.Fatalf("исполнитель заблокирован сразу после обнаружения: %s", fresh[0].BlockReason)
	}
	if !fresh[0].View.AvailabilityFresh {
		t.Fatal("свежий снимок считается просроченным")
	}

	// Снимок действует 15 минут — переводим часы за этот срок.
	clk.Advance(20 * 60 * 1e9)

	stale, err := reg.Rank(ctx, worker.RankRequest{
		RequiredCapabilities: []string{worker.CapRepositoryAudit},
		AuditOnly:            true, RequireRunnable: true,
		ModelPolicy: worker.PreferFree(),
	})
	if err != nil {
		t.Fatalf("ранжирование после устаревания: %v", err)
	}
	if stale[0].View.AvailabilityFresh {
		t.Fatal("просроченный снимок считается свежим")
	}
	if stale[0].Score >= fresh[0].Score {
		t.Fatalf("просроченный снимок не снизил оценку: было %.2f, стало %.2f",
			fresh[0].Score, stale[0].Score)
	}
	var mentioned bool
	for _, r := range stale[0].Reasons {
		if contains(r, "просрочен") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Fatalf("причина не объясняет устаревание снимка: %v", stale[0].Reasons)
	}
}

func TestManifestAdapterCannotBeSelectedForRun(t *testing.T) {
	// Adapter, умеющий только обнаружение, не должен получать поручение:
	// иначе появился бы соблазн подделать запуск.
	ctx := context.Background()
	reg, clk := newRegistry(t)

	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 1.17.9\n"), 0o755); err != nil {
		t.Fatalf("создание поддельного opencode: %v", err)
	}
	// Манифест не объявляет Runnable, поэтому adapter не должен допускаться к запуску.
	ma := worker.NewManifestAdapter(worker.Manifest{
		ID: "opencode", DisplayName: "OpenCode", Executables: []string{"opencode"},
		VersionArgs: []string{"--version"}, DefaultTrust: worker.TrustWorktreeWrite,
		DeclaredCapabilities: []string{worker.CapRepositoryAudit},
	})
	ma.Now = clk.Now
	ma.LookPath = func(string) (string, error) { return bin, nil }
	ma.HomeDir = func() (string, error) { return t.TempDir(), nil }
	if err := reg.Register(ma); err != nil {
		t.Fatalf("регистрация adapter: %v", err)
	}
	if _, err := reg.Discover(ctx, event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("обнаружение: %v", err)
	}

	ranked, err := reg.Rank(ctx, worker.RankRequest{
		RequiredCapabilities: []string{worker.CapRepositoryAudit},
		RequireRunnable:      true,
		ModelPolicy:          worker.PreferFree(),
	})
	if err != nil {
		t.Fatalf("ранжирование: %v", err)
	}
	if !ranked[0].Blocked {
		t.Fatal("adapter без плана запуска допущен к поручению")
	}
}

func TestCodexPlanUsesReadOnlySandboxForAudit(t *testing.T) {
	ctx := context.Background()
	clk := testsupport.Clock()
	a := fakeCodex(t, clk, true)

	inst, found, err := a.Discover(ctx)
	if err != nil || !found {
		t.Fatalf("обнаружение codex: %v, found=%v", err, found)
	}

	plan, err := a.Plan(ctx, inst, worker.RunRequest{
		RunID: "run_1", WorkDir: "/tmp/ws", Prompt: "проверь репозиторий",
		AuditOnly: true, OutputDir: "/tmp/out", ScratchDir: "/tmp/scratch",
		ReportSchemaPath: "/tmp/schema.json",
	})
	if err != nil {
		t.Fatalf("план запуска: %v", err)
	}

	argv := plan.Argv
	assertArgPair(t, argv, "--sandbox", "read-only")
	assertArgPair(t, argv, "-C", "/tmp/ws")
	assertArgPair(t, argv, "--output-schema", "/tmp/schema.json")
	assertHasArg(t, argv, "--json")
	assertHasArg(t, argv, "--ignore-user-config")

	if plan.Stdin != "проверь репозиторий" {
		t.Fatal("промпт не передан через stdin")
	}
	for _, arg := range argv {
		if arg == "проверь репозиторий" {
			t.Fatal("промпт попал в командную строку и будет виден в списке процессов")
		}
	}
	if !plan.StructuredOutput {
		t.Fatal("план не отмечен как дающий структурированный вывод")
	}
	// Рабочий каталог не должен попасть в список записываемых.
	scratchWritable := false
	for _, m := range plan.Sandbox.Mounts {
		if m.Kind == worker.MountWritable && m.Dst == "/tmp/ws" {
			t.Fatal("рабочий каталог отдан на запись при audit-only")
		}
		if m.Kind == worker.MountWritable && m.Dst == "/tmp/scratch" {
			scratchWritable = true
		}
	}
	if !scratchWritable {
		t.Fatal("исполнителю не выдан писчий каталог: запуск не состоится")
	}

	// Тот же запуск без audit-only обязан использовать другой sandbox.
	writePlan, err := a.Plan(ctx, inst, worker.RunRequest{
		RunID: "run_2", WorkDir: "/tmp/ws", ScratchDir: "/tmp/scratch", AuditOnly: false,
	})
	if err != nil {
		t.Fatalf("план записи: %v", err)
	}
	assertArgPair(t, writePlan.Argv, "--sandbox", "workspace-write")
}

func TestCodexParsesEventStreamAndKeepsUnknownLines(t *testing.T) {
	clk := testsupport.Clock()
	a := codex.New()
	a.Now = clk.Now

	cases := []struct {
		line string
		kind string
	}{
		{`{"type":"item","msg":{"type":"agent_message","message":"Отчёт готов"}}`, worker.RunEventMessage},
		{`{"type":"item","msg":{"type":"exec_command_begin","command":["git","status"],"cwd":"/w"}}`, worker.RunEventCommand},
		{`{"type":"item","msg":{"type":"exec_command_end","exit_code":0}}`, worker.RunEventCommand},
		{`{"type":"item","msg":{"type":"error","error":"нет доступа"}}`, worker.RunEventError},
		{`не json, просто строка`, worker.RunEventOther},
	}
	for _, tc := range cases {
		ev, ok := a.ParseLine([]byte(tc.line))
		if !ok {
			t.Fatalf("строка не разобрана: %s", tc.line)
		}
		if ev.Kind != tc.kind {
			t.Errorf("строка %s → вид %q, ожидался %q", tc.line, ev.Kind, tc.kind)
		}
		if ev.Raw == "" {
			t.Errorf("исходная строка потеряна: %s", tc.line)
		}
	}

	if _, ok := a.ParseLine([]byte("   ")); ok {
		t.Fatal("пустая строка разобрана как событие")
	}
}

func assertHasArg(t *testing.T, argv []string, want string) {
	t.Helper()
	for _, a := range argv {
		if a == want {
			return
		}
	}
	t.Fatalf("в команде нет аргумента %q: %v", want, argv)
}

func assertArgPair(t *testing.T, argv []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			if argv[i+1] != value {
				t.Fatalf("аргумент %s = %q, ожидалось %q", flag, argv[i+1], value)
			}
			return
		}
	}
	t.Fatalf("в команде нет аргумента %s: %v", flag, argv)
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
