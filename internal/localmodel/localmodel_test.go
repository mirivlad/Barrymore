package localmodel_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/localmodel"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

// fakeProvider отвечает так, как велено тестом.
type fakeProvider struct{ status model.Status }

func (p *fakeProvider) ID() string       { return "fake" }
func (p *fakeProvider) Describe() string { return "поддельный провайдер" }
func (p *fakeProvider) Probe(context.Context) model.Status {
	return p.status
}
func (p *fakeProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

type harness struct {
	sup  *localmodel.Supervisor
	rt   *runtime.Runtime
	clk  *clock.Fake
	prov *fakeProvider
	dir  string
	db   *store.DB
}

func newHarness(t *testing.T, spec localmodel.Spec) *harness {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	j := event.NewJournal(db, clk)
	rt := runtime.New(runtime.Config{DB: db, Journal: j, Clock: clk, Logger: testsupport.Logger(t)})
	prov := &fakeProvider{status: model.Status{Status: model.StatusUnreachable, Reason: "соединение отклонено"}}
	dir := t.TempDir()

	sup := localmodel.New(localmodel.Config{
		Spec: spec, Runtime: rt, Clock: clk, Logger: testsupport.Logger(t),
		StateDir: dir, Caps: runner.Capabilities{}, Provider: prov,
	})
	return &harness{sup: sup, rt: rt, clk: clk, prov: prov, dir: dir, db: db}
}

// writeIdentity подкладывает запись о процессе, как будто он был запущен.
func writeIdentity(t *testing.T, dir string, pid int) {
	t.Helper()
	id := runner.ProcessIdentity{PID: pid}
	if ticks, err := runner.StartTicks(pid); err == nil {
		id.StartTicks = ticks
	}
	data, err := json.Marshal(map[string]any{
		"identity": id, "started_at": time.Now().UTC(),
		"endpoint": "http://127.0.0.1:18080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "process.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gguf(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(p, []byte("не настоящие веса"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArgvCarriesSpikeParameters(t *testing.T) {
	spec := localmodel.Spec{
		ModelPath: "/w/model.gguf", Host: "127.0.0.1", Port: 18080,
		ContextSize: 32768, Threads: 14, GPULayers: 99, CPUMoE: 40, Jinja: true,
	}
	argv := spec.Argv("/bin/llama-server")

	for _, want := range [][2]string{
		{"-m", "/w/model.gguf"}, {"--port", "18080"}, {"-c", "32768"},
		{"-t", "14"}, {"-ngl", "99"}, {"-ncmoe", "40"},
	} {
		i := slices.Index(argv, want[0])
		if i < 0 || i+1 >= len(argv) || argv[i+1] != want[1] {
			t.Fatalf("в команде нет %s %s: %v", want[0], want[1], argv)
		}
	}
	if !slices.Contains(argv, "--jinja") {
		t.Fatalf("без --jinja принуждение к схеме работает не так, как проверялось: %v", argv)
	}
}

// Ноль параметра означает «умолчание llama-server», а не «передать ноль»:
// -ngl 0 и отсутствие -ngl — разные вещи.
func TestArgvOmitsUnsetTuning(t *testing.T) {
	argv := localmodel.Spec{ModelPath: "/w/model.gguf", Port: 18080, ContextSize: 4096}.
		Argv("/bin/llama-server")
	for _, flag := range []string{"-t", "-ngl", "-ncmoe", "--jinja"} {
		if slices.Contains(argv, flag) {
			t.Fatalf("незаданный параметр %s попал в команду: %v", flag, argv)
		}
	}
}

func TestResolveExplainsMissingModel(t *testing.T) {
	_, err := localmodel.Spec{ModelPath: "/нет/такого.gguf"}.Resolve()
	if err == nil {
		t.Fatal("отсутствующий файл модели должен быть отказом, а не молчанием")
	}
	if !strings.Contains(err.Error(), "/нет/такого.gguf") {
		t.Fatalf("причина не называет файл: %v", err)
	}
}

func TestResolveExplainsMissingBinary(t *testing.T) {
	_, err := localmodel.Spec{ModelPath: gguf(t), Binary: "/нет/такого/llama-server"}.Resolve()
	if err == nil {
		t.Fatal("отсутствующий llama-server должен быть отказом")
	}
	if !strings.Contains(err.Error(), "llama-server") {
		t.Fatalf("причина не называет инструмент: %v", err)
	}
}

// Ненастроенный надзор ничего не выдумывает и ни на что не жалуется.
func TestUnconfiguredIsQuiet(t *testing.T) {
	h := newHarness(t, localmodel.Spec{})
	if h.sup.Configured() || h.sup.Enabled() {
		t.Fatal("без файла модели надзор должен быть выключен")
	}
	if note := h.sup.StartupNote(); note != "" {
		t.Fatalf("выключенный надзор не должен считаться ограничением запуска: %q", note)
	}
	st, err := h.sup.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Serving || st.Loading {
		t.Fatal("ненастроенный надзор не должен утверждать ничего о состоянии")
	}
}

// Настроенная, но неподъёмная модель — честное ограничение запуска, а не отказ.
func TestUnrunnableModelIsAnHonestLimitation(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Binary: "/нет/такого/llama-server"})
	if !h.sup.Configured() {
		t.Fatal("модель задана, значит надзор настроен")
	}
	if h.sup.Enabled() {
		t.Fatal("без llama-server поднять модель нечем")
	}
	note := h.sup.StartupNote()
	if !strings.Contains(note, "вручную") {
		t.Fatalf("ограничение запуска не объясняет владельцу, что делать: %q", note)
	}
}

func TestObserveReportsServingAndUnmanaged(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Port: 18080})
	h.prov.status = model.Status{Status: model.StatusReady}

	st, err := h.sup.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Serving {
		t.Fatalf("отвечающий endpoint должен считаться работающей моделью: %+v", st)
	}
	if st.Managed {
		t.Fatal("Бэрримор не запускал этот сервер и не должен приписывать его себе")
	}
	if !strings.Contains(st.Reason, "не поднимал") {
		t.Fatalf("причина умалчивает о происхождении процесса: %q", st.Reason)
	}

	obs, err := h.rt.Observations(context.Background(), runtime.SubjectProvider, localmodel.SubjectID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].Kind != runtime.ObsLocalModel {
		t.Fatalf("наблюдение не записано: %+v", obs)
	}
}

// Живой процесс без ответа — это загрузка, а не отказ.
func TestObserveDistinguishesLoadingFromFailure(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Port: 18080})
	writeIdentity(t, h.dir, os.Getpid())

	st, err := h.sup.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Serving {
		t.Fatal("живой процесс сам по себе не означает готовности модели")
	}
	if !st.Loading {
		t.Fatalf("живой процесс без ответа должен считаться загрузкой: %+v", st)
	}
	if !st.Managed {
		t.Fatal("процесс с записанной идентичностью поднят Бэрримором")
	}
}

// Мёртвый процесс перестаёт быть загрузкой, а запись о нём убирается: иначе
// следующая проверка приняла бы призрак за живого.
func TestObserveForgetsDeadProcess(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Port: 18080})
	// Заведомо свободный номер: PID_MAX по умолчанию меньше.
	writeIdentity(t, h.dir, 4194303)

	st, err := h.sup.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Loading || st.Serving || st.Managed {
		t.Fatalf("мёртвый процесс не должен выглядеть живым: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "process.json")); !os.IsNotExist(err) {
		t.Fatal("запись о мёртвом процессе должна быть забыта")
	}
}

