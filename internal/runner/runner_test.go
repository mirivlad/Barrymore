package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/worker"
)

// shAdapter запускает произвольный сценарий оболочки и разбирает JSONL.
// Он заменяет настоящего исполнителя там, где важен сам контур запуска,
// а не поведение конкретного CLI.
type shAdapter struct {
	script  string
	sandbox worker.Sandbox
}

func (a *shAdapter) Descriptor() worker.Descriptor {
	return worker.Descriptor{ID: "sh", DisplayName: "Тестовый сценарий", SupportsAuditOnly: false}
}

func (a *shAdapter) Discover(context.Context) (worker.Installation, bool, error) {
	return worker.Installation{ExecutablePath: "/bin/sh", Version: "test"}, true, nil
}

func (a *shAdapter) Availability(context.Context, worker.Installation) (worker.Availability, error) {
	return worker.Availability{Status: worker.StatusLikelyAvailable}, nil
}

func (a *shAdapter) Plan(context.Context, worker.Installation, worker.RunRequest) (worker.RunPlan, error) {
	return worker.RunPlan{
		Argv:             []string{"/bin/sh", "-c", a.script},
		StructuredOutput: true,
		Sandbox:          a.sandbox,
	}, nil
}

func (a *shAdapter) Models(context.Context, worker.Installation) ([]worker.Model, error) {
	return []worker.Model{{Ref: "test/free", CostTier: worker.CostFree, LastCost: -1}}, nil
}

func (a *shAdapter) Collect(context.Context, string) error { return nil }

func (a *shAdapter) ParseLine(line []byte) (worker.RunEvent, bool) {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return worker.RunEvent{}, false
	}
	summary, _ := m["summary"].(string)
	return worker.RunEvent{Kind: worker.RunEventMessage, Summary: summary, Raw: string(line)}, true
}

type recordingSink struct {
	mu         sync.Mutex
	offset     int64
	attachment string
	exitCode   int
	exited     bool
}

func (s *recordingSink) SaveOffset(_ context.Context, _ string, off int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offset = off
	return nil
}

func (s *recordingSink) SetAttachment(_ context.Context, _, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachment = state
	return nil
}

func (s *recordingSink) MarkExited(_ context.Context, _ string, code int, _ time.Time, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitCode, s.exited = code, true
	return nil
}

func (s *recordingSink) snapshot() (int64, string, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset, s.attachment, s.exitCode, s.exited
}

func newRunner(t *testing.T, sink runner.Sink) (*runner.Runner, *runtime.Runtime) {
	t.Helper()
	clk := clock.Real{}
	db := testsupport.OpenDB(t)
	j := event.NewJournal(db, clk)
	rt := runtime.New(runtime.Config{DB: db, Journal: j, Clock: clk, Logger: testsupport.Logger(t)})
	r := runner.New(rt, clk, sink, testsupport.Logger(t))
	r.PollInterval = 20 * time.Millisecond
	t.Cleanup(r.Shutdown)
	return r, rt
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// Запас намеренно велик: под параллельным прогоном пакетов запуск
	// песочницы через bwrap и systemd-run занимает заметно дольше обычного,
	// и тест, падающий от чужой нагрузки, ничего не проверяет — он мешает.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func observationsOf(t *testing.T, rt *runtime.Runtime, runID, kind string) []runtime.Observation {
	t.Helper()
	all, err := rt.Observations(context.Background(), runtime.SubjectWorkerRun, runID, 500)
	if err != nil {
		t.Fatalf("чтение наблюдений: %v", err)
	}
	var out []runtime.Observation
	for _, o := range all {
		if o.Kind == kind {
			out = append(out, o)
		}
	}
	return out
}

