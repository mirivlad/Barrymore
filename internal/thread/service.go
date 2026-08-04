package thread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
)

// ErrNotFound возвращается, когда нить или её часть отсутствует.
var ErrNotFound = errors.New("нить не найдена")

// Service управляет жизненным циклом нитей.
type Service struct {
	db      *store.DB
	journal *event.Journal
	clock   clock.Clock
}

// NewService создаёт сервис нитей.
func NewService(db *store.DB, j *event.Journal, clk clock.Clock) *Service {
	return &Service{db: db, journal: j, clock: clk}
}

// CreateRequest — создание нити.
type CreateRequest struct {
	Title       string
	Kind        string
	State       string
	Summary     string
	Origin      string
	Importance  string
	Sensitivity string
	WorkspaceID string
	Actor       event.Actor
}

// Create создаёт нить.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Thread, error) {
	if req.Title == "" {
		return Thread{}, fmt.Errorf("у нити должно быть название")
	}
	if req.Kind == "" {
		req.Kind = KindConversation
	}
	if err := ValidateKind(req.Kind); err != nil {
		return Thread{}, err
	}
	if req.State == "" {
		req.State = StateActive
	}
	if err := ValidateState(req.State); err != nil {
		return Thread{}, err
	}
	if req.Importance == "" {
		req.Importance = "normal"
	}
	if req.Sensitivity == "" {
		req.Sensitivity = "normal"
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorPerson}
	}

	now := s.clock.Now()
	th := Thread{
		ID: ids.New(ids.Thread), Title: req.Title, Kind: req.Kind, State: req.State,
		Summary: req.Summary, Origin: req.Origin, Importance: req.Importance,
		Sensitivity: req.Sensitivity, WorkspaceID: req.WorkspaceID,
		CreatedAt: now, UpdatedAt: now, Revision: 1,
	}

	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: th.ID, ExpectedRevision: 0,
			EventType: EvCreated, Actor: req.Actor, Payload: th,
		}); err != nil {
			return err
		}
		return applyCreated(ctx, tx, th)
	})
	if err != nil {
		return Thread{}, err
	}
	return th, nil
}

// UpdateRequest — изменение полей нити.
//
// Nil-поле означает «не менять». Пустая строка — законное новое значение.
type UpdateRequest struct {
	Title        *string
	Summary      *string
	Origin       *string
	Importance   *string
	Sensitivity  *string
	WorkspaceID  *string
	NextReviewAt *time.Time
	MutedUntil   *time.Time
	// ExpectedRevision реализует оптимистичную конкурентность;
	// event.AnyRevision отключает проверку.
	ExpectedRevision int64
	Actor            event.Actor
}

