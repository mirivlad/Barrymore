package runtime_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

type harness struct {
	db  *store.DB
	rt  *runtime.Runtime
	clk *clock.Fake
	j   *event.Journal
}

func newHarness(t *testing.T, path string, tune func(*runtime.Config)) *harness {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, path)
	j := event.NewJournal(db, clk)
	cfg := runtime.Config{DB: db, Journal: j, Clock: clk, Logger: testsupport.Logger(t)}
	if tune != nil {
		tune(&cfg)
	}
	return &harness{db: db, rt: runtime.New(cfg), clk: clk, j: j}
}

func tempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "barrymore.db")
}

// --- ожидания ---

func TestSignalExpectationSatisfiedByObservation(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)

	exp := mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType:   runtime.SubjectWorkerRun,
		SubjectID:     "run_1",
		Kind:          runtime.KindRunStarts,
		Params:        runtime.ParamsRunStarts{StartDeadline: 30 * time.Second},
		Basis:         "operational contract",
		CheckInterval: 5 * time.Second,
	})

	// Пока дедлайн не наступил, ожидание остаётся в силе.
	h.clk.Advance(10 * time.Second)
	tick(t, h)
	if got := reload(t, h, exp.ID); got.Status != runtime.ExpectationPending {
		t.Fatalf("до дедлайна статус %q, ожидался pending", got.Status)
	}

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	tick(t, h)

	got := reload(t, h, exp.ID)
	if got.Status != runtime.ExpectationSatisfied {
		t.Fatalf("после наблюдения о запуске статус %q, ожидался satisfied", got.Status)
	}
	if got.SatisfiedAt == nil {
		t.Fatal("не проставлено время удовлетворения ожидания")
	}
}

func TestSilenceIsNotAHangUntilMaxSilencePasses(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType:   runtime.SubjectWorkerRun,
		SubjectID:     "run_1",
		Kind:          runtime.KindRunSignal,
		Params:        runtime.ParamsRunSignal{MaxSilence: 60 * time.Second},
		CheckInterval: 10 * time.Second,
	})

	// Сценарий P: отсутствие вывода само по себе не считается зависанием.
	h.clk.Advance(30 * time.Second)
	tick(t, h)
	if n := openCount(t, h); n != 0 {
		t.Fatalf("тишина в 30с при допустимых 60с дала %d расхождений", n)
	}

	h.clk.Advance(45 * time.Second)
	tick(t, h)
	open := mustDiscrepancies(t, h, true)
	if len(open) != 1 {
		t.Fatalf("после превышения допустимой тишины ожидалось 1 расхождение, получено %d", len(open))
	}
	if open[0].Severity != runtime.SeverityWarning {
		t.Fatalf("тишина должна быть warning, а не %q: сначала диагностика, потом тревога", open[0].Severity)
	}
}

func TestRepeatedSignalMergesIntoOneDiscrepancy(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:          runtime.KindRunSignal,
		Params:        runtime.ParamsRunSignal{MaxSilence: 10 * time.Second},
		CheckInterval: 5 * time.Second,
	})

	for i := 0; i < 4; i++ {
		h.clk.Advance(15 * time.Second)
		tick(t, h)
	}

	open := mustDiscrepancies(t, h, true)
	if len(open) != 1 {
		t.Fatalf("повторные сигналы создали %d расхождений вместо одного", len(open))
	}
	if open[0].Occurrences < 2 {
		t.Fatalf("повторы не учтены: occurrences = %d", open[0].Occurrences)
	}
}

func TestAuditOnlyWriteIsCritical(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)

	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:             runtime.KindRunNoWrites,
		Params:           runtime.ParamsRunNoWrites{BaselineDigest: "sha256:base"},
		SeverityIfMissed: runtime.SeverityCritical,
		CheckInterval:    5 * time.Second,
	})

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsWorkspaceScan, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Payload: runtime.WorkspaceScanPayload{
			Digest: "sha256:changed", ChangedPaths: []string{"internal/foo.go"},
		},
	})
	h.clk.Advance(6 * time.Second)
	tick(t, h)

	open := mustDiscrepancies(t, h, true)
	if len(open) != 1 {
		t.Fatalf("запись при audit-only не зафиксирована: %d расхождений", len(open))
	}
	if open[0].Severity != runtime.SeverityCritical {
		t.Fatalf("запись при audit-only должна быть critical, получено %q", open[0].Severity)
	}
}

