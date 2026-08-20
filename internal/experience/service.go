package experience

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/store"
)

// Service owns durable experience. Every meaningful write is journalled and
// projected in the same transaction, so rebuild-projections cannot erase it.
type Service struct {
	db      *store.DB
	journal *event.Journal
	clock   clock.Clock
}

func New(db *store.DB, journal *event.Journal, clk clock.Clock) *Service {
	return &Service{db: db, journal: journal, clock: clk}
}

func (s *Service) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}

func (s *Service) Begin(ctx context.Context, req StartRequest, actor event.Actor) (Episode, error) {
	if strings.TrimSpace(req.Goal) == "" {
		return Episode{}, errors.New("эпизод без цели не создаётся")
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorBarrymore}
	}
	now := s.now()
	initial, err := ensureJSON(req.InitialContext, `{}`)
	if err != nil {
		return Episode{}, fmt.Errorf("initial_context: %w", err)
	}
	ep := Episode{
		ID: ids.New(ids.Episode), Goal: strings.TrimSpace(req.Goal), Scope: strings.TrimSpace(req.Scope),
		ThreadID: req.ThreadID, ConversationID: req.ConversationID,
		Status: EpisodeOpen, InitialContext: initial,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamEpisode, StreamID: ep.ID, ExpectedRevision: 0,
			EventType: EvEpisodeStarted, Actor: actor, CorrelationID: req.ThreadID,
			Payload: ep,
		}); err != nil {
			return err
		}
		return applyEpisodeStarted(ctx, tx, ep)
	})
	return ep, err
}

func (s *Service) AddSource(ctx context.Context, episodeID string, src Source, actor event.Actor) (Source, error) {
	ep, err := s.Episode(ctx, episodeID)
	if err != nil {
		return Source{}, err
	}
	if ep.Status != EpisodeOpen {
		return Source{}, fmt.Errorf("эпизод %s уже завершён: новые основания требуют нового эпизода", episodeID)
	}
	if strings.TrimSpace(src.Kind) == "" || strings.TrimSpace(src.Evidence) == "" {
		return Source{}, errors.New("источник требует kind и evidence")
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorRuntime}
	}
	now := s.now()
	if src.Confidence == 0 {
		src.Confidence = 1
	} else if src.Confidence < 0 || src.Confidence > 1 {
		return Source{}, fmt.Errorf("confidence должен быть от 0 до 1, получено %.3f", src.Confidence)
	}
	if src.ObservedAt.IsZero() {
		src.ObservedAt = now
	}
	src.ID = ids.New(ids.Source)
	src.EpisodeID = episodeID
	src.Kind = strings.TrimSpace(src.Kind)
	src.Locator = strings.TrimSpace(src.Locator)
	src.Title = strings.TrimSpace(src.Title)
	src.Evidence = strings.TrimSpace(src.Evidence)
	src.CreatedAt = now

	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamEpisode, StreamID: episodeID, ExpectedRevision: event.AnyRevision,
			EventType: EvSourceRecorded, Actor: actor, CorrelationID: ep.ThreadID,
			Payload: src,
		}); err != nil {
			return err
		}
		return applySourceRecorded(ctx, tx, src)
	})
	return src, err
}

type completedPayload struct {
	EpisodeID    string          `json:"episode_id"`
	Outcome      string          `json:"outcome"`
	Result       string          `json:"result"`
	Verification json.RawMessage `json:"verification"`
	FinishedAt   time.Time       `json:"finished_at"`
}

func (s *Service) Complete(ctx context.Context, episodeID string, req CompleteRequest, actor event.Actor) (Episode, error) {
	ep, err := s.Episode(ctx, episodeID)
	if err != nil {
		return Episode{}, err
	}
	if ep.Status != EpisodeOpen {
		return Episode{}, fmt.Errorf("эпизод %s уже завершён", episodeID)
	}
	outcome := strings.TrimSpace(req.Outcome)
	switch outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomePartial, OutcomeUnknown:
	default:
		return Episode{}, fmt.Errorf("неизвестный исход эпизода %q", outcome)
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorBarrymore}
	}
	verification, err := ensureJSON(req.Verification, `{}`)
	if err != nil {
		return Episode{}, fmt.Errorf("verification: %w", err)
	}
	payload := completedPayload{
		EpisodeID: episodeID, Outcome: outcome, Result: strings.TrimSpace(req.Result),
		Verification: verification, FinishedAt: s.now(),
	}
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamEpisode, StreamID: episodeID, ExpectedRevision: event.AnyRevision,
			EventType: EvEpisodeCompleted, Actor: actor, CorrelationID: ep.ThreadID,
			Payload: payload,
		}); err != nil {
			return err
		}
		return applyEpisodeCompleted(ctx, tx, payload)
	})
	if err != nil {
		return Episode{}, err
	}
	return s.Episode(ctx, episodeID)
}

