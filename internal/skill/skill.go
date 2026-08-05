// Package skill — то, что Бэрримор умеет сделать сам.
//
// До сих пор он умел ровно одно: позвать исполнителя. Любой вопрос — даже
// «чем занят этот каталог» — стоил внешнего процесса, изоляции, подтверждения
// владельца, сетевого запроса к чужому провайдеру и минуты ожидания. Дворецкий,
// который на просьбу посмотреть в окно вызывает подрядчика, — плохой дворецкий.
//
// Умение здесь — не свобода действий, а её противоположность. Оно собрано из
// примитивов, каждый из которых реализован в Go, объявляет свои аргументы и
// ничего не меняет. Модель может предложить умение из показанного ей списка
// и указать каталог — и только. Ни строки команды из ответа модели не
// исполняется: это прямо запрещено (09_DEVELOPMENT_PLAN §14), и запрет
// соблюдается устройством, а не обещанием.
//
// Отсюда правило выбора: если на вопрос отвечает собственное умение, оно и
// отвечает. Делегирование остаётся для того, ради чего оно и нужно, — для
// работы, которую Бэрримор сделать не может.
package skill

import (
	"context"
	"time"
)

// Откуда взялось умение.
const (
	// OriginBuiltin — умение заложено в Бэрримора.
	OriginBuiltin = "builtin"
	// OriginLearned — умение освоено: владелец принял его как способ работы.
	OriginLearned = "learned"
)

// Исходы применения умения.
const (
	StatusDone   = "done"
	StatusFailed = "failed"
)

// Виды аргументов. Их немного намеренно: чем меньше видов, тем меньше мест,
// где проверка может оказаться слабее, чем кажется.
const (
	// ArgPath — каталог владельца. Проверяется по разрешённым корням.
	ArgPath = "path"
	// ArgCount — небольшое положительное число.
	ArgCount = "count"
)

// Target — имя аргумента, который умение получает снаружи.
//
// Шаги ссылаются на него как на "$target": подставлять значения куда угодно
// умение не может, и это тоже часть границы.
const Target = "$target"

// Arg — аргумент примитива.
type Arg struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Why  string `json:"why,omitempty"`
}

// Args — значения аргументов.
type Args map[string]string

// Fact — наблюдение, записанное так, как о нём говорят вслух.
//
// Detail — тот же факт в сыром виде. Он не выбрасывается: владелец в
// техническом режиме должен видеть, из чего сделан вывод.
type Fact struct {
	Text   string `json:"text"`
	Detail string `json:"detail,omitempty"`
}

// Observation — то, что примитив увидел.
//
// Signals — машинно-читаемые признаки для сводки умения. Они не показываются
// владельцу и существуют, чтобы сводка не разбирала обратно русский текст.
type Observation struct {
	Facts   []Fact            `json:"facts,omitempty"`
	Signals map[string]string `json:"signals,omitempty"`
}

// Primitive — элементарное действие Бэрримора.
//
// Все примитивы читают и ничего не меняют. Появление примитива с побочным
// действием потребует отдельного решения: разрешение владельца, политика
// и запись в журнал — то есть тот же путь, что у поручения, а не тихое
// расширение этого списка.
type Primitive struct {
	ID    string
	Title string
	Args  []Arg
	Run   func(ctx context.Context, in Args) (Observation, error)
}

// Step — шаг умения.
type Step struct {
	Primitive string            `json:"primitive"`
	Args      map[string]string `json:"args,omitempty"`
	Why       string            `json:"why,omitempty"`
}

// Skill — умение Бэрримора.
//
// Question — то, на что умение отвечает, словами владельца. Именно по нему
// модель выбирает умение, и именно оно показывается в интерфейсе: «посмотреть
// состояние worktree» понятнее, чем `git.worktree.diagnose`.
type Skill struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Question string `json:"question"`
	// NeedsTarget сообщает, нужен ли умению каталог.
	NeedsTarget bool   `json:"needs_target"`
	Steps       []Step `json:"steps"`
	Origin      string `json:"origin"`
	Enabled     bool   `json:"enabled"`
	// RetiredWhy заполняется, когда способ признан негодным. Умение не
	// удаляется: исчезнувшее без следа умение нельзя ни вспомнить, ни оспорить.
	RetiredWhy string `json:"retired_why,omitempty"`
	// Summarize собирает короткий ответ из наблюдений. Может быть nil —
	// тогда ответом становится перечень фактов.
	Summarize func([]StepResult) string `json:"-"`
}

// Live сообщает, можно ли применять умение.
func (s Skill) Live() bool { return s.Enabled && s.RetiredWhy == "" }

// StepResult — что вышло на шаге.
type StepResult struct {
	Primitive string            `json:"primitive"`
	Title     string            `json:"title"`
	Facts     []Fact            `json:"facts,omitempty"`
	Signals   map[string]string `json:"signals,omitempty"`
	Failure   string            `json:"failure,omitempty"`
}

// Run — применение умения.
type Run struct {
	ID             string       `json:"id"`
	SkillID        string       `json:"skill_id"`
	SkillTitle     string       `json:"skill_title"`
	Target         string       `json:"target,omitempty"`
	ThreadID       string       `json:"thread_id,omitempty"`
	ConversationID string       `json:"conversation_id,omitempty"`
	Status         string       `json:"status"`
	Answer         string       `json:"answer"`
	Failure        string       `json:"failure,omitempty"`
	Steps          []StepResult `json:"steps,omitempty"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     time.Time    `json:"finished_at"`
	// TookMS честно называет цену умения. Она и есть главный довод в пользу
	// того, чтобы не звать исполнителя ради того же ответа.
	TookMS int64 `json:"took_ms"`
}

// Типы событий.
const (
	StreamType      = "skill_run"
	EvRunCompleted  = "skill.run.completed"
	EvSkillLearned  = "skill.learned"
	EvSkillRetired  = "skill.retired"
	EvSkillProposed = "skill.proposed"
)

// Signal возвращает признак шага по имени примитива.
func (r Run) Signal(primitive, name string) string {
	for _, s := range r.Steps {
		if s.Primitive == primitive {
			return s.Signals[name]
		}
	}
	return ""
}
