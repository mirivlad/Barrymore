// Package worker ведёт реестр внешних исполнителей и их adapter'ов.
//
// ADR 0002: worker владеет только исполнением WorkOrder. Контекст, политики,
// наблюдение, проверка и история остаются у Бэрримора.
package worker

import (
	"context"
	"time"
)

// Уровни доверия (05_STAFF_AND_DELEGATION §7).
const (
	TrustObserve       = "observe"
	TrustWorkspaceRead = "workspace_read"
	TrustProposalOnly  = "proposal_only"
	TrustWorktreeWrite = "worktree_write"
	TrustWorkspaceWrit = "workspace_write"
	TrustExternal      = "external_side_effects"
)

// Статусы доступности. Порядок соответствует 02_DOMAIN_MODEL §6.
const (
	StatusAvailable       = "available"
	StatusLikelyAvailable = "likely_available"
	StatusUnknown         = "unknown"
	StatusQuotaExhausted  = "quota_exhausted"
	StatusAuthRequired    = "auth_required"
	StatusPaymentRequired = "payment_confirmation_required"
	StatusOffline         = "offline"
	StatusBroken          = "broken"
)

// Основания уверенности в возможности.
const (
	EvidenceDeclared  = "declared"
	EvidenceProbe     = "probe"
	EvidenceExecution = "execution"
	EvidenceUser      = "user"
)

// Состояния аутентификации, наблюдаемые без обращения к провайдеру.
const (
	AuthUnknown    = "unknown"
	AuthConfigured = "configured"
	AuthMissing    = "missing"
)

// Возможности.
const (
	CapRepositoryAudit  = "repository-audit"
	CapCodeEdit         = "code-edit"
	CapTests            = "tests"
	CapWebResearch      = "web-research"
	CapStructuredOutput = "structured-output"
	CapNonInteractive   = "non-interactive"
	CapReadOnlySandbox  = "read-only-sandbox"
	CapRussian          = "russian"
	CapLongContext      = "long-context"
)

// Worker — запись реестра об установленном исполнителе.
type Worker struct {
	ID             string `json:"id"`
	AdapterID      string `json:"adapter_id"`
	DisplayName    string `json:"display_name"`
	ExecutablePath string `json:"executable_path,omitempty"`
	Version        string `json:"version,omitempty"`
	TrustLevel     string `json:"trust_level"`
	Enabled        bool   `json:"enabled"`
	AuthState      string `json:"auth_state"`
	CostPolicy     string `json:"cost_policy"`
	// Class различает повседневного исполнителя и мастера по вызову.
	Class string `json:"class"`
	// PreferredModel — ручной выбор владельца; пустая строка отдаёт выбор runtime.
	PreferredModel string `json:"preferred_model,omitempty"`
	// ModelsRefreshedAt — когда каталог моделей обновлялся целиком.
	// Составы бесплатных моделей меняются, поэтому у каталога есть срок годности.
	ModelsRefreshedAt *time.Time `json:"models_refreshed_at,omitempty"`
	DiscoveredAt      time.Time  `json:"discovered_at"`
	LastProbeAt       *time.Time `json:"last_probe_at,omitempty"`
	Notes             string     `json:"notes,omitempty"`
}

// Capability — подтверждённая возможность с основанием.
type Capability struct {
	ID         string    `json:"id"`
	WorkerID   string    `json:"worker_id"`
	Capability string    `json:"capability"`
	Evidence   string    `json:"evidence"`
	Confidence float64   `json:"confidence"`
	ObservedAt time.Time `json:"observed_at"`
	Detail     string    `json:"detail,omitempty"`
}