type updatePayload struct {
	ID           string     `json:"id"`
	Title        *string    `json:"title,omitempty"`
	Summary      *string    `json:"summary,omitempty"`
	Origin       *string    `json:"origin,omitempty"`
	Importance   *string    `json:"importance,omitempty"`
	Sensitivity  *string    `json:"sensitivity,omitempty"`
	WorkspaceID  *string    `json:"workspace_id,omitempty"`
	NextReviewAt *time.Time `json:"next_review_at,omitempty"`
	MutedUntil   *time.Time `json:"muted_until,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Revision     int64      `json:"revision"`
}

// Update изменяет нить.
func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (Thread, error) {
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorPerson}
	}
	now := s.clock.Now()
	var out Thread

	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := loadThread(ctx, tx, id); err != nil {
			return err
		}
		p := updatePayload{
			ID: id, Title: req.Title, Summary: req.Summary, Origin: req.Origin,
			Importance: req.Importance, Sensitivity: req.Sensitivity,
			WorkspaceID: req.WorkspaceID, NextReviewAt: req.NextReviewAt,
			MutedUntil: req.MutedUntil, UpdatedAt: now,
		}
		env, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: id, ExpectedRevision: req.ExpectedRevision,
			EventType: EvUpdated, Actor: req.Actor, Payload: p,
		})
		if err != nil {
			return err
		}
		p.Revision = env.StreamRevision
		if err := applyUpdated(ctx, tx, p); err != nil {
			return err
		}
		out, err = loadThread(ctx, tx, id)
		return err
	})
	if err != nil {
		return Thread{}, err
	}
	return out, nil
}

type stateChangePayload struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
	Revision  int64     `json:"revision"`
}

// ChangeState переводит нить в новое состояние.
func (s *Service) ChangeState(ctx context.Context, id, state, reason string, expectedRevision int64, actor event.Actor) (Thread, error) {
	if err := ValidateState(state); err != nil {
		return Thread{}, err
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	eventType := EvStateChanged
	if state == StateReleased {
		// Отпускание нити — осознанное решение, а не рядовая смена статуса.
		eventType = EvReleased
	}

	var out Thread
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := loadThread(ctx, tx, id); err != nil {
			return err
		}
		p := stateChangePayload{ID: id, State: state, Reason: reason, ChangedAt: s.clock.Now()}
		env, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: id, ExpectedRevision: expectedRevision,
			EventType: eventType, Actor: actor, Payload: p,
		})
		if err != nil {
			return err
		}
		p.Revision = env.StreamRevision
		if err := applyStateChange(ctx, tx, p); err != nil {
			return err
		}
		out, err = loadThread(ctx, tx, id)
		return err
	})
	if err != nil {
		return Thread{}, err
	}
	return out, nil
}

// PositionRequest — обновление позиции по нити.
type PositionRequest struct {
	Owner      string
	Statement  string
	Confidence float64
	Basis      string
	Actor      event.Actor
}

// SetPosition записывает новую позицию, закрывая предыдущую того же владельца.
//
// Позиции сторон не сводятся к одной: разногласие сохраняется как факт.
func (s *Service) SetPosition(ctx context.Context, threadID string, req PositionRequest) (Position, error) {
	if err := ValidateOwner(req.Owner); err != nil {
		return Position{}, err
	}
	if req.Statement == "" {
		return Position{}, fmt.Errorf("позиция без формулировки")
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: req.Owner}
	}

	now := s.clock.Now()
	pos := Position{
		ID: ids.New(ids.Position), ThreadID: threadID, Owner: req.Owner,
		Statement: req.Statement, Confidence: req.Confidence, Basis: req.Basis,
		ValidFrom: now, CreatedAt: now,
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := loadThread(ctx, tx, threadID); err != nil {
			return err
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: threadID, ExpectedRevision: event.AnyRevision,
			EventType: EvPositionUpdated, Actor: req.Actor, Payload: pos,
		}); err != nil {
			return err
		}
		return applyPosition(ctx, tx, pos)
	})
	if err != nil {
		return Position{}, err
	}
	return pos, nil
}

// DecisionRequest — фиксация решения.
type DecisionRequest struct {
	Statement    string
	DecidedBy    string
	Rationale    string
	Alternatives []string
	Consequences string
	ReviewAt     *time.Time
	Actor        event.Actor
}

// RecordDecision фиксирует решение по нити.
func (s *Service) RecordDecision(ctx context.Context, threadID string, req DecisionRequest) (Decision, error) {
	if req.Statement == "" {
		return Decision{}, fmt.Errorf("решение без формулировки")
	}
	if req.DecidedBy == "" {
		req.DecidedBy = OwnerPerson
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorPerson}
	}
	d := Decision{
		ID: ids.New(ids.Decision), ThreadID: threadID, Statement: req.Statement,
		DecidedBy: req.DecidedBy, Rationale: req.Rationale, Alternatives: req.Alternatives,
		Consequences: req.Consequences, ReviewAt: req.ReviewAt, DecidedAt: s.clock.Now(),
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := loadThread(ctx, tx, threadID); err != nil {
			return err
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: threadID, ExpectedRevision: event.AnyRevision,
			EventType: EvDecisionRecorded, Actor: req.Actor, Payload: d,
		}); err != nil {
			return err
		}
		return applyDecision(ctx, tx, d)
	})
	if err != nil {
		return Decision{}, err
	}
	return d, nil
}

// OpenQuestion фиксирует вопрос, который не следует превращать в факт или задачу.
func (s *Service) OpenQuestion(ctx context.Context, threadID, question, askedBy string, actor event.Actor) (Question, error) {
	if question == "" {
		return Question{}, fmt.Errorf("пустой вопрос")
	}
	if askedBy == "" {
		askedBy = OwnerPerson
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	q := Question{
		ID: ids.New(ids.Question), ThreadID: threadID, Question: question,
		AskedBy: askedBy, Status: QuestionOpen, OpenedAt: s.clock.Now(),
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := loadThread(ctx, tx, threadID); err != nil {
			return err
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: threadID, ExpectedRevision: event.AnyRevision,
			EventType: EvQuestionOpened, Actor: actor, Payload: q,
		}); err != nil {
			return err
		}
		return applyQuestionOpened(ctx, tx, q)
	})
	if err != nil {
		return Question{}, err
	}
	return q, nil
}

type questionClosePayload struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"thread_id"`
	Status   string    `json:"status"`
	Answer   string    `json:"answer,omitempty"`
	ClosedAt time.Time `json:"closed_at"`
}

