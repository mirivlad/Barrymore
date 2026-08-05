package delegation_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/delegation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/thread"
	"github.com/mirivlad/barrymore/internal/worker"
)

// fakeAdapter изображает повседневного исполнителя на бесплатной модели.
//
// Он нужен, чтобы проверять сам контур делегирования, не завися от того,
// что установлено на машине и в каком состоянии находятся чужие квоты.
type fakeAdapter struct {
	script string
	// model — что adapter сообщает о своей модели.
	model worker.Model
	// visibleDir отдаётся процессу на чтение. Внутри изоляции /tmp — это
	// tmpfs, поэтому обычный временный каталог процессу не виден.
	visibleDir string
}

func (a *fakeAdapter) Descriptor() worker.Descriptor {
	return worker.Descriptor{
		ID: "fake", DisplayName: "Тестовый исполнитель",
		Executables: []string{"sh"}, DefaultTrust: worker.TrustWorkspaceRead,
		Class: worker.ClassRoutine, Runnable: true,
		DeclaredCapabilities: []string{worker.CapRepositoryAudit},
	}
}

func (a *fakeAdapter) Discover(context.Context) (worker.Installation, bool, error) {
	return worker.Installation{
		ExecutablePath: "/bin/sh", Version: "test-1",
		AuthState: worker.AuthConfigured, AuthDetail: "тестовая учётная запись",
	}, true, nil
}

func (a *fakeAdapter) Availability(context.Context, worker.Installation) (worker.Availability, error) {
	until := time.Now().Add(time.Hour)
	return worker.Availability{
		Status: worker.StatusLikelyAvailable, Confidence: 0.6,
		ObservedAt: time.Now(), ValidUntil: &until, Source: "test",
	}, nil
}

func (a *fakeAdapter) Models(context.Context, worker.Installation) ([]worker.Model, error) {
	return []worker.Model{a.model}, nil
}

func (a *fakeAdapter) Plan(_ context.Context, _ worker.Installation, req worker.RunRequest) (worker.RunPlan, error) {
	sb := worker.Sandbox{Network: false}.Writable(req.ScratchDir, req.ScratchDir)
	if req.OutputDir != "" {
		sb = sb.Writable(req.OutputDir, req.OutputDir)
	}
	if a.visibleDir != "" {
		sb = sb.ReadOnly(a.visibleDir, a.visibleDir)
	}
	return worker.RunPlan{
		Argv:             []string{"/bin/sh", "-c", a.script},
		Env:              []string{"OUT_DIR=" + req.OutputDir},
		StructuredOutput: true,
		Sandbox:          sb,
		Dir:              req.WorkDir,
	}, nil
}

func (a *fakeAdapter) ParseLine(line []byte) (worker.RunEvent, bool) {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return worker.RunEvent{}, false
	}
	ev := worker.RunEvent{
		Kind: worker.RunEventMessage, At: time.Now(),
		Raw: string(line), Detail: map[string]any{},
	}
	if s, ok := m["summary"].(string); ok {
		ev.Summary = s
	}
	if c, ok := m["cost"].(float64); ok {
		ev.Detail["observed_cost"] = c
	}
	return ev, true
}

func (a *fakeAdapter) Collect(context.Context, string) error { return nil }

type harness struct {
	deleg    *delegation.Service
	rt       *runtime.Runtime
	registry *worker.Registry
	threads  *thread.Service
	workDir  string
}

func newHarness(t *testing.T, a worker.Adapter) *harness {
	t.Helper()
	ctx := context.Background()
	clk := clock.Real{}
	db := testsupport.OpenDB(t)
	j := event.NewJournal(db, clk)
	rt := runtime.New(runtime.Config{
		DB: db, Journal: j, Clock: clk, Logger: testsupport.Logger(t),
	})
	reg := worker.NewRegistry(db, j, clk, rt)
	if err := reg.Register(a); err != nil {
		t.Fatalf("регистрация adapter: %v", err)
	}
	threads := thread.NewService(db, j, clk)

	d := delegation.New(delegation.Config{
		DB: db, Journal: j, Clock: clk, Runtime: rt, Registry: reg, Threads: threads,
		Logger: testsupport.Logger(t), DataRoot: t.TempDir(),
		ModelPolicy: worker.FreeOnly(),
	})
	if err := d.RegisterReflexes(); err != nil {
		t.Fatalf("регистрация реакций: %v", err)
	}
	t.Cleanup(d.Runner().Shutdown)

	if _, err := reg.Discover(ctx, event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("обнаружение: %v", err)
	}

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "README.md"),
		[]byte("тестовый репозиторий\n"), 0o600); err != nil {
		t.Fatalf("подготовка рабочего каталога: %v", err)
	}
	return &harness{deleg: d, rt: rt, registry: reg, threads: threads, workDir: workDir}
}

