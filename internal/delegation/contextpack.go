package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/thread"
)

// ContextPackVersion — версия схемы пакета.
const ContextPackVersion = 1

// ContextPack — версионируемый пакет контекста поручения (08_API_AND_EVENTS §6).
type ContextPack struct {
	SchemaVersion int       `json:"schema_version"`
	WorkOrderID   string    `json:"work_order_id"`
	GeneratedAt   time.Time `json:"generated_at"`
	Thread        struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Origin string `json:"origin,omitempty"`
		State  string `json:"state"`
	} `json:"thread"`
	Goal string `json:"goal"`
	Why  string `json:"why,omitempty"`
	// ConfirmedDecisions — только зафиксированные решения нити.
	ConfirmedDecisions []PackDecision `json:"confirmed_decisions"`
	// Positions передаются раздельно: расхождение сторон — это сведение,
	// а не шум, который надо усреднить.
	Positions           []PackPosition      `json:"positions"`
	OpenQuestions       []string            `json:"open_questions"`
	Constraints         []string            `json:"constraints"`
	Workspace           PackWorkspace       `json:"workspace"`
	AllowedActions      []string            `json:"allowed_actions"`
	ForbiddenActions    []string            `json:"forbidden_actions"`
	AcceptanceCriteria  []string            `json:"acceptance_criteria"`
	OperationalContract OperationalContract `json:"operational_contract"`
	// VerificationCommands приводятся для сведения исполнителя.
	// ADR 0011: runtime исполняет собственную запись, а не полученную обратно.
	VerificationCommands []string        `json:"verification_commands"`
	RequiredReport       []string        `json:"required_report"`
	ReportSchema         json.RawMessage `json:"report_schema,omitempty"`
	// Notes — то, что исполнителю нужно знать о самой обстановке работы,
	// а не о задаче: например, что он пишет в копию, а не в каталог владельца.
	Notes []string `json:"notes,omitempty"`
}

// PackDecision — решение в пакете.
type PackDecision struct {
	Statement string `json:"statement"`
	DecidedBy string `json:"decided_by"`
	Rationale string `json:"rationale,omitempty"`
}

// PackPosition — позиция стороны в пакете.
type PackPosition struct {
	Owner      string  `json:"owner"`
	Statement  string  `json:"statement"`
	Confidence float64 `json:"confidence"`
}

// PackWorkspace — состояние рабочего каталога на момент подготовки.
type PackWorkspace struct {
	Root           string   `json:"root"`
	GitHead        string   `json:"git_head,omitempty"`
	WorktreePolicy string   `json:"worktree_policy"`
	Dirty          []string `json:"uncommitted_changes,omitempty"`
	FileCount      int      `json:"file_count"`
}

// BuildContextPack собирает пакет из нити и состояния каталога.
func BuildContextPack(order WorkOrder, detail thread.Detail, ws WorkspaceState, schema json.RawMessage) ContextPack {
	p := ContextPack{
		SchemaVersion:       ContextPackVersion,
		WorkOrderID:         order.ID,
		GeneratedAt:         order.UpdatedAt,
		Goal:                order.Goal,
		Why:                 order.Why,
		Constraints:         order.Constraints,
		AcceptanceCriteria:  order.AcceptanceCriteria,
		OperationalContract: order.Contract,
		RequiredReport: []string{
			"краткое резюме состояния репозитория",
			"перечень находок с указанием файлов",
			"явные ограничения проверки",
		},
		ReportSchema: schema,
	}
	p.Thread.ID = detail.Thread.ID
	p.Thread.Title = detail.Thread.Title
	p.Thread.Origin = detail.Thread.Origin
	p.Thread.State = detail.Thread.State

	for _, d := range detail.Decisions {
		p.ConfirmedDecisions = append(p.ConfirmedDecisions, PackDecision{
			Statement: d.Statement, DecidedBy: d.DecidedBy, Rationale: d.Rationale,
		})
	}
	for _, pos := range detail.Positions {
		if pos.ValidUntil != nil {
			continue // в пакет идут только действующие позиции
		}
		p.Positions = append(p.Positions, PackPosition{
			Owner: pos.Owner, Statement: pos.Statement, Confidence: pos.Confidence,
		})
	}
	for _, q := range detail.Questions {
		if q.Status == thread.QuestionOpen {
			p.OpenQuestions = append(p.OpenQuestions, q.Question)
		}
	}

	p.Workspace = PackWorkspace{
		Root: ws.Root, GitHead: ws.GitHead, FileCount: ws.FileCount,
		WorktreePolicy: "read-only", Dirty: ws.GitDirty,
	}
	if !order.AuditOnly {
		p.Workspace.WorktreePolicy = "isolated-copy"
	}

	if order.AuditOnly {
		p.AllowedActions = []string{
			"читать файлы рабочего каталога",
			"запускать команды только для чтения",
			"вернуть отчёт по заданной схеме",
		}
		p.ForbiddenActions = []string{
			"создавать, изменять или удалять файлы",
			"выполнять git commit, git push и любые команды, меняющие репозиторий",
			"устанавливать зависимости",
			"обращаться к сети помимо собственного провайдера модели",
		}
	} else {
		// Исполнитель должен знать, что работает в копии. Иначе он попытается
		// сделать то, чего от него не ждут: закоммитить, отправить, слить —
		// и потратит попытки на упирание в запрет.
		p.AllowedActions = []string{
			"читать и изменять файлы рабочего каталога",
			"создавать и удалять файлы в нём",
			"запускать сборку и тесты",
			"вернуть отчёт по заданной схеме",
		}
		p.ForbiddenActions = []string{
			"выполнять git commit и git push: изменения собирает Бэрримор",
			"менять историю репозитория",
			"выходить за пределы рабочего каталога",
			"обращаться к сети помимо собственного провайдера модели",
		}
		p.Notes = append(p.Notes,
			"Это копия каталога владельца, а не он сам. Изменения не попадут "+
				"к владельцу автоматически: он посмотрит их и решит сам. "+
				"Коммитить не нужно — достаточно оставить файлы в нужном виде.")
	}
	return p
}

