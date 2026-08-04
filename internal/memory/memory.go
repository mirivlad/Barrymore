// Package memory хранит подтверждённое знание и кандидатов в него.
//
// 00_PRODUCT_VISION §9.2: значимые сведения не записываются в память скрытно.
// 04_MEMORY_AND_CONTINUITY §4: модель никогда не пишет MemoryItem напрямую.
// Путь один: наблюдение → кандидат → решение владельца → память.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
)

// ErrNotFound возвращается, когда записи нет.
var ErrNotFound = errors.New("запись памяти не найдена")

// Типы записей (02_DOMAIN_MODEL §4).
const (
	TypeFact         = "fact"
	TypePreference   = "preference"
	TypeDecision     = "decision"
	TypeEpisode      = "episode"
	TypeProcedure    = "procedure"
	TypeKnownFailure = "known_failure"
	TypeOpenQuestion = "open_question"
	TypeLesson       = "barrymore_lesson"
)

// Кто предложил запись.
const (
	// ProposedByOwner — владелец сказал это сам или прямо попросил запомнить.
	ProposedByOwner = "person"
	// ProposedByBarrymore — вывод Бэрримора из разговора.
	ProposedByBarrymore = "barrymore"
)

// Статусы кандидата.
const (
	StatusPending  = "pending"
	StatusAccepted = "accepted"
	StatusRejected = "rejected"
	StatusExpired  = "expired"
	StatusMerged   = "merged"
)

// Candidate — предложение записать в память.
type Candidate struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Content        string     `json:"content"`
	Reason         string     `json:"reason,omitempty"`
	ProposedBy     string     `json:"proposed_by"`
	ThreadID       string     `json:"thread_id,omitempty"`
	ConversationID string     `json:"conversation_id,omitempty"`
	MessageID      string     `json:"message_id,omitempty"`
	Sensitivity    string     `json:"sensitivity"`
	Confidence     float64    `json:"confidence"`
	Status         string     `json:"status"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecisionNote   string     `json:"decision_note,omitempty"`
	// AutoDecision объясняет, почему запись сделана автоматически либо
	// почему её вынесли на решение владельца.
	AutoDecision string    `json:"auto_decision,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Provenance — происхождение записи.
