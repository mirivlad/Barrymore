package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/worker"
)

// Состояния подключения к потоку событий запуска.
const (
	AttachmentAttached = "attached"
	AttachmentLost     = "lost"
	AttachmentClosed   = "closed"
)

// Имена файлов внутри каталога запуска.
const (
	StdoutFile = "stdout.jsonl"
	StderrFile = "stderr.log"
	OutputDir  = "out"
)

// Sink сохраняет то, что runner наблюдает. Реализация живёт в делегировании,
// чтобы runner не знал ни о базе, ни о доменной модели поручений.
type Sink interface {
	SaveOffset(ctx context.Context, runID string, offset int64) error
	SetAttachment(ctx context.Context, runID, state string) error
	MarkExited(ctx context.Context, runID string, exitCode int, at time.Time, errText string) error
}

// Runner запускает и наблюдает процессы исполнителей.
type Runner struct {
	caps  Capabilities
	rt    *runtime.Runtime
	clock clock.Clock
	log   *slog.Logger
	sink  Sink

	// PollInterval задаёт частоту чтения хвоста вывода.
	PollInterval time.Duration

	mu     sync.Mutex
	active map[string]*attachment
	// attachWG считает только читателей вывода. Ожидание завершения самих
	// процессов исполнителей сюда не входит намеренно: они живут в собственных
	// scope и должны переживать перезапуск Бэрримора (сценарий H).
	attachWG sync.WaitGroup
}

type attachment struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New создаёт runner.
func New(rt *runtime.Runtime, clk clock.Clock, sink Sink, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		caps: DetectCapabilities(), rt: rt, clock: clk, sink: sink, log: log,
		PollInterval: 200 * time.Millisecond,
		active:       map[string]*attachment{},
	}
}

// Capabilities возвращает обнаруженные механизмы изоляции.
func (r *Runner) Capabilities() Capabilities { return r.caps }

// StartRequest — что запустить.
type StartRequest struct {
	RunID       string
	WorkOrderID string
	WorkerID    string
	Adapter     worker.Adapter
	Plan        worker.RunPlan
	RunDir      string
	AuditOnly   bool
	// Workspace — каталог, с которым работает исполнитель. При audit-only
	// монтируется только для чтения.
	Workspace string
	// MemoryMax и CPUQuota передаются systemd как свойства scope.
	MemoryMax string
	CPUQuota  string
}

// StartResult — что получилось.
type StartResult struct {
	Identity   ProcessIdentity `json:"identity"`
	Argv       []string        `json:"argv"`
	Profile    SandboxProfile  `json:"sandbox_profile"`
	StdoutPath string          `json:"stdout_path"`
	StderrPath string          `json:"stderr_path"`
	StartedAt  time.Time       `json:"started_at"`
}

// Start запускает исполнителя.
//
// Вывод пишется прямо в файл, а разбором занимается отдельный «хвостовой»
// читатель. Благодаря этому потеря подключения не теряет события: после
// восстановления чтение продолжается с сохранённого смещения.
func (r *Runner) Start(ctx context.Context, req StartRequest) (StartResult, error) {
	if req.RunID == "" {
		return StartResult{}, errors.New("runner: не задан идентификатор запуска")
	}
	outDir := filepath.Join(req.RunDir, OutputDir)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return StartResult{}, fmt.Errorf("runner: каталог запуска: %w", err)
	}

	unitName := "barrymore-run-" + sanitizeUnit(req.RunID) + ".scope"
	var props []string
	if req.MemoryMax != "" {
		props = append(props, "MemoryMax="+req.MemoryMax)
	}
	if req.CPUQuota != "" {
		props = append(props, "CPUQuota="+req.CPUQuota)
	}

	argv, profile, err := buildCommand(r.caps, req.Plan, commandOptions{
		AuditOnly:         req.AuditOnly,
		Workspace:         req.Workspace,
		WorkspaceWritable: !req.AuditOnly,
		UnitName:          unitName,
		ScopeProperties:   props,
	})
	if err != nil {
		return StartResult{}, err
	}

	stdoutPath := filepath.Join(req.RunDir, StdoutFile)
	stderrPath := filepath.Join(req.RunDir, StderrFile)
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return StartResult{}, fmt.Errorf("runner: файл вывода: %w", err)
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		stdout.Close()
		return StartResult{}, fmt.Errorf("runner: файл ошибок: %w", err)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	// Многие CLI не имеют флага рабочего каталога и опираются на текущий.
	if req.Plan.Dir != "" {
		cmd.Dir = req.Plan.Dir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = strings.NewReader(req.Plan.Stdin)
	// Минимальное окружение: наследуется только необходимое, остальное задаёт adapter.
	cmd.Env = minimalEnv(req.Plan.Env)
	// Отдельная группа процессов позволяет остановить всё дерево одним сигналом.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startedAt := r.clock.Now()
	if err := cmd.Start(); err != nil {
		stdout.Close()
		stderr.Close()
		return StartResult{}, fmt.Errorf("runner: запуск %s: %w", argv[0], err)
	}

	id := ProcessIdentity{UnitName: unitName, PID: cmd.Process.Pid}
	if r.caps.SystemdRun == "" {
		id.UnitName = ""
	}
	if ticks, err := readStartTicks(id.PID); err == nil {
		id.StartTicks = ticks
	} else {
		r.log.Warn("время старта процесса не прочитано; идентичность менее надёжна",
			"run", req.RunID, "pid", id.PID, "error", err)
	}

	res := StartResult{
		Identity: id, Argv: argv, Profile: profile,
		StdoutPath: stdoutPath, StderrPath: stderrPath, StartedAt: startedAt,
	}

	if _, err := r.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:        runtime.ObsRunStarted,
		SubjectType: runtime.SubjectWorkerRun,
		SubjectID:   req.RunID,
		ObservedAt:  startedAt,
		Source:      "runner",
		DedupeKey:   "run_started:" + req.RunID,
		Payload:     res,
	}); err != nil {
		return res, err
	}

	// Ожидание завершения живёт в отдельной горутине и намеренно не входит
	// в attachWG: остановка Бэрримора не должна ждать чужой процесс.
	go func() {
		defer stdout.Close()
		defer stderr.Close()
		r.wait(req, cmd, stderrPath)
	}()

	r.Attach(req.RunID, req.Adapter, stdoutPath, 0)
	return res, nil
}

