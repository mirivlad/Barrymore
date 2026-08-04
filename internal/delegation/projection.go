package delegation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

type preparePayload struct {
	OrderID             string    `json:"order_id"`
	ContextPackPath     string    `json:"context_pack_path"`
	ContextPackChecksum string    `json:"context_pack_checksum"`
	ContextPackRevision int       `json:"context_pack_revision"`
	WorkspaceGitHead    string    `json:"workspace_git_head"`
	WorkspaceBaseline   string    `json:"workspace_baseline"`
	State               string    `json:"state"`
	At                  time.Time `json:"at"`
}

type statePayload struct {
	OrderID string    `json:"order_id"`
	State   string    `json:"state"`
	Reason  string    `json:"reason,omitempty"`
	At      time.Time `json:"at"`
}

type approvalDecisionPayload struct {
	ID          string    `json:"id"`
	WorkOrderID string    `json:"work_order_id"`
	Status      string    `json:"status"`
	DecidedAt   time.Time `json:"decided_at"`
	DecidedBy   string    `json:"decided_by"`
	Reason      string    `json:"reason,omitempty"`
	// OrderState — состояние поручения после решения.
	OrderState string `json:"order_state,omitempty"`
}

type runExitPayload struct {
	RunID    string    `json:"run_id"`
	OrderID  string    `json:"order_id"`
	Status   string    `json:"status"`
	ExitCode int       `json:"exit_code"`
	ExitedAt time.Time `json:"exited_at"`
	Error    string    `json:"error,omitempty"`
}

type runUpdatePayload struct {
	RunID           string `json:"run_id"`
	Status          string `json:"status,omitempty"`
	AttachmentState string `json:"attachment_state,omitempty"`
	Error           string `json:"error,omitempty"`
}

// ---------- проекторы ----------

func applyOrder(ctx context.Context, tx *sql.Tx, o WorkOrder) error {
	contract, _ := json.Marshal(o.Contract)
	criteria, _ := json.Marshal(orEmpty(o.AcceptanceCriteria))
	constraints, _ := json.Marshal(orEmpty(o.Constraints))
	artifacts, _ := json.Marshal(orEmpty(o.RequiredArtifacts))
	audit := 0
	if o.AuditOnly {
		audit = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO work_orders (id, thread_id, title, goal, why, state, worker_id,
		    worker_rationale, trust_level, audit_only, workspace_root, workspace_git_head,
		    workspace_baseline, context_pack_path, context_pack_checksum, context_pack_revision,
		    operational_contract, acceptance_criteria, constraints_json, required_artifacts,
		    created_at, updated_at, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		o.ID, o.ThreadID, o.Title, o.Goal, o.Why, o.State, nullable(o.WorkerID),
		o.WorkerRationale, o.TrustLevel, audit, o.WorkspaceRoot, o.WorkspaceGitHead,
		o.WorkspaceBaseline, o.ContextPackPath, o.ContextPackChecksum, o.ContextPackRevision,
		string(contract), string(criteria), string(constraints), string(artifacts),
		ts(o.CreatedAt), ts(o.UpdatedAt), o.Revision)
	if err != nil {
		return fmt.Errorf("проекция поручения %s: %w", o.ID, err)
	}
	return nil
}

func projectOrder(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var o WorkOrder
	if err := env.Decode(&o); err != nil {
		return err
	}
	o.Revision = env.StreamRevision
	return applyOrder(ctx, tx, o)
}

func applyPrepared(ctx context.Context, tx *sql.Tx, p preparePayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE work_orders
		   SET context_pack_path = ?, context_pack_checksum = ?, context_pack_revision = ?,
		       workspace_git_head = ?, workspace_baseline = ?, state = ?, updated_at = ?
		 WHERE id = ?`,
		p.ContextPackPath, p.ContextPackChecksum, p.ContextPackRevision,
		p.WorkspaceGitHead, p.WorkspaceBaseline, p.State, ts(p.At), p.OrderID)
	if err != nil {
		return fmt.Errorf("проекция подготовки поручения %s: %w", p.OrderID, err)
	}
	return nil
}

func projectPrepared(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p preparePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyPrepared(ctx, tx, p)
}

func applyState(ctx context.Context, tx *sql.Tx, p statePayload) error {
	var started, finished, approved any
	switch p.State {
	case StateRunning:
		started = ts(p.At)
	case StateCompleted, StateFailed, StateCancelled:
		finished = ts(p.At)
	case StateApproved:
		approved = ts(p.At)
	}
	outcome := ""
	switch p.State {
	case StateCompleted:
		outcome = "verified"
	case StateFailed:
		outcome = "failed"
	case StateCancelled:
		outcome = "cancelled"
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE work_orders
		   SET state = ?, updated_at = ?,
		       started_at = COALESCE(started_at, ?),
		       finished_at = COALESCE(?, finished_at),
		       approved_at = COALESCE(approved_at, ?),
		       outcome = CASE WHEN ? = '' THEN outcome ELSE ? END,
		       failure_reason = CASE WHEN ? = '' THEN failure_reason ELSE ? END
		 WHERE id = ?`,
		p.State, ts(p.At), started, finished, approved,
		outcome, outcome, p.Reason, p.Reason, p.OrderID)
	if err != nil {
		return fmt.Errorf("проекция состояния поручения %s: %w", p.OrderID, err)
	}
	return nil
}

