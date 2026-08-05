package delegation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/thread"
	"github.com/mirivlad/barrymore/internal/worker"
)

// ErrNotFound возвращается, когда поручение или его часть отсутствует.
var ErrNotFound = errors.New("поручение не найдено")

// Service ведёт поручения.
type Service struct {
	db       *store.DB
	journal  *event.Journal
	clock    clock.Clock
	rt       *runtime.Runtime
	registry *worker.Registry
	threads  *thread.Service
	runner   *runner.Runner
	log      *slog.Logger
	dataRoot string
	// watcher узнаёт об исходах поручений.
	watcher Watcher

	// modelPolicy задаёт допустимую стоимость моделей по умолчанию.
	modelPolicy worker.ModelPolicy
}

// Watcher узнаёт, чем закончилось поручение.
//
// Опыт об исполнителях собирается тем же путём, что и опыт о собственных
// умениях: иначе выбор между «сам» и «поручить» опирался бы на измерение
// только одной из сторон.
type Watcher interface {
	OrderFinished(ctx context.Context, workerID, title, outcome, evidence string, tookMS int64)
}

// Watch подключает наблюдателя за исходами поручений.
func (s *Service) Watch(w Watcher) { s.watcher = w }

// Config — параметры сервиса.
type Config struct {
	DB          *store.DB
	Journal     *event.Journal
	Clock       clock.Clock
	Runtime     *runtime.Runtime
	Registry    *worker.Registry
	Threads     *thread.Service
	Logger      *slog.Logger
	DataRoot    string
	ModelPolicy worker.ModelPolicy
}

// New создаёт сервис делегирования.
func New(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if len(cfg.ModelPolicy.AllowedTiers) == 0 {
		cfg.ModelPolicy = worker.FreeOnly()
	}
	s := &Service{
		db: cfg.DB, journal: cfg.Journal, clock: cfg.Clock, rt: cfg.Runtime,
		registry: cfg.Registry, threads: cfg.Threads, log: cfg.Logger,
		dataRoot: cfg.DataRoot, modelPolicy: cfg.ModelPolicy,
	}
	s.runner = runner.New(cfg.Runtime, cfg.Clock, s, cfg.Logger)
	return s
}

// Runner возвращает супервизор процессов.
func (s *Service) Runner() *runner.Runner { return s.runner }

// ProposeRequest — запрос на поручение.
type ProposeRequest struct {
	ThreadID           string
	Title              string
	Goal               string
	Why                string
	WorkspaceRoot      string
	AuditOnly          bool
	AcceptanceCriteria []string
	Constraints        []string
	// WorkerID позволяет владельцу переопределить выбор runtime.
	WorkerID string
	// Hard помечает задачу как трудную: только тогда привлекается мастер по вызову.
	Hard bool
	// ModelPolicyName переопределяет политику стоимости для этого поручения.
	ModelPolicyName string
	Actor           event.Actor
}

// Proposal — предложение с объяснением выбора.
type Proposal struct {
	Order      WorkOrder       `json:"order"`
	Approval   Approval        `json:"approval"`
	Candidates []worker.Ranked `json:"candidates"`
}