func (r *Runner) wait(req StartRequest, cmd *exec.Cmd, stderrPath string) {
	err := cmd.Wait()
	at := r.clock.Now()

	exitCode := 0
	errText := ""
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
			errText = err.Error()
		}
	}
	// Хвост stderr помогает объяснить ненулевой код, не заставляя открывать файл.
	tail := tailFile(stderrPath, 2000)

	ctx := context.Background()
	if _, obsErr := r.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:        runtime.ObsRunExited,
		SubjectType: runtime.SubjectWorkerRun,
		SubjectID:   req.RunID,
		ObservedAt:  at,
		Source:      "runner",
		DedupeKey:   "run_exited:" + req.RunID,
		Payload: map[string]any{
			"exit_code":   exitCode,
			"error":       errText,
			"stderr_tail": tail,
		},
	}); obsErr != nil {
		r.log.Error("наблюдение о завершении не записано", "run", req.RunID, "error", obsErr)
	}
	if r.sink != nil {
		if err := r.sink.MarkExited(ctx, req.RunID, exitCode, at, errText); err != nil {
			r.log.Error("состояние запуска не сохранено", "run", req.RunID, "error", err)
		}
	}
}

// Attach начинает читать вывод запуска с указанного смещения.
//
// Повторный вызов для уже подключённого запуска ничего не делает: подключение
// должно быть идемпотентным, иначе восстановление породило бы двойные события.
func (r *Runner) Attach(runID string, a worker.Adapter, stdoutPath string, offset int64) {
	r.mu.Lock()
	if _, exists := r.active[runID]; exists {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	att := &attachment{cancel: cancel, done: make(chan struct{})}
	r.active[runID] = att
	r.mu.Unlock()

	if r.sink != nil {
		if err := r.sink.SetAttachment(context.Background(), runID, AttachmentAttached); err != nil {
			r.log.Error("состояние подключения не сохранено", "run", runID, "error", err)
		}
	}

	r.attachWG.Add(1)
	go func() {
		defer r.attachWG.Done()
		defer close(att.done)
		defer func() {
			r.mu.Lock()
			delete(r.active, runID)
			r.mu.Unlock()
		}()
		r.tail(ctx, runID, a, stdoutPath, offset)
	}()
}

// Detach прекращает чтение вывода, не трогая процесс.
//
// Используется при остановке Бэрримора и в проверках потери подключения.
func (r *Runner) Detach(runID string) {
	r.mu.Lock()
	att, ok := r.active[runID]
	r.mu.Unlock()
	if !ok {
		return
	}
	att.cancel()
	<-att.done
	if r.sink != nil {
		if err := r.sink.SetAttachment(context.Background(), runID, AttachmentLost); err != nil {
			r.log.Error("состояние подключения не сохранено", "run", runID, "error", err)
		}
	}
}

// Attached сообщает, читается ли вывод запуска прямо сейчас.
func (r *Runner) Attached(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[runID]
	return ok
}

// tail читает файл вывода, превращая строки в наблюдения.
func (r *Runner) tail(ctx context.Context, runID string, a worker.Adapter, path string, offset int64) {
	f, err := os.Open(path)
	if err != nil {
		r.log.Error("файл вывода не открыт", "run", runID, "path", path, "error", err)
		return
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			r.log.Error("смещение вывода не восстановлено", "run", runID, "error", err)
			offset = 0
		}
	}

	reader := bufio.NewReader(f)
	idle := 0
	for {
		if ctx.Err() != nil {
			return
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && err == nil {
			offset += int64(len(line))
			idle = 0
			r.emit(ctx, runID, a, line, offset)
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			r.log.Error("чтение вывода прервано", "run", runID, "error", err)
			return
		}
		// Неполная строка: возвращаемся к её началу и ждём остаток.
		if len(line) > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				r.log.Error("возврат к границе строки не удался", "run", runID, "error", err)
				return
			}
			reader.Reset(f)
		}

		idle++
		// Процесс завершился и новых строк нет — читать больше нечего.
		if idle > 3 && !r.Alive(ProcessIdentityForRun(runID)) {
			r.finishAttachment(ctx, runID, offset)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.PollInterval):
		}
	}
}