func (h *harness) propose(t *testing.T) delegation.Proposal {
	t.Helper()
	ctx := context.Background()
	th, err := h.threads.Create(ctx, thread.CreateRequest{
		Title: "Тестовая нить", Kind: thread.KindProject,
	})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}
	p, err := h.deleg.Propose(ctx, delegation.ProposeRequest{
		ThreadID: th.ID, Goal: "проверить контур", WorkspaceRoot: h.workDir,
		AuditOnly: true,
	})
	if err != nil {
		t.Fatalf("формирование поручения: %v", err)
	}
	return p
}

func (h *harness) approveAndStart(t *testing.T, p delegation.Proposal) delegation.WorkerRun {
	t.Helper()
	ctx := context.Background()
	if _, err := h.deleg.Approve(ctx, p.Approval.ID, "test",
		event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("подтверждение: %v", err)
	}
	run, err := h.deleg.Start(ctx, p.Order.ID, event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatalf("запуск: %v", err)
	}
	// Процесс останавливается явно. Раньше его убивал `--die-with-parent`,
	// но запуски теперь переживают смерть Бэрримора (сценарий H) — и тест,
	// полагавшийся на побочный эффект, оставлял бы процесс-сироту.
	//
	// Уборка ждёт, пока запуск действительно завершится: горутина ожидания
	// дописывает наблюдения, и уйти раньше неё значило бы дать ей обратиться
	// к уже закрытой базе.
	t.Cleanup(func() {
		id := runner.ProcessIdentity{
			UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
		}
		_ = runner.Terminate(h.deleg.Runner().Capabilities(), id, true)
		// Ждём терминального состояния поручения, а не просто ухода запуска
		// из активных: приёмка идёт уже после отметки о завершении и сама
		// обходит рабочий каталог, который вот-вот удалят.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			o, err := h.deleg.Get(context.Background(), p.Order.ID)
			if err != nil {
				return
			}
			switch o.State {
			case delegation.StateCompleted, delegation.StateFailed, delegation.StateCancelled:
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return run
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func freeModel() worker.Model {
	return worker.Model{
		Ref: "test/flash-free", Provider: "test", CostTier: worker.CostFree,
		Source: "cli-list", Evidence: "пометка free в названии",
		Confidence: 0.9, LastCost: -1, ObservedAt: time.Now(),
	}
}

func TestStartRequiresApproval(t *testing.T) {
	h := newHarness(t, &fakeAdapter{script: "true", model: freeModel()})
	p := h.propose(t)

	// Запуск исполнителя — необратимое действие, требующее явного разрешения.
	if _, err := h.deleg.Start(context.Background(), p.Order.ID,
		event.Actor{Type: event.ActorPerson}); err == nil {
		t.Fatal("поручение запущено без подтверждения владельца")
	}
}

func TestProposalChoosesFreeModelAndExplainsIt(t *testing.T) {
	h := newHarness(t, &fakeAdapter{script: "true", model: freeModel()})
	p := h.propose(t)

	if p.Order.Model != "test/flash-free" {
		t.Fatalf("выбрана модель %q вместо бесплатной", p.Order.Model)
	}
	if p.Order.ModelCostTier != worker.CostFree {
		t.Fatalf("стоимость модели помечена как %q", p.Order.ModelCostTier)
	}
	if p.Order.ModelRationale == "" {
		t.Fatal("выбор модели не объяснён")
	}
	// Владелец должен увидеть модель и стоимость до того, как что-то запустится.
	if p.Approval.Scope.Model != "test/flash-free" ||
		p.Approval.Scope.CostTier != worker.CostFree {
		t.Fatalf("подтверждение не показывает модель и стоимость: %+v", p.Approval.Scope)
	}
}

func TestPaidModelIsRefusedUnderFreeOnlyPolicy(t *testing.T) {
	paid := freeModel()
	paid.Ref = "test/premium"
	paid.CostTier = worker.CostUnknown
	paid.Evidence = "провайдер не пометил модель как бесплатную"

	h := newHarness(t, &fakeAdapter{script: "true", model: paid})
	ctx := context.Background()
	th, err := h.threads.Create(ctx, thread.CreateRequest{Title: "Нить", Kind: thread.KindProject})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}

	_, err = h.deleg.Propose(ctx, delegation.ProposeRequest{
		ThreadID: th.ID, Goal: "цель", WorkspaceRoot: h.workDir, AuditOnly: true,
	})
	if err == nil {
		t.Fatal("поручение сформировано на модели без пометки бесплатной")
	}
}

func TestCompletedOnlyAfterVerification(t *testing.T) {
	// Отчёт по схеме и неизменный рабочий каталог: поручение принимается.
	script := `
		echo '{"summary":"начал"}'
		cat > "$OUT_DIR/last-message.txt" <<'REPORT'
{"summary":"репозиторий состоит из одного файла","findings":[],"limitations":"проверено только чтение"}
REPORT
		echo '{"summary":"закончил"}'`
	h := newHarness(t, &fakeAdapter{script: script, model: freeModel()})

	p := h.propose(t)
	run := h.approveAndStart(t, p)

	ctx := context.Background()
	waitFor(t, "поручение принято", func() bool {
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil && (o.State == delegation.StateCompleted || o.State == delegation.StateFailed)
	})

	d, err := h.deleg.Detail(ctx, p.Order.ID)
	if err != nil {
		t.Fatalf("чтение поручения: %v", err)
	}
	if d.Order.State != delegation.StateCompleted {
		t.Fatalf("состояние %q, причина: %s", d.Order.State, d.Order.FailureReason)
	}

	byName := map[string]delegation.Verification{}
	for _, v := range d.Verifications {
		byName[v.Name] = v
	}
	for _, name := range []string{
		"завершение процесса", "обязательные артефакты", "схема отчёта",
		"неизменность рабочего каталога",
	} {
		v, ok := byName[name]
		if !ok {
			t.Fatalf("проверка %q не выполнялась", name)
		}
		if v.Status != delegation.VerifyPassed {
			t.Fatalf("проверка %q: %s — %s", name, v.Status, v.Detail)
		}
	}
	if run.ID == "" {
		t.Fatal("запуск не зарегистрирован")
	}
}

func TestInvalidReportKeepsOrderFailed(t *testing.T) {
	// «Готово» исполнителя не является доказательством: отчёт не по схеме
	// не даёт принять поручение.
	script := `
		echo '{"summary":"работаю"}'
		printf 'всё отлично, я закончил' > "$OUT_DIR/last-message.txt"`
	h := newHarness(t, &fakeAdapter{script: script, model: freeModel()})

	p := h.propose(t)
	h.approveAndStart(t, p)

	ctx := context.Background()
	waitFor(t, "приёмка завершилась", func() bool {
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil && (o.State == delegation.StateCompleted || o.State == delegation.StateFailed)
	})

	o, err := h.deleg.Get(ctx, p.Order.ID)
	if err != nil {
		t.Fatalf("чтение поручения: %v", err)
	}
	if o.State != delegation.StateFailed {
		t.Fatalf("поручение с невалидным отчётом принято: %q", o.State)
	}

	d, _ := h.deleg.Detail(ctx, p.Order.ID)
	var schemaCheck delegation.Verification
	for _, v := range d.Verifications {
		if v.Name == "схема отчёта" {
			schemaCheck = v
		}
	}
	if schemaCheck.Status != delegation.VerifyFailed {
		t.Fatalf("проверка схемы отчёта: %q", schemaCheck.Status)
	}
}

func TestChargeOnFreeModelStopsRunAndMarksModelPaid(t *testing.T) {
	// Модель выбрана как бесплатная, а провайдер начал списывать.
	// Работа обязана прекратиться, а модель — навсегда стать платной.
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "stop")
	script := `
		echo '{"summary":"начал","cost":0}'
		echo '{"summary":"провайдер списал","cost":0.0042}'
		while [ ! -f ` + marker + ` ]; do sleep 0.1; done
		echo '{"summary":"так и не остановили"}'`

	h := newHarness(t, &fakeAdapter{script: script, model: freeModel(), visibleDir: markerDir})
	t.Cleanup(func() { _ = os.WriteFile(marker, []byte("x"), 0o600) })

	p := h.propose(t)
	run := h.approveAndStart(t, p)
	ctx := context.Background()

	waitFor(t, "списание замечено", func() bool {
		obs, err := h.rt.Observations(ctx, runtime.SubjectWorkerRun, run.ID, 200)
		if err != nil {
			return false
		}
		for _, o := range obs {
			var ev worker.RunEvent
			if err := o.Decode(&ev); err != nil {
				continue
			}
			if c, ok := ev.Detail["observed_cost"].(float64); ok && c > 0 {
				return true
			}
		}
		return false
	})

	// Предиктивный контур: ожидание по стоимости даёт расхождение,
	// расхождение запускает реакцию остановки.
	waitFor(t, "запуск остановлен предиктивным контуром", func() bool {
		if _, err := h.rt.Tick(ctx); err != nil {
			t.Fatalf("тик: %v", err)
		}
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil && o.State == delegation.StateFailed
	})

	o, err := h.deleg.Get(ctx, p.Order.ID)
	if err != nil {
		t.Fatalf("чтение поручения: %v", err)
	}
	if o.FailureReason == "" {
		t.Fatal("причина остановки не записана")
	}

	// Модель больше не считается бесплатной.
	workers, err := h.registry.List(ctx)
	if err != nil {
		t.Fatalf("чтение реестра: %v", err)
	}
	var found bool
	for _, v := range workers {
		for _, m := range v.Models {
			if m.Ref != "test/flash-free" {
				continue
			}
			found = true
			if m.CostTier != worker.CostPaid {
				t.Fatalf("после списания модель осталась %q", m.CostTier)
			}
			if !m.Charged() {
				t.Fatal("факт списания не зафиксирован в реестре")
			}
		}
	}
	if !found {
		t.Fatal("модель не найдена в реестре")
	}

	// Повторное поручение на этой модели больше не формируется.
	th, _ := h.threads.Create(ctx, thread.CreateRequest{Title: "Ещё", Kind: thread.KindProject})
	if _, err := h.deleg.Propose(ctx, delegation.ProposeRequest{
		ThreadID: th.ID, Goal: "снова", WorkspaceRoot: h.workDir, AuditOnly: true,
	}); err == nil {
		t.Fatal("после списания модель снова предложена как бесплатная")
	}
}

func TestOrphanedRunDoesNotBecomeCompleted(t *testing.T) {
	// Сценарий H: процесс не пережил рестарт. Поручение не объявляется
	// выполненным, а запуск честно помечается потерянным.
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "stop")
	script := `
		echo '{"summary":"работаю"}'
		while [ ! -f ` + marker + ` ]; do sleep 0.1; done`

	h := newHarness(t, &fakeAdapter{script: script, model: freeModel(), visibleDir: markerDir})
	t.Cleanup(func() { _ = os.WriteFile(marker, []byte("x"), 0o600) })

	p := h.propose(t)
	run := h.approveAndStart(t, p)
	ctx := context.Background()

	waitFor(t, "запуск подал признаки жизни", func() bool {
		obs, err := h.rt.Observations(ctx, runtime.SubjectWorkerRun, run.ID, 50)
		return err == nil && len(obs) > 1
	})

	// Имитируем потерю процесса: отпускаем его и забываем идентичность,
	// как это выглядит после перезапуска Бэрримора.
	h.deleg.Runner().Detach(run.ID)
	_ = os.WriteFile(marker, []byte("x"), 0o600)
	waitFor(t, "процесс завершился", func() bool {
		r, err := h.deleg.Run(ctx, run.ID)
		return err == nil && r.Status != delegation.RunRunning
	})

	o, err := h.deleg.Get(ctx, p.Order.ID)
	if err != nil {
		t.Fatalf("чтение поручения: %v", err)
	}
	if o.State == delegation.StateCompleted {
		t.Fatal("поручение объявлено выполненным без отчёта и проверок")
	}
}

func TestReconcileResumesLiveRun(t *testing.T) {
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "stop")
	script := `
		echo '{"summary":"работаю"}'
		while [ ! -f ` + marker + ` ]; do sleep 0.1; done
		echo '{"summary":"готово"}'`

	h := newHarness(t, &fakeAdapter{script: script, model: freeModel(), visibleDir: markerDir})
	t.Cleanup(func() { _ = os.WriteFile(marker, []byte("x"), 0o600) })

	p := h.propose(t)
	run := h.approveAndStart(t, p)
	ctx := context.Background()

	waitFor(t, "первое событие прочитано", func() bool {
		obs, err := h.rt.Observations(ctx, runtime.SubjectWorkerRun, run.ID, 50)
		return err == nil && len(obs) > 1
	})

	// Теряем подключение, процесс продолжает жить.
	h.deleg.Runner().Detach(run.ID)
	if h.deleg.Runner().Attached(run.ID) {
		t.Fatal("подключение не разорвано")
	}

	res, err := h.deleg.Reconcile(ctx)
	if err != nil {
		t.Fatalf("сверка: %v", err)
	}
	if res.Resumed != 1 {
		t.Fatalf("живой запуск не возобновлён: %+v", res)
	}
	if !h.deleg.Runner().Attached(run.ID) {
		t.Fatal("чтение вывода не восстановлено")
	}
	_ = runner.ProcessIdentityForRun(run.ID)
}