// Prompt превращает пакет в текст поручения для исполнителя.
//
// Пакет остаётся каноническим артефактом; текст — его представление.
func (p ContextPack) Prompt() string {
	var b strings.Builder
	b.WriteString("Ты выполняешь поручение системы Бэрримор.\n\n")
	b.WriteString("## Цель\n" + p.Goal + "\n\n")
	if p.Why != "" {
		b.WriteString("## Зачем это нужно\n" + p.Why + "\n\n")
	}
	b.WriteString("## Нить\n" + p.Thread.Title)
	if p.Thread.Origin != "" {
		b.WriteString("\nПроисхождение: " + p.Thread.Origin)
	}
	b.WriteString("\n\n")

	if len(p.ConfirmedDecisions) > 0 {
		b.WriteString("## Принятые решения\n")
		for _, d := range p.ConfirmedDecisions {
			b.WriteString("- " + d.Statement)
			if d.Rationale != "" {
				b.WriteString(" (причина: " + d.Rationale + ")")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(p.OpenQuestions) > 0 {
		b.WriteString("## Открытые вопросы\n")
		for _, q := range p.OpenQuestions {
			b.WriteString("- " + q + "\n")
		}
		b.WriteString("\n")
	}
	if len(p.Constraints) > 0 {
		b.WriteString("## Ограничения\n")
		for _, c := range p.Constraints {
			b.WriteString("- " + c + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Рабочий каталог\n")
	b.WriteString("Корень: " + p.Workspace.Root + "\n")
	if p.Workspace.GitHead != "" {
		b.WriteString("HEAD: " + p.Workspace.GitHead + "\n")
	}
	if len(p.Workspace.Dirty) > 0 {
		b.WriteString(fmt.Sprintf(
			"В каталоге есть незакоммиченные изменения (%d). Они не должны пострадать.\n",
			len(p.Workspace.Dirty)))
	}
	b.WriteString("\n")

	if len(p.AllowedActions) > 0 {
		b.WriteString("## Разрешено\n")
		for _, a := range p.AllowedActions {
			b.WriteString("- " + a + "\n")
		}
		b.WriteString("\n")
	}
	if len(p.ForbiddenActions) > 0 {
		b.WriteString("## Запрещено\n")
		for _, a := range p.ForbiddenActions {
			b.WriteString("- " + a + "\n")
		}
		// Про изоляцию говорится только там, где она действительно запрещает
		// запись. При контролируемой записи запись разрешена, и эта фраза
		// сбивала бы исполнителя с толку.
		if p.Workspace.WorktreePolicy == "read-only" {
			b.WriteString("\nЗапрет обеспечивается изоляцией файловой системы: " +
				"попытка записи завершится ошибкой. Это ожидаемо, обходить её не нужно.\n")
		}
		b.WriteString("\n")
	}
	if len(p.Notes) > 0 {
		b.WriteString("## Об обстановке\n")
		for _, n := range p.Notes {
			b.WriteString(n + "\n")
		}
		b.WriteString("\n")
	}
	if len(p.AcceptanceCriteria) > 0 {
		b.WriteString("## Критерии приёмки\n")
		for _, c := range p.AcceptanceCriteria {
			b.WriteString("- " + c + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Формат ответа\n")
	b.WriteString("Итоговое сообщение должно быть строго JSON по заданной схеме, без пояснений вокруг.\n")
	b.WriteString("Поля: summary (строка), findings (массив объектов с title, severity, evidence, " +
		"необязательный path), checked_paths (массив строк), limitations (строка).\n")
	b.WriteString("В limitations честно перечисли, что проверить не удалось.\n")
	return b.String()
}

// WritePack сохраняет пакет как артефакт и возвращает путь и контрольную сумму.
func WritePack(dir string, p ContextPack) (string, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("каталог пакета контекста: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("сериализация пакета контекста: %w", err)
	}
	path := filepath.Join(dir, "context-pack.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", fmt.Errorf("запись пакета контекста: %w", err)
	}
	sum := sha256.Sum256(data)
	return path, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ReportSchema — схема обязательного отчёта.
//
// ADR 0011: схема принадлежит Бэрримору. Исполнитель обязан ей соответствовать,
// но не может её изменить.
func ReportSchema() json.RawMessage {
	return json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "findings", "limitations"],
  "properties": {
    "summary": {"type": "string", "minLength": 1},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "severity", "evidence"],
        "properties": {
          "title": {"type": "string", "minLength": 1},
          "severity": {"type": "string", "enum": ["info", "low", "medium", "high"]},
          "evidence": {"type": "string", "minLength": 1},
          "path": {"type": "string"}
        }
      }
    },
    "checked_paths": {"type": "array", "items": {"type": "string"}},
    "limitations": {"type": "string"}
  }
}`)
}

// WriteReportSchema сохраняет схему рядом с пакетом.
func WriteReportSchema(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("каталог схемы отчёта: %w", err)
	}
	path := filepath.Join(dir, "report.schema.json")
	if err := os.WriteFile(path, ReportSchema(), 0o600); err != nil {
		return "", fmt.Errorf("запись схемы отчёта: %w", err)
	}
	return path, nil
}
