package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/worker"
)

// Registrar принимает нового исполнителя в штат.
type Registrar interface {
	Register(a worker.Adapter) error
	Known(id string) bool
}

// Service ведёт подключение незнакомых инструментов.
type Service struct {
	provider model.Provider
	journal  *event.Journal
	clock    clock.Clock
	registry Registrar
	log      *slog.Logger
	look     Look
}

// Config — что нужно для подключения.
type Config struct {
	Provider model.Provider
	Journal  *event.Journal
	Clock    clock.Clock
	Registry Registrar
	Logger   *slog.Logger
	// Look подменяется в тестах.
	Look Look
}

// New создаёт службу подключения.
func New(cfg Config) *Service {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		provider: cfg.Provider, journal: cfg.Journal, clock: cfg.Clock,
		registry: cfg.Registry, log: log, look: cfg.Look,
	}
}

// Study изучает названный инструмент и выводит способ обращения с ним.
//
// Порядок именно такой: сперва наблюдение своими средствами, потом чтение.
// Модель не спрашивают «как запускать X» — иначе она отвечала бы по памяти
// о версии двухлетней давности. Ей дают справку, напечатанную установленным
// здесь инструментом, и просят выбрать из неё.
func (s *Service) Study(ctx context.Context, name string) (Survey, Draft, error) {
	if s.registry != nil && s.registry.Known(strings.TrimSpace(name)) {
		return Survey{}, Draft{}, fmt.Errorf(
			"%s уже в штате: подключать заново нечего", name)
	}

	sv, err := Observe(ctx, name, s.look)
	if err != nil {
		return Survey{}, Draft{}, err
	}
	s.note(ctx, EvSurveyed, sv.Name, sv)

	if s.provider == nil {
		return sv, Draft{}, fmt.Errorf(
			"справку я собрал, но разобрать её без разговорного слоя не могу")
	}

	draft, err := s.derive(ctx, sv)
	if err != nil {
		return sv, Draft{}, err
	}
	if err := Validate(draft, sv); err != nil {
		draft.Refused = err.Error()
		s.note(ctx, EvRefused, sv.Name, map[string]any{
			"draft": draft, "why": err.Error(),
		})
		return sv, draft, nil
	}
	return sv, draft, nil
}

// Adopt принимает исполнителя в штат по решению владельца.
func (s *Service) Adopt(ctx context.Context, d Draft, sv Survey) (worker.Manifest, error) {
	if err := Validate(d, sv); err != nil {
		return worker.Manifest{}, err
	}
	m := ToManifest(d)
	if s.registry == nil {
		return worker.Manifest{}, fmt.Errorf("штат недоступен")
	}
	if err := s.registry.Register(worker.NewManifestAdapter(m)); err != nil {
		return worker.Manifest{}, err
	}
	s.note(ctx, EvAdopted, m.ID, m)
	s.log.Info("исполнитель подключён Бэрримором", "worker", m.ID, "argv", m.Run.Args)
	return m, nil
}

func (s *Service) note(ctx context.Context, eventType, id string, payload any) {
	if s.journal == nil {
		return
	}
	if _, err := s.journal.Write(ctx, func(_ *sql.Tx, w *event.TxWriter) error {
		_, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: id, ExpectedRevision: event.AnyRevision,
			EventType: eventType, Actor: event.Actor{Type: event.ActorBarrymore},
			Payload: payload,
		})
		return err
	}); err != nil {
		s.log.Error("подключение не записано в журнал", "harness", id, "error", err)
	}
}

// derive просит модель прочитать справку и выбрать из неё способ запуска.
func (s *Service) derive(ctx context.Context, sv Survey) (Draft, error) {
	resp, err := s.provider.Complete(ctx, model.Request{
		System: systemPrompt(sv),
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "Вот что напечатал " + sv.Name + ":\n\n" + sv.Help,
		}},
		Schema:          DraftSchema(),
		SchemaName:      "harness_draft",
		MaxTokens:       900,
		Temperature:     0.1,
		DisableThinking: true,
	})
	if err != nil {
		return Draft{}, fmt.Errorf("справку разобрать не вышло: %w", err)
	}

	text := strings.TrimSpace(resp.Content)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")

	var d Draft
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &d); err != nil {
		return Draft{}, fmt.Errorf("разбор предложения: %v", err)
	}
	d.Name = sv.Name
	if d.DisplayName == "" {
		d.DisplayName = sv.Name
	}
	return d, nil
}

