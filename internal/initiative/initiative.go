// Package initiative решает, когда Бэрримору стоит обратиться первым.
//
// 07_USER_EXPERIENCE §4: каждое инициативное сообщение отвечает на вопрос
// «почему Бэрримор обратился сейчас?». Плохой ответ — «вы давно не занимались
// проектом». Хороший — «аудит завершился, но два теста упали, поэтому я не
// отметил поручение выполненным».
//
// Отсюда главное правило: повод — наблюдаемый факт, а не догадка о состоянии
// владельца. Бэрримор не напоминает о себе и не подталкивает; он сообщает
// о том, что действительно произошло и требует человека.
package initiative

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Поводы обратиться. Каждый — наблюдаемое событие, а не вывод о настроении.
const (
	// KindOrderFinished — поручение завершилось, результат готов.
	KindOrderFinished = "order.finished"
	// KindChangesWaiting — изменения исполнителя ждут решения.
	KindChangesWaiting = "changes.waiting"
	// KindEscalated — расхождение исчерпало бюджет реакций.
	KindEscalated = "discrepancy.escalated"
	// KindApprovalWaiting — поручение ждёт подтверждения дольше разумного.
	KindApprovalWaiting = "approval.waiting"
	// KindMemoryWaiting — кандидаты в память ждут решения.
	KindMemoryWaiting = "memory.waiting"
)

// Состояния уведомления.
const (
	// StatusHeld — повод есть, но время неподходящее либо предел исчерпан.
	StatusHeld = "held"
	// StatusDelivered — показано владельцу.
	StatusDelivered = "delivered"
	// StatusRead — владелец увидел.
	StatusRead = "read"
	// StatusStale — повод отпал раньше, чем сообщение дошло.
	StatusStale = "stale"
)

// Важность повода. От неё зависит, ждать ли конца тихих часов.
const (
	// LevelRoutine — можно подождать до утра.
	LevelRoutine = "routine"
	// LevelAttention — стоит знать, но мир не рухнет.
	LevelAttention = "attention"
	// LevelUrgent — Бэрримор не справился сам, дальше без человека никак.
	LevelUrgent = "urgent"
)