//
// Без него запись нельзя оспорить: владелец должен видеть, откуда она взялась.
type Provenance struct {
	Source         string `json:"source"`
	ProposedBy     string `json:"proposed_by"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
	AcceptedBy     string `json:"accepted_by,omitempty"`
}

// Item — подтверждённая запись памяти.
type Item struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Content      string     `json:"content"`
	Provenance   Provenance `json:"provenance"`
	CandidateID  string     `json:"candidate_id,omitempty"`
	ThreadID     string     `json:"thread_id,omitempty"`
	Sensitivity  string     `json:"sensitivity"`
	Confidence   float64    `json:"confidence"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokeReason string     `json:"revoke_reason,omitempty"`
	// ForgottenAt отмечает, что содержание удалено владельцем.
	ForgottenAt *time.Time `json:"forgotten_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Active сообщает, действует ли запись.
func (i Item) Active() bool { return i.RevokedAt == nil && i.ForgottenAt == nil }

// Forgotten сообщает, что содержание удалено владельцем.
func (i Item) Forgotten() bool { return i.ForgottenAt != nil }

// Типы событий памяти (08_API_AND_EVENTS §4).
const (
	EvCandidateProposed = "memory.candidate.proposed"
	EvCandidateAccepted = "memory.candidate.accepted"
	EvCandidateRejected = "memory.candidate.rejected"
	EvCreated           = "memory.created"
	EvRevoked           = "memory.revoked"
	EvForgotten         = "memory.forgotten"
)

// StreamType — тип потока событий памяти.
const StreamType = "memory"

// ProjectionTables — таблицы проекций памяти.
var ProjectionTables = []string{"memory_candidates", "memory_items"}

// Service ведёт память.
type Service struct {
	db      *store.DB
	journal *event.Journal
	clock   clock.Clock
	policy  Policy
}

// NewService создаёт сервис памяти.
func NewService(db *store.DB, j *event.Journal, clk clock.Clock, policy Policy) *Service {
	if policy.Mode == "" {
		policy = DefaultPolicy()
	}
	return &Service{db: db, journal: j, clock: clk, policy: policy}
}

// Policy возвращает действующий режим памяти.
func (s *Service) Policy() Policy { return s.policy }

// ProposeRequest — предложение запомнить.
type ProposeRequest struct {
	Type           string
	Content        string
	Reason         string
	ProposedBy     string
	ThreadID       string
	ConversationID string
	MessageID      string
	Sensitivity    string
	Confidence     float64
}

// ProposeResult — что стало с предложением.
type ProposeResult struct {
	Candidate Candidate `json:"candidate"`
	// Item заполнен, если Бэрримор записал сведение сам.
	Item *Item `json:"item,omitempty"`
	// Auto сообщает, была ли запись автоматической.
	Auto bool `json:"auto"`
	// Reason объясняет решение владельцу.
	Reason string `json:"reason"`
}

// Propose создаёт кандидата и, если политика позволяет, сразу записывает его.
//
// Автоматическая запись не является скрытой: у неё есть видимое основание,
// она показана в разделе памяти и в любой момент может быть удалена владельцем.
func (s *Service) Propose(ctx context.Context, req ProposeRequest) (ProposeResult, error) {
	c, err := s.propose(ctx, req)
	if err != nil {
		return ProposeResult{}, err
	}
	decision := s.policy.Decide(c)
	if err := s.saveDecisionReason(ctx, c.ID, decision.Reason); err != nil {
		return ProposeResult{}, err
	}
	c.AutoDecision = decision.Reason

	if !decision.Auto {
		return ProposeResult{Candidate: c, Reason: decision.Reason}, nil
	}
	item, err := s.Accept(ctx, c.ID, "barrymore", decision.Reason)
	if err != nil {
		return ProposeResult{}, err
	}
	c.Status = StatusAccepted
	return ProposeResult{Candidate: c, Item: &item, Auto: true, Reason: decision.Reason}, nil
}

// Remember записывает то, что владелец прямо попросил запомнить.
//
// Просьба владельца и есть решение: отдельного подтверждения не требуется.
func (s *Service) Remember(ctx context.Context, req ProposeRequest) (Item, error) {
	req.ProposedBy = ProposedByOwner
	if req.Confidence == 0 {
		req.Confidence = 1
	}
	if req.Reason == "" {
		req.Reason = "владелец попросил это запомнить"
	}
	c, err := s.propose(ctx, req)
	if err != nil {
		return Item{}, err
	}
	if err := s.saveDecisionReason(ctx, c.ID, "владелец попросил это запомнить"); err != nil {
		return Item{}, err
	}
	return s.Accept(ctx, c.ID, "owner", "прямая просьба владельца")
}

func (s *Service) saveDecisionReason(ctx context.Context, candidateID, reason string) error {
	_, err := s.db.Writer().ExecContext(ctx,
		`UPDATE memory_candidates SET auto_decision = ? WHERE id = ?`, reason, candidateID)
	if err != nil {
		return fmt.Errorf("сохранение основания решения %s: %w", candidateID, err)
	}
	return nil
}

func (s *Service) propose(ctx context.Context, req ProposeRequest) (Candidate, error) {
	if req.Content == "" {
		return Candidate{}, fmt.Errorf("кандидат без содержания")
	}
	if req.Type == "" {
		req.Type = TypeFact
	}
	if req.Sensitivity == "" {
		req.Sensitivity = "normal"
	}
	if req.Confidence == 0 {
		// Модель не назвала уверенность. Подставлять середину нельзя: это
		// выдало бы отсутствие оценки за оценку. Без неё запись не может
		// быть автоматической.
		req.Confidence = 0
	}
	if req.ProposedBy == "" {
		req.ProposedBy = "barrymore"
	}

	c := Candidate{
		ID: ids.New("mem"), Type: req.Type, Content: req.Content, Reason: req.Reason,
		ProposedBy: req.ProposedBy, ThreadID: req.ThreadID,
		ConversationID: req.ConversationID, MessageID: req.MessageID,
		Sensitivity: req.Sensitivity, Confidence: req.Confidence,
		Status: StatusPending, CreatedAt: s.clock.Now(),
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: c.ID, ExpectedRevision: 0,
			EventType: EvCandidateProposed,
			Actor:     event.Actor{Type: event.ActorBarrymore}, Payload: c,
		}); err != nil {
			return err
		}
		return applyCandidate(ctx, tx, c)
	})
	if err != nil {
		return Candidate{}, err
	}
	return c, nil
}

// Accept превращает кандидата в подтверждённую запись.
func (s *Service) Accept(ctx context.Context, candidateID, decidedBy, note string) (Item, error) {
	var item Item
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		c, err := candidateByID(ctx, tx, candidateID)
		if err != nil {
			return err
		}
		if c.Status != StatusPending {
			return fmt.Errorf("кандидат %s уже в состоянии %s", candidateID, c.Status)
		}
		now := s.clock.Now()

		decision := decisionPayload{
			ID: candidateID, Status: StatusAccepted, At: now,
			DecidedBy: decidedBy, Note: note,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: candidateID, ExpectedRevision: event.AnyRevision,
			EventType: EvCandidateAccepted,
			Actor:     event.Actor{Type: event.ActorPerson, ID: decidedBy}, Payload: decision,
		}); err != nil {
			return err
		}
		if err := applyDecision(ctx, tx, decision); err != nil {
			return err
		}

		item = Item{
			ID: ids.New("mi"), Type: c.Type, Content: c.Content,
			Provenance: Provenance{
				Source: "conversation", ProposedBy: c.ProposedBy,
				ConversationID: c.ConversationID, MessageID: c.MessageID,
				Reason: c.Reason, AcceptedBy: decidedBy,
			},
			CandidateID: c.ID, ThreadID: c.ThreadID, Sensitivity: c.Sensitivity,
			Confidence: c.Confidence, ValidFrom: now, CreatedAt: now,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: candidateID, ExpectedRevision: event.AnyRevision,
			EventType: EvCreated,
			Actor:     event.Actor{Type: event.ActorPerson, ID: decidedBy}, Payload: item,
		}); err != nil {
			return err
		}
		return applyItem(ctx, tx, item)
	})
	if err != nil {
		return Item{}, err
	}
	return item, nil
}

// Reject отклоняет кандидата.
func (s *Service) Reject(ctx context.Context, candidateID, decidedBy, note string) error {
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		c, err := candidateByID(ctx, tx, candidateID)
		if err != nil {
			return err
		}
		if c.Status != StatusPending {
			return fmt.Errorf("кандидат %s уже в состоянии %s", candidateID, c.Status)
		}
		decision := decisionPayload{
			ID: candidateID, Status: StatusRejected, At: s.clock.Now(),
			DecidedBy: decidedBy, Note: note,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: candidateID, ExpectedRevision: event.AnyRevision,
			EventType: EvCandidateRejected,
			Actor:     event.Actor{Type: event.ActorPerson, ID: decidedBy}, Payload: decision,
		}); err != nil {
			return err
		}
		return applyDecision(ctx, tx, decision)
	})
	return err
}

// Forget удаляет содержание записи по требованию владельца.
//
// Событие из журнала физически не убирается: это нарушило бы его целостность
// (06_SECURITY §11). Вместо этого содержание в памяти затирается, а запись
// остаётся надгробием — видно, что нечто было и было удалено.
func (s *Service) Forget(ctx context.Context, itemID, reason string) error {
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		p := revokePayload{ID: itemID, At: s.clock.Now(), Reason: reason}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: itemID, ExpectedRevision: event.AnyRevision,
			EventType: EvForgotten, Actor: event.Actor{Type: event.ActorPerson}, Payload: p,
		}); err != nil {
			return err
		}
		return applyForget(ctx, tx, p)
	})
	return err
}

// Revoke отзывает подтверждённую запись.
//
// Запись не удаляется физически: остаётся видимый след того, что она была
// и была отозвана (04_MEMORY_AND_CONTINUITY §6).
func (s *Service) Revoke(ctx context.Context, itemID, reason string) error {
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		p := revokePayload{ID: itemID, At: s.clock.Now(), Reason: reason}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: itemID, ExpectedRevision: event.AnyRevision,
			EventType: EvRevoked, Actor: event.Actor{Type: event.ActorPerson}, Payload: p,
		}); err != nil {
			return err
		}
		return applyRevoke(ctx, tx, p)
	})
	return err
}

// Projections регистрирует проекторы памяти.
func (s *Service) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvCandidateProposed, projectCandidate)
	reg.On(EvCandidateAccepted, projectDecision)
	reg.On(EvCandidateRejected, projectDecision)
	reg.On(EvCreated, projectItem)
	reg.On(EvRevoked, projectRevoke)
	reg.On(EvForgotten, projectForget)
}

func marshalProvenance(p Provenance) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}