func TestRunProducesObservationsAndExitCode(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	r, rt := newRunner(t, sink)

	a := &shAdapter{script: `
		echo '{"summary":"начал работу"}'
		echo '{"summary":"закончил работу"}'
		exit 3`}
	plan, _ := a.Plan(ctx, worker.Installation{}, worker.RunRequest{})

	runDir := t.TempDir()
	res, err := r.Start(ctx, runner.StartRequest{
		RunID: "run_obs", Adapter: a, Plan: plan, RunDir: runDir,
	})
	if err != nil {
		t.Fatalf("запуск: %v", err)
	}
	runner.RememberIdentity("run_obs", res.Identity)

	waitFor(t, "процесс завершился", func() bool {
		_, _, _, exited := sink.snapshot()
		return exited
	})
	waitFor(t, "события запуска разобраны", func() bool {
		return len(observationsOf(t, rt, "run_obs", runtime.ObsRunEvent)) >= 2
	})

	if _, _, code, _ := sink.snapshot(); code != 3 {
		t.Fatalf("код завершения %d, ожидался 3", code)
	}
	if len(observationsOf(t, rt, "run_obs", runtime.ObsRunStarted)) != 1 {
		t.Fatal("наблюдение о запуске не записано")
	}
	if len(observationsOf(t, rt, "run_obs", runtime.ObsRunExited)) != 1 {
		t.Fatal("наблюдение о завершении не записано")
	}

	// Сообщения исполнителя — недоверенные данные, а не факты о мире.
	for _, o := range observationsOf(t, rt, "run_obs", runtime.ObsRunEvent) {
		if o.SourceQuality != runtime.QualityReported {
			t.Fatalf("событию исполнителя присвоено качество %q вместо reported", o.SourceQuality)
		}
	}

	offset, _, _, _ := sink.snapshot()
	if offset <= 0 {
		t.Fatal("смещение вывода не сохранено: возобновить чтение после рестарта не выйдет")
	}
}

func TestAuditOnlyRunCannotWriteToWorkspace(t *testing.T) {
	// Сценарий F: попытка записи при read-only scope блокируется.
	// Проверяется не обещание adapter'а, а настоящая изоляция ядра.
	caps := runner.DetectCapabilities()
	if caps.Bwrap == "" {
		t.Skip("bubblewrap недоступен: проверка изоляции невозможна")
	}

	ctx := context.Background()
	sink := &recordingSink{}
	r, rt := newRunner(t, sink)

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "existing.txt"),
		[]byte("исходное содержимое\n"), 0o600); err != nil {
		t.Fatalf("подготовка рабочего каталога: %v", err)
	}

	a := &shAdapter{script: `
		if [ -f ` + workspace + `/existing.txt ]; then
			echo '{"summary":"рабочий каталог виден"}'
		else
			echo '{"summary":"рабочий каталог НЕ виден"}'
		fi
		if echo испорчено > ` + workspace + `/evil.txt 2>/dev/null; then
			echo '{"summary":"ЗАПИСЬ УДАЛАСЬ"}'
		else
			echo '{"summary":"запись отклонена"}'
		fi
		if echo испорчено >> ` + workspace + `/existing.txt 2>/dev/null; then
			echo '{"summary":"ДОПИСЫВАНИЕ УДАЛОСЬ"}'
		else
			echo '{"summary":"дописывание отклонено"}'
		fi`,
		sandbox: worker.Sandbox{Network: false},
	}
	plan, _ := a.Plan(ctx, worker.Installation{}, worker.RunRequest{})

	if _, err := r.Start(ctx, runner.StartRequest{
		RunID: "run_audit", Adapter: a, Plan: plan, RunDir: t.TempDir(),
		AuditOnly: true, Workspace: workspace,
	}); err != nil {
		t.Fatalf("запуск: %v", err)
	}

	waitFor(t, "процесс завершился", func() bool {
		_, _, _, exited := sink.snapshot()
		return exited
	})
	waitFor(t, "все события разобраны", func() bool {
		return len(observationsOf(t, rt, "run_audit", runtime.ObsRunEvent)) >= 3
	})

	summaries := map[string]bool{}
	for _, o := range observationsOf(t, rt, "run_audit", runtime.ObsRunEvent) {
		var ev worker.RunEvent
		if err := o.Decode(&ev); err != nil {
			t.Fatalf("разбор события: %v", err)
		}
		summaries[ev.Summary] = true
	}

	if !summaries["рабочий каталог виден"] {
		t.Fatal("исполнитель не увидел рабочий каталог: аудит был бы бессмысленным")
	}
	if summaries["ЗАПИСЬ УДАЛАСЬ"] {
		t.Fatal("audit-only запуск создал файл в рабочем каталоге")
	}
	if summaries["ДОПИСЫВАНИЕ УДАЛОСЬ"] {
		t.Fatal("audit-only запуск изменил существующий файл")
	}

	// Настоящее состояние каталога важнее того, что сообщил исполнитель.
	if _, err := os.Stat(filepath.Join(workspace, "evil.txt")); err == nil {
		t.Fatal("в рабочем каталоге появился новый файл")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "existing.txt"))
	if err != nil {
		t.Fatalf("чтение исходного файла: %v", err)
	}
	if string(data) != "исходное содержимое\n" {
		t.Fatalf("исходный файл изменён: %q", string(data))
	}
}

