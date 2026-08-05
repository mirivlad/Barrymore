package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Manifest описывает исполнителя декларативно (08_API_AND_EVENTS §5).
//
// Manifest достаточен для обнаружения и опроса версии. Для запуска нужен
// полноценный adapter: план команды и разбор событий у каждого инструмента свои.
type Manifest struct {
	ID          string   `yaml:"id" json:"id"`
	DisplayName string   `yaml:"displayName" json:"display_name"`
	Executables []string `yaml:"executables" json:"executables"`
	// VersionArgs — аргументы опроса версии. Обязаны быть бесплатными.
	VersionArgs  []string `yaml:"versionArgs" json:"version_args"`
	DefaultTrust string   `yaml:"defaultTrust" json:"default_trust"`
	CostPolicy   string   `yaml:"costPolicy" json:"cost_policy"`
	// AuthPaths — пути, существование которых означает настроенную учётную запись.
	// Содержимое не читается.
	AuthPaths            []string `yaml:"authPaths" json:"auth_paths"`
	DeclaredCapabilities []string `yaml:"declaredCapabilities" json:"declared_capabilities"`
	SupportsAuditOnly    bool     `yaml:"supportsAuditOnly" json:"supports_audit_only"`
	Class                string   `yaml:"class" json:"class"`
	Notes                string   `yaml:"notes" json:"notes"`
	// Run описывает неинтерактивный запуск. Пустой означает «обнаруживать
	// умею, запускать нет» — так было у всех манифестов до ADR 0021.
	Run RunSpec `yaml:"run" json:"run"`
}

// RunSpec — способ обращения с инструментом, выведенный из его собственной
// справки (ADR 0021).
//
// Здесь нет строки команды: только имя исполняемого файла из PATH и список
// аргументов, каждый из которых проверен по справке. Оболочка не участвует.
type RunSpec struct {
	// Args — аргументы запуска. Элемент "{prompt}" заменяется заданием.
	Args []string `yaml:"args" json:"args,omitempty"`
	// PromptVia — "argv" или "stdin".
	PromptVia string `yaml:"promptVia" json:"prompt_via,omitempty"`
	// AuditArgs добавляются, когда запуск только на чтение.
	AuditArgs []string `yaml:"auditArgs" json:"audit_args,omitempty"`
	// ModelFlag — флаг выбора модели, если инструмент его понимает.
	ModelFlag string `yaml:"modelFlag" json:"model_flag,omitempty"`
	Network   bool   `yaml:"network" json:"network,omitempty"`
}

// Runnable сообщает, достаточно ли манифеста для запуска.
func (r RunSpec) Runnable() bool { return len(r.Args) > 0 }

// PromptToken — место, куда подставляется задание.
const PromptToken = "{prompt}"

// ReportFile — обязательный артефакт запуска по манифесту.
//
// Инструмент, о котором известна одна справка, не обязан уметь писать отчёт
// файлом. Отчётом становится всё, что он напечатал, — так же, как у hermes.
const ReportFile = "last-message.txt"

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ManifestAdapter умеет обнаруживать исполнителя, но не умеет его запускать.
//
// Попытка запуска возвращает честную ошибку вместо подделки: adapter без
// плана команды — это найденный, но неподключённый исполнитель.
type ManifestAdapter struct {
	M            Manifest
	Now          func() time.Time
	LookPath     func(string) (string, error)
	HomeDir      func() (string, error)
	ProbeTimeout time.Duration
}

// NewManifestAdapter создаёт adapter из манифеста.
func NewManifestAdapter(m Manifest) *ManifestAdapter {
	return &ManifestAdapter{
		M:            m,
		Now:          func() time.Time { return time.Now().UTC() },
		LookPath:     exec.LookPath,
		HomeDir:      os.UserHomeDir,
		ProbeTimeout: 15 * time.Second,
	}
}

// Descriptor возвращает паспорт.
func (a *ManifestAdapter) Descriptor() Descriptor {
	return Descriptor{
		ID:                   a.M.ID,
		DisplayName:          a.M.DisplayName,
		Executables:          a.M.Executables,
		DefaultTrust:         a.M.DefaultTrust,
		CostPolicy:           a.M.CostPolicy,
		DeclaredCapabilities: a.M.DeclaredCapabilities,
		SupportsAuditOnly:    a.M.SupportsAuditOnly,
		Class:                orDefault(a.M.Class, ClassRoutine),
		Runnable:             a.M.Run.Runnable(),
		Notes:                a.M.Notes,
	}
}

