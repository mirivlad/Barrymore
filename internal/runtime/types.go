// Package runtime реализует предиктивный контур: наблюдения, оценку состояния,
// ожидания, расхождения, ограниченные probes и локальные реакции.
//
// ADR 0004: этот слой работает без LLM. ADR 0008: бюджеты реакций хранятся в базе.
// ADR 0009: ожидания проверяются одним детерминированным тиком.
package runtime

import (
	"encoding/json"
	"time"
)

// Типы субъектов, о которых runtime что-либо знает.
const (
	SubjectSystem    = "system"
	SubjectWorker    = "worker"
	SubjectWorkerRun = "worker_run"
	SubjectWorkOrder = "work_order"
	SubjectThread    = "thread"
	SubjectProvider  = "provider"
)

// Качество источника наблюдения.
const (
	// QualityDirect — runtime наблюдал это сам (exit code, содержимое файла).
	QualityDirect = "direct"
	// QualityDerived — выведено из другого наблюдения.
	QualityDerived = "derived"
	// QualityReported — сообщено внешней стороной, включая worker. Недоверенное.
	QualityReported = "reported"
)

// Виды наблюдений, используемые первым контуром.
const (
	ObsRunStarted        = "run.started"
	ObsRunEvent          = "run.event"
	ObsRunExited         = "run.exited"
	ObsRunHeartbeat      = "run.heartbeat"
	ObsRunAttachmentLost = "run.attachment_lost"
	ObsRunAttached       = "run.attached"
	ObsProcessLiveness   = "process.liveness"
	ObsWorkspaceScan     = "workspace.scan"
	ObsArtifactCollected = "artifact.collected"
	ObsWorkerVersion     = "worker.version"
	ObsProbeResult       = "probe.result"
	ObsStorage           = "storage.state"
	// ObsLocalModel — состояние локального сервера модели.
	ObsLocalModel = "local_model.state"
)

// LocalModelStatePayload — наблюдаемое состояние локального сервера модели.
//
// Живёт здесь, а не в пакете сервера, чтобы оценка ожидания оставалась чистой
// функцией и не тянула за собой запуск процессов.
type LocalModelStatePayload struct {
	// Serving означает, что endpoint отвечает и модель готова принимать запросы.
	Serving bool `json:"serving"`
	// Loading означает, что процесс жив, но модель ещё грузится. Это не отказ:
	// 35B через mmap поднимается минутами, и считать это расхождением нельзя.
	Loading  bool   `json:"loading"`
	Endpoint string `json:"endpoint,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Managed отличает процесс, поднятый Бэрримором, от чужого на том же адресе.
	Managed bool `json:"managed"`
}

// Observation — типизированное наблюдение.
//
// Наблюдение не является фактом: у него есть источник, качество и уверенность.
type Observation struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	SubjectType   string          `json:"subject_type"`
	SubjectID     string          `json:"subject_id"`
	ObservedAt    time.Time       `json:"observed_at"`
	RecordedAt    time.Time       `json:"recorded_at"`
	Source        string          `json:"source"`
	SourceQuality string          `json:"source_quality"`
	Confidence    float64         `json:"confidence"`
	DedupeKey     string          `json:"dedupe_key,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	EventSeq      int64           `json:"event_seq,omitempty"`
}

// Decode распаковывает payload наблюдения.
func (o Observation) Decode(v any) error { return json.Unmarshal(o.Payload, v) }

// Snapshot — версионированный снимок наблюдаемого состояния с TTL.
type Snapshot struct {
	ID         string          `json:"id"`
	Scope      string          `json:"scope"`
	Status     string          `json:"status"`
	Confidence float64         `json:"confidence"`
	ObservedAt time.Time       `json:"observed_at"`
	ValidUntil *time.Time      `json:"valid_until,omitempty"`
	Source     string          `json:"source"`
	Reason     string          `json:"reason,omitempty"`
	Payload    json.RawMessage `json:"payload"`
}

// Fresh сообщает, действителен ли снимок на момент now.
//
// Снимок без valid_until считается бессрочным только для неизменяемых сведений
// (например, версии исполняемого файла).
func (s Snapshot) Fresh(now time.Time) bool {
	if s.ValidUntil == nil {
		return true
	}
	return now.Before(*s.ValidUntil)
}

// Статусы ожидания.
const (
	ExpectationPending    = "pending"
	ExpectationSatisfied  = "satisfied"
	ExpectationExpired    = "expired"
	ExpectationSuperseded = "superseded"
	ExpectationCancelled  = "cancelled"
)