// Propose формирует поручение и запрашивает подтверждение.
//
// Выбор исполнителя делает runtime и объясняет его; владелец может переопределить.
func (s *Service) Propose(ctx context.Context, req ProposeRequest) (Proposal, error) {
	if req.ThreadID == "" || req.Goal == "" {
		return Proposal{}, fmt.Errorf("поручение без нити или цели")
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorPerson}
	}
	if _, err := s.threads.Get(ctx, req.ThreadID); err != nil {
		return Proposal{}, err
	}
	if req.WorkspaceRoot != "" {
		if _, err := os.Stat(req.WorkspaceRoot); err != nil {
			return Proposal{}, fmt.Errorf("рабочий каталог %q недоступен: %w", req.WorkspaceRoot, err)
		}
	}

	policy := s.modelPolicy
	if req.ModelPolicyName != "" {
		p, err := worker.ParseModelPolicy(req.ModelPolicyName)
		if err != nil {
			return Proposal{}, err
		}
		policy = p
	}
	if req.WorkerID != "" || req.Hard {
		// Ручной выбор и трудная задача — осознанное решение владельца,
		// поэтому мастер по вызову перестаёт быть запрещённым.
		policy.AllowSpecialists = true
	}

	required := []string{worker.CapRepositoryAudit}
	candidates, err := s.registry.Rank(ctx, worker.RankRequest{
		RequiredCapabilities: required,
		AuditOnly:            req.AuditOnly,
		RequireRunnable:      true,
		ModelPolicy:          policy,
		Hard:                 req.Hard,
	})
	if err != nil {
		return Proposal{}, err
	}

	chosen, rationale, err := pickWorker(candidates, req.WorkerID)
	if err != nil {
		return Proposal{}, err
	}

	now := s.clock.Now()
	artifacts := []string{"last-message.txt"}
	order := WorkOrder{
		ID: ids.New(ids.WorkOrder), ThreadID: req.ThreadID,
		Title: req.Title, Goal: req.Goal, Why: req.Why,
		State: StateProposed, WorkerID: chosen.View.Worker.ID, WorkerRationale: rationale,
		TrustLevel: worker.TrustWorkspaceRead, AuditOnly: req.AuditOnly,
		WorkspaceRoot: req.WorkspaceRoot,
		Contract:      DefaultContract(artifacts),
		AcceptanceCriteria: defaultIfEmpty(req.AcceptanceCriteria, []string{
			"отчёт соответствует обязательной схеме",
			"рабочий каталог не изменён",
			"ограничения проверки перечислены явно",
		}),
		Constraints:       req.Constraints,
		RequiredArtifacts: artifacts,
		Model:             chosen.Model.Ref,
		ModelCostTier:     chosen.Model.CostTier,
		ModelRationale:    chosen.ModelReason,
		CreatedAt:         now, UpdatedAt: now,
	}
	if !req.AuditOnly {
		order.TrustLevel = worker.TrustWorktreeWrite
	}
	if order.Title == "" {
		order.Title = "Аудит " + filepath.Base(req.WorkspaceRoot)
	}

	approval := Approval{
		ID: ids.New(ids.Approval), WorkOrderID: order.ID,
		ActionClass: "process_execute",
		Summary: fmt.Sprintf("Запустить %s (модель %s, стоимость: %s) в каталоге %s%s",
			chosen.View.Worker.DisplayName, chosen.Model.Ref, chosen.Model.CostTier,
			order.WorkspaceRoot, auditSuffix(req.AuditOnly)),
		Scope: ScopeInfo{
			Worker: chosen.View.Worker.DisplayName, WorkspaceRoot: order.WorkspaceRoot,
			WriteLevel: writeLevel(req.AuditOnly), Network: true,
			Model: chosen.Model.Ref, CostTier: chosen.Model.CostTier,
			Notes: "исполнитель обращается к своему провайдеру по собственной учётной записи; " +
				costNote(chosen.Model),
		},
		Status: ApprovalPending, RequestedAt: now,
	}

	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		env, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: order.ID, ExpectedRevision: 0,
			EventType: EvProposed, Actor: req.Actor, Payload: order,
		})
		if err != nil {
			return err
		}
		order.Revision = env.StreamRevision
		if err := applyOrder(ctx, tx, order); err != nil {
			return err
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: order.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvApprovalRequested, Actor: req.Actor, Payload: approval,
		}); err != nil {
			return err
		}
		return applyApproval(ctx, tx, approval)
	})
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Order: order, Approval: approval, Candidates: candidates}, nil
}

func pickWorker(candidates []worker.Ranked, override string) (worker.Ranked, string, error) {
	if len(candidates) == 0 {
		return worker.Ranked{}, "", fmt.Errorf(
			"подходящих исполнителей нет: сначала выполните обнаружение штата")
	}
	if override != "" {
		for _, c := range candidates {
			if c.View.Worker.ID != override {
				continue
			}
			if c.Blocked {
				return worker.Ranked{}, "", fmt.Errorf(
					"исполнитель %s выбран вручную, но не может взять поручение: %s",
					c.View.Worker.DisplayName, c.BlockReason)
			}
			return c, "выбран владельцем вручную; " + joinReasons(c.Reasons), nil
		}
		return worker.Ranked{}, "", fmt.Errorf("исполнитель %q не найден в реестре", override)
	}

	best := candidates[0]
	if best.Blocked {
		return worker.Ranked{}, "", fmt.Errorf(
			"ни один исполнитель не может взять поручение; лучший кандидат %s заблокирован: %s",
			best.View.Worker.DisplayName, best.BlockReason)
	}
	return best, joinReasons(best.Reasons), nil
}

