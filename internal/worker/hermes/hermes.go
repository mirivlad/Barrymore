// Package hermes реализует adapter для Hermes Agent.
//
// Hermes — второй повседневный исполнитель. Он настроен на бесплатную модель
// и в одноразовом режиме печатает только итоговый ответ, поэтому его вывод
// сам по себе является отчётом.
package hermes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/worker"
)

// ID adapter'а.
const ID = "hermes"

// ReportFile — имя обязательного артефакта.
const ReportFile = "last-message.txt"

// Adapter — Hermes Agent CLI.
type Adapter struct {
	Now          func() time.Time
	LookPath     func(string) (string, error)
	HomeDir      func() (string, error)
	ProbeTimeout time.Duration
	StatusTimout time.Duration
}

// New создаёт adapter.
func New() *Adapter {
	return &Adapter{
		Now:          func() time.Time { return time.Now().UTC() },
		LookPath:     exec.LookPath,
		HomeDir:      os.UserHomeDir,
		ProbeTimeout: 20 * time.Second,
		StatusTimout: 40 * time.Second,
	}
}

// Descriptor возвращает паспорт adapter'а.
func (a *Adapter) Descriptor() worker.Descriptor {
	return worker.Descriptor{
		ID:           ID,
		DisplayName:  "Hermes Agent",
		Executables:  []string{"hermes"},
		DefaultTrust: worker.TrustWorkspaceRead,
		CostPolicy:   "free-model-configured",
		Class:        worker.ClassRoutine,
		DeclaredCapabilities: []string{
			worker.CapRepositoryAudit,
			worker.CapWebResearch,
			worker.CapNonInteractive,
			worker.CapRussian,
		},
		SupportsAuditOnly: false,
		Runnable:          true,
		Notes: "одноразовый режим -z печатает только итоговый ответ; " +
			"структурированного потока событий нет",
	}
}

// Discover ищет hermes и опрашивает версию.
func (a *Adapter) Discover(ctx context.Context) (worker.Installation, bool, error) {
	path, err := a.LookPath("hermes")
	if err != nil {
		return worker.Installation{}, false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, a.ProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		return worker.Installation{
			ExecutablePath: path, AuthState: worker.AuthUnknown,
			AuthDetail: fmt.Sprintf("hermes --version завершился ошибкой: %v", err),
		}, true, nil
	}

	inst := worker.Installation{ExecutablePath: path, Version: firstLine(string(out))}
	inst.AuthState, inst.AuthDetail = a.authState()
	return inst, true, nil
}

func (a *Adapter) authState() (string, string) {
	home, err := a.HomeDir()
	if err != nil {
		return worker.AuthUnknown, "домашний каталог недоступен: " + err.Error()
	}
	for _, p := range []string{
		filepath.Join(home, ".hermes", "hermes-agent", ".env"),
		filepath.Join(home, ".hermes"),
	} {
		if _, err := os.Stat(p); err == nil {
			return worker.AuthConfigured,
				"настройки найдены (" + p + "); содержимое не читается"
		}
	}
	return worker.AuthMissing, "каталог настроек hermes не найден"
}

// Availability оценивает доступность.
func (a *Adapter) Availability(ctx context.Context, inst worker.Installation) (worker.Availability, error) {
	now := a.Now()
	validUntil := now.Add(15 * time.Minute)

	switch {
	case inst.ExecutablePath == "":
		return worker.Availability{
			Status: worker.StatusOffline, Confidence: 1, ObservedAt: now,
			Source: "discovery", Reason: "исполняемый файл не найден",
		}, nil
	case inst.Version == "":
		return worker.Availability{
			Status: worker.StatusBroken, Confidence: 0.8, ObservedAt: now,
			ValidUntil: &validUntil, Source: "version-probe",
			Reason: "исполняемый файл найден, но не отвечает на --version",
		}, nil
	default:
		return worker.Availability{
			Status: worker.StatusLikelyAvailable, Confidence: 0.6, ObservedAt: now,
			ValidUntil: &validUntil, Source: "version-probe",
			Reason:     "исполняемый файл отвечает, настройки на месте",
			QuotaKnown: false,
			QuotaNote:  "состояние квоты провайдера не проверялось",
		}, nil
	}
}

var (
	reModel    = regexp.MustCompile(`(?m)^\s*Model:\s*(.+?)\s*$`)
	reProvider = regexp.MustCompile(`(?m)^\s*Provider:\s*(.+?)\s*$`)
)