// Discover ищет исполняемый файл и опрашивает версию.
func (a *ManifestAdapter) Discover(ctx context.Context) (Installation, bool, error) {
	var path string
	for _, name := range a.M.Executables {
		p, err := a.LookPath(name)
		if err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return Installation{}, false, nil
	}

	inst := Installation{ExecutablePath: path}
	if len(a.M.VersionArgs) > 0 {
		probeCtx, cancel := context.WithTimeout(ctx, a.ProbeTimeout)
		defer cancel()
		out, err := exec.CommandContext(probeCtx, path, a.M.VersionArgs...).CombinedOutput()
		if err != nil {
			inst.AuthState = AuthUnknown
			inst.AuthDetail = fmt.Sprintf("опрос версии завершился ошибкой: %v", err)
			return inst, true, nil
		}
		inst.Version = firstLine(string(out))
	}
	inst.AuthState, inst.AuthDetail = a.authState()
	return inst, true, nil
}

func (a *ManifestAdapter) authState() (string, string) {
	if len(a.M.AuthPaths) == 0 {
		return AuthUnknown, "adapter не объявил признаков настроенной учётной записи"
	}
	home, err := a.HomeDir()
	if err != nil {
		return AuthUnknown, "домашний каталог недоступен: " + err.Error()
	}
	for _, p := range a.M.AuthPaths {
		full := p
		if strings.HasPrefix(p, "~/") {
			full = filepath.Join(home, p[2:])
		}
		if _, err := os.Stat(full); err == nil {
			return AuthConfigured, "признак настроенной учётной записи: " + full +
				" (содержимое не читается)"
		}
	}
	return AuthMissing, "ни один из ожидаемых путей учётных данных не найден"
}

// Availability оценивает доступность по локальным признакам.
func (a *ManifestAdapter) Availability(ctx context.Context, inst Installation) (Availability, error) {
	now := a.Now()
	validUntil := now.Add(15 * time.Minute)

	switch {
	case inst.ExecutablePath == "":
		return Availability{
			Status: StatusOffline, Confidence: 1, ObservedAt: now,
			Source: "discovery", Reason: "исполняемый файл не найден",
		}, nil
	case inst.Version == "":
		return Availability{
			Status: StatusBroken, Confidence: 0.8, ObservedAt: now,
			ValidUntil: &validUntil, Source: "version-probe",
			Reason: "исполняемый файл найден, но версия не получена",
		}, nil
	case inst.AuthState == AuthMissing:
		return Availability{
			Status: StatusAuthRequired, Confidence: 0.8, ObservedAt: now,
			ValidUntil: &validUntil, Source: "auth-path-check", Reason: inst.AuthDetail,
		}, nil
	default:
		return Availability{
			Status: StatusUnknown, Confidence: 0.4, ObservedAt: now,
			ValidUntil: &validUntil, Source: "version-probe",
			Reason:     availabilityReason(a.M.Run.Runnable()),
			QuotaKnown: false,
			QuotaNote:  "состояние квоты не проверялось",
		}, nil
	}
}

func availabilityReason(runnable bool) string {
	if runnable {
		return "версия получена; способ запуска взят из справки инструмента " +
			"и работой пока не подтверждён"
	}
	return "версия получена, но adapter не умеет запускать поручения"
}