// Approve выдаёт подтверждение.
func (s *Service) Approve(ctx context.Context, approvalID, decidedBy string, actor event.Actor) (Approval, error) {
	return s.decideApproval(ctx, approvalID, ApprovalGranted, decidedBy, "", actor)
}

// Deny отклоняет подтверждение.
func (s *Service) Deny(ctx context.Context, approvalID, decidedBy, reason string, actor event.Actor) (Approval, error) {
	return s.decideApproval(ctx, approvalID, ApprovalDenied, decidedBy, reason, actor)
}

func (s *Service) decideApproval(ctx context.Context, approvalID, status, decidedBy, reason string, actor event.Actor) (Approval, error) {
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	var out Approval
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		ap, err := approvalByID(ctx, tx, approvalID)
		if err != nil {
			return err
		}
		if ap.Status != ApprovalPending {
			return fmt.Errorf("подтверждение %s уже в состоянии %s", approvalID, ap.Status)
		}
		now := s.clock.Now()
		p := approvalDecisionPayload{
			ID: approvalID, WorkOrderID: ap.WorkOrderID, Status: status,
			DecidedAt: now, DecidedBy: decidedBy, Reason: reason,
		}
		if status == ApprovalGranted {
			p.OrderState = StateApproved
		}
		eventType := EvApprovalGranted
		if status == ApprovalDenied {
			eventType = EvApprovalDenied
			p.OrderState = StateCancelled
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: ap.WorkOrderID, ExpectedRevision: event.AnyRevision,
			EventType: eventType, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		if err := applyApprovalDecision(ctx, tx, p); err != nil {
			return err
		}
		out, err = approvalByID(ctx, tx, approvalID)
		return err
	})
	return out, err
}