func TestExpectationExpiresWhenWindowCloses(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)

	until := testsupport.Epoch.Add(time.Minute)
	exp := mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:          runtime.KindRunReport,
		Params:        runtime.ParamsRunReport{RequiredArtifacts: []string{"report.json"}},
		WindowUntil:   &until,
		CheckInterval: 10 * time.Second,
	})

	h.clk.Advance(2 * time.Minute)
	tick(t, h)

	got := reload(t, h, exp.ID)
	if got.Status != runtime.ExpectationExpired {
		t.Fatalf("после закрытия окна статус %q, ожидался expired", got.Status)
	}
}

// --- локальные реакции ---

// reconnectPolicy имитирует восстановление attachment: две попытки, обе неудачные.
func failingPolicy(calls *int) *runtime.ReflexPolicy {
	return &runtime.ReflexPolicy{
		ID:               "run.reconnect",
		DiscrepancyKinds: []string{runtime.KindRunSignal},
		MaxAttempts:      2,
		ActionClass:      "read",
		EscalateTo:       "user",
		Act: func(ctx context.Context, in runtime.ReflexInput) (runtime.ReflexOutcome, error) {
			*calls++
			return runtime.ReflexOutcome{Succeeded: false, Detail: "переподключение не удалось"}, nil
		},
	}
}

func TestReflexBudgetSurvivesRestart(t *testing.T) {
	// Сценарий Q: максимум две попытки reconnect. Рестарт Бэрримора не должен
	// обнулять бюджет — иначе правило нарушается незаметно.
	path := tempDBPath(t)
	calls := 0

	h := newHarness(t, path, func(cfg *runtime.Config) {
		rx := runtime.NewReflexes()
		rx.MustRegister(failingPolicy(&calls))
		cfg.Reflexes = rx
	})

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:          runtime.KindRunSignal,
		Params:        runtime.ParamsRunSignal{MaxSilence: 10 * time.Second},
		CheckInterval: 5 * time.Second,
	})

	// Первый тик: расхождение и первая попытка.
	h.clk.Advance(15 * time.Second)
	tick(t, h)
	if calls != 1 {
		t.Fatalf("после первого тика попыток %d, ожидалась 1", calls)
	}

	open := mustDiscrepancies(t, h, true)
	if len(open) != 1 {
		t.Fatalf("ожидалось одно расхождение, получено %d", len(open))
	}
	discrepancyID := open[0].ID

	// Рестарт: новый процесс, новая база подключений, тот же файл.
	if err := h.db.Close(); err != nil {
		t.Fatalf("закрытие базы: %v", err)
	}
	h2 := newHarness(t, path, func(cfg *runtime.Config) {
		rx := runtime.NewReflexes()
		rx.MustRegister(failingPolicy(&calls))
		cfg.Reflexes = rx
	})
	h2.clk.Advance(15 * time.Second)

	budget, err := h2.rt.Budget(context.Background(), discrepancyID, "run.reconnect")
	if err != nil {
		t.Fatalf("чтение бюджета после рестарта: %v", err)
	}
	if budget.Used != 1 {
		t.Fatalf("после рестарта израсходовано %d попыток, ожидалась 1: бюджет не пережил рестарт", budget.Used)
	}

	// Вторая попытка расходует бюджет полностью.
	tick(t, h2)
	if calls != 2 {
		t.Fatalf("после второго тика попыток %d, ожидалось 2", calls)
	}

	// Третья попытка не должна запускаться — только эскалация.
	h2.clk.Advance(15 * time.Second)
	res := tick(t, h2)
	if calls != 2 {
		t.Fatalf("третья попытка запущена вопреки лимиту: попыток %d", calls)
	}
	if res.Escalations == 0 {
		t.Fatal("после исчерпания бюджета не создана эскалация")
	}

	after := mustDiscrepancies(t, h2, true)
	if len(after) != 1 || after[0].Status != runtime.DiscrepancyEscalated {
		t.Fatalf("расхождение не переведено в escalated: %+v", after)
	}
}

