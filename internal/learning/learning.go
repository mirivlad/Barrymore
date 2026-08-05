// Package learning превращает опыт в изменённое поведение.
//
// Урок, лежащий в памяти, ничему не учит. Статистика, которую никто не
// смотрит, — тоже. Обучение здесь замкнуто: каждое применение способа
// оставляет исход, исходы складываются в практику, практика меняет выбор
// способа, а способ, переставший работать, снимается с применения — с
// названной причиной и без участия владельца.
//
// Три вещи, которые из этого следуют и которых раньше не было:
//
//  1. Бэрримор знает, что у него получается. Список способов, показываемый
//     модели, идёт с записью применений, а не как ровный перечень.
//  2. Бэрримор перестаёт делать то, что перестало работать. Три неудачи
//     подряд снимают умение — молчаливое упорство хуже честного отказа.
//  3. Бэрримор осваивает новое. Повторяющийся порядок действий превращается
//     в предложение нового умения; принимает его владелец, одним нажатием.
package learning

import (
	"time"
)

// Как решалась задача.
const (
	// KindOwn — Бэрримор сделал это сам.
	KindOwn = "own"
	// KindDelegated — работу выполнил внешний исполнитель.
	KindDelegated = "delegated"
)

// Исходы.
const (
	OutcomeGood = "good"
	OutcomeBad  = "bad"
)

// failureStreakLimit — сколько неудач подряд снимают способ с применения.
//
// Три, а не одна: разовый отказ бывает от чужой заминки. И не десять:
// способ, отказавший трижды подряд на настоящей работе, уже не случайность,
// а свойство. Продолжать им пользоваться значило бы врать владельцу видом
// уверенной работы.
const failureStreakLimit = 3

// Outcome — что вышло из одного применения способа.
//
// Evidence обязателен: практика без основания — это мнение, а мнение не
// должно менять поведение системы.
type Outcome struct {
	Kind     string    `json:"kind"`
	Ref      string    `json:"ref"`
	Title    string    `json:"title"`
	Question string    `json:"question,omitempty"`
	Result   string    `json:"result"`
	Evidence string    `json:"evidence"`
	TookMS   int64     `json:"took_ms"`
	ThreadID string    `json:"thread_id,omitempty"`
	At       time.Time `json:"at"`
}

// Practice — способ работы и то, что о нём известно из опыта.
type Practice struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Question string `json:"question,omitempty"`
	// Applied, Succeeded и Failed — не украшение, а основание выбора.
	Applied   int `json:"applied"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	// Streak — неудач подряд. Обнуляется первой удачей.
	Streak int `json:"streak"`
	// AvgMS — во что способ обходится по времени.
	AvgMS       int64     `json:"avg_ms"`
	LastAt      time.Time `json:"last_at"`
	LastOutcome string    `json:"last_outcome"`
	LastNote    string    `json:"last_note,omitempty"`
	// Stale означает, что способ признан негодным, и говорит почему.
	Stale    bool   `json:"stale"`
	StaleWhy string `json:"stale_why,omitempty"`
}

// Reliable сообщает, стоит ли на способ полагаться.
func (p Practice) Reliable() bool {
	return !p.Stale && p.Applied > 0 && p.Succeeded*2 >= p.Applied
}

// Record — одна строка опыта человеческими словами.
//
// Она уходит в контекст модели и в интерфейс. Именно она и есть «изменённое
// поведение»: выбор способа опирается на неё, а не на ровный список.
func (p Practice) Record() string {
	switch {
	case p.Stale:
		return "больше не пользуюсь: " + p.StaleWhy
	case p.Applied == 0:
		return "ещё не применялось"
	case p.Failed == 0 && p.AvgMS > 0:
		return plural(p.Applied) + " без осечек, обычно за " + duration(p.AvgMS)
	case p.Failed == 0:
		return plural(p.Applied) + " без осечек"
	default:
		return plural(p.Applied) + ", из них неудачных " + itoa(p.Failed) +
			"; последний раз — " + outcomeWord(p.LastOutcome)
	}
}

// Suggestion — предложение освоить новое умение.
//
// Оно выведено из повторившегося порядка действий, а не придумано: владелец
// уже делал это дважды, и Бэрримор предлагает не изобретение, а имя для того,
// что и так происходит.
type Suggestion struct {
	// ID устойчив: одно и то же предложение не должно приходить дважды.
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Question string   `json:"question"`
	Skills   []string `json:"skills"`
	Titles   []string `json:"titles"`
	// SeenTimes — сколько раз этот порядок уже повторялся.
	SeenTimes int `json:"seen_times"`
	Why       string
}

// Типы событий.
const (
	StreamType       = "practice"
	EvOutcomeNoted   = "practice.outcome.noted"
	EvPracticeStale  = "practice.stale"
	EvSuggestionMade = "practice.suggestion"
)