// Start готовит и запускает поручение.
//
// До запуска: проверяется подтверждение, снимается слепок каталога, собирается
// и сохраняется ContextPack, создаются ожидания из операционного контракта.
func (s *Service) Start(ctx context.Context, orderID string, actor event.Actor) (WorkerRun, error) {
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return WorkerRun{}, err
	}
	switch order.State {
	case StateApproved:
	case StateProposed:
		return WorkerRun{}, fmt.Errorf(
			"поручение %s ещё не подтверждено: запуск исполнителя требует явного разрешения", orderID)
	default:
		return WorkerRun{}, fmt.Errorf("поручение %s находится в состоянии %s и не может быть запущено",
			orderID, order.State)
	}
	granted, err := s.hasGrantedApproval(ctx, orderID)
	if err != nil {
		return WorkerRun{}, err
	}
	if !granted {
		return WorkerRun{}, fmt.Errorf("для поручения %s нет действующего подтверждения", orderID)
	}

	w, err := s.registry.Get(ctx, order.WorkerID)
	if err != nil {
		return WorkerRun{}, err
	}
	adapter, ok := s.registry.Adapter(w.AdapterID)
	if !ok {
		return WorkerRun{}, fmt.Errorf("adapter %q не зарегистрирован", w.AdapterID)
	}

	// Слепок каталога снимается до запуска: без исходного состояния проверка
	// «ничего не изменилось» была бы пустым утверждением.
	baseline, err := ScanWorkspace(ctx, order.WorkspaceRoot)
	if err != nil {
		return WorkerRun{}, err
	}

	runID := ids.New(ids.WorkerRun)
	runDir := filepath.Join(s.dataRoot, "runs", runID)
	scratchDir := filepath.Join(runDir, "scratch")
	for _, dir := range []string{filepath.Join(runDir, runner.OutputDir), scratchDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return WorkerRun{}, fmt.Errorf("каталог запуска: %w", err)
		}
	}

	schemaPath, err := WriteReportSchema(runDir)
	if err != nil {
		return WorkerRun{}, err
	}

	// Контролируемая запись: исполнитель получает копию, а не каталог владельца.
	// До решения владельца его каталог не трогают вообще, поэтому проверка
	// «ничего не изменилось» остаётся в силе и для поручений с записью.
	workspaceForRun := order.WorkspaceRoot
	var copyInfo WorkCopy
	if !order.AuditOnly {
		copyInfo, err = PrepareWorkCopy(ctx, order.WorkspaceRoot,
			filepath.Join(runDir, "workcopy"), "barrymore/"+shortID(order.ID))
		if err != nil {
			if markErr := s.fail(ctx, order.ID,
				"копия рабочего каталога не подготовлена: "+err.Error()); markErr != nil {
				s.log.Error("состояние поручения не обновлено", "order", order.ID, "error", markErr)
			}
			return WorkerRun{}, err
		}
		workspaceForRun = copyInfo.Path
		s.log.Info("подготовлена копия для контролируемой записи",
			"order", order.ID, "files", copyInfo.FileCount, "path", copyInfo.Path)
	}

	detail, err := s.threads.Detail(ctx, order.ThreadID)
	if err != nil {
		return WorkerRun{}, err
	}
	order.UpdatedAt = s.clock.Now()
	pack := BuildContextPack(order, detail, baseline, ReportSchema())
	packPath, packSum, err := WritePack(runDir, pack)
	if err != nil {
		return WorkerRun{}, err
	}

	plan, err := adapter.Plan(ctx, worker.Installation{
		ExecutablePath: w.ExecutablePath, Version: w.Version, AuthState: w.AuthState,
	}, worker.RunRequest{
		RunID: runID, WorkDir: workspaceForRun, Prompt: pack.Prompt(),
		AuditOnly: order.AuditOnly, OutputDir: filepath.Join(runDir, runner.OutputDir),
		ScratchDir:       scratchDir,
		Model:            order.Model,
		ReportSchemaPath: schemaPath, Timeout: order.Contract.MaxDuration.D(),
	})
	if err != nil {
		return WorkerRun{}, err
	}

	prep := preparePayload{
		OrderID: order.ID, ContextPackPath: packPath, ContextPackChecksum: packSum,
		ContextPackRevision: order.ContextPackRevision + 1,
		WorkspaceGitHead:    baseline.GitHead, WorkspaceBaseline: baseline.Digest,
		State: StatePreparing, At: s.clock.Now(),
		WorkCopyPath: copyInfo.Path, WorkCopyBranch: copyInfo.Branch,
		WorkCopyBaseline: copyInfo.Baseline,
	}
	if _, err := s.journal.Write(ctx, func(tx *sql.Tx, tw *event.TxWriter) error {
		if _, err := tw.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: order.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvPrepared, Actor: actor, Payload: prep,
		}); err != nil {
			return err
		}
		return applyPrepared(ctx, tx, prep)
	}); err != nil {
		return WorkerRun{}, err
	}

	started, err := s.runner.Start(ctx, runner.StartRequest{
		RunID: runID, WorkOrderID: order.ID, WorkerID: order.WorkerID,
		Adapter: adapter, Plan: plan, RunDir: runDir,
		AuditOnly: order.AuditOnly, Workspace: workspaceForRun,
	})
	if err != nil {
		if markErr := s.fail(ctx, order.ID, "запуск исполнителя не удался: "+err.Error()); markErr != nil {
			s.log.Error("состояние поручения не обновлено", "order", order.ID, "error", markErr)
		}
		return WorkerRun{}, err
	}
	runner.RememberIdentity(runID, started.Identity)

	profile, _ := json.Marshal(started.Profile)
	run := WorkerRun{
		ID: runID, WorkOrderID: order.ID, WorkerID: order.WorkerID, RunDir: runDir,
		UnitName: started.Identity.UnitName, PID: started.Identity.PID,
		PIDStartTicks: started.Identity.StartTicks, Argv: started.Argv,
		SandboxProfile: string(profile), Status: RunRunning,
		AttachmentState: runner.AttachmentAttached, StartedAt: started.StartedAt,
	}

	if _, err := s.journal.Write(ctx, func(tx *sql.Tx, tw *event.TxWriter) error {
		if _, err := tw.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: order.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvRunStarted, Actor: event.Actor{Type: event.ActorRuntime}, Payload: run,
		}); err != nil {
			return err
		}
		if err := applyRunStarted(ctx, tx, run); err != nil {
			return err
		}
		sp := statePayload{OrderID: order.ID, State: StateRunning, At: started.StartedAt}
		if _, err := tw.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: order.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvStateChanged, Actor: event.Actor{Type: event.ActorRuntime}, Payload: sp,
		}); err != nil {
			return err
		}
		return applyState(ctx, tx, sp)
	}); err != nil {
		return WorkerRun{}, err
	}

	if err := s.createExpectations(ctx, order, run, baseline); err != nil {
		return run, err
	}
	s.reflectStart(ctx, order)
	return run, nil
}

