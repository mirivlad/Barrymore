// Package delegation ведёт поручения от предложения до проверенного результата.
//
// Runtime владеет контекстом, политиками, наблюдением и проверкой.
// Исполнитель владеет только выполнением (ADR 0002).
package delegation

import (
	"encoding/json"
	"time"
)

// Состояния поручения (02_DOMAIN_MODEL §7).
const (
	StateDraft       = "draft"
	StateProposed    = "proposed"
	StateApproved    = "approved"
	StatePreparing   = "preparing"
	StateRunning     = "running"
	StatePaused      = "paused"
	StateAwaitingUsr = "awaiting_user"
	StateVerifying   = "verifying"
	StateCompleted   = "completed"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
)

// Состояния запуска.
const (
	RunStarting  = "starting"
	RunRunning   = "running"
	RunExited    = "exited"
	RunCancelled = "cancelled"
	RunOrphaned  = "orphaned"
	RunFailed    = "failed"
)

// Состояния проверки.
const (
	VerifyPending = "pending"
	VerifyPassed  = "passed"
	VerifyFailed  = "failed"
	VerifySkipped = "skipped"
)

// Состояния подтверждения.
const (
	ApprovalPending = "pending"
	ApprovalGranted = "granted"
	ApprovalDenied  = "denied"
	ApprovalExpired = "expired"
)

// OperationalContract задаёт, чего runtime ждёт от запуска.
//
// Из контракта чистой функцией выводятся Expectations, поэтому после рестарта
// они пересоздаются одинаково (ADR 0009).
type OperationalContract struct {
	// StartDeadline — сколько ждать появления процесса.
	StartDeadline Duration `json:"start_deadline"`
	// MaxSilence — допустимая тишина. Зависит от режима исполнителя.
	MaxSilence Duration `json:"max_silence"`
	// MaxDuration ограничивает общую продолжительность запуска.
	MaxDuration Duration `json:"max_duration"`
	// CollectDeadline — сколько ждать артефактов после завершения процесса.
	CollectDeadline Duration `json:"collect_deadline"`
	// RequiredArtifacts — без них результат не принимается.
	RequiredArtifacts []string `json:"required_artifacts"`
	// AllowedRecovery перечисляет разрешённые локальные реакции.
	AllowedRecovery []string `json:"allowed_recovery"`
	// StopConditions — при чём запуск обязан остановиться.
	StopConditions []string `json:"stop_conditions"`
}

// Duration сериализуется в строку вида "2m30s", а не в наносекунды:
// контракт читают люди.
type Duration time.Duration

// MarshalJSON пишет длительность строкой.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON принимает строку или число наносекунд.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*d = Duration(n)
	return nil
}

// D возвращает обычную длительность.
func (d Duration) D() time.Duration { return time.Duration(d) }

// DefaultContract — контракт audit-only поручения.
func DefaultContract(artifacts []string) OperationalContract {
	return OperationalContract{
		StartDeadline:     Duration(30 * time.Second),
		MaxSilence:        Duration(3 * time.Minute),
		MaxDuration:       Duration(30 * time.Minute),
		CollectDeadline:   Duration(2 * time.Minute),
		RequiredArtifacts: artifacts,
		AllowedRecovery: []string{
			"проверить живость процесса",
			"восстановить чтение вывода",
		},
		StopConditions: []string{
			"обнаружена запись в рабочий каталог",
			"превышена максимальная продолжительность",
			"исчерпан бюджет локальных реакций",
		},
	}
}

// Состояния изменений, сделанных исполнителем.
const (
	// ChangeNone — изменений не было или поручение только на чтение.
	ChangeNone = "none"
	// ChangeCollected — изменения собраны и ждут решения владельца.
	ChangeCollected = "collected"
	// ChangeApplied — владелец применил их к своему каталогу.
	ChangeApplied = "applied"
	// ChangeDiscarded — владелец отказался, копия удалена.
	ChangeDiscarded = "discarded"
)

// WorkOrder — формализованное поручение.
type WorkOrder struct {
	ID                  string              `json:"id"`
	ThreadID            string              `json:"thread_id"`
	Title               string              `json:"title"`
	Goal                string              `json:"goal"`
	Why                 string              `json:"why,omitempty"`
	State               string              `json:"state"`
	WorkerID            string              `json:"worker_id,omitempty"`
	WorkerRationale     string              `json:"worker_rationale,omitempty"`
	TrustLevel          string              `json:"trust_level"`
	AuditOnly           bool                `json:"audit_only"`
	WorkspaceRoot       string              `json:"workspace_root,omitempty"`
	WorkspaceGitHead    string              `json:"workspace_git_head,omitempty"`
	WorkspaceBaseline   string              `json:"workspace_baseline,omitempty"`
	ContextPackPath     string              `json:"context_pack_path,omitempty"`
	ContextPackChecksum string              `json:"context_pack_checksum,omitempty"`
	ContextPackRevision int                 `json:"context_pack_revision"`
	Contract            OperationalContract `json:"operational_contract"`
	AcceptanceCriteria  []string            `json:"acceptance_criteria"`
	Constraints         []string            `json:"constraints"`
	RequiredArtifacts   []string            `json:"required_artifacts"`
	// Model и стоимость выбираются Бэрримором, а не исполнителем.
	Model          string `json:"model,omitempty"`
	ModelCostTier  string `json:"model_cost_tier,omitempty"`
	ModelRationale string `json:"model_rationale,omitempty"`
	// Копия рабочего каталога для контролируемой записи. Пуста у поручений
	// только на чтение: там копировать нечего.
	WorkCopyPath     string `json:"work_copy_path,omitempty"`
	WorkCopyBranch   string `json:"work_copy_branch,omitempty"`
	WorkCopyBaseline string `json:"work_copy_baseline,omitempty"`
	// ChangeState — судьба изменений, отдельная от состояния поручения:
	// поручение может быть выполнено, а изменения ещё не рассмотрены.
	ChangeState        string     `json:"change_state"`
	ChangeSummary      Change     `json:"change_summary,omitempty"`
	ChangeDecidedAt    *time.Time `json:"change_decided_at,omitempty"`
	ChangeDecisionNote string     `json:"change_decision_note,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ApprovedAt         *time.Time `json:"approved_at,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
	Outcome            string     `json:"outcome,omitempty"`
	FailureReason      string     `json:"failure_reason,omitempty"`
	Revision           int64      `json:"revision"`
}