// Plan строит запуск по манифесту.
//
// Манифеста без раздела `run` для запуска недостаточно, и это честный отказ,
// а не подделка: adapter без плана команды — найденный, но неподключённый
// исполнитель.
func (a *ManifestAdapter) Plan(_ context.Context, inst Installation, req RunRequest) (RunPlan, error) {
	if !a.M.Run.Runnable() {
		return RunPlan{}, fmt.Errorf(
			"adapter %q обнаруживает исполнителя, но не умеет готовить запуск; "+
				"нужен способ неинтерактивного запуска", a.M.ID)
	}
	if inst.ExecutablePath == "" {
		return RunPlan{}, fmt.Errorf("%s: не найден исполняемый файл", a.M.ID)
	}
	if req.WorkDir == "" || req.ScratchDir == "" {
		return RunPlan{}, fmt.Errorf("%s: не заданы каталоги запуска", a.M.ID)
	}
	if req.AuditOnly && !a.M.SupportsAuditOnly {
		return RunPlan{}, fmt.Errorf(
			"%s: собственного режима только для чтения не объявлено", a.M.ID)
	}

	argv := []string{inst.ExecutablePath}
	for _, arg := range a.M.Run.Args {
		if arg == PromptToken {
			argv = append(argv, req.Prompt)
			continue
		}
		argv = append(argv, arg)
	}
	if req.AuditOnly {
		argv = append(argv, a.M.Run.AuditArgs...)
	}
	if req.Model != "" && a.M.Run.ModelFlag != "" {
		argv = append(argv, a.M.Run.ModelFlag, req.Model)
	}

	plan := RunPlan{
		Argv: argv,
		Env: []string{
			"TERM=dumb", "NO_COLOR=1",
			"XDG_CACHE_HOME=" + req.ScratchDir,
			"XDG_STATE_HOME=" + req.ScratchDir,
		},
		Sandbox: Sandbox{Network: a.M.Run.Network}.Writable(req.ScratchDir, req.ScratchDir),
		Dir:     req.WorkDir,
	}
	if a.M.Run.PromptVia == "stdin" {
		plan.Stdin = req.Prompt
	}
	if req.OutputDir != "" {
		plan.Sandbox = plan.Sandbox.Writable(req.OutputDir, req.OutputDir)
	}
	return plan, nil
}

// Collect делает вывод пригодным для приёмки.
//
// Инструмент, известный по одной справке, отчёта файлом писать не обязан:
// отчётом становится весь напечатанный текст.
func (a *ManifestAdapter) Collect(_ context.Context, runDir string) error {
	if !a.M.Run.Runnable() {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(runDir, "stdout.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%s: чтение вывода: %w", a.M.ID, err)
	}
	dst := filepath.Join(runDir, "out", ReportFile)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("%s: каталог артефактов: %w", a.M.ID, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("%s: запись отчёта: %w", a.M.ID, err)
	}
	return nil
}

// Models не перечисляет модели: манифест этого не описывает.
func (a *ManifestAdapter) Models(context.Context, Installation) ([]Model, error) {
	return nil, nil
}

// ParseLine сохраняет строку как неразобранное событие.
func (a *ManifestAdapter) ParseLine(line []byte) (RunEvent, bool) {
	s := strings.TrimSpace(string(line))
	if s == "" {
		return RunEvent{}, false
	}
	return RunEvent{Kind: RunEventOther, At: a.Now(), Summary: s, Raw: s}, true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// BuiltinManifests — исполнители, которых Бэрримор умеет обнаруживать,
// но пока не умеет запускать.
//
// opencode и hermes здесь отсутствуют намеренно: у них есть полноценные
// адаптеры. Claude Code остаётся в списке, чтобы запись появилась сама,
// если `claude` будет установлен (ADR 0005).
func BuiltinManifests() []Manifest {
	return []Manifest{
		{
			ID: "pi", DisplayName: "Pi Coding Agent",
			Executables: []string{"pi"}, VersionArgs: []string{"--version"},
			DefaultTrust: TrustProposalOnly, CostPolicy: "provider-account",
			AuthPaths:            []string{"~/.pi"},
			DeclaredCapabilities: []string{CapCodeEdit, CapStructuredOutput},
		},
		{
			ID: "qwen", DisplayName: "Qwen Code",
			Executables: []string{"qwen"}, VersionArgs: []string{"--version"},
			DefaultTrust: TrustProposalOnly, CostPolicy: "provider-account",
			AuthPaths:            []string{"~/.qwen"},
			DeclaredCapabilities: []string{CapCodeEdit},
		},
		{
			ID: "claude-code", DisplayName: "Claude Code",
			Executables: []string{"claude"}, VersionArgs: []string{"--version"},
			DefaultTrust: TrustWorktreeWrite, CostPolicy: "provider-account",
			Class:                ClassSpecialist,
			AuthPaths:            []string{"~/.claude"},
			DeclaredCapabilities: []string{CapCodeEdit, CapRepositoryAudit, CapTests},
			Notes:                "на момент host audit не установлен; запись активируется при появлении в PATH",
		},
	}
}