// Availability — наблюдаемая доступность с TTL и основанием.
//
// «Доступен» без основания показывать нельзя (05_STAFF_AND_DELEGATION §5).
type Availability struct {
	Status     string     `json:"status"`
	Confidence float64    `json:"confidence"`
	ObservedAt time.Time  `json:"observed_at"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	Source     string     `json:"source"`
	Reason     string     `json:"reason,omitempty"`
	// QuotaKnown отличает «квота не исчерпана» от «о квоте ничего не известно».
	QuotaKnown bool   `json:"quota_known"`
	QuotaNote  string `json:"quota_note,omitempty"`
}

// View — worker вместе с возможностями и доступностью.
type View struct {
	Worker       Worker       `json:"worker"`
	Capabilities []Capability `json:"capabilities"`
	Availability Availability `json:"availability"`
	// AvailabilityFresh показывает, действителен ли снимок прямо сейчас.
	AvailabilityFresh bool `json:"availability_fresh"`
	// Models — известный каталог моделей исполнителя.
	Models []Model `json:"models"`
	// FreeModels — сколько из них бесплатны.
	FreeModels int `json:"free_models"`
}

// Installation — найденная установка исполнителя.
type Installation struct {
	ExecutablePath string
	Version        string
	AuthState      string
	AuthDetail     string
}

// Descriptor — паспорт adapter'а.
type Descriptor struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"display_name"`
	Executables  []string `json:"executables"`
	DefaultTrust string   `json:"default_trust"`
	CostPolicy   string   `json:"cost_policy"`
	// DeclaredCapabilities — заявленные возможности. Основание `declared`,
	// то есть низкая уверенность до подтверждения запуском.
	DeclaredCapabilities []string `json:"declared_capabilities"`
	// SupportsAuditOnly сообщает, умеет ли adapter собственный read-only режим.
	SupportsAuditOnly bool `json:"supports_audit_only"`
	// Runnable отличает полноценный adapter от того, который умеет только
	// обнаруживать исполнителя. Свойство объявляется явно, а не выводится
	// пробным вызовом построения команды.
	Runnable bool `json:"runnable"`
	// Class различает повседневного исполнителя и мастера по вызову.
	// Повседневная работа идёт на бесплатных моделях; специалист привлекается
	// осознанно, потому что расходует платную квоту.
	Class string `json:"class"`
	Notes string `json:"notes,omitempty"`
}

// RunEventKind — виды наблюдаемых событий запуска (05_STAFF_AND_DELEGATION §12).
const (
	RunEventMessage    = "worker.message"
	RunEventAction     = "worker.action"
	RunEventCommand    = "worker.command"
	RunEventFileChange = "worker.file_change"
	RunEventWaiting    = "worker.waiting_for_input"
	RunEventWarning    = "worker.warning"
	RunEventError      = "worker.error"
	RunEventTokenUsage = "worker.token_usage"
	RunEventReasoning  = "worker.reasoning"
	RunEventOther      = "worker.other"
)

// RunEvent — нормализованное событие запуска.
type RunEvent struct {
	Kind    string         `json:"kind"`
	At      time.Time      `json:"at"`
	Summary string         `json:"summary"`
	Detail  map[string]any `json:"detail,omitempty"`
	// Raw сохраняется для аудита: разбор мог потерять смысл.
	Raw string `json:"raw,omitempty"`
}

// RunRequest — что adapter должен запустить.
type RunRequest struct {
	RunID string
	// WorkDir — корень, с которым работает исполнитель.
	WorkDir string
	// Prompt передаётся через stdin, а не через argv: так он не попадает
	// в список процессов и не ограничен длиной командной строки.
	Prompt string
	// AuditOnly требует от adapter собственного режима только для чтения.
	AuditOnly bool
	// OutputDir — куда adapter кладёт артефакты.
	OutputDir string
	// ScratchDir — настоящий писчий каталог исполнителя внутри изоляции.
	// Нужен там, где инструмент отказывается работать во временном каталоге.
	ScratchDir string
	// ReportSchemaPath — JSON Schema обязательного отчёта, если adapter умеет.
	ReportSchemaPath string
	// Model — строка модели, выбранная runtime по политике стоимости.
	Model   string
	Timeout time.Duration
}

// Виды монтирования внутри изоляции.
const (
	// MountTmpfs — пустой писчий каталог, исчезающий вместе с запуском.
	MountTmpfs = "tmpfs"
	// MountReadOnly — настоящий путь, доступный только для чтения.
	MountReadOnly = "ro"
	// MountWritable — настоящий путь, доступный на запись.
	MountWritable = "rw"
)

// Mount — одно монтирование.
type Mount struct {
	Kind string
	Src  string
	Dst  string
}