// WorkerRun — конкретный запуск исполнителя.
type WorkerRun struct {
	ID              string     `json:"id"`
	WorkOrderID     string     `json:"work_order_id"`
	WorkerID        string     `json:"worker_id"`
	RunDir          string     `json:"run_dir"`
	UnitName        string     `json:"unit_name,omitempty"`
	PID             int        `json:"pid"`
	PIDStartTicks   uint64     `json:"pid_start_ticks"`
	Argv            []string   `json:"argv"`
	SandboxProfile  string     `json:"sandbox_profile,omitempty"`
	Status          string     `json:"status"`
	AttachmentState string     `json:"attachment_state"`
	StdoutOffset    int64      `json:"stdout_offset"`
	StartedAt       time.Time  `json:"started_at"`
	ExitedAt        *time.Time `json:"exited_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	LastSignalAt    *time.Time `json:"last_signal_at,omitempty"`
	Error           string     `json:"error,omitempty"`
}

// Artifact — собранный результат запуска.
type Artifact struct {
	ID          string    `json:"id"`
	RunID       string    `json:"run_id"`
	WorkOrderID string    `json:"work_order_id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	Checksum    string    `json:"checksum"`
	Kind        string    `json:"kind"`
	CollectedAt time.Time `json:"collected_at"`
}

// Verification — проверка результата.
type Verification struct {
	ID          string     `json:"id"`
	WorkOrderID string     `json:"work_order_id"`
	RunID       string     `json:"run_id,omitempty"`
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Detail      string     `json:"detail,omitempty"`
	Command     []string   `json:"command,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// Approval — подтверждение владельца с точной областью действия.
type Approval struct {
	ID          string     `json:"id"`
	WorkOrderID string     `json:"work_order_id,omitempty"`
	ActionClass string     `json:"action_class"`
	Summary     string     `json:"summary"`
	Scope       ScopeInfo  `json:"scope"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	DecidedBy   string     `json:"decided_by,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	MaxCost     string     `json:"max_cost,omitempty"`
}

// ScopeInfo описывает, что именно разрешается.
//
// «Разрешить всё навсегда» не является значением по умолчанию (06_SECURITY §5).
type ScopeInfo struct {
	Worker        string   `json:"worker"`
	WorkspaceRoot string   `json:"workspace_root"`
	WriteLevel    string   `json:"write_level"`
	Network       bool     `json:"network"`
	Model         string   `json:"model,omitempty"`
	CostTier      string   `json:"cost_tier,omitempty"`
	Commands      []string `json:"commands,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}

// Detail — поручение со всем содержимым.
type Detail struct {
	Order         WorkOrder      `json:"order"`
	Runs          []WorkerRun    `json:"runs"`
	Artifacts     []Artifact     `json:"artifacts"`
	Verifications []Verification `json:"verifications"`
	Approvals     []Approval     `json:"approvals"`
}

// Типы событий делегирования.
const (
	EvProposed          = "work_order.proposed"
	EvApprovalRequested = "approval.requested"
	EvApprovalGranted   = "approval.granted"
	EvApprovalDenied    = "approval.denied"
	EvPrepared          = "work_order.prepared"
	EvRunStarted        = "worker_run.started"
	EvRunUpdated        = "worker_run.updated"
	EvRunExited         = "worker_run.exited"
	EvRunCancelled      = "worker_run.cancelled"
	EvRunOrphaned       = "worker_run.orphaned"
	EvArtifactCollected = "work_order.artifact.collected"
	EvVerifyStarted     = "verification.started"
	EvVerifyCompleted   = "verification.completed"
	EvStateChanged      = "work_order.state.changed"
	// Изменения исполнителя: собраны, применены владельцем либо отброшены.
	EvChangeCollected = "work_order.change.collected"
	EvChangeDecided   = "work_order.change.decided"
)

// StreamType — тип потока событий поручения.
const StreamType = "work_order"

// ProjectionTables — таблицы проекций делегирования.
var ProjectionTables = []string{
	"work_orders", "worker_runs", "artifacts", "verifications", "approvals",
}