func projectState(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p statePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyState(ctx, tx, p)
}

func applyApproval(ctx context.Context, tx *sql.Tx, a Approval) error {
	scope, _ := json.Marshal(a.Scope)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO approvals (id, work_order_id, action_class, summary, scope, status,
		                       requested_at, expires_at, max_cost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		a.ID, nullable(a.WorkOrderID), a.ActionClass, a.Summary, string(scope), a.Status,
		ts(a.RequestedAt), tsp(a.ExpiresAt), a.MaxCost)
	if err != nil {
		return fmt.Errorf("проекция подтверждения %s: %w", a.ID, err)
	}
	return nil
}

func projectApproval(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var a Approval
	if err := env.Decode(&a); err != nil {
		return err
	}
	return applyApproval(ctx, tx, a)
}

func applyApprovalDecision(ctx context.Context, tx *sql.Tx, p approvalDecisionPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE approvals SET status = ?, decided_at = ?, decided_by = ?, reason = ? WHERE id = ?`,
		p.Status, ts(p.DecidedAt), p.DecidedBy, p.Reason, p.ID)
	if err != nil {
		return fmt.Errorf("проекция решения по подтверждению %s: %w", p.ID, err)
	}
	if p.OrderState == "" {
		return nil
	}
	return applyState(ctx, tx, statePayload{
		OrderID: p.WorkOrderID, State: p.OrderState, Reason: p.Reason, At: p.DecidedAt,
	})
}

func projectApprovalDecision(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p approvalDecisionPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyApprovalDecision(ctx, tx, p)
}

func applyRunStarted(ctx context.Context, tx *sql.Tx, r WorkerRun) error {
	argv, _ := json.Marshal(orEmpty(r.Argv))
	_, err := tx.ExecContext(ctx, `
		INSERT INTO worker_runs (id, work_order_id, worker_id, run_dir, unit_name, pid,
		    pid_start_ticks, argv, sandbox_profile, status, attachment_state, stdout_offset,
		    started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		r.ID, r.WorkOrderID, r.WorkerID, r.RunDir, r.UnitName, r.PID, int64(r.PIDStartTicks),
		string(argv), r.SandboxProfile, r.Status, r.AttachmentState, r.StdoutOffset,
		ts(r.StartedAt))
	if err != nil {
		return fmt.Errorf("проекция запуска %s: %w", r.ID, err)
	}
	return nil
}

func projectRunStarted(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var r WorkerRun
	if err := env.Decode(&r); err != nil {
		return err
	}
	return applyRunStarted(ctx, tx, r)
}

func applyRunUpdate(ctx context.Context, tx *sql.Tx, p runUpdatePayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE worker_runs
		   SET status = CASE WHEN ? = '' THEN status ELSE ? END,
		       attachment_state = CASE WHEN ? = '' THEN attachment_state ELSE ? END,
		       error = CASE WHEN ? = '' THEN error ELSE ? END
		 WHERE id = ?`,
		p.Status, p.Status, p.AttachmentState, p.AttachmentState, p.Error, p.Error, p.RunID)
	if err != nil {
		return fmt.Errorf("проекция изменения запуска %s: %w", p.RunID, err)
	}
	return nil
}

func projectRunUpdate(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p runUpdatePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyRunUpdate(ctx, tx, p)
}

func applyRunExit(ctx context.Context, tx *sql.Tx, p runExitPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE worker_runs
		   SET status = ?, exit_code = ?, exited_at = ?, error = ?, attachment_state = 'closed'
		 WHERE id = ?`,
		p.Status, p.ExitCode, ts(p.ExitedAt), p.Error, p.RunID)
	if err != nil {
		return fmt.Errorf("проекция завершения запуска %s: %w", p.RunID, err)
	}
	return nil
}