func (r *Runner) emit(ctx context.Context, runID string, a worker.Adapter, line []byte, offset int64) {
	ev, ok := a.ParseLine(line)
	if !ok {
		return
	}
	if ev.At.IsZero() {
		ev.At = r.clock.Now()
	}
	if _, err := r.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:        runtime.ObsRunEvent,
		SubjectType: runtime.SubjectWorkerRun,
		SubjectID:   runID,
		ObservedAt:  ev.At,
		Source:      "runner",
		// Сообщение исполнителя — недоверенные данные, а не факт о мире.
		SourceQuality: runtime.QualityReported,
		DedupeKey:     fmt.Sprintf("run_event:%s:%d", runID, offset),
		Payload:       ev,
	}); err != nil {
		r.log.Error("событие запуска не записано", "run", runID, "error", err)
		return
	}
	if r.sink != nil {
		if err := r.sink.SaveOffset(ctx, runID, offset); err != nil {
			r.log.Error("смещение вывода не сохранено", "run", runID, "error", err)
		}
	}
}

func (r *Runner) finishAttachment(ctx context.Context, runID string, offset int64) {
	if r.sink == nil {
		return
	}
	if err := r.sink.SaveOffset(ctx, runID, offset); err != nil {
		r.log.Error("смещение вывода не сохранено", "run", runID, "error", err)
	}
	if err := r.sink.SetAttachment(ctx, runID, AttachmentClosed); err != nil {
		r.log.Error("состояние подключения не сохранено", "run", runID, "error", err)
	}
}

// identityLookup позволяет tail узнавать состояние процесса.
// Значение устанавливается делегированием при старте и восстановлении.
var (
	identityMu sync.RWMutex
	identities = map[string]ProcessIdentity{}
)

// RememberIdentity сохраняет идентичность процесса для проверок живости.
func RememberIdentity(runID string, id ProcessIdentity) {
	identityMu.Lock()
	identities[runID] = id
	identityMu.Unlock()
}

// ForgetIdentity убирает запись об идентичности.
func ForgetIdentity(runID string) {
	identityMu.Lock()
	delete(identities, runID)
	identityMu.Unlock()
}

// ProcessIdentityForRun возвращает известную идентичность процесса запуска.
func ProcessIdentityForRun(runID string) ProcessIdentity {
	identityMu.RLock()
	defer identityMu.RUnlock()
	return identities[runID]
}

// Alive проверяет, жив ли процесс.
func (r *Runner) Alive(id ProcessIdentity) bool {
	if id.PID == 0 && id.UnitName == "" {
		return false
	}
	return checkLiveness(r.caps, id).Alive
}

// Liveness возвращает подробный вывод о состоянии процесса.
func (r *Runner) Liveness(id ProcessIdentity) Liveness {
	return checkLiveness(r.caps, id)
}

// Cancel останавливает запуск.
func (r *Runner) Cancel(ctx context.Context, runID string, id ProcessIdentity, hard bool) error {
	if err := terminate(r.caps, id, hard); err != nil {
		return err
	}
	_, err := r.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:        runtime.ObsProcessLiveness,
		SubjectType: runtime.SubjectWorkerRun,
		SubjectID:   runID,
		Source:      "runner",
		Payload:     map[string]any{"action": "cancel", "hard": hard},
	})
	return err
}

// Shutdown прекращает чтение выводов и дожидается завершения горутин.
//
// Процессы исполнителей при этом не убиваются: они живут в собственных
// scope и переживают перезапуск Бэрримора (сценарий H).
func (r *Runner) Shutdown() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.active))
	for id := range r.active {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Detach(id)
	}
	r.attachWG.Wait()
}

// minimalEnv собирает окружение процесса.
//
// Наследуется только то, без чего не работают базовые вещи. Переменные с
// секретами не пробрасываются: исполнитель пользуется собственной учётной записью.
func minimalEnv(extra []string) []string {
	keep := []string{"PATH", "HOME", "LANG", "LC_ALL", "TZ", "XDG_RUNTIME_DIR", "USER", "LOGNAME"}
	env := make([]string, 0, len(keep)+len(extra))
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

func sanitizeUnit(s string) string {
	var b strings.Builder
	for _, ch := range s {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-':
			b.WriteRune(ch)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func tailFile(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := int64(0)
	if info.Size() > max {
		start = info.Size() - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(data)
}

// MarshalIdentity сериализует идентичность для хранения.
func MarshalIdentity(id ProcessIdentity) string {
	b, err := json.Marshal(id)
	if err != nil {
		return "{}"
	}
	return string(b)
}