// createExpectations выводит ожидания из операционного контракта.
//
// Функция детерминирована, поэтому после рестарта ожидания пересоздаются
// одинаково (ADR 0009).
func (s *Service) createExpectations(ctx context.Context, order WorkOrder, run WorkerRun, baseline WorkspaceState) error {
	until := run.StartedAt.Add(order.Contract.MaxDuration.D())

	specs := []runtime.ExpectationRequest{
		{
			Kind:             runtime.KindRunStarts,
			Params:           runtime.ParamsRunStarts{StartDeadline: order.Contract.StartDeadline.D()},
			Basis:            "операционный контракт поручения",
			SeverityIfMissed: runtime.SeverityCritical,
			CheckInterval:    5 * time.Second,
		},
		{
			Kind:             runtime.KindRunSignal,
			Params:           runtime.ParamsRunSignal{MaxSilence: order.Contract.MaxSilence.D()},
			Basis:            "исполнитель должен подавать наблюдаемые признаки работы",
			SeverityIfMissed: runtime.SeverityWarning,
			CheckInterval:    15 * time.Second,
			ReactionPolicy:   PolicyReattach,
		},
		{
			Kind: runtime.KindRunReport,
			Params: runtime.ParamsRunReport{
				RequiredArtifacts: order.RequiredArtifacts,
				CollectDeadline:   order.Contract.CollectDeadline.D(),
			},
			Basis:            "без обязательных артефактов результат не принимается",
			SeverityIfMissed: runtime.SeverityCritical,
			CheckInterval:    15 * time.Second,
		},
	}
	if order.ModelCostTier == worker.CostFree {
		// Бесплатность решена до запуска. Это ожидание — страховка:
		// если провайдер изменил условия, любое списание останавливает работу.
		specs = append(specs, runtime.ExpectationRequest{
			Kind: runtime.KindRunCostPolicy,
			Params: runtime.ParamsRunCostPolicy{
				MaxCost: 0, Model: order.Model,
				PolicyName: "модель выбрана как бесплатная",
			},
			Basis:            "на бесплатной модели списаний быть не должно",
			SeverityIfMissed: runtime.SeverityCritical,
			CheckInterval:    10 * time.Second,
			ReactionPolicy:   PolicyStopOnCharge,
		})
	}
	if order.AuditOnly {
		specs = append(specs, runtime.ExpectationRequest{
			Kind:             runtime.KindRunNoWrites,
			Params:           runtime.ParamsRunNoWrites{BaselineDigest: baseline.Digest},
			Basis:            "поручение выполняется только на чтение",
			SeverityIfMissed: runtime.SeverityCritical,
			CheckInterval:    30 * time.Second,
		})
	}

	for _, spec := range specs {
		spec.SubjectType = runtime.SubjectWorkerRun
		spec.SubjectID = run.ID
		spec.WindowFrom = run.StartedAt
		spec.WindowUntil = &until
		spec.CorrelationID = order.ID
		if _, err := s.rt.CreateExpectation(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

// Cancel останавливает запуск и поручение.
func (s *Service) Cancel(ctx context.Context, orderID, reason string, actor event.Actor) error {
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	runs, err := s.runsOf(ctx, orderID)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status != RunRunning && r.Status != RunStarting {
			continue
		}
		id := runner.ProcessIdentity{UnitName: r.UnitName, PID: r.PID, StartTicks: r.PIDStartTicks}
		if err := s.runner.Cancel(ctx, r.ID, id, false); err != nil {
			s.log.Warn("мягкая остановка не удалась", "run", r.ID, "error", err)
		}
		s.runner.Detach(r.ID)
	}
	return s.setState(ctx, orderID, StateCancelled, reason, actor)
}

func (s *Service) fail(ctx context.Context, orderID, reason string) error {
	return s.setState(ctx, orderID, StateFailed, reason, event.Actor{Type: event.ActorRuntime})
}

func (s *Service) setState(ctx context.Context, orderID, state, reason string, actor event.Actor) error {
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		p := statePayload{OrderID: orderID, State: state, Reason: reason, At: s.clock.Now()}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: orderID, ExpectedRevision: event.AnyRevision,
			EventType: EvStateChanged, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyState(ctx, tx, p)
	})
	return err
}