func projectRunExit(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p runExitPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyRunExit(ctx, tx, p)
}

func applyArtifact(ctx context.Context, tx *sql.Tx, a Artifact) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts (id, run_id, work_order_id, name, path, size, checksum, kind, collected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (run_id, name) DO UPDATE SET
		    size = excluded.size, checksum = excluded.checksum, collected_at = excluded.collected_at`,
		a.ID, a.RunID, a.WorkOrderID, a.Name, a.Path, a.Size, a.Checksum, a.Kind, ts(a.CollectedAt))
	if err != nil {
		return fmt.Errorf("проекция артефакта %s: %w", a.Name, err)
	}
	return nil
}

func projectArtifact(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var a Artifact
	if err := env.Decode(&a); err != nil {
		return err
	}
	return applyArtifact(ctx, tx, a)
}

func applyVerification(ctx context.Context, tx *sql.Tx, v Verification) error {
	cmd, _ := json.Marshal(orEmpty(v.Command))
	_, err := tx.ExecContext(ctx, `
		INSERT INTO verifications (id, work_order_id, run_id, kind, name, status, detail,
		                           command, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    status = excluded.status, detail = excluded.detail, finished_at = excluded.finished_at`,
		v.ID, v.WorkOrderID, nullable(v.RunID), v.Kind, v.Name, v.Status, v.Detail,
		string(cmd), ts(v.StartedAt), tsp(v.FinishedAt))
	if err != nil {
		return fmt.Errorf("проекция проверки %s: %w", v.Name, err)
	}
	return nil
}

func projectVerification(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var v Verification
	if err := env.Decode(&v); err != nil {
		return err
	}
	return applyVerification(ctx, tx, v)
}

func projectVerificationResult(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	return projectVerification(ctx, tx, env)
}

// ---------- чтение ----------

const selectOrderColumns = `
	SELECT id, thread_id, title, goal, why, state, COALESCE(worker_id, ''), worker_rationale,
	       trust_level, audit_only, workspace_root, workspace_git_head, workspace_baseline,
	       context_pack_path, context_pack_checksum, context_pack_revision, operational_contract,
	       acceptance_criteria, constraints_json, required_artifacts, created_at, updated_at,
	       approved_at, started_at, finished_at, outcome, failure_reason, revision
	FROM work_orders`

type scanner interface{ Scan(dest ...any) error }

func scanOrder(row scanner) (WorkOrder, error) {
	var (
		o                                          WorkOrder
		audit                                      int
		contract, criteria, constraints, artifacts string
		createdAt, updatedAt                       string
		approvedAt, startedAt, finishedAt          sql.NullString
	)
	err := row.Scan(&o.ID, &o.ThreadID, &o.Title, &o.Goal, &o.Why, &o.State, &o.WorkerID,
		&o.WorkerRationale, &o.TrustLevel, &audit, &o.WorkspaceRoot, &o.WorkspaceGitHead,
		&o.WorkspaceBaseline, &o.ContextPackPath, &o.ContextPackChecksum, &o.ContextPackRevision,
		&contract, &criteria, &constraints, &artifacts, &createdAt, &updatedAt,
		&approvedAt, &startedAt, &finishedAt, &o.Outcome, &o.FailureReason, &o.Revision)
	if err != nil {
		return WorkOrder{}, err
	}
	o.AuditOnly = audit == 1
	if err := json.Unmarshal([]byte(contract), &o.Contract); err != nil {
		return WorkOrder{}, fmt.Errorf("разбор контракта поручения %s: %w", o.ID, err)
	}
	_ = json.Unmarshal([]byte(criteria), &o.AcceptanceCriteria)
	_ = json.Unmarshal([]byte(constraints), &o.Constraints)
	_ = json.Unmarshal([]byte(artifacts), &o.RequiredArtifacts)
	if o.CreatedAt, err = parseTS(createdAt); err != nil {
		return WorkOrder{}, err
	}
	if o.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return WorkOrder{}, err
	}
	if o.ApprovedAt, err = parseTSPtr(approvedAt); err != nil {
		return WorkOrder{}, err
	}
	if o.StartedAt, err = parseTSPtr(startedAt); err != nil {
		return WorkOrder{}, err
	}
	if o.FinishedAt, err = parseTSPtr(finishedAt); err != nil {
		return WorkOrder{}, err
	}
	return o, nil
}

// Get возвращает поручение.
func (s *Service) Get(ctx context.Context, id string) (WorkOrder, error) {
	row := s.db.Reader().QueryRowContext(ctx, selectOrderColumns+` WHERE id = ?`, id)
	o, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkOrder{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return WorkOrder{}, fmt.Errorf("чтение поручения %s: %w", id, err)
	}
	return o, nil
}

// List возвращает поручения, свежие сверху.
func (s *Service) List(ctx context.Context, threadID string, limit int) ([]WorkOrder, error) {
	query := selectOrderColumns
	var args []any
	if threadID != "" {
		query += ` WHERE thread_id = ?`
		args = append(args, threadID)
	}
	if limit <= 0 {
		limit = 100
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("выборка поручений: %w", err)
	}
	defer rows.Close()

	out := []WorkOrder{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Detail возвращает поручение со всем содержимым.
func (s *Service) Detail(ctx context.Context, id string) (Detail, error) {
	o, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Order: o}
	if d.Runs, err = s.runsOf(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Artifacts, err = s.artifactsOf(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Verifications, err = s.verificationsOf(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Approvals, err = s.approvalsOf(ctx, id); err != nil {
		return Detail{}, err
	}
	return d, nil
}

const selectRunColumns = `
	SELECT id, work_order_id, worker_id, run_dir, unit_name, pid, pid_start_ticks, argv,
	       sandbox_profile, status, attachment_state, stdout_offset, started_at, exited_at,
	       exit_code, last_signal_at, error
	FROM worker_runs`

func scanRun(row scanner) (WorkerRun, error) {
	var (
		r                    WorkerRun
		argv                 string
		startTicks           int64
		startedAt            string
		exitedAt, lastSignal sql.NullString
		exitCode             sql.NullInt64
	)
	err := row.Scan(&r.ID, &r.WorkOrderID, &r.WorkerID, &r.RunDir, &r.UnitName, &r.PID,
		&startTicks, &argv, &r.SandboxProfile, &r.Status, &r.AttachmentState,
		&r.StdoutOffset, &startedAt, &exitedAt, &exitCode, &lastSignal, &r.Error)
	if err != nil {
		return WorkerRun{}, err
	}
	r.PIDStartTicks = uint64(startTicks)
	_ = json.Unmarshal([]byte(argv), &r.Argv)
	if r.StartedAt, err = parseTS(startedAt); err != nil {
		return WorkerRun{}, err
	}
	if r.ExitedAt, err = parseTSPtr(exitedAt); err != nil {
		return WorkerRun{}, err
	}
	if r.LastSignalAt, err = parseTSPtr(lastSignal); err != nil {
		return WorkerRun{}, err
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		r.ExitCode = &code
	}
	return r, nil
}

func (s *Service) runByID(ctx context.Context, id string) (WorkerRun, error) {
	row := s.db.Reader().QueryRowContext(ctx, selectRunColumns+` WHERE id = ?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerRun{}, fmt.Errorf("%w: запуск %s", ErrNotFound, id)
	}
	if err != nil {
		return WorkerRun{}, fmt.Errorf("чтение запуска %s: %w", id, err)
	}
	return r, nil
}