// Останавливать можно только то, что Бэрримор запускал сам.
func TestStopRefusesForeignProcess(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Port: 18080})
	err := h.sup.Stop(context.Background(), false)
	if err == nil {
		t.Fatal("остановка чужого процесса должна быть отказом")
	}
	if !strings.Contains(err.Error(), "не запускал") {
		t.Fatalf("причина отказа неясна: %v", err)
	}
}

// Стоячее ожидание создаётся один раз: перезапуск Бэрримора не должен плодить
// дубликаты, иначе одно расхождение считалось бы несколько раз.
func TestEnsureExpectationIsIdempotent(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Port: 18080})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := h.sup.EnsureExpectation(ctx); err != nil {
			t.Fatal(err)
		}
	}
	exps, err := h.rt.Expectations(ctx, runtime.SubjectProvider, localmodel.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, e := range exps {
		if e.Kind == runtime.KindLocalModelServing && e.Status == runtime.ExpectationPending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("стоячих ожиданий %d, ожидалось 1", pending)
	}
}

// Ожидание нужно и тогда, когда поднять модель нечем: заметить пропажу и
// сказать о ней Бэрримор обязан в любом случае.
func TestExpectationExistsWithoutAbilityToStart(t *testing.T) {
	h := newHarness(t, localmodel.Spec{ModelPath: gguf(t), Binary: "/нет/такого/llama-server"})
	ctx := context.Background()
	if err := h.sup.EnsureExpectation(ctx); err != nil {
		t.Fatal(err)
	}
	exps, err := h.rt.Expectations(ctx, runtime.SubjectProvider, localmodel.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exps) != 1 {
		t.Fatalf("ожиданий %d, ожидалось 1", len(exps))
	}
	if exps[0].ReactionPolicy != "" {
		t.Fatalf("реакция назначена там, где действовать нечем: %q", exps[0].ReactionPolicy)
	}
	if !strings.Contains(exps[0].Basis, "не может") {
		t.Fatalf("основание умалчивает об ограничении: %q", exps[0].Basis)
	}
}

// Локальные реакции выполняются внутри тика предиктивного контура. Реакция,
// ждущая загрузки 22 ГБ весов, остановила бы весь контур на минуты — и
// Бэрримор перестал бы замечать всё остальное.
func TestEnsureStartedDoesNotWaitOutTheLoad(t *testing.T) {
	h := newHarness(t, localmodel.Spec{
		ModelPath: gguf(t), Port: 18080, LoadTimeout: 2 * time.Second,
	})
	// Живой процесс, который не отвечает: ровно та ситуация, в которой
	// полное ожидание затянулось бы на час.
	writeIdentity(t, h.dir, os.Getpid())

	started := time.Now()
	st, err := h.sup.EnsureStarted(context.Background())
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("загрузка не должна быть ошибкой: %v", err)
	}
	if !st.Loading {
		t.Fatalf("живой процесс без ответа — это загрузка: %+v", st)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("реакция держала контур %s: это остановило бы всё наблюдение", elapsed)
	}
}
