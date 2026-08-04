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
		Notes:             "codex exec --json даёт JSONL-поток событий",
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

	return worker.RunPlan{
		Argv: argv,
		Env: []string{
			"CODEX_HOME=" + a.codexHome(),
			"TERM=dumb",
			"NO_COLOR=1",
		},
		Stdin:             req.Prompt,
		StructuredOutput:  true,
		ExpectedArtifacts: artifacts,
	}, nil
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
		ev.Summary = truncate(firstNonEmpty(msg.Message, msg.Text), 400)
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
	case strings.Contains(kindHint, "error"):
		ev.Kind = worker.RunEventError
		ev.Summary = truncate(firstNonEmpty(msg.Error, msg.Message, "ошибка"), 400)
	case strings.Contains(kindHint, "warning"):
		ev.Kind = worker.RunEventWarning
		ev.Summary = truncate(firstNonEmpty(msg.Message, "предупреждение"), 400)
	case strings.Contains(kindHint, "task_started"), strings.Contains(kindHint, "session"):
		ev.Kind = worker.RunEventAction
		ev.Summary = "codex: " + kindHint
	default:
		ev.Kind = worker.RunEventOther
		ev.Summary = "codex: " + kindHint
	}
	return ev, true
}

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
