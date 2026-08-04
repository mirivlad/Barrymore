// Package opencode реализует adapter для OpenCode.
//
// OpenCode — повседневный исполнитель Бэрримора: у него есть собственные
// бесплатные модели и доступ к бесплатным моделям OpenRouter, поэтому обычная
// работа не расходует платную квоту. Мастера по вызову (codex, Claude Code)
// привлекаются отдельно и осознанно.
package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/worker"
)

// ID adapter'а.
const ID = "opencode"

// ReportFile — имя артефакта с итоговым текстом агента.
const ReportFile = "last-message.txt"

// Adapter — OpenCode CLI.
type Adapter struct {
	Now          func() time.Time
	LookPath     func(string) (string, error)
	HomeDir      func() (string, error)
	ProbeTimeout time.Duration
	// ModelsTimeout ограничивает перечисление моделей: список большой.
	ModelsTimeout time.Duration
}

// New создаёт adapter.
func New() *Adapter {
	return &Adapter{
		Now:           func() time.Time { return time.Now().UTC() },
		LookPath:      exec.LookPath,
		HomeDir:       os.UserHomeDir,
		ProbeTimeout:  15 * time.Second,
		ModelsTimeout: 90 * time.Second,
	}
}

// Descriptor возвращает паспорт adapter'а.
func (a *Adapter) Descriptor() worker.Descriptor {
	return worker.Descriptor{
		ID:           ID,
		DisplayName:  "OpenCode",
		Executables:  []string{"opencode"},
		DefaultTrust: worker.TrustWorktreeWrite,
		CostPolicy:   "free-models-available",
		Class:        worker.ClassRoutine,
		DeclaredCapabilities: []string{
			worker.CapRepositoryAudit,
			worker.CapCodeEdit,
			worker.CapStructuredOutput,
			worker.CapNonInteractive,
			worker.CapRussian,
		},
		// Собственного read-only режима нет: audit-only держится
		// исключительно на внешней изоляции (ADR 0007).
		SupportsAuditOnly: false,
		Runnable:          true,
		Notes:             "run --format json даёт поток событий; есть бесплатные модели",
	}
}

// Discover ищет opencode и опрашивает версию.
func (a *Adapter) Discover(ctx context.Context) (worker.Installation, bool, error) {
	path, err := a.LookPath("opencode")
	if err != nil {
		return worker.Installation{}, false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, a.ProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		return worker.Installation{
			ExecutablePath: path, AuthState: worker.AuthUnknown,
			AuthDetail: fmt.Sprintf("opencode --version завершился ошибкой: %v", err),
		}, true, nil
	}

	inst := worker.Installation{
		ExecutablePath: path,
		Version:        firstLine(string(out)),
	}
	inst.AuthState, inst.AuthDetail = a.authState()
	return inst, true, nil
}

// authState проверяет наличие файла учётных данных, не читая значения.
func (a *Adapter) authState() (string, string) {
	home, err := a.HomeDir()
	if err != nil {
		return worker.AuthUnknown, "домашний каталог недоступен: " + err.Error()
	}
	candidates := []string{
		home + "/.local/share/opencode/auth.json",
		home + "/.config/opencode/auth.json",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return worker.AuthConfigured,
				"учётные данные настроены (" + p + "); содержимое не читается"
		}
	}
	// Собственные бесплатные модели могут работать и без внешнего ключа,
	// поэтому отсутствие файла не объявляется отказом.
	return worker.AuthUnknown,
		"файл учётных данных не найден; собственные модели могут работать без него"
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
			Reason:     "исполняемый файл отвечает; доступны собственные бесплатные модели",
			QuotaKnown: false,
			QuotaNote: "квота платных провайдеров не проверялась; " +
				"на бесплатных моделях она и не расходуется",
		}, nil
	}
}