// Sandbox описывает, что исполнителю нужно внутри изоляции.
//
// ADR 0007: рабочий каталог остаётся доступным только для чтения. Adapter
// перечисляет ровно то, без чего процесс не запустится, а не «дайте всё».
//
// Порядок монтирований значим и потому задаётся списком, а не набором:
// одному инструменту нужно положить файл только для чтения внутрь писчего
// каталога, другому — открыть на запись подкаталог внутри каталога только
// для чтения. Сортировка по видам ломала бы и то, и другое.
type Sandbox struct {
	Mounts []Mount
	// Network требуется, если исполнитель обращается к своему провайдеру.
	Network bool
}

// Tmpfs добавляет эфемерный писчий каталог.
func (s Sandbox) Tmpfs(dir string) Sandbox {
	s.Mounts = append(s.Mounts, Mount{Kind: MountTmpfs, Dst: dir})
	return s
}

// ReadOnly добавляет путь только для чтения.
func (s Sandbox) ReadOnly(src, dst string) Sandbox {
	s.Mounts = append(s.Mounts, Mount{Kind: MountReadOnly, Src: src, Dst: dst})
	return s
}

// Writable добавляет путь, доступный на запись.
func (s Sandbox) Writable(src, dst string) Sandbox {
	s.Mounts = append(s.Mounts, Mount{Kind: MountWritable, Src: src, Dst: dst})
	return s
}

// RunPlan — как именно запускать процесс.
type RunPlan struct {
	Argv []string
	// Env перечисляет переменные, добавляемые к минимальному окружению.
	// Секреты сюда не попадают: исполнитель пользуется собственной учётной записью.
	Env []string
	// Stdin передаётся процессу.
	Stdin string
	// StructuredOutput сообщает, что stdout содержит JSONL событий.
	StructuredOutput bool
	// ExpectedArtifacts — файлы, которые обязаны появиться в OutputDir.
	ExpectedArtifacts []string
	// Sandbox — требования к изоляции.
	Sandbox Sandbox
	// Dir — рабочий каталог процесса. Многие CLI не имеют флага каталога
	// и опираются на текущий каталог запуска.
	Dir string
}

// Adapter описывает, как обращаться с конкретным исполнителем.
//
// Adapter не имеет доступа к базе, памяти и политикам: он превращает намерение
// в argv и разбирает вывод обратно в типизированные события.
type Adapter interface {
	Descriptor() Descriptor
	// Discover ищет установку. Платные запросы запрещены.
	Discover(ctx context.Context) (Installation, bool, error)
	// Availability оценивает доступность по локально наблюдаемым признакам.
	Availability(ctx context.Context, inst Installation) (Availability, error)
	// Models перечисляет доступные модели. Запрос должен быть бесплатным:
	// список моделей нельзя получать ценой платного вызова.
	Models(ctx context.Context, inst Installation) ([]Model, error)
	// Plan строит план запуска.
	Plan(ctx context.Context, inst Installation, req RunRequest) (RunPlan, error)
	// ParseLine превращает строку stdout в событие. ok=false — строка не разобрана.
	ParseLine(line []byte) (ev RunEvent, ok bool)
	// Collect приводит вывод к обязательным артефактам в <runDir>/out.
	// У инструментов, которые сами пишут отчёт файлом, ничего не делает.
	Collect(ctx context.Context, runDir string) error
}

// Типы событий штата.
const (
	EvDiscovered          = "worker.discovered"
	EvUpdated             = "worker.updated"
	EvProbed              = "worker.probed"
	EvAvailabilityObserve = "worker.availability.observed"
	EvCapabilityObserved  = "worker.capability.observed"
	EvModelsObserved      = "worker.models.observed"
	EvModelCostObserved   = "worker.model.cost.observed"
	EvTrustChanged        = "worker.trust.changed"
	EvEnabledChanged      = "worker.enabled.changed"
)

// StreamType — тип потока событий исполнителя.
const StreamType = "worker"

// ProjectionTables — таблицы проекций штата.
var ProjectionTables = []string{"workers", "worker_capabilities", "worker_models"}

// SnapshotScope возвращает область снимка доступности исполнителя.
func SnapshotScope(workerID string) string { return "worker:" + workerID }