// Models сообщает настроенную модель.
//
// Полноценного неинтерактивного каталога у hermes нет: `hermes model` открывает
// интерактивный выбор. Поэтому Бэрримор берёт то, что можно узнать бесплатно и
// без диалога — текущую настроенную модель из `hermes status`, — и честно
// помечает источник. Придумывать список моделей нельзя.
func (a *Adapter) Models(ctx context.Context, inst worker.Installation) ([]worker.Model, error) {
	if inst.ExecutablePath == "" {
		return nil, nil
	}
	statusCtx, cancel := context.WithTimeout(ctx, a.StatusTimout)
	defer cancel()

	out, err := exec.CommandContext(statusCtx, inst.ExecutablePath, "status").Output()
	if err != nil {
		return nil, fmt.Errorf("hermes status: %w", err)
	}
	text := stripANSI(string(out))

	model := firstSubmatch(reModel, text)
	if model == "" {
		return nil, nil
	}
	provider := firstSubmatch(reProvider, text)

	tier, evidence, confidence := worker.ClassifyModelRef(model)
	return []worker.Model{{
		Ref: model, Provider: provider, Name: model,
		CostTier: tier, Source: "cli-status", Confidence: confidence, LastCost: -1,
		Evidence:  evidence + "; hermes показывает только текущую модель, полный список доступен лишь интерактивно",
		IsDefault: true, ObservedAt: a.Now(),
	}}, nil
}

// Plan строит команду запуска.
func (a *Adapter) Plan(ctx context.Context, inst worker.Installation, req worker.RunRequest) (worker.RunPlan, error) {
	if inst.ExecutablePath == "" {
		return worker.RunPlan{}, fmt.Errorf("hermes: не найден исполняемый файл")
	}
	if req.WorkDir == "" {
		return worker.RunPlan{}, fmt.Errorf("hermes: не задан рабочий каталог")
	}
	if req.ScratchDir == "" {
		return worker.RunPlan{}, fmt.Errorf("hermes: не задан писчий каталог запуска")
	}

	argv := []string{
		inst.ExecutablePath,
		// Одноразовый режим печатает только итоговый ответ: это и есть отчёт.
		"-z", req.Prompt,
		// Пользовательские настройки и правила не наследуются: запуск должен
		// быть воспроизводимым и не зависеть от локальных предпочтений.
		"--ignore-user-config",
		"--ignore-rules",
		"--safe-mode",
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}

	home, _ := a.HomeDir()
	sb := worker.Sandbox{Network: true}.Writable(req.ScratchDir, req.ScratchDir)
	if req.OutputDir != "" {
		sb = sb.Writable(req.OutputDir, req.OutputDir)
	}
	if home != "" {
		hermesHome := filepath.Join(home, ".hermes")
		if _, err := os.Stat(hermesHome); err == nil {
			// Настройки и ключи — только на чтение.
			sb = sb.ReadOnly(hermesHome, hermesHome)
			// Поверх них открывается на запись каталог логов: hermes пишет туда
			// и без этого не стартует. Порядок обязателен — точка монтирования
			// создаётся внутри уже примонтированного каталога.
			logs := filepath.Join(req.ScratchDir, "hermes-logs")
			if err := os.MkdirAll(logs, 0o700); err != nil {
				return worker.RunPlan{}, fmt.Errorf("hermes: каталог логов: %w", err)
			}
			sb = sb.Writable(logs, filepath.Join(hermesHome, "logs"))
		}
	}

	return worker.RunPlan{
		Argv: argv,
		Env: []string{
			"TERM=dumb",
			"NO_COLOR=1",
			"XDG_CACHE_HOME=" + req.ScratchDir,
			"XDG_STATE_HOME=" + req.ScratchDir,
		},
		// Структурированного потока нет: hermes печатает связный текст.
		StructuredOutput: false,
		Sandbox:          sb,
		Dir:              req.WorkDir,
	}, nil
}

// ParseLine превращает строку вывода в событие.
//
// Событий как таковых нет: каждая строка — часть итогового текста. Она всё
// равно записывается наблюдением, иначе ход работы был бы невидим.
func (a *Adapter) ParseLine(line []byte) (worker.RunEvent, bool) {
	s := strings.TrimRight(string(line), "\r\n")
	if strings.TrimSpace(s) == "" {
		return worker.RunEvent{}, false
	}
	kind := worker.RunEventMessage
	lower := strings.ToLower(s)
	if strings.Contains(lower, "error") || strings.Contains(lower, "ошибка") {
		kind = worker.RunEventError
	}
	ev := worker.RunEvent{
		Kind: kind, At: a.Now(), Summary: truncate(strings.TrimSpace(s), 400), Raw: s,
		Detail: map[string]any{"source": "hermes-oneshot"},
	}
	if quotaExhausted(lower) {
		ev.Kind = worker.RunEventError
		ev.Detail["quota_exhausted"] = true
		ev.Detail["quota_message"] = strings.TrimSpace(s)
	}
	return ev, true
}

// Collect делает вывод пригодным для приёмки.
//
// Отчётом является весь напечатанный текст, поэтому он переносится в
// обязательный артефакт целиком.
func (a *Adapter) Collect(ctx context.Context, runDir string) error {
	src := filepath.Join(runDir, "stdout.jsonl")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("hermes: чтение вывода: %w", err)
	}
	dst := filepath.Join(runDir, "out", ReportFile)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("hermes: каталог артефактов: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("hermes: запись отчёта: %w", err)
	}
	return nil
}

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripANSI(s string) string { return reANSI.ReplaceAllString(s, "") }

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func quotaExhausted(lower string) bool {
	for _, m := range []string{"usage limit", "rate limit", "quota", "429"} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// readLines используется тестами разбора.
func readLines(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}
