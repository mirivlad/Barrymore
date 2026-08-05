// Package harness даёт Бэрримору мета-умение: подключать незнакомые
// инструменты самому.
//
// Adapter в коде — расширяемость для разработчика: чтобы Бэрримор научился
// звать новый CLI, кто-то должен написать Go-файл, собрать и перезапустить.
// Для владельца это означает, что список исполнителей заморожен ровно в том
// виде, в каком его оставили. Инструмент, поставленный вчера, невидим.
//
// Здесь другое разделение труда. Владелец называет команду — это единственное
// знание, которого система не может вывести сама. Всё остальное Бэрримор
// делает сам: опрашивает инструмент безопасными пробами, читает его же
// справку, выводит из неё способ запуска и приносит владельцу готовое
// предложение.
//
// Опасность очевидна: способ запуска приходит из ответа модели, а исполнять
// команды из ответа модели запрещено (09_DEVELOPMENT_PLAN §14). Поэтому
// правило жёсткое: ни один аргумент не принимается, если его нет в справке,
// напечатанной самим инструментом. Модель не придумывает флаги — она читает
// то, что инструмент о себе сказал. Всё, чего в справке нет, отвергается
// с объяснением.
package harness

import (
	"time"
)

// Probe — одна безопасная проба.
type Probe struct {
	Args []string `json:"args"`
	// Output — то, что инструмент напечатал. Обрезается: справки бывают
	// огромными, а в контекст модели нужно поместиться.
	Output string `json:"output"`
	OK     bool   `json:"ok"`
	Note   string `json:"note,omitempty"`
}

// Survey — всё, что Бэрримор увидел об инструменте своими средствами.
type Survey struct {
	Name           string  `json:"name"`
	ExecutablePath string  `json:"executable_path"`
	Version        string  `json:"version,omitempty"`
	Probes         []Probe `json:"probes"`
	// Flags — флаги, действительно встреченные в справке. Именно они
	// и образуют границу дозволенного.
	Flags      []string  `json:"flags"`
	Help       string    `json:"help"`
	SurveyedAt time.Time `json:"surveyed_at"`
}

// Draft — предложенный способ обращения с инструментом.
//
// Это ещё не исполнитель: пока владелец не согласился, ничего не
// зарегистрировано и ничего не запускается.
type Draft struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	VersionArgs []string `json:"version_args"`
	// RunArgs — неинтерактивный запуск. Ровно один элемент обязан быть
	// «{prompt}», если задание передаётся аргументом.
	RunArgs   []string `json:"run_args"`
	PromptVia string   `json:"prompt_via"`
	AuditArgs []string `json:"audit_args,omitempty"`
	ModelFlag string   `json:"model_flag,omitempty"`
	AuthPaths []string `json:"auth_paths,omitempty"`
	// Capabilities — то, что инструмент о себе заявляет. Основание у них
	// одно и то же — `declared`: справка не является доказательством работы.
	Capabilities []string `json:"capabilities,omitempty"`
	Why          string   `json:"why"`
	// Evidence перечисляет строки справки, на которых держится предложение.
	// Без них проверить вывод было бы нечем.
	Evidence []string `json:"evidence,omitempty"`
	// Refused объясняет, почему предложение не годится.
	Refused string `json:"refused,omitempty"`
}

// Как передаётся задание.
const (
	PromptViaArgv  = "argv"
	PromptViaStdin = "stdin"
)

// PromptToken — место в аргументах, куда подставляется задание.
const PromptToken = "{prompt}"

// Типы событий.
const (
	StreamType = "harness"
	EvSurveyed = "harness.surveyed"
	EvAdopted  = "harness.adopted"
	EvRefused  = "harness.refused"
)
