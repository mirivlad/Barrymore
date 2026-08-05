package initiative

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
)

// ErrNotFound возвращается, когда обращения нет.
var ErrNotFound = errors.New("обращение не найдено")

// Source поставляет поводы обратиться.
//
// Источник обязан возвращать только наблюдаемые факты. Догадки о том, что
// владельцу «пора бы» чем-то заняться, сюда не попадают.
type Source interface {
	// Candidates перечисляет поводы на текущий момент.
	Candidates(ctx context.Context, now time.Time) ([]Candidate, error)
}

// Service ведёт инициативу.
type Service struct {
	db      *store.DB
	journal *event.Journal
	clock   clock.Clock
	log     *slog.Logger
	policy  Policy
	sources []Source
}

// NewService создаёт службу инициативы.
func NewService(db *store.DB, j *event.Journal, clk clock.Clock, p Policy, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if p.MaxPerDay <= 0 {
		p.MaxPerDay = DefaultPolicy().MaxPerDay
	}
	return &Service{db: db, journal: j, clock: clk, policy: p, log: log}
}

// AddSource подключает поставщика поводов.
func (s *Service) AddSource(src Source) { s.sources = append(s.sources, src) }

// Policy возвращает действующие рамки.
func (s *Service) Policy() Policy { return s.policy }

// Tick собирает поводы и решает, что из них станет обращением.
//
// Вызывается тем же циклом, что и предиктивный контур: инициатива — не
// отдельный демон, а следствие того, что Бэрримор и так наблюдает.
func (s *Service) Tick(ctx context.Context) (created, delivered int, err error) {
	if !s.policy.Enabled {
		return 0, 0, nil
	}
	now := s.clock.Now()

	for _, src := range s.sources {
		cands, err := src.Candidates(ctx, now)
		if err != nil {
			// Отказ одного источника не должен глушить остальные: молчание
			// из-за чужой поломки — худший вид молчания.
			s.log.Error("источник поводов недоступен", "error", err)
			continue
		}
		for _, c := range cands {
			ok, err := s.consider(ctx, c, now)
			if err != nil {
				return created, delivered, err
			}
			if ok {
				created++
			}
		}
	}

	n, err := s.deliverDue(ctx, now)
	if err != nil {
		return created, delivered, err
	}
	return created, n, nil
}

// consider превращает повод в обращение, если политика позволяет.
func (s *Service) consider(ctx context.Context, c Candidate, now time.Time) (bool, error) {
	if c.Why == "" || c.Title == "" {
		// Обращение без причины запрещено: оно неотличимо от навязчивости.
		return false, fmt.Errorf("initiative: повод %q без причины обращения", c.Kind)
	}
	if s.policy.Muted(c.Kind, c.SubjectID) {
		return false, nil
	}

	key := c.DedupeKey
	if key == "" {
		key = c.Kind + ":" + c.SubjectID
	}
	exists, err := s.hasNotice(ctx, key)
	if err != nil || exists {
		return false, err
	}

	level := c.Level
	if level == "" {
		level = LevelRoutine
	}

	// Тихие часы откладывают, но не отменяют: повод никуда не денется,
	// а срочное проходит сразу — молчать о том, с чем Бэрримор не справился,
	// значило бы скрывать неудачу.
	deliverAt := now
	if level != LevelUrgent {
		deliverAt = s.policy.NextAudible(now)
	}

	n := Notice{
		ID: ids.New("ntc"), Kind: c.Kind,
		SubjectType: c.SubjectType, SubjectID: c.SubjectID,
		Level: level, Title: c.Title, Why: c.Why,
		Status: StatusHeld, CreatedAt: now, DeliverAt: deliverAt,
		DedupeKey: key,
	}

	p := noticePayload{Notice: n}
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: n.ID, ExpectedRevision: 0,
			EventType: EvNoticeCreated, Actor: event.Actor{Type: event.ActorBarrymore},
			CorrelationID: c.SubjectID, Payload: p,
		}); err != nil {
			return err
		}
		return applyNotice(ctx, tx, p)
	})
	return err == nil, err
}