func (s *Service) SaveProcedure(ctx context.Context, p Procedure, actor event.Actor) (Procedure, error) {
	p.Intent = strings.TrimSpace(p.Intent)
	p.Title = strings.TrimSpace(p.Title)
	if p.Intent == "" || p.Title == "" {
		return Procedure{}, errors.New("процедура требует intent и title")
	}
	if len(p.Steps) == 0 {
		return Procedure{}, errors.New("процедура без шагов не сохраняется")
	}
	for i := range p.Steps {
		p.Steps[i].Capability = strings.TrimSpace(p.Steps[i].Capability)
		if p.Steps[i].Capability == "" {
			return Procedure{}, fmt.Errorf("шаг %d не называет capability", i+1)
		}
		args, err := ensureJSON(p.Steps[i].Args, `{}`)
		if err != nil {
			return Procedure{}, fmt.Errorf("шаг %d args: %w", i+1, err)
		}
		p.Steps[i].Args = args
	}
	for i := range p.Rollback {
		p.Rollback[i].Capability = strings.TrimSpace(p.Rollback[i].Capability)
		if p.Rollback[i].Capability == "" {
			return Procedure{}, fmt.Errorf("rollback-шаг %d не называет capability", i+1)
		}
		args, err := ensureJSON(p.Rollback[i].Args, `{}`)
		if err != nil {
			return Procedure{}, fmt.Errorf("rollback-шаг %d args: %w", i+1, err)
		}
		p.Rollback[i].Args = args
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorBarrymore}
	}
	now := s.now()
	isNew := p.ID == ""
	if isNew {
		p.ID = ids.New(ids.Procedure)
		p.CreatedAt = now
		p.Succeeded = 0
		p.Failed = 0
		p.LastUsedAt = nil
	} else {
		old, err := s.Procedure(ctx, p.ID)
		if err != nil {
			return Procedure{}, fmt.Errorf("обновление неизвестной процедуры %s: %w", p.ID, err)
		}
		p.CreatedAt = old.CreatedAt
		p.Succeeded = old.Succeeded
		p.Failed = old.Failed
		p.LastUsedAt = old.LastUsedAt
	}
	if p.RiskClass == "" {
		p.RiskClass = RiskReadOnly
	}
	switch p.RiskClass {
	case RiskReadOnly, RiskLocalChange, RiskWorkspaceChange, RiskRemoteChange, RiskDestructive:
	default:
		return Procedure{}, fmt.Errorf("неизвестный класс риска %q", p.RiskClass)
	}
	if p.Status == "" {
		p.Status = ProcedureActive
	}
	switch p.Status {
	case ProcedureActive, ProcedureStale, ProcedureRetired:
	default:
		return Procedure{}, fmt.Errorf("неизвестный статус процедуры %q", p.Status)
	}
	p.UpdatedAt = now
	expected := event.AnyRevision
	if isNew {
		expected = 0
	}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamProcedure, StreamID: p.ID, ExpectedRevision: expected,
			EventType: EvProcedureSaved, Actor: actor, CorrelationID: p.SourceEpisodeID,
			Payload: p,
		}); err != nil {
			return err
		}
		return applyProcedureSaved(ctx, tx, p)
	})
	return p, err
}

func (s *Service) AddArtifact(ctx context.Context, episodeID string, a Artifact, actor event.Actor) (Artifact, error) {
	ep, err := s.Episode(ctx, episodeID)
	if err != nil {
		return Artifact{}, err
	}
	if ep.Status != EpisodeOpen {
		return Artifact{}, fmt.Errorf("эпизод %s уже завершён: новый артефакт требует нового эпизода", episodeID)
	}
	a.Name = strings.TrimSpace(a.Name)
	a.Path = strings.TrimSpace(a.Path)
	if a.Name == "" || a.Path == "" {
		return Artifact{}, errors.New("артефакт требует name и path")
	}
	if a.Size < 0 {
		return Artifact{}, errors.New("размер артефакта не может быть отрицательным")
	}
	if a.Kind == "" {
		a.Kind = "file"
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorRuntime}
	}
	a.ID = ids.New(ids.Artifact)
	a.EpisodeID = episodeID
	a.CreatedAt = s.now()
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamEpisode, StreamID: episodeID, ExpectedRevision: event.AnyRevision,
			EventType: EvArtifactRecorded, Actor: actor, CorrelationID: ep.ThreadID,
			Payload: a,
		}); err != nil {
			return err
		}
		return applyArtifactRecorded(ctx, tx, a)
	})
	return a, err
}

func (s *Service) RecordFeedback(ctx context.Context, episodeID, value, note string, actor event.Actor) (Feedback, error) {
	ep, err := s.Episode(ctx, episodeID)
	if err != nil {
		return Feedback{}, err
	}
	if ep.Status != EpisodeCompleted {
		return Feedback{}, fmt.Errorf("оценивать можно только завершённый эпизод %s", episodeID)
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value != FeedbackLike && value != FeedbackDislike {
		return Feedback{}, errors.New("оценка должна быть like или dislike; отсутствие оценки остаётся отсутствием")
	}
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	if actor.Type != event.ActorPerson {
		return Feedback{}, fmt.Errorf("явная оценка результата принадлежит владельцу, actor=%s", actor.Type)
	}
	fb := Feedback{
		ID: ids.New(ids.Feedback), EpisodeID: episodeID, Value: value, Note: strings.TrimSpace(note),
		ActorType: actor.Type, ActorID: actor.ID, CreatedAt: s.now(),
	}
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamEpisode, StreamID: episodeID, ExpectedRevision: event.AnyRevision,
			EventType: EvFeedbackRecorded, Actor: actor, CorrelationID: ep.ThreadID,
			Payload: fb,
		}); err != nil {
			return err
		}
		return applyFeedbackRecorded(ctx, tx, fb)
	})
	return fb, err
}

func ensureJSON(v json.RawMessage, fallback string) (json.RawMessage, error) {
	if len(v) == 0 {
		return json.RawMessage(fallback), nil
	}
	if !json.Valid(v) {
		return nil, errors.New("невалидный JSON")
	}
	return v, nil
}