// ---------- контролируемая запись ----------

// gitWorkspace превращает рабочий каталог стенда в настоящий репозиторий:
// контролируемая запись держится на git.
func (h *harness) gitWorkspace(t *testing.T) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", h.workDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Тест", "GIT_AUTHOR_EMAIL=t@localhost",
			"GIT_COMMITTER_NAME=Тест", "GIT_COMMITTER_EMAIL=t@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "исходное состояние")
}

func (h *harness) proposeWrite(t *testing.T, goal string) delegation.Proposal {
	t.Helper()
	ctx := context.Background()
	th, err := h.threads.Create(ctx, thread.CreateRequest{
		Title: "Нить с записью", Kind: thread.KindProject,
	})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}
	p, err := h.deleg.Propose(ctx, delegation.ProposeRequest{
		ThreadID: th.ID, Goal: goal, WorkspaceRoot: h.workDir, AuditOnly: false,
	})
	if err != nil {
		t.Fatalf("формирование поручения: %v", err)
	}
	return p
}

// Полный контур записи: исполнитель правит копию, каталог владельца остаётся
// нетронутым, изменения ждут решения и доходят только по нему.
func TestControlledWriteReachesOwnerOnlyByDecision(t *testing.T) {
	script := `
		echo '{"summary":"правлю"}'
		echo "добавлено исполнителем" >> README.md
		echo "новый файл" > добавленный.txt
		cat > "$OUT_DIR/last-message.txt" <<'REPORT'
{"summary":"правки внесены","findings":[],"limitations":"нет"}
REPORT
		echo '{"summary":"готово"}'`
	h := newHarness(t, &fakeAdapter{script: script, model: freeModel()})
	h.gitWorkspace(t)

	p := h.proposeWrite(t, "дописать строку в README")
	// Владельцу до запуска сказано, что запись идёт в копию.
	if !strings.Contains(p.Approval.Summary, "копию") {
		t.Fatalf("подтверждение не объясняет, куда пишет исполнитель: %q", p.Approval.Summary)
	}

	h.approveAndStart(t, p)
	ctx := context.Background()
	waitFor(t, "поручение завершилось", func() bool {
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil && (o.State == delegation.StateCompleted || o.State == delegation.StateFailed)
	})

	o, err := h.deleg.Get(ctx, p.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != delegation.StateCompleted {
		t.Fatalf("состояние %q, причина: %s", o.State, o.FailureReason)
	}

	// Каталог владельца не тронут.
	readme, err := os.ReadFile(filepath.Join(h.workDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readme), "добавлено исполнителем") {
		t.Fatal("исполнитель дописал прямо в каталог владельца, минуя копию")
	}
	if _, err := os.Stat(filepath.Join(h.workDir, "добавленный.txt")); err == nil {
		t.Fatal("исполнитель создал файл в каталоге владельца без решения владельца")
	}

	// Изменения собраны и ждут решения.
	if o.ChangeState != delegation.ChangeCollected {
		t.Fatalf("состояние изменений %q, ожидалось collected", o.ChangeState)
	}
	if len(o.ChangeSummary.Files) != 2 {
		t.Fatalf("собрано файлов %d, ожидалось 2: %+v", len(o.ChangeSummary.Files), o.ChangeSummary.Files)
	}
	if o.ChangeSummary.Patch == "" {
		t.Fatal("дифф не сохранён: владельцу нечего смотреть")
	}

	// Решение владельца — и только оно — доносит изменения.
	res, err := h.deleg.ApplyChanges(ctx, p.Order.ID, "проверено",
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatalf("применение: %v", err)
	}
	if !res.Applied {
		t.Fatal("применение не состоялось")
	}
	readme, err = os.ReadFile(filepath.Join(h.workDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "добавлено исполнителем") {
		t.Fatal("после решения владельца правка не дошла до файла")
	}

	after, err := h.deleg.Get(ctx, p.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ChangeState != delegation.ChangeApplied {
		t.Fatalf("состояние изменений после применения %q", after.ChangeState)
	}
	if after.ChangeDecidedAt == nil {
		t.Fatal("время решения не записано")
	}
}

// Отказ убирает копию и не трогает каталог владельца.
func TestDiscardedChangesNeverReachTheOwner(t *testing.T) {
	script := `
		echo "мусор" > мусор.txt
		cat > "$OUT_DIR/last-message.txt" <<'REPORT'
{"summary":"насорил","findings":[],"limitations":"нет"}
REPORT`
	h := newHarness(t, &fakeAdapter{script: script, model: freeModel()})
	h.gitWorkspace(t)

	p := h.proposeWrite(t, "что-нибудь сделать")
	h.approveAndStart(t, p)
	ctx := context.Background()
	waitFor(t, "поручение завершилось", func() bool {
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil && (o.State == delegation.StateCompleted || o.State == delegation.StateFailed)
	})

	o, _ := h.deleg.Get(ctx, p.Order.ID)
	copyPath := o.WorkCopyPath
	if copyPath == "" {
		t.Fatal("копия не создана")
	}

	if err := h.deleg.DiscardChanges(ctx, p.Order.ID, "не нужно",
		event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("отказ: %v", err)
	}

	if _, err := os.Stat(filepath.Join(h.workDir, "мусор.txt")); err == nil {
		t.Fatal("отброшенные изменения оказались в каталоге владельца")
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatal("копия не убрана после отказа")
	}

	after, _ := h.deleg.Get(ctx, p.Order.ID)
	if after.ChangeState != delegation.ChangeDiscarded {
		t.Fatalf("состояние изменений %q, ожидалось discarded", after.ChangeState)
	}
}

// Исполнитель ничего не изменил — так и записано, и решать владельцу нечего.
func TestNoChangesNeedNoDecision(t *testing.T) {
	script := `cat > "$OUT_DIR/last-message.txt" <<'REPORT'
{"summary":"менять нечего","findings":[],"limitations":"нет"}
REPORT`
	h := newHarness(t, &fakeAdapter{script: script, model: freeModel()})
	h.gitWorkspace(t)

	p := h.proposeWrite(t, "посмотреть и, если надо, поправить")
	h.approveAndStart(t, p)
	ctx := context.Background()
	waitFor(t, "поручение завершилось", func() bool {
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil && (o.State == delegation.StateCompleted || o.State == delegation.StateFailed)
	})

	o, _ := h.deleg.Get(ctx, p.Order.ID)
	if o.ChangeState != delegation.ChangeNone {
		t.Fatalf("состояние изменений %q, ожидалось none: решать владельцу нечего", o.ChangeState)
	}
	if _, err := h.deleg.ApplyChanges(ctx, p.Order.ID, "",
		event.Actor{Type: event.ActorPerson}); err == nil {
		t.Fatal("применение пустого набора изменений должно быть отказом")
	}
}

// Без git контролируемая запись не начинается, и причина названа.
func TestControlledWriteRefusedWithoutGit(t *testing.T) {
	h := newHarness(t, &fakeAdapter{script: "true", model: freeModel()})
	// Каталог намеренно не превращён в репозиторий.

	p := h.proposeWrite(t, "поправить")
	ctx := context.Background()
	if _, err := h.deleg.Approve(ctx, p.Approval.ID, "test",
		event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	_, err := h.deleg.Start(ctx, p.Order.ID, event.Actor{Type: event.ActorPerson})
	if err == nil {
		t.Fatal("запуск с записью начался в каталоге без git")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Fatalf("причина отказа неясна: %v", err)
	}

	o, _ := h.deleg.Get(ctx, p.Order.ID)
	if o.State != delegation.StateFailed {
		t.Fatalf("поручение осталось в состоянии %q вместо failed", o.State)
	}
}