func TestSuccessfulReflexResolvesDiscrepancyWithoutUser(t *testing.T) {
	// Сценарий O: потеря attachment восстанавливается локально, без LLM и без
	// уведомления пользователя как о катастрофе.
	calls := 0
	h := newHarness(t, tempDBPath(t), func(cfg *runtime.Config) {
		rx := runtime.NewReflexes()
		rx.MustRegister(&runtime.ReflexPolicy{
			ID:               "run.reattach",
			DiscrepancyKinds: []string{runtime.KindRunSignal},
			MaxAttempts:      2,
			ActionClass:      "read",
			Act: func(ctx context.Context, in runtime.ReflexInput) (runtime.ReflexOutcome, error) {
				calls++
				return runtime.ReflexOutcome{
					Succeeded:  true,
					Detail:     "attachment восстановлен",
					Resolution: "процесс жив, поток событий восстановлен",
					Observations: []runtime.ObservationRequest{{
						Kind:        runtime.ObsRunAttached,
						SubjectType: in.Discrepancy.SubjectType,
						SubjectID:   in.Discrepancy.SubjectID,
					}},
				}, nil
			},
		})
		cfg.Reflexes = rx
	})

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:          runtime.KindRunSignal,
		Params:        runtime.ParamsRunSignal{MaxSilence: 10 * time.Second},
		CheckInterval: 5 * time.Second,
	})

	h.clk.Advance(15 * time.Second)
	tick(t, h)

	if calls != 1 {
		t.Fatalf("реакция вызвана %d раз, ожидался 1", calls)
	}
	if n := openCount(t, h); n != 0 {
		t.Fatalf("успешная реакция не закрыла расхождение: осталось %d открытых", n)
	}

	// Реакция должна была оставить наблюдение, а не только запись в журнале.
	obs, err := h.rt.Observations(context.Background(), runtime.SubjectWorkerRun, "run_1", 50)
	if err != nil {
		t.Fatalf("чтение наблюдений: %v", err)
	}
	var reattached bool
	for _, o := range obs {
		if o.Kind == runtime.ObsRunAttached {
			reattached = true
		}
	}
	if !reattached {
		t.Fatal("реакция не записала наблюдение о восстановлении attachment")
	}
}

type denyAll struct{}

func (denyAll) Check(context.Context, runtime.PolicyRequest) runtime.PolicyResult {
	return runtime.PolicyResult{Allowed: false, Rule: "test.deny", Reason: "запрещено тестовой политикой"}
}

func TestPolicyDenialStopsReflexAndEscalates(t *testing.T) {
	calls := 0
	h := newHarness(t, tempDBPath(t), func(cfg *runtime.Config) {
		rx := runtime.NewReflexes()
		rx.MustRegister(failingPolicy(&calls))
		cfg.Reflexes = rx
		cfg.Policy = denyAll{}
	})

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:          runtime.KindRunSignal,
		Params:        runtime.ParamsRunSignal{MaxSilence: 10 * time.Second},
		CheckInterval: 5 * time.Second,
	})

	h.clk.Advance(15 * time.Second)
	res := tick(t, h)

	if calls != 0 {
		t.Fatalf("реакция выполнена вопреки отказу политики: вызовов %d", calls)
	}
	if res.Escalations == 0 {
		t.Fatal("отказ политики не привёл к эскалации")
	}

	var denied int
	if err := h.db.Reader().QueryRowContext(context.Background(),
		`SELECT count(*) FROM policy_decisions WHERE allowed = 0`).Scan(&denied); err != nil {
		t.Fatalf("чтение решений политики: %v", err)
	}
	if denied == 0 {
		t.Fatal("отказ политики не записан в аудит")
	}
}

// --- пересборка проекций ---