// Notice — обращение Бэрримора по конкретному поводу.
type Notice struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// SubjectType и SubjectID указывают, о чём речь: поручение, нить, память.
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Level       string `json:"level"`
	// Title — что произошло, одной строкой.
	Title string `json:"title"`
	// Why отвечает на «почему сейчас». Без него уведомления не бывает.
	Why string `json:"why"`
	// Status и время показывают судьбу обращения.
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliverAt   time.Time  `json:"deliver_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	// DedupeKey не даёт обратиться дважды по одному и тому же поводу.
	DedupeKey string `json:"dedupe_key"`
}

// Fresh сообщает, ждёт ли уведомление владельца.
func (n Notice) Fresh() bool { return n.Status == StatusHeld || n.Status == StatusDelivered }

// Policy — рамки, в которых Бэрримор может обращаться первым.
type Policy struct {
	// Enabled выключает инициативу целиком.
	Enabled bool `json:"enabled"`
	// QuietFrom и QuietTo — часы, когда Бэрримор молчит (местное время).
	// Срочное проходит и в тихие часы: молчать о том, с чем он не справился,
	// значило бы скрывать неудачу.
	QuietFrom int `json:"quiet_from"`
	QuietTo   int `json:"quiet_to"`
	// MaxPerDay ограничивает число обращений в сутки. Превышение не отменяет
	// повод: сообщения ждут, и Бэрримор говорит, сколько их накопилось.
	MaxPerDay int `json:"max_per_day"`
	// MutedKinds — поводы, о которых владелец просил не сообщать.
	MutedKinds []string `json:"muted_kinds,omitempty"`
	// MutedSubjects — конкретные нити или поручения, о которых он молчит.
	MutedSubjects []string `json:"muted_subjects,omitempty"`
	// ApprovalPatience — сколько ждать, прежде чем напомнить о подтверждении.
	ApprovalPatience time.Duration `json:"approval_patience"`
}

// DefaultPolicy — разумные рамки: молчать ночью, не больше десяти раз в сутки.
func DefaultPolicy() Policy {
	return Policy{
		Enabled:   true,
		QuietFrom: 23, QuietTo: 8,
		MaxPerDay: 10,
		// Час — не «долго не отвечает», а «похоже, забыли»: подтверждение
		// стоит между владельцем и работой, которую он сам заказал.
		ApprovalPatience: time.Hour,
	}
}

// Muted сообщает, просил ли владелец молчать об этом.
func (p Policy) Muted(kind, subjectID string) bool {
	for _, k := range p.MutedKinds {
		if k == kind {
			return true
		}
	}
	for _, s := range p.MutedSubjects {
		if s == subjectID {
			return true
		}
	}
	return false
}

// Quiet сообщает, попадает ли момент в тихие часы.
func (p Policy) Quiet(at time.Time) bool {
	if p.QuietFrom == p.QuietTo {
		return false
	}
	h := at.Hour()
	if p.QuietFrom < p.QuietTo {
		return h >= p.QuietFrom && h < p.QuietTo
	}
	// Промежуток через полночь.
	return h >= p.QuietFrom || h < p.QuietTo
}

// NextAudible возвращает ближайший момент, когда можно говорить.
func (p Policy) NextAudible(at time.Time) time.Time {
	if !p.Quiet(at) {
		return at
	}
	next := time.Date(at.Year(), at.Month(), at.Day(), p.QuietTo, 0, 0, 0, at.Location())
	if !next.After(at) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// Describe объясняет режим человеческими словами.
func (p Policy) Describe() string {
	if !p.Enabled {
		return "Бэрримор не обращается первым"
	}
	return fmt.Sprintf(
		"Бэрримор обращается сам не чаще %d раз в сутки и молчит с %02d:00 до %02d:00; "+
			"срочное проходит и в тихие часы",
		p.MaxPerDay, p.QuietFrom, p.QuietTo)
}

// ParsePolicy разбирает режим инициативы.
func ParsePolicy(mode string) (Policy, error) {
	p := DefaultPolicy()
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "on":
		return p, nil
	case "off":
		p.Enabled = false
		return p, nil
	case "urgent-only":
		// Только то, с чем Бэрримор не справился сам.
		p.MutedKinds = []string{
			KindOrderFinished, KindChangesWaiting, KindApprovalWaiting, KindMemoryWaiting,
		}
		return p, nil
	default:
		return Policy{}, fmt.Errorf(
			"неизвестный режим инициативы %q: допустимы on, urgent-only, off", mode)
	}
}

// Candidate — повод, найденный планировщиком.
//
// Ещё не уведомление: политика решает, станет ли он им и когда.
type Candidate struct {
	Kind        string
	SubjectType string
	SubjectID   string
	Level       string
	Title       string
	Why         string
	// DedupeKey отличает повод от повода. Один и тот же не превращается
	// в два обращения.
	DedupeKey string
	// ObservedAt — когда повод возник.
	ObservedAt time.Time
}

// Типы событий инициативы.
const (
	EvNoticeCreated   = "initiative.notice.created"
	EvNoticeDelivered = "initiative.notice.delivered"
	EvNoticeRead      = "initiative.notice.read"
	EvNoticeStale     = "initiative.notice.stale"
)

// StreamType — тип потока событий инициативы.
const StreamType = "initiative"

// ProjectionTables — таблицы проекций.
var ProjectionTables = []string{"initiative_notices"}

// noticePayload — событие о создании обращения.
type noticePayload struct {
	Notice Notice `json:"notice"`
}

// statusPayload — смена судьбы обращения.
type statusPayload struct {
	NoticeID string    `json:"notice_id"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason,omitempty"`
}

// MarshalPolicy сериализует политику для показа.
func MarshalPolicy(p Policy) json.RawMessage {
	b, _ := json.Marshal(p)
	return b
}