// Models перечисляет модели.
//
// `opencode models` читает локальный каталог провайдеров и не выполняет
// платного запроса к модели.
func (a *Adapter) Models(ctx context.Context, inst worker.Installation) ([]worker.Model, error) {
	if inst.ExecutablePath == "" {
		return nil, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, a.ModelsTimeout)
	defer cancel()

	out, err := exec.CommandContext(listCtx, inst.ExecutablePath, "models").Output()
	if err != nil {
		return nil, fmt.Errorf("opencode models: %w", err)
	}

	now := a.Now()
	var models []worker.Model
	seen := map[string]bool{}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		ref := strings.TrimSpace(scanner.Text())
		if ref == "" || seen[ref] || strings.ContainsAny(ref, " \t") {
			continue
		}
		seen[ref] = true

		provider, name := ref, ""
		if i := strings.Index(ref, "/"); i > 0 {
			provider, name = ref[:i], ref[i+1:]
		}
		tier, evidence, confidence := worker.ClassifyModelRef(ref)
		models = append(models, worker.Model{
			Ref: ref, Provider: provider, Name: name,
			CostTier: tier, Source: "cli-list", Evidence: evidence,
			Confidence: confidence, LastCost: -1, ObservedAt: now,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("разбор списка моделей opencode: %w", err)
	}
	return models, nil
}

// Plan строит команду запуска.
func (a *Adapter) Plan(ctx context.Context, inst worker.Installation, req worker.RunRequest) (worker.RunPlan, error) {
	if inst.ExecutablePath == "" {
		return worker.RunPlan{}, fmt.Errorf("opencode: не найден исполняемый файл")
	}
	if req.WorkDir == "" {
		return worker.RunPlan{}, fmt.Errorf("opencode: не задан рабочий каталог")
	}
	if req.ScratchDir == "" {
		return worker.RunPlan{}, fmt.Errorf("opencode: не задан писчий каталог запуска")
	}
	if req.Model == "" {
		// Без явной модели инструмент возьмёт свою — возможно платную.
		// Выбор стоимости принадлежит Бэрримору, а не исполнителю.
		return worker.RunPlan{}, fmt.Errorf(
			"opencode: модель не выбрана; стоимость запуска определяет Бэрримор")
	}

	argv := []string{
		inst.ExecutablePath, "run",
		"--format", "json",
		"-m", req.Model,
		// Внешние плагины не загружаются: запуск должен быть воспроизводимым.
		"--pure",
		req.Prompt,
	}

	home, _ := a.HomeDir()
	sb := worker.Sandbox{Network: true}.Writable(req.ScratchDir, req.ScratchDir)
	if req.OutputDir != "" {
		sb = sb.Writable(req.OutputDir, req.OutputDir)
	}
	// Состояние и учётные данные нужны на чтение: копировать секреты Бэрримор
	// не станет, а без них исполнитель не обратится к провайдеру.
	if home != "" {
		stateDir := filepath.Join(home, ".local", "share", "opencode")
		for _, dir := range []string{stateDir, filepath.Join(home, ".config", "opencode")} {
			if _, err := os.Stat(dir); err == nil {
				sb = sb.ReadOnly(dir, dir)
			}
		}
		// Поверх каталога состояния открывается на запись только подкаталог
		// логов: без него opencode не стартует. Порядок обязателен — точка
		// монтирования создаётся внутри уже примонтированного каталога.
		if _, err := os.Stat(stateDir); err == nil {
			logs := filepath.Join(req.ScratchDir, "opencode-log")
			if err := os.MkdirAll(logs, 0o700); err != nil {
				return worker.RunPlan{}, fmt.Errorf("opencode: каталог логов: %w", err)
			}
			sb = sb.Writable(logs, filepath.Join(stateDir, "log"))
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
		StructuredOutput:  true,
		ExpectedArtifacts: nil,
		Sandbox:           sb,
		Dir:               req.WorkDir,
	}, nil
}

// ocEvent — часть схемы вывода `opencode run --format json`.
type ocEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Tool   string `json:"tool"`
		Reason string `json:"reason"`
		State  struct {
			Status string `json:"status"`
			Title  string `json:"title"`
			Error  string `json:"error"`
		} `json:"state"`
		Cost   *float64 `json:"cost"`
		Tokens struct {
			Total  int `json:"total"`
			Input  int `json:"input"`
			Output int `json:"output"`
		} `json:"tokens"`
	} `json:"part"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// ParseLine превращает строку вывода в типизированное событие.
func (a *Adapter) ParseLine(line []byte) (worker.RunEvent, bool) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return worker.RunEvent{}, false
	}
	now := a.Now()

	var oc ocEvent
	if err := json.Unmarshal([]byte(trimmed), &oc); err != nil {
		return worker.RunEvent{
			Kind: worker.RunEventOther, At: now,
			Summary: truncate(trimmed, 200), Raw: trimmed,
		}, true
	}

	ev := worker.RunEvent{
		At: now, Raw: trimmed,
		Detail: map[string]any{"opencode_type": oc.Type},
	}

	switch oc.Type {
	case "text":
		ev.Kind = worker.RunEventMessage
		ev.Summary = truncate(oc.Part.Text, 400)
		// Итоговый текст пригодится приёмке результата.
		ev.Detail["text"] = oc.Part.Text
	case "reasoning":
		ev.Kind = worker.RunEventReasoning
		ev.Summary = "рассуждение агента"
	case "tool":
		ev.Kind = worker.RunEventCommand
		ev.Summary = "инструмент " + firstNonEmpty(oc.Part.Tool, oc.Part.State.Title, "без имени")
		if oc.Part.State.Error != "" {
			ev.Kind = worker.RunEventError
			ev.Summary = "инструмент завершился ошибкой: " + truncate(oc.Part.State.Error, 300)
		}
	case "step_start":
		ev.Kind = worker.RunEventAction
		ev.Summary = "шаг начат"
	case "step_finish":
		ev.Kind = worker.RunEventTokenUsage
		ev.Summary = fmt.Sprintf("шаг завершён (%s), токенов: %d",
			firstNonEmpty(oc.Part.Reason, "без причины"), oc.Part.Tokens.Total)
		ev.Detail["tokens_total"] = oc.Part.Tokens.Total
		if oc.Part.Cost != nil {
			// Исполнитель сам сообщает стоимость шага. Это единственное
			// надёжное основание считать модель бесплатной или платной:
			// состав бесплатных моделей у провайдеров меняется.
			ev.Detail["observed_cost"] = *oc.Part.Cost
		}
	case "error":
		ev.Kind = worker.RunEventError
		text := firstNonEmpty(oc.Error, oc.Message, oc.Part.State.Error)
		ev.Summary = truncate(firstNonEmpty(text, "ошибка"), 400)
		if quotaExhausted(text) {
			ev.Detail["quota_exhausted"] = true
			ev.Detail["quota_message"] = text
		}
	default:
		ev.Kind = worker.RunEventOther
		ev.Summary = "opencode: " + oc.Type
	}
	return ev, true
}

func quotaExhausted(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range []string{"usage limit", "rate limit", "quota", "429", "insufficient"} {
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Collect извлекает итоговый текст агента в обязательный артефакт.
//
// opencode печатает поток событий, а не файл отчёта, поэтому итоговое
// сообщение собирается из частей типа "text".
func (a *Adapter) Collect(ctx context.Context, runDir string) error {
	f, err := os.Open(filepath.Join(runDir, "stdout.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("opencode: чтение вывода: %w", err)
	}
	defer f.Close()

	var parts []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var oc ocEvent
		if err := json.Unmarshal(sc.Bytes(), &oc); err != nil {
			continue
		}
		if oc.Type == "text" && strings.TrimSpace(oc.Part.Text) != "" {
			parts = append(parts, oc.Part.Text)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("opencode: разбор вывода: %w", err)
	}
	if len(parts) == 0 {
		// Пустой отчёт не создаётся: отсутствие результата должно быть видно.
		return nil
	}

	dst := filepath.Join(runDir, "out", ReportFile)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("opencode: каталог артефактов: %w", err)
	}
	if err := os.WriteFile(dst, []byte(strings.Join(parts, "\n")), 0o600); err != nil {
		return fmt.Errorf("opencode: запись отчёта: %w", err)
	}
	return nil
}