func systemPrompt(sv Survey) string {
	var b strings.Builder
	b.WriteString("Ты Бэрримор. Тебе показали справку незнакомой программы, и надо\n")
	b.WriteString("понять, можно ли поручать ей работу и как её запускать неинтерактивно.\n\n")
	b.WriteString("Главное правило: ты ничего не придумываешь. Каждый флаг и каждая\n")
	b.WriteString("подкоманда, которые ты назовёшь, обязаны дословно встречаться\n")
	b.WriteString("в показанной справке. Всё, чего в ней нет, будет отвергнуто\n")
	b.WriteString("проверкой — и подключение не состоится.\n\n")
	b.WriteString("Аргумент — это одно слово. «-p задание» — два аргумента, а не один.\n")
	b.WriteString("Строку команды с кавычками, конвейерами и подстановками не пиши:\n")
	b.WriteString("оболочки здесь нет, argv собирается по списку.\n\n")
	b.WriteString("`run_args` — как запустить с заданием и получить ответ, не задавая\n")
	b.WriteString("вопросов и не открывая интерфейса. Место задания обозначь ровно одним\n")
	b.WriteString("элементом `{prompt}`, если оно передаётся аргументом; если инструмент\n")
	b.WriteString("читает задание со стандартного ввода — поставь `prompt_via` в `stdin`\n")
	b.WriteString("и `{prompt}` не пиши.\n\n")
	b.WriteString("`audit_args` — флаги, при которых инструмент точно ничего не меняет\n")
	b.WriteString("(например «только чтение», «без правок», «сухой прогон»). Если таких\n")
	b.WriteString("в справке нет — оставь список пустым, это нормально и честно.\n\n")
	b.WriteString("`evidence` — строки справки, на которых держится твой вывод.\n")
	b.WriteString("`why` — почему запуск именно такой, одной фразой.\n\n")
	if len(sv.Flags) > 0 {
		b.WriteString("Флаги, которые я разобрал в справке (выбирать можно только из них\n")
		b.WriteString("и из подкоманд, встреченных в тексте):\n")
		b.WriteString(strings.Join(sv.Flags, " "))
		b.WriteString("\n\n")
	}
	if sv.Version != "" {
		b.WriteString("Версия отвечает: " + sv.Version + "\n")
	}
	return b.String()
}

// DraftSchema — контракт предложения.
func DraftSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["display_name", "version_args", "run_args", "prompt_via",
               "audit_args", "model_flag", "auth_paths", "capabilities", "why", "evidence"],
  "properties": {
    "display_name": {"type": "string", "description": "Человеческое имя инструмента."},
    "version_args": {"type": "array", "items": {"type": "string"},
      "description": "Аргументы опроса версии, например [\"--version\"]."},
    "run_args": {"type": "array", "items": {"type": "string"},
      "description": "Аргументы неинтерактивного запуска. Ровно один элемент {prompt}, если задание идёт аргументом."},
    "prompt_via": {"type": "string", "enum": ["argv", "stdin"]},
    "audit_args": {"type": "array", "items": {"type": "string"},
      "description": "Флаги режима без изменений. Пустой список, если таких нет."},
    "model_flag": {"type": "string", "description": "Флаг выбора модели или пустая строка."},
    "auth_paths": {"type": "array", "items": {"type": "string"},
      "description": "Пути настроек вида ~/.имя, если они названы в справке."},
    "capabilities": {"type": "array",
      "items": {"type": "string", "enum": ["repository-audit", "code-edit", "tests", "web-research", "structured-output", "russian", "long-context"]}},
    "why": {"type": "string"},
    "evidence": {"type": "array", "items": {"type": "string"},
      "description": "Строки справки, на которых держится вывод."}
  }
}`)
}