// deliverDue показывает то, чему настало время, в пределах суточного лимита.
func (s *Service) deliverDue(ctx context.Context, now time.Time) (int, error) {
	used, err := s.deliveredToday(ctx, now)
	if err != nil {
		return 0, err
	}
	left := s.policy.MaxPerDay - used
	if left <= 0 {
		return 0, nil
	}

	held, err := s.byStatus(ctx, StatusHeld, 100)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, n := range held {
		if count >= left {
			// Предел исчерпан. Остальные ждут: повод не отменяется оттого,
			// что Бэрримор уже сегодня обращался.
			break
		}
		if n.DeliverAt.After(now) {
			continue
		}
		if err := s.setStatus(ctx, n.ID, StatusDelivered, "", now); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// MarkRead отмечает, что владелец увидел обращение.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	return s.setStatus(ctx, id, StatusRead, "", s.clock.Now())
}

// MarkStale снимает обращение, повод которого отпал.
//
// Нужен, чтобы Бэрримор не звал разбираться с тем, что владелец уже решил
// сам: увидеть уведомление о деле, которого больше нет, — мелкая, но обидная
// потеря доверия.
func (s *Service) MarkStale(ctx context.Context, dedupeKey, reason string) error {
	n, err := s.byDedupeKey(ctx, dedupeKey)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if n.Status == StatusRead || n.Status == StatusStale {
		return nil
	}
	return s.setStatus(ctx, n.ID, StatusStale, reason, s.clock.Now())
}

// Mute просит молчать о поводе или о предмете.
func (s *Service) Mute(kind, subjectID string) Policy {
	if kind != "" && !s.policy.Muted(kind, "") {
		s.policy.MutedKinds = append(s.policy.MutedKinds, kind)
	}
	if subjectID != "" && !s.policy.Muted("", subjectID) {
		s.policy.MutedSubjects = append(s.policy.MutedSubjects, subjectID)
	}
	return s.policy
}

// Unmute снимает запрет.
func (s *Service) Unmute(kind, subjectID string) Policy {
	s.policy.MutedKinds = without(s.policy.MutedKinds, kind)
	s.policy.MutedSubjects = without(s.policy.MutedSubjects, subjectID)
	return s.policy
}

func without(list []string, v string) []string {
	if v == "" {
		return list
	}
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// setStatus меняет судьбу обращения.
func (s *Service) setStatus(ctx context.Context, id, status, reason string, at time.Time) error {
	p := statusPayload{NoticeID: id, Status: status, At: at, Reason: reason}
	eventType := map[string]string{
		StatusDelivered: EvNoticeDelivered,
		StatusRead:      EvNoticeRead,
		StatusStale:     EvNoticeStale,
	}[status]
	if eventType == "" {
		return fmt.Errorf("initiative: неизвестное состояние обращения %q", status)
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: id, ExpectedRevision: event.AnyRevision,
			EventType: eventType, Actor: event.Actor{Type: event.ActorBarrymore},
			Payload: p,
		}); err != nil {
			return err
		}
		return applyStatus(ctx, tx, p)
	})
	return err
}

// Projections регистрирует проекции.
func (s *Service) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvNoticeCreated, projectNotice)
	reg.On(EvNoticeDelivered, projectStatus)
	reg.On(EvNoticeRead, projectStatus)
	reg.On(EvNoticeStale, projectStatus)
}

// Summary — что сейчас ждёт владельца.
type Summary struct {
	// Waiting — показанное и ещё не прочитанное.
	Waiting []Notice `json:"waiting"`
	// HeldCount — сколько ждёт своего часа: тихих часов или завтрашнего лимита.
	HeldCount int `json:"held_count"`
	// HeldReason объясняет, почему они ждут.
	HeldReason string `json:"held_reason,omitempty"`
	Policy     Policy `json:"policy"`
}

// Pending собирает то, что Бэрримор хочет сказать.
func (s *Service) Pending(ctx context.Context) (Summary, error) {
	out := Summary{Policy: s.policy, Waiting: []Notice{}}
	delivered, err := s.byStatus(ctx, StatusDelivered, 100)
	if err != nil {
		return out, err
	}
	out.Waiting = delivered

	held, err := s.byStatus(ctx, StatusHeld, 100)
	if err != nil {
		return out, err
	}
	out.HeldCount = len(held)
	if out.HeldCount > 0 {
		now := s.clock.Now()
		switch {
		case s.policy.Quiet(now):
			out.HeldReason = fmt.Sprintf(
				"ждут конца тихих часов: скажу после %02d:00", s.policy.QuietTo)
		default:
			out.HeldReason = fmt.Sprintf(
				"сегодня уже %d обращений — остальное подождёт до завтра", s.policy.MaxPerDay)
		}
	}
	return out, nil
}

// Reasons перечисляет поводы, о которых Бэрримор умеет сообщать.
//
// Нужен интерфейсу: владелец должен видеть, о чём его вообще могут побеспокоить,
// прежде чем решать, что заглушить.
func Reasons() []struct{ Kind, Label string } {
	return []struct{ Kind, Label string }{
		{KindOrderFinished, "поручение завершилось"},
		{KindChangesWaiting, "изменения ждут решения"},
		{KindEscalated, "Бэрримор не справился сам"},
		{KindApprovalWaiting, "поручение ждёт подтверждения"},
		{KindMemoryWaiting, "память ждёт решения"},
	}
}

// Label переводит повод на человеческий язык.
func Label(kind string) string {
	for _, r := range Reasons() {
		if r.Kind == kind {
			return r.Label
		}
	}
	return kind
}

// LevelLabel переводит важность.
func LevelLabel(level string) string {
	switch level {
	case LevelUrgent:
		return "нужно ваше решение"
	case LevelAttention:
		return "стоит знать"
	default:
		return "к сведению"
	}
}

// dedupeFor собирает ключ повторов.
func dedupeFor(kind, subject string, extra ...string) string {
	parts := append([]string{kind, subject}, extra...)
	return strings.Join(parts, ":")
}