// --- реализация runner.Sink ---

// SaveOffset сохраняет позицию чтения вывода.
func (s *Service) SaveOffset(ctx context.Context, runID string, offset int64) error {
	now := s.clock.Now()
	_, err := s.db.Writer().ExecContext(ctx,
		`UPDATE worker_runs SET stdout_offset = ?, last_signal_at = ? WHERE id = ?`,
		offset, ts(now), runID)
	if err != nil {
		return fmt.Errorf("сохранение позиции чтения %s: %w", runID, err)
	}
	return nil
}

// SetAttachment сохраняет состояние подключения к выводу.
func (s *Service) SetAttachment(ctx context.Context, runID, state string) error {
	_, err := s.db.Writer().ExecContext(ctx,
		`UPDATE worker_runs SET attachment_state = ? WHERE id = ?`, state, runID)
	if err != nil {
		return fmt.Errorf("сохранение состояния подключения %s: %w", runID, err)
	}
	return nil
}

// MarkExited фиксирует завершение процесса и запускает приёмку результата.
func (s *Service) MarkExited(ctx context.Context, runID string, exitCode int, at time.Time, errText string) error {
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return err
	}
	p := runExitPayload{
		RunID: runID, OrderID: run.WorkOrderID, Status: RunExited,
		ExitCode: exitCode, ExitedAt: at, Error: errText,
	}
	if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: run.WorkOrderID, ExpectedRevision: event.AnyRevision,
			EventType: EvRunExited, Actor: event.Actor{Type: event.ActorRuntime}, Payload: p,
		}); err != nil {
			return err
		}
		return applyRunExit(ctx, tx, p)
	}); err != nil {
		return err
	}
	runner.ForgetIdentity(runID)

	// Приёмка выполняется отдельно: сбой проверки не должен потерять сам факт
	// завершения процесса.
	if err := s.Finalize(ctx, run.WorkOrderID, runID); err != nil {
		s.log.Error("приёмка результата не выполнена",
			"order", run.WorkOrderID, "run", runID, "error", err)
		return err
	}
	return nil
}

func defaultIfEmpty(v, fallback []string) []string {
	if len(v) == 0 {
		return fallback
	}
	return v
}

// costNote объясняет стоимость выбранной модели.
func costNote(m worker.Model) string {
	switch m.CostTier {
	case worker.CostFree:
		return "модель бесплатна, платного списания не ожидается"
	case worker.CostSubscription:
		return "модель расходует квоту подписки"
	case worker.CostPaid:
		return "модель платная: запуск приведёт к списанию"
	default:
		return "стоимость модели неизвестна"
	}
}

func auditSuffix(auditOnly bool) string {
	if auditOnly {
		return " только для чтения"
	}
	// Формулировка важна: «с правом записи» звучит так, будто исполнитель
	// сейчас полезет в каталог владельца. Он полезет в копию, и до отдельного
	// решения владельца ничего оттуда не выйдет.
	return " с записью в копию каталога; изменения дойдут до вас только по " +
		"вашему отдельному решению"
}