// CloseQuestion закрывает вопрос ответом или отказом от него.
func (s *Service) CloseQuestion(ctx context.Context, threadID, questionID, status, answer string, actor event.Actor) error {
	if status != QuestionAnswered && status != QuestionDropped {
		return fmt.Errorf("недопустимый статус закрытия вопроса %q", status)
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		p := questionClosePayload{
			ID: questionID, ThreadID: threadID, Status: status,
			Answer: answer, ClosedAt: s.clock.Now(),
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: threadID, ExpectedRevision: event.AnyRevision,
			EventType: EvQuestionClosed, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyQuestionClosed(ctx, tx, p)
	})
	return err
}

// Link связывает две нити.
func (s *Service) Link(ctx context.Context, fromID, toID, kind, note string, actor event.Actor) (Link, error) {
	if err := ValidateLinkKind(kind); err != nil {
		return Link{}, err
	}
	if fromID == toID {
		return Link{}, fmt.Errorf("нить нельзя связать саму с собой")
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	l := Link{
		ID: ids.New(ids.Link), FromID: fromID, ToID: toID, Kind: kind,
		Note: note, CreatedAt: s.clock.Now(),
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := loadThread(ctx, tx, fromID); err != nil {
			return err
		}
		if _, err := loadThread(ctx, tx, toID); err != nil {
			return err
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: fromID, ExpectedRevision: event.AnyRevision,
			EventType: EvLinked, Actor: actor, Payload: l,
		}); err != nil {
			return err
		}
		return applyLink(ctx, tx, l)
	})
	if err != nil {
		return Link{}, err
	}
	return l, nil
}

// TouchActivity отмечает значимую активность по нити.
func (s *Service) TouchActivity(ctx context.Context, threadID string) error {
	now := s.clock.Now()
	_, err := s.db.Writer().ExecContext(ctx,
		`UPDATE threads SET last_meaningful_activity_at = ?, updated_at = ? WHERE id = ?`,
		ts(now), ts(now), threadID)
	if err != nil {
		return fmt.Errorf("отметка активности нити %s: %w", threadID, err)
	}
	return nil
}

// Projections регистрирует проекторы нитей.
func (s *Service) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvCreated, projectCreated)
	reg.On(EvUpdated, projectUpdated)
	reg.On(EvStateChanged, projectStateChange)
	reg.On(EvReleased, projectStateChange)
	reg.On(EvPositionUpdated, projectPosition)
	reg.On(EvDecisionRecorded, projectDecision)
	reg.On(EvQuestionOpened, projectQuestionOpened)
	reg.On(EvQuestionClosed, projectQuestionClosed)
	reg.On(EvLinked, projectLink)
}