// Severity расхождения.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Expectation — явное проверяемое предсказание.
type Expectation struct {
	ID               string          `json:"id"`
	SubjectType      string          `json:"subject_type"`
	SubjectID        string          `json:"subject_id"`
	Kind             string          `json:"kind"`
	Params           json.RawMessage `json:"params"`
	Basis            string          `json:"basis"`
	Confidence       float64         `json:"confidence"`
	SeverityIfMissed string          `json:"severity_if_missed"`
	WindowFrom       time.Time       `json:"window_from"`
	WindowUntil      *time.Time      `json:"window_until,omitempty"`
	NextCheckAt      *time.Time      `json:"next_check_at,omitempty"`
	CheckInterval    time.Duration   `json:"check_interval"`
	ProbePolicy      string          `json:"probe_policy,omitempty"`
	ReactionPolicy   string          `json:"reaction_policy,omitempty"`
	Status           string          `json:"status"`
	SatisfiedAt      *time.Time      `json:"satisfied_at,omitempty"`
	ExpiredAt        *time.Time      `json:"expired_at,omitempty"`
	SupersededBy     string          `json:"superseded_by,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// DecodeParams распаковывает параметры ожидания.
func (e Expectation) DecodeParams(v any) error {
	if len(e.Params) == 0 {
		return nil
	}
	return json.Unmarshal(e.Params, v)
}

// Outcome — результат оценки ожидания.
type Outcome string

const (
	// OutcomePending — ожидание ещё в силе, наблюдений достаточно для спокойствия.
	OutcomePending Outcome = "pending"
	// OutcomeSatisfied — ожидание подтверждено наблюдением.
	OutcomeSatisfied Outcome = "satisfied"
	// OutcomeMissed — ожидаемое не наступило; создаётся расхождение.
	OutcomeMissed Outcome = "missed"
	// OutcomeExpired — окно закрылось, ожидание больше не проверяется.
	OutcomeExpired Outcome = "expired"
)

// Verdict — итог чистой функции оценки.
//
// Verdict не выполняет действий. Побочные эффекты остаются у вызывающего кода,
// поэтому оценку можно тестировать без базы и без времени.
type Verdict struct {
	Outcome Outcome
	// Expected и Observed попадают в расхождение и в интерфейс как есть.
	Expected string
	Observed string
	// Severity переопределяет severity_if_missed, если расхождение мягче ожидаемого.
	Severity string
	// NextCheckAt — когда проверить снова; nil означает «по интервалу ожидания».
	NextCheckAt *time.Time
	// DedupeKey объединяет повторные сигналы одного класса.
	DedupeKey string
}

// Статусы расхождения.
const (
	DiscrepancyOpen         = "open"
	DiscrepancyProbing      = "probing"
	DiscrepancyReacting     = "reacting"
	DiscrepancyEscalated    = "escalated"
	DiscrepancyResolved     = "resolved"
	DiscrepancyAcknowledged = "acknowledged"
)

// Discrepancy — зафиксированное расхождение ожидания и наблюдения.
type Discrepancy struct {
	ID            string    `json:"id"`
	ExpectationID string    `json:"expectation_id,omitempty"`
	SubjectType   string    `json:"subject_type"`
	SubjectID     string    `json:"subject_id"`
	Kind          string    `json:"kind"`
	Expected      string    `json:"expected"`
	Observed      string    `json:"observed"`
	Severity      string    `json:"severity"`
	Confidence    float64   `json:"confidence"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	Occurrences   int       `json:"occurrences"`
	Status        string    `json:"status"`
	Resolution    string    `json:"resolution,omitempty"`
	DedupeKey     string    `json:"dedupe_key,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Статусы probe.
const (
	ProbeRequested = "requested"
	ProbeRunning   = "running"
	ProbeCompleted = "completed"
	ProbeFailed    = "failed"
	ProbeDenied    = "denied"
)

// Probe — ограниченное действие для уменьшения неопределённости.
type Probe struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	SubjectType   string          `json:"subject_type"`
	SubjectID     string          `json:"subject_id"`
	RequestedBy   string          `json:"requested_by"`
	DiscrepancyID string          `json:"discrepancy_id,omitempty"`
	Params        json.RawMessage `json:"params"`
	Status        string          `json:"status"`
	RequestedAt   time.Time       `json:"requested_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// Исходы попытки локальной реакции.
const (
	ReflexStarted   = "started"
	ReflexSucceeded = "succeeded"
	ReflexFailed    = "failed"
	ReflexDenied    = "denied"
)

// ReflexAttempt — израсходованная попытка реакции.
type ReflexAttempt struct {
	ID            string     `json:"id"`
	DiscrepancyID string     `json:"discrepancy_id"`
	PolicyID      string     `json:"policy_id"`
	AttemptNo     int        `json:"attempt_no"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Outcome       string     `json:"outcome"`
	Detail        string     `json:"detail,omitempty"`
}