func writeLevel(auditOnly bool) string {
	if auditOnly {
		return "none"
	}
	return "изолированная копия каталога"
}

func joinReasons(reasons []string) string {
	if len(reasons) == 0 {
		return "явных оснований не зафиксировано"
	}
	out := reasons[0]
	for _, r := range reasons[1:] {
		out += "; " + r
	}
	return out
}

// Projections регистрирует проекторы делегирования.
func (s *Service) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvProposed, projectOrder)
	reg.On(EvApprovalRequested, projectApproval)
	reg.On(EvApprovalGranted, projectApprovalDecision)
	reg.On(EvApprovalDenied, projectApprovalDecision)
	reg.On(EvPrepared, projectPrepared)
	reg.On(EvRunStarted, projectRunStarted)
	reg.On(EvRunUpdated, projectRunUpdate)
	reg.On(EvRunExited, projectRunExit)
	reg.On(EvRunCancelled, projectRunExit)
	reg.On(EvRunOrphaned, projectRunExit)
	reg.On(EvArtifactCollected, projectArtifact)
	reg.On(EvVerifyStarted, projectVerification)
	reg.On(EvVerifyCompleted, projectVerificationResult)
	reg.On(EvStateChanged, projectState)
	reg.On(EvChangeCollected, projectChangeCollected)
	reg.On(EvChangeDecided, projectChangeDecided)
}

// ---------- контролируемая запись ----------

// recordChange сохраняет то, что исполнитель сделал в копии.
func (s *Service) recordChange(ctx context.Context, orderID string, change Change) error {
	p := changeCollectedPayload{OrderID: orderID, Change: change, At: s.clock.Now()}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: orderID, ExpectedRevision: event.AnyRevision,
			EventType: EvChangeCollected, Actor: event.Actor{Type: event.ActorRuntime},
			Payload: p,
		}); err != nil {
			return err
		}
		return applyChangeCollected(ctx, tx, p)
	})
	return err
}

// ApplyChanges переносит изменения исполнителя в каталог владельца.
//
// Отдельное действие по отдельному решению (05_STAFF_AND_DELEGATION §10).
// Ничего не коммитится: владелец смотрит наложенное своими инструментами
// и решает сам, а откат остаётся обычным `git checkout`.
func (s *Service) ApplyChanges(ctx context.Context, orderID, note string, actor event.Actor) (ApplyResult, error) {
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return ApplyResult{}, err
	}
	if order.ChangeState != ChangeCollected {
		return ApplyResult{}, fmt.Errorf(
			"применять нечего: изменения в состоянии %q", order.ChangeState)
	}
	if order.ChangeSummary.Truncated {
		return ApplyResult{}, errors.New(
			"дифф был обрезан по размеру, применить его целиком нельзя: " +
				"изменения лежат в копии, перенесите их своими инструментами")
	}

	res, err := ApplyChange(ctx, order.WorkspaceRoot, order.ChangeSummary.Patch)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := s.decideChange(ctx, orderID, ChangeApplied, note, actor); err != nil {
		return res, err
	}
	return res, nil
}

// DiscardChanges отказывается от изменений и убирает копию.
func (s *Service) DiscardChanges(ctx context.Context, orderID, note string, actor event.Actor) error {
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.WorkCopyPath != "" {
		if err := RemoveWorkCopy(WorkCopy{Path: order.WorkCopyPath}); err != nil {
			// Не смертельно: решение владельца важнее уборки, а копия лежит
			// в каталоге запуска и никому не мешает.
			s.log.Warn("копия не удалена", "order", orderID, "error", err)
		}
	}
	return s.decideChange(ctx, orderID, ChangeDiscarded, note, actor)
}

func (s *Service) decideChange(ctx context.Context, orderID, state, note string, actor event.Actor) error {
	p := changeDecidedPayload{OrderID: orderID, State: state, Note: note, At: s.clock.Now()}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: orderID, ExpectedRevision: event.AnyRevision,
			EventType: EvChangeDecided, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyChangeDecided(ctx, tx, p)
	})
	return err
}
