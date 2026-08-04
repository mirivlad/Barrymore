package memory

import (
	"fmt"
	"strings"
)

// Режимы записи в память.
const (
	// ModeAsk — каждая запись требует решения владельца.
	ModeAsk = "ask"
	// ModeAutoSafe — Бэрримор сам записывает то, что владелец сказал о себе
	// прямо и что не помечено чувствительным. Остальное спрашивает.
	ModeAutoSafe = "auto-safe"
	// ModeAuto — Бэрримор решает сам во всех случаях.
	ModeAuto = "auto"
)

// Уровни чувствительности.
const (
	SensitivityNormal    = "normal"
	SensitivitySensitive = "sensitive"
	SensitivityPrivate   = "private"
)

// Policy определяет, что Бэрримор записывает сам, а что выносит на решение.
//
// 04_MEMORY_AND_CONTINUITY §4 допускает auto-accept для малочувствительных и
// явно сформулированных сведений. Непереговорное остаётся прежним: запись
// видима, объяснима и отменяема. Автоматическая запись — не скрытая запись.
type Policy struct {
	Mode string `json:"mode"`
	// AutoTypes перечисляет типы, которые допускают автоматическую запись.
	AutoTypes []string `json:"auto_types"`
	// MinConfidence — ниже этого порога Бэрримор спрашивает.
	MinConfidence float64 `json:"min_confidence"`
	// MaxSensitivity — самый чувствительный уровень, допускающий автозапись.
	MaxSensitivity string `json:"max_sensitivity"`
}

// DefaultPolicy — разумное умолчание: очевидное записывается само.
func DefaultPolicy() Policy {
	return Policy{
		Mode: ModeAutoSafe,
		// Эпизоды и уроки не записываются сами: это выводы Бэрримора о мире
		// и о себе, а не сказанное владельцем.
		AutoTypes:      []string{TypeFact, TypePreference, TypeDecision},
		MinConfidence:  0.6,
		MaxSensitivity: SensitivityNormal,
	}
}

// ParsePolicy разбирает название режима.
func ParsePolicy(mode string) (Policy, error) {
	p := DefaultPolicy()
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeAutoSafe:
		return p, nil
	case ModeAsk:
		p.Mode = ModeAsk
		return p, nil
	case ModeAuto:
		p.Mode = ModeAuto
		p.MaxSensitivity = SensitivitySensitive
		p.MinConfidence = 0.4
		return p, nil
	default:
		return Policy{}, fmt.Errorf(
			"неизвестный режим памяти %q: допустимы ask, auto-safe, auto", mode)
	}
}

// Describe объясняет режим человеческими словами.
func (p Policy) Describe() string {
	switch p.Mode {
	case ModeAsk:
		return "каждая запись требует вашего решения"
	case ModeAuto:
		return "Бэрримор записывает сам; чувствительное всё равно спрашивает"
	default:
		return "Бэрримор сам записывает очевидное о вас; остальное спрашивает"
	}
}

var sensitivityRank = map[string]int{
	SensitivityNormal:    0,
	SensitivitySensitive: 1,
	SensitivityPrivate:   2,
}

// Decision — что делать с кандидатом.
type Decision struct {
	Auto bool
	// Reason объясняет решение и попадает в запись: владелец должен видеть,
	// почему Бэрримор записал это сам.
	Reason string
}

// Decide решает судьбу кандидата.
func (p Policy) Decide(c Candidate) Decision {
	if p.Mode == ModeAsk {
		return Decision{Reason: "режим памяти требует вашего решения по каждой записи"}
	}

	// Сказанное владельцем прямо — не догадка Бэрримора, и спрашивать
	// разрешения на то, что владелец только что сообщил, незачем.
	if c.ProposedBy == ProposedByOwner {
		return Decision{Auto: true, Reason: "владелец попросил это запомнить"}
	}

	if sensitivityRank[c.Sensitivity] > sensitivityRank[p.MaxSensitivity] {
		return Decision{Reason: "сведение помечено чувствительным (" + c.Sensitivity +
			"), поэтому требует вашего решения"}
	}
	if c.Confidence < p.MinConfidence {
		return Decision{Reason: fmt.Sprintf(
			"Бэрримор не уверен (%.1f при пороге %.1f), поэтому спрашивает",
			c.Confidence, p.MinConfidence)}
	}
	for _, t := range p.AutoTypes {
		if t == c.Type {
			return Decision{Auto: true, Reason: fmt.Sprintf(
				"Бэрримор записал сам: тип «%s», уверенность %.1f, чувствительность обычная",
				c.Type, c.Confidence)}
		}
	}
	return Decision{Reason: "тип «" + c.Type + "» не записывается автоматически"}
}
