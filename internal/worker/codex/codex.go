// Package codex реализует adapter для Codex CLI.
//
// ADR 0005: codex выбран первым, потому что `codex exec` даёт JSONL-поток
// событий, собственный read-only sandbox, JSON Schema отчёта и явный рабочий
// каталог — то есть все точки контроля, которых требует делегирование.
package codex

import (
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
const ID = "codex"

// Имя файла, куда codex пишет последнее сообщение агента.
const LastMessageFile = "last-message.txt"

// Adapter — Codex CLI.
type Adapter struct {
	// Now используется вместо time.Now, чтобы разбор событий был тестируемым.
	Now func() time.Time
	// LookPath подменяется в тестах.
	LookPath func(string) (string, error)
	// HomeDir возвращает домашний каталог; подменяется в тестах.
	HomeDir func() (string, error)
	// ProbeTimeout ограничивает опрос версии.
	ProbeTimeout time.Duration
}

// New создаёт adapter с настройками по умолчанию.
func New() *Adapter {
	return &Adapter{
		Now:          func() time.Time { return time.Now().UTC() },
		LookPath:     exec.LookPath,
		HomeDir:      os.UserHomeDir,
		ProbeTimeout: 15 * time.Second,
	}
}

// Descriptor возвращает паспорт adapter'а.
func (a *Adapter) Descriptor() worker.Descriptor {
	return worker.Descriptor{
		ID:           ID,
		DisplayName:  "Codex CLI",
		Executables:  []string{"codex"},
		DefaultTrust: worker.TrustWorktreeWrite,
		CostPolicy:   "provider-account",
		DeclaredCapabilities: []string{
			worker.CapRepositoryAudit,
			worker.CapCodeEdit,
			worker.CapTests,
			worker.CapStructuredOutput,
			worker.CapNonInteractive,
			worker.CapReadOnlySandbox,
			worker.CapRussian,
		},
		SupportsAuditOnly: true,
		Runnable:          true,
		// Мастер по вызову: расходует платную квоту подписки, поэтому
		// привлекается к трудной задаче осознанно, а не по умолчанию.
		Class: worker.ClassSpecialist,
		Notes: "codex exec --json даёт JSONL-поток событий",
	}
}

// Discover ищет установленный codex и опрашивает версию.
//
// Опрос версии не обращается к провайдеру и не расходует квоту.
func (a *Adapter) Discover(ctx context.Context) (worker.Installation, bool, error) {
	path, err := a.LookPath("codex")
	if err != nil {
		return worker.Installation{}, false, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, a.ProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		// Исполняемый файл есть, но не отвечает: это находка со сломанным adapter,
		// а не отсутствие установки. Честнее показать её, чем скрыть.
		return worker.Installation{
			ExecutablePath: path,
			Version:        "",
			AuthState:      worker.AuthUnknown,
			AuthDetail:     fmt.Sprintf("codex --version завершился ошибкой: %v", err),
		}, true, nil
	}

	inst := worker.Installation{
		ExecutablePath: path,
		Version:        strings.TrimSpace(string(out)),
	}
	inst.AuthState, inst.AuthDetail = a.authState()
	return inst, true, nil
}

// authState проверяет наличие файла учётных данных, не читая его содержимое.
//
// 06_SECURITY §6: значения секретов не попадают ни в базу, ни в журнал.
// Бэрримору достаточно знать, что учётная запись настроена.
func (a *Adapter) authState() (string, string) {
	home, err := a.HomeDir()
	if err != nil {
		return worker.AuthUnknown, "домашний каталог недоступен: " + err.Error()
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	authPath := filepath.Join(codexHome, "auth.json")
	info, err := os.Stat(authPath)
	if err != nil {
		return worker.AuthMissing, "файл учётных данных не найден: " + authPath
	}
	return worker.AuthConfigured, fmt.Sprintf(
		"учётные данные настроены (%s, изменён %s); содержимое не читается",
		authPath, info.ModTime().UTC().Format(time.RFC3339))
}

// Availability оценивает доступность по локально наблюдаемым признакам.
//
// Состояние квоты локально не наблюдается, поэтому честный ответ —
// «вероятно доступен, о квоте неизвестно», а не «доступен».
func (a *Adapter) Availability(ctx context.Context, inst worker.Installation) (worker.Availability, error) {
	now := a.Now()
	// Признаки устаревают: снимок действителен ограниченное время.
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
	case inst.AuthState == worker.AuthMissing:
		return worker.Availability{
			Status: worker.StatusAuthRequired, Confidence: 0.9, ObservedAt: now,
			ValidUntil: &validUntil, Source: "auth-file-check",
			Reason: inst.AuthDetail,
		}, nil
	default:
		return worker.Availability{
			Status: worker.StatusLikelyAvailable, Confidence: 0.6, ObservedAt: now,
			ValidUntil: &validUntil, Source: "version-probe+auth-file-check",
			Reason: "исполняемый файл отвечает, учётная запись настроена",
			// Состояние квоты не проверялось: локальных признаков нет,
			// а запрос к провайдеру был бы платным действием.
			QuotaKnown: false,
			QuotaNote:  "состояние квоты не проверялось: проверка требует запроса к провайдеру",
		}, nil
	}
}

// Plan строит команду запуска.
//
// Шаблон команды собирается из argv-массива без интерполяции строк в shell
// (08_API_AND_EVENTS §5). Промпт передаётся через stdin.
func (a *Adapter) Plan(ctx context.Context, inst worker.Installation, req worker.RunRequest) (worker.RunPlan, error) {
	if inst.ExecutablePath == "" {
		return worker.RunPlan{}, fmt.Errorf("codex: не найден исполняемый файл")
	}
	if req.WorkDir == "" {
		return worker.RunPlan{}, fmt.Errorf("codex: не задан рабочий каталог")
	}

	argv := []string{
		inst.ExecutablePath, "exec",
		"--json",
		"-C", req.WorkDir,
		"--skip-git-repo-check",
		// Сессии на диск не пишем: состояние принадлежит Бэрримору.
		"--ephemeral",
		// Пользовательский config.toml не наследуется: запуск должен быть
		// воспроизводимым и не зависеть от локальных настроек оператора.
		"--ignore-user-config",
	}

	sandbox := "workspace-write"
	if req.AuditOnly {
		sandbox = "read-only"
	}
	argv = append(argv, "--sandbox", sandbox)

	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}

	var artifacts []string
	if req.OutputDir != "" {
		argv = append(argv, "-o", filepath.Join(req.OutputDir, LastMessageFile))
		artifacts = append(artifacts, LastMessageFile)
	}
	if req.ReportSchemaPath != "" {
		argv = append(argv, "--output-schema", req.ReportSchemaPath)
	}

	// Промпт читается из stdin: "-" явно указывает источник.
	argv = append(argv, "-")

	// Внутри изоляции codex получает собственный писчий CODEX_HOME на tmpfs,
	// а настоящий auth.json подмонтирован в него только для чтения. Значение
	// секрета не копируется и Бэрримором не читается (06_SECURITY §6).
	// codex отказывается создавать вспомогательные файлы во временном каталоге,
	// поэтому CODEX_HOME — настоящий каталог запуска, отданный на запись.
	// Под корнем только для чтения точку монтирования создать нельзя, значит
	// каталог должен существовать на хосте заранее.
	sandboxHome := req.ScratchDir
	if sandboxHome == "" {
		return worker.RunPlan{}, fmt.Errorf("codex: не задан писчий каталог запуска")
	}
	// Сначала каталог отдаётся на запись, и только потом внутрь него
	// монтируется файл учётных данных: создать точку монтирования в каталоге
	// только для чтения невозможно. Секрет при этом не копируется.
	sb := worker.Sandbox{Network: true}.Writable(sandboxHome, sandboxHome)
	if req.OutputDir != "" {
		sb = sb.Writable(req.OutputDir, req.OutputDir)
	}
	if hostAuth := filepath.Join(a.codexHome(), "auth.json"); fileExists(hostAuth) {
		sb = sb.ReadOnly(hostAuth, filepath.Join(sandboxHome, "auth.json"))
	}

	return worker.RunPlan{
		Argv: argv,
		Env: []string{
			"CODEX_HOME=" + sandboxHome,
			"TERM=dumb",
			"NO_COLOR=1",
		},
		Stdin:             req.Prompt,
		StructuredOutput:  true,
		ExpectedArtifacts: artifacts,
		Sandbox:           sb,
	}, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (a *Adapter) codexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	home, err := a.HomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexEvent — минимальная часть схемы JSONL, на которую мы опираемся.
//
// Схема принадлежит внешнему инструменту и может измениться. Поэтому разбор
// устойчив к незнакомым полям, а неразобранная строка не теряется: она
// сохраняется как событие worker.other с исходным текстом.
type codexEvent struct {
	Type string          `json:"type"`
	Msg  json.RawMessage `json:"msg"`
	ID   string          `json:"id"`
	// Message встречается на верхнем уровне, а не только внутри msg:
	// схема внешнего инструмента неоднородна, и терять текст ошибки нельзя.
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexMsg struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	Text     string `json:"text"`
	Command  any    `json:"command"`
	Cwd      string `json:"cwd"`
	ExitCode *int   `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Info     any    `json:"info"`
	Error    string `json:"error"`
}

// ParseLine превращает строку JSONL в типизированное событие.
func (a *Adapter) ParseLine(line []byte) (worker.RunEvent, bool) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return worker.RunEvent{}, false
	}

	now := a.Now()
	var ce codexEvent
	if err := json.Unmarshal([]byte(trimmed), &ce); err != nil {
		// Не JSON — обычный вывод. Сохраняем как есть, не выбрасывая.
		return worker.RunEvent{
			Kind: worker.RunEventOther, At: now, Summary: truncate(trimmed, 200), Raw: trimmed,
		}, true
	}

	var msg codexMsg
	if len(ce.Msg) > 0 {
		_ = json.Unmarshal(ce.Msg, &msg)
	}
	kindHint := msg.Type
	if kindHint == "" {
		kindHint = ce.Type
	}

	ev := worker.RunEvent{At: now, Raw: trimmed, Detail: map[string]any{"codex_type": kindHint}}

	switch {
	case strings.Contains(kindHint, "agent_reasoning"):
		ev.Kind = worker.RunEventReasoning
		ev.Summary = "рассуждение агента"
	case strings.Contains(kindHint, "agent_message"):
		ev.Kind = worker.RunEventMessage
		ev.Summary = truncate(firstNonEmpty(msg.Message, msg.Text, ce.Message), 400)
	case strings.Contains(kindHint, "exec_command_begin"),
		strings.Contains(kindHint, "command_begin"):
		ev.Kind = worker.RunEventCommand
		ev.Summary = "команда: " + truncate(commandString(msg.Command), 200)
		if msg.Cwd != "" {
			ev.Detail["cwd"] = msg.Cwd
		}
	case strings.Contains(kindHint, "exec_command_end"),
		strings.Contains(kindHint, "command_end"):
		ev.Kind = worker.RunEventCommand
		if msg.ExitCode != nil {
			ev.Summary = fmt.Sprintf("команда завершилась с кодом %d", *msg.ExitCode)
			ev.Detail["exit_code"] = *msg.ExitCode
		} else {
			ev.Summary = "команда завершилась"
		}
	case strings.Contains(kindHint, "patch"), strings.Contains(kindHint, "file_change"):
		ev.Kind = worker.RunEventFileChange
		ev.Summary = "агент сообщает об изменении файлов"
	case strings.Contains(kindHint, "token_count"), strings.Contains(kindHint, "usage"):
		ev.Kind = worker.RunEventTokenUsage
		ev.Summary = "учёт токенов"
	case strings.Contains(kindHint, "error"), strings.Contains(kindHint, "failed"):
		ev.Kind = worker.RunEventError
		text := firstNonEmpty(msg.Error, msg.Message, ce.Message, ce.Error.Message, errorText(ce.Msg))
		ev.Summary = truncate(firstNonEmpty(text, "ошибка"), 400)
		// Исчерпание квоты распознаётся отдельно: это не сбой инструмента,
		// а состояние учётной записи, и оно должно попасть в снимок
		// доступности, а не утонуть в общей ошибке (сценарий E).
		if quotaExhausted(text) {
			ev.Detail["quota_exhausted"] = true
			ev.Detail["quota_message"] = text
		} else if providerRefused(text) {
			// Отказ провайдера — не поломка инструмента. Причину снаружи
			// не видно: за 403 может стоять и исчерпанный лимит, и проблема
			// с учётной записью. Поэтому фиксируется сам факт отказа.
			ev.Detail["provider_refused"] = true
			ev.Detail["provider_message"] = text
		}
	case strings.Contains(kindHint, "warning"):
		ev.Kind = worker.RunEventWarning
		ev.Summary = truncate(firstNonEmpty(msg.Message, "предупреждение"), 400)
	case strings.Contains(kindHint, "task_started"), strings.Contains(kindHint, "session"),
		strings.Contains(kindHint, "thread.started"), strings.Contains(kindHint, "turn."):
		ev.Kind = worker.RunEventAction
		ev.Summary = "codex: " + kindHint
	default:
		ev.Kind = worker.RunEventOther
		ev.Summary = "codex: " + kindHint
	}
	return ev, true
}

// Collect ничего не делает: codex сам пишет итоговое сообщение файлом
// через -o, поэтому обязательный артефакт уже на месте.
func (a *Adapter) Collect(context.Context, string) error { return nil }

func commandString(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, " ")
	case nil:
		return ""
	default:
		return fmt.Sprint(c)
	}
}

// quotaExhausted распознаёт сообщение провайдера об исчерпанном лимите.
//
// Бэрримор не обходит лимиты (01_PRODUCT_BOUNDARY §2.7); он лишь честно
// фиксирует состояние и перестаёт считать исполнителя доступным.
func quotaExhausted(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"usage limit", "rate limit", "quota", "insufficient_quota",
		"purchase more credits", "429",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// providerRefused распознаёт отказ провайдера в соединении.
func providerRefused(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "403") || strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized")
}

// errorText достаёт текст ошибки из вложенных структур события.
func errorText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &nested); err != nil {
		return ""
	}
	return firstNonEmpty(nested.Error.Message, nested.Message)
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

// modelsCache — локальный кэш codex со списком моделей учётной записи.
type modelsCache struct {
	FetchedAt string `json:"fetched_at"`
	Models    []struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
	} `json:"models"`
}

// Models перечисляет модели codex из его собственного локального кэша.
//
// Запрос к провайдеру не выполняется: список читается из файла, который codex
// обновляет сам. Все модели относятся к подписке — отдельного счёта нет, но
// ресурс конечен, поэтому бесплатными они не считаются.
func (a *Adapter) Models(ctx context.Context, inst worker.Installation) ([]worker.Model, error) {
	if inst.ExecutablePath == "" {
		return nil, nil
	}
	path := filepath.Join(a.codexHome(), "models_cache.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Кэша нет — придумывать список нельзя.
			return nil, nil
		}
		return nil, fmt.Errorf("чтение кэша моделей codex: %w", err)
	}

	var cache modelsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("разбор кэша моделей codex: %w", err)
	}

	now := a.Now()
	evidence := "из локального кэша codex"
	if cache.FetchedAt != "" {
		evidence += " от " + cache.FetchedAt
	}

	out := make([]worker.Model, 0, len(cache.Models))
	for i, m := range cache.Models {
		if m.Slug == "" {
			continue
		}
		out = append(out, worker.Model{
			Ref: m.Slug, Provider: "openai", Name: m.DisplayName,
			// Подписка, а не бесплатность: квота конечна и уже была исчерпана.
			CostTier: worker.CostSubscription,
			Source:   "cli-cache", Evidence: evidence,
			Confidence: 0.8, LastCost: -1,
			IsDefault:  i == 0,
			ObservedAt: now,
		})
	}
	return out, nil
}
