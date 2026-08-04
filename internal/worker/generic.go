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
	Notes                string   `yaml:"notes" json:"notes"`
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
			Reason:     "версия получена, но adapter не умеет запускать поручения",
			QuotaKnown: false,
			QuotaNote:  "состояние квоты не проверялось",
		}, nil
	}
}

// Plan отказывает: манифеста недостаточно для запуска.
func (a *ManifestAdapter) Plan(context.Context, Installation, RunRequest) (RunPlan, error) {
	return RunPlan{}, fmt.Errorf(
		"adapter %q обнаруживает исполнителя, но не умеет готовить запуск; "+
			"нужен полноценный adapter с планом команды и разбором событий", a.M.ID)
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

// BuiltinManifests — исполнители, обнаруживаемые из коробки.
//
// Список отражает host audit: Claude Code в нём нет, потому что `claude`
// на этом хосте не установлен (ADR 0005).
func BuiltinManifests() []Manifest {
	return []Manifest{
		{
			ID: "opencode", DisplayName: "OpenCode",
			Executables: []string{"opencode"}, VersionArgs: []string{"--version"},
			DefaultTrust: TrustWorktreeWrite, CostPolicy: "provider-account",
			AuthPaths:            []string{"~/.config/opencode", "~/.opencode"},
			DeclaredCapabilities: []string{CapCodeEdit, CapRepositoryAudit, CapStructuredOutput},
			SupportsAuditOnly:    false,
			Notes:                "есть headless-режим serve и run --format json",
		},
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
			ID: "hermes", DisplayName: "Hermes Agent",
			Executables: []string{"hermes"}, VersionArgs: []string{"--version"},
			DefaultTrust: TrustProposalOnly, CostPolicy: "provider-account",
			AuthPaths:            []string{"~/.hermes", "~/.config/hermes"},
			DeclaredCapabilities: []string{CapCodeEdit, CapWebResearch},
		},
		{
			ID: "claude-code", DisplayName: "Claude Code",
			Executables: []string{"claude"}, VersionArgs: []string{"--version"},
			DefaultTrust: TrustWorktreeWrite, CostPolicy: "provider-account",
			AuthPaths:            []string{"~/.claude"},
			DeclaredCapabilities: []string{CapCodeEdit, CapRepositoryAudit, CapTests},
			Notes:                "на момент host audit не установлен; запись активируется при появлении в PATH",
		},
	}
}