func TestProjectionsRebuildFromJournal(t *testing.T) {
	// ADR 0003: состояние выводится из журнала. Проверяем это буквально:
	// стираем проекции, проигрываем журнал заново и сравниваем.
	ctx := context.Background()
	calls := 0
	h := newHarness(t, tempDBPath(t), func(cfg *runtime.Config) {
		rx := runtime.NewReflexes()
		rx.MustRegister(failingPolicy(&calls))
		cfg.Reflexes = rx
	})

	mustObserve(t, h, runtime.ObservationRequest{
		Kind: runtime.ObsRunStarted, SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
	})
	if _, err := h.rt.UpdateSnapshot(ctx, runtime.SnapshotRequest{
		Scope: "worker:codex", Status: "unknown", Confidence: 0.2, Source: "probe",
		Reason: "квота не проверялась",
	}); err != nil {
		t.Fatalf("обновление снимка: %v", err)
	}
	mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectWorkerRun, SubjectID: "run_1",
		Kind:          runtime.KindRunSignal,
		Params:        runtime.ParamsRunSignal{MaxSilence: 10 * time.Second},
		CheckInterval: 5 * time.Second,
	})
	for i := 0; i < 3; i++ {
		h.clk.Advance(15 * time.Second)
		tick(t, h)
	}

	before := snapshotState(t, h)

	reg := projection.NewRegistry()
	h.rt.Projections(reg)
	if err := projection.Rebuild(ctx, h.db, h.j, reg); err != nil {
		t.Fatalf("пересборка проекций: %v", err)
	}

	after := snapshotState(t, h)
	for table, want := range before {
		if after[table] != want {
			t.Errorf("таблица %s: до пересборки %v, после %v", table, want, after[table])
		}
	}

	// Статус расхождения тоже обязан восстановиться, а не только количество строк.
	open := mustDiscrepancies(t, h, false)
	if len(open) != 1 {
		t.Fatalf("после пересборки расхождений %d, ожидалось 1", len(open))
	}
	if open[0].Status != runtime.DiscrepancyEscalated {
		t.Fatalf("после пересборки статус расхождения %q, ожидался escalated", open[0].Status)
	}
	if open[0].Occurrences < 2 {
		t.Fatalf("после пересборки occurrences = %d", open[0].Occurrences)
	}
}

// --- вспомогательное ---

func mustExpectation(t *testing.T, h *harness, req runtime.ExpectationRequest) runtime.Expectation {
	t.Helper()
	exp, err := h.rt.CreateExpectation(context.Background(), req)
	if err != nil {
		t.Fatalf("создание ожидания %s: %v", req.Kind, err)
	}
	return exp
}

func mustObserve(t *testing.T, h *harness, req runtime.ObservationRequest) runtime.Observation {
	t.Helper()
	o, err := h.rt.RecordObservation(context.Background(), req)
	if err != nil {
		t.Fatalf("запись наблюдения %s: %v", req.Kind, err)
	}
	return o
}

func tick(t *testing.T, h *harness) runtime.TickResult {
	t.Helper()
	res, err := h.rt.Tick(context.Background())
	if err != nil {
		t.Fatalf("тик планировщика: %v", err)
	}
	return res
}

func reload(t *testing.T, h *harness, id string) runtime.Expectation {
	t.Helper()
	exp, err := h.rt.ExpectationByID(context.Background(), id)
	if err != nil {
		t.Fatalf("чтение ожидания %s: %v", id, err)
	}
	return exp
}

func mustDiscrepancies(t *testing.T, h *harness, openOnly bool) []runtime.Discrepancy {
	t.Helper()
	d, err := h.rt.Discrepancies(context.Background(), openOnly, 100)
	if err != nil {
		t.Fatalf("чтение расхождений: %v", err)
	}
	return d
}

func openCount(t *testing.T, h *harness) int {
	t.Helper()
	return len(mustDiscrepancies(t, h, true))
}

func snapshotState(t *testing.T, h *harness) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range runtime.ProjectionTables {
		var n int
		if err := h.db.Reader().QueryRowContext(context.Background(),
			"SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("подсчёт строк %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}