// Run возвращает запуск.
func (s *Service) Run(ctx context.Context, id string) (WorkerRun, error) { return s.runByID(ctx, id) }

func (s *Service) runsOf(ctx context.Context, orderID string) ([]WorkerRun, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		selectRunColumns+` WHERE work_order_id = ? ORDER BY started_at`, orderID)
	if err != nil {
		return nil, fmt.Errorf("чтение запусков поручения %s: %w", orderID, err)
	}
	defer rows.Close()

	out := []WorkerRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRuns возвращает незавершённые запуски. Используется при восстановлении.
func (s *Service) ActiveRuns(ctx context.Context) ([]WorkerRun, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		selectRunColumns+` WHERE status IN ('starting','running') ORDER BY started_at`)
	if err != nil {
		return nil, fmt.Errorf("чтение незавершённых запусков: %w", err)
	}
	defer rows.Close()

	out := []WorkerRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) artifactsOf(ctx context.Context, orderID string) ([]Artifact, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, run_id, work_order_id, name, path, size, checksum, kind, collected_at
		  FROM artifacts WHERE work_order_id = ? ORDER BY name`, orderID)
	if err != nil {
		return nil, fmt.Errorf("чтение артефактов %s: %w", orderID, err)
	}
	defer rows.Close()

	out := []Artifact{}
	for rows.Next() {
		var (
			a           Artifact
			collectedAt string
		)
		if err := rows.Scan(&a.ID, &a.RunID, &a.WorkOrderID, &a.Name, &a.Path, &a.Size,
			&a.Checksum, &a.Kind, &collectedAt); err != nil {
			return nil, err
		}
		if a.CollectedAt, err = parseTS(collectedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) verificationsOf(ctx context.Context, orderID string) ([]Verification, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, work_order_id, COALESCE(run_id, ''), kind, name, status, detail, command,
		       started_at, finished_at
		  FROM verifications WHERE work_order_id = ? ORDER BY started_at`, orderID)
	if err != nil {
		return nil, fmt.Errorf("чтение проверок %s: %w", orderID, err)
	}
	defer rows.Close()

	out := []Verification{}
	for rows.Next() {
		var (
			v          Verification
			cmd        string
			startedAt  string
			finishedAt sql.NullString
		)
		if err := rows.Scan(&v.ID, &v.WorkOrderID, &v.RunID, &v.Kind, &v.Name, &v.Status,
			&v.Detail, &cmd, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cmd), &v.Command)
		if v.StartedAt, err = parseTS(startedAt); err != nil {
			return nil, err
		}
		if v.FinishedAt, err = parseTSPtr(finishedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const selectApprovalColumns = `
	SELECT id, COALESCE(work_order_id, ''), action_class, summary, scope, status, requested_at,
	       decided_at, decided_by, reason, expires_at, max_cost
	FROM approvals`

func scanApproval(row scanner) (Approval, error) {
	var (
		a                    Approval
		scope                string
		requestedAt          string
		decidedAt, expiresAt sql.NullString
	)
	err := row.Scan(&a.ID, &a.WorkOrderID, &a.ActionClass, &a.Summary, &scope, &a.Status,
		&requestedAt, &decidedAt, &a.DecidedBy, &a.Reason, &expiresAt, &a.MaxCost)
	if err != nil {
		return Approval{}, err
	}
	_ = json.Unmarshal([]byte(scope), &a.Scope)
	if a.RequestedAt, err = parseTS(requestedAt); err != nil {
		return Approval{}, err
	}
	if a.DecidedAt, err = parseTSPtr(decidedAt); err != nil {
		return Approval{}, err
	}
	if a.ExpiresAt, err = parseTSPtr(expiresAt); err != nil {
		return Approval{}, err
	}
	return a, nil
}

func approvalByID(ctx context.Context, tx *sql.Tx, id string) (Approval, error) {
	row := tx.QueryRowContext(ctx, selectApprovalColumns+` WHERE id = ?`, id)
	a, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, fmt.Errorf("%w: подтверждение %s", ErrNotFound, id)
	}
	return a, err
}

func (s *Service) approvalsOf(ctx context.Context, orderID string) ([]Approval, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		selectApprovalColumns+` WHERE work_order_id = ? ORDER BY requested_at`, orderID)
	if err != nil {
		return nil, fmt.Errorf("чтение подтверждений %s: %w", orderID, err)
	}
	defer rows.Close()

	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PendingApprovals возвращает ожидающие решения подтверждения.
func (s *Service) PendingApprovals(ctx context.Context) ([]Approval, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		selectApprovalColumns+` WHERE status = 'pending' ORDER BY requested_at`)
	if err != nil {
		return nil, fmt.Errorf("чтение ожидающих подтверждений: %w", err)
	}
	defer rows.Close()

	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) hasGrantedApproval(ctx context.Context, orderID string) (bool, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM approvals WHERE work_order_id = ? AND status = 'granted'`,
		orderID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("проверка подтверждения %s: %w", orderID, err)
	}
	return n > 0, nil
}

// ---------- вспомогательное ----------

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func tsp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("разбор времени %q: %w", s, err)
	}
	return t.UTC(), nil
}

func parseTSPtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := parseTS(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