func TestAuditOnlyRefusesToStartWithoutIsolation(t *testing.T) {
	// ADR 0007: без bubblewrap audit-only не запускается вовсе.
	// Тихо перейти к «попросим исполнителя не писать» нельзя.
	if runner.DetectCapabilities().Bwrap != "" {
		t.Skip("bubblewrap доступен: отказ проверяется только на хосте без него")
	}
	ctx := context.Background()
	r, _ := newRunner(t, &recordingSink{})
	a := &shAdapter{script: "true"}
	plan, _ := a.Plan(ctx, worker.Installation{}, worker.RunRequest{})

	_, err := r.Start(ctx, runner.StartRequest{
		RunID: "run_no_iso", Adapter: a, Plan: plan, RunDir: t.TempDir(), AuditOnly: true,
	})
	if err == nil {
		t.Fatal("audit-only запуск разрешён без изоляции")
	}
}

func TestDetachAndReattachResumesWithoutDuplicates(t *testing.T) {
	// Сценарий O: подключение теряется, процесс жив, чтение возобновляется
	// с сохранённого смещения и не порождает повторных событий.
	ctx := context.Background()
	sink := &recordingSink{}
	r, rt := newRunner(t, sink)

	// Каталог маркера отдаётся в изоляцию явно: внутри песочницы /tmp — tmpfs,
	// поэтому обычный временный файл процессу не виден.
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "continue")
	a := &shAdapter{
		script: `
		echo '{"summary":"первое"}'
		while [ ! -f ` + marker + ` ]; do sleep 0.05; done
		echo '{"summary":"второе"}'`,
		sandbox: worker.Sandbox{}.Writable(markerDir, markerDir),
	}
	// Даже при провале теста процесс не должен остаться висеть.
	t.Cleanup(func() { _ = os.WriteFile(marker, []byte("go"), 0o600) })
	plan, _ := a.Plan(ctx, worker.Installation{}, worker.RunRequest{})

	runDir := t.TempDir()
	res, err := r.Start(ctx, runner.StartRequest{
		RunID: "run_reattach", Adapter: a, Plan: plan, RunDir: runDir,
	})
	if err != nil {
		t.Fatalf("запуск: %v", err)
	}
	runner.RememberIdentity("run_reattach", res.Identity)
	// Останов явный: запуски переживают смерть родителя (сценарий H), поэтому
	// полагаться на это как на уборку больше нельзя.
	t.Cleanup(func() { _ = runner.Terminate(r.Capabilities(), res.Identity, true) })

	waitFor(t, "первое событие прочитано", func() bool {
		return len(observationsOf(t, rt, "run_reattach", runtime.ObsRunEvent)) == 1
	})

	// Теряем подключение, процесс при этом остаётся живым.
	r.Detach("run_reattach")
	if r.Attached("run_reattach") {
		t.Fatal("подключение не разорвано")
	}
	if !r.Alive(res.Identity) {
		t.Fatal("процесс завершился раньше времени: проверка теряет смысл")
	}
	if _, state, _, _ := sink.snapshot(); state != runner.AttachmentLost {
		t.Fatalf("состояние подключения %q, ожидалось lost", state)
	}

	offset, _, _, _ := sink.snapshot()

	// Разрешаем процессу продолжить, пока никто не читает вывод.
	if err := os.WriteFile(marker, []byte("go"), 0o600); err != nil {
		t.Fatalf("создание маркера: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Восстанавливаем чтение с сохранённого смещения.
	r.Attach("run_reattach", a, res.StdoutPath, offset)
	waitFor(t, "второе событие прочитано после восстановления", func() bool {
		return len(observationsOf(t, rt, "run_reattach", runtime.ObsRunEvent)) == 2
	})

	got := observationsOf(t, rt, "run_reattach", runtime.ObsRunEvent)
	if len(got) != 2 {
		t.Fatalf("после восстановления событий %d, ожидалось ровно 2: "+
			"повторное чтение не должно дублировать наблюдения", len(got))
	}
}

func TestLivenessRejectsReusedPID(t *testing.T) {
	// ADR 0006: совпадения одного лишь номера процесса недостаточно.
	r, _ := newRunner(t, &recordingSink{})

	self := runner.ProcessIdentity{PID: os.Getpid()}
	if !r.Alive(self) {
		t.Fatal("собственный процесс признан мёртвым")
	}

	// Тот же номер, но другое время старта — значит, это уже не наш процесс.
	impostor := runner.ProcessIdentity{PID: os.Getpid(), StartTicks: 1}
	if r.Alive(impostor) {
		t.Fatal("чужой процесс с тем же номером признан нашим запуском")
	}
	l := r.Liveness(impostor)
	if !l.Certain {
		t.Fatal("вывод о переиспользованном номере должен быть уверенным")
	}
}

// Между fork и регистрацией scope systemd отвечает «inactive» про живой
// процесс. Принять этот ответ за смерть значит потерять только что запущенный
// процесс — и тут же запустить второй такой же.
func TestLivenessTrustsProcOverUnregisteredUnit(t *testing.T) {
	r, _ := newRunner(t, &recordingSink{})
	if r.Capabilities().SystemdRun == "" {
		t.Skip("пользовательский systemd недоступен")
	}

	id := runner.ProcessIdentity{
		UnitName: "barrymore-test-nonexistent.scope",
		PID:      os.Getpid(),
	}
	if ticks, err := runner.StartTicks(id.PID); err == nil {
		id.StartTicks = ticks
	}

	l := r.Liveness(id)
	if !l.Alive {
		t.Fatalf("живой процесс признан мёртвым из-за незарегистрированного unit: %+v", l)
	}
	if !strings.Contains(l.Detail, "inactive") {
		t.Fatalf("расхождение источников скрыто от читателя: %q", l.Detail)
	}
}

// А вот мёртвый процесс остаётся мёртвым: доверие к /proc не должно
// превращаться в отказ замечать смерть.
func TestLivenessReportsDeadProcessWithUnit(t *testing.T) {
	r, _ := newRunner(t, &recordingSink{})
	l := r.Liveness(runner.ProcessIdentity{
		UnitName: "barrymore-test-nonexistent.scope",
		PID:      4194303,
	})
	if l.Alive {
		t.Fatalf("несуществующий процесс признан живым: %+v", l)
	}
	if !l.Certain {
		t.Fatal("отсутствие процесса — уверенный вывод, а не предположение")
	}
}

// Запуск исполнителя должен пережить перезапуск Бэрримора (сценарий H).
//
// Обещание было в документации и в сообщении при остановке, а на деле
// `--die-with-parent` убивал песочницу вместе с управляющим процессом. Здесь
// проверяется само устройство команды: со scope владельцем времени жизни
// является systemd, без него — только Бэрримор, и тогда флаг нужен.
func TestSandboxLifetimeMatchesItsOwner(t *testing.T) {
	sink := &recordingSink{}
	r, _ := newRunner(t, sink)
	caps := r.Capabilities()
	if caps.Bwrap == "" {
		t.Skip("bubblewrap недоступен")
	}

	a := &shAdapter{script: `echo '{"summary":"ок"}'`}
	plan, _ := a.Plan(context.Background(), worker.Installation{}, worker.RunRequest{})
	res, err := r.Start(context.Background(), runner.StartRequest{
		RunID: "run_lifetime", Adapter: a, Plan: plan, RunDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("запуск: %v", err)
	}
	// Процесс надо дождаться: иначе горутина ожидания переживёт тест и
	// напишет в уже закрытую базу.
	waitFor(t, "процесс завершился", func() bool {
		_, _, _, exited := sink.snapshot()
		return exited
	})

	hasFlag := slices.Contains(res.Argv, "--die-with-parent")
	if caps.SystemdRun != "" {
		if hasFlag {
			t.Fatal("под systemd-scope запуск не должен умирать вместе с Бэрримором: " +
				"это ровно то, что обещано сценарием H")
		}
		if res.Profile.Supervision != "systemd-scope" {
			t.Fatalf("супервизия %q", res.Profile.Supervision)
		}
	} else {
		if !hasFlag {
			t.Fatal("без systemd песочнице некому владеть: она обязана умирать " +
				"вместе с Бэрримором, иначе останется жить бесконтрольно")
		}
		if !slices.ContainsFunc(res.Profile.Warnings, func(w string) bool {
			return strings.Contains(w, "не переживёт перезапуск")
		}) {
			t.Fatalf("ограничение не названо владельцу: %v", res.Profile.Warnings)
		}
	}
}
