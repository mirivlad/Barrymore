package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
)

// PolicyGate проверяет допустимость действия. Реализация живёт в internal/policy.
//
// Runtime знает только то, что реакция и probe могут быть запрещены и что
// отказ фиксируется. Расширить себе полномочия ради устранения расхождения
// нижний уровень не может (03_SYSTEM_ARCHITECTURE §3).
type PolicyGate interface {
	Check(ctx context.Context, req PolicyRequest) PolicyResult
}

// PolicyRequest — запрос на действие.
type PolicyRequest struct {
	ActionClass string
	SubjectType string
	SubjectID   string
	Actor       event.Actor
	Detail      string
}

// PolicyResult — решение политики.
type PolicyResult struct {
	Allowed bool
	Rule    string
	Reason  string
}

// AllowAll — политика по умолчанию для чтения и локальной диагностики.
type AllowAll struct{}

// Check разрешает действие.
func (AllowAll) Check(context.Context, PolicyRequest) PolicyResult {
	return PolicyResult{Allowed: true, Rule: "default.allow"}
}

// Runtime — предиктивный контур.
type Runtime struct {
	db       *store.DB
	journal  *event.Journal
	clock    clock.Clock
	kinds    *Kinds
	reflexes *Reflexes
	policy   PolicyGate
	log      *slog.Logger

	// maxBatch ограничивает число ожиданий, обрабатываемых за один тик.
	maxBatch int
}

// Config — параметры создания Runtime.
type Config struct {
	DB       *store.DB
	Journal  *event.Journal
	Clock    clock.Clock
	Kinds    *Kinds
	Reflexes *Reflexes
	Policy   PolicyGate
	Logger   *slog.Logger
	MaxBatch int
}

// New создаёт предиктивный контур.
func New(cfg Config) *Runtime {
	if cfg.Kinds == nil {
		cfg.Kinds = NewKinds()
	}
	if cfg.Reflexes == nil {
		cfg.Reflexes = NewReflexes()
	}
	if cfg.Policy == nil {
		cfg.Policy = AllowAll{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = 200
	}
	return &Runtime{
		db: cfg.DB, journal: cfg.Journal, clock: cfg.Clock, kinds: cfg.Kinds,
		reflexes: cfg.Reflexes, policy: cfg.Policy, log: cfg.Logger, maxBatch: cfg.MaxBatch,
	}
}

// Kinds возвращает реестр видов ожиданий.
func (r *Runtime) Kinds() *Kinds { return r.kinds }

// Reflexes возвращает реестр локальных реакций.
func (r *Runtime) Reflexes() *Reflexes { return r.reflexes }

// Now возвращает текущее время runtime.
func (r *Runtime) Now() time.Time { return r.clock.Now() }

// ObservationRequest — намерение записать наблюдение.
type ObservationRequest struct {
	Kind          string
	SubjectType   string
	SubjectID     string
	ObservedAt    time.Time
	Source        string
	SourceQuality string
	Confidence    float64
	// DedupeKey защищает от повторной записи одного и того же наблюдения.
	DedupeKey string
	Payload   any
	Actor     event.Actor
	// CorrelationID связывает наблюдение с породившей его работой.
	CorrelationID string
}

// RecordObservation записывает наблюдение.
//
// Возвращает записанное наблюдение; при совпадении DedupeKey возвращается ранее
// сохранённое, новое событие не создаётся.
func (r *Runtime) RecordObservation(ctx context.Context, req ObservationRequest) (Observation, error) {
	var out Observation
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		var err error
		out, err = r.recordObservationTx(ctx, tx, w, req)
		return err
	})
	return out, err
}

func (r *Runtime) recordObservationTx(ctx context.Context, tx *sql.Tx, w *event.TxWriter, req ObservationRequest) (Observation, error) {
	if req.Kind == "" || req.SubjectType == "" || req.SubjectID == "" {
		return Observation{}, fmt.Errorf("runtime: неполное наблюдение (%q/%q/%q)",
			req.Kind, req.SubjectType, req.SubjectID)
	}
	if req.DedupeKey != "" {
		existing, found, err := observationByDedupe(ctx, tx, req.DedupeKey)
		if err != nil {
			return Observation{}, err
		}
		if found {
			return existing, nil
		}
	}

	now := r.clock.Now()
	if req.ObservedAt.IsZero() {
		req.ObservedAt = now
	}
	if req.SourceQuality == "" {
		req.SourceQuality = QualityDirect
	}
	if req.Confidence == 0 {
		req.Confidence = confidenceFor(req.SourceQuality)
	}
	if req.Source == "" {
		req.Source = "runtime"
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorRuntime}
	}

	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return Observation{}, fmt.Errorf("runtime: сериализация наблюдения %s: %w", req.Kind, err)
	}
	if req.Payload == nil {
		payload = []byte("{}")
	}

	obs := Observation{
		ID:            ids.New(ids.Observation),
		Kind:          req.Kind,
		SubjectType:   req.SubjectType,
		SubjectID:     req.SubjectID,
		ObservedAt:    req.ObservedAt.UTC(),
		RecordedAt:    now,
		Source:        req.Source,
		SourceQuality: req.SourceQuality,
		Confidence:    req.Confidence,
		DedupeKey:     req.DedupeKey,
		Payload:       payload,
	}

	env, err := w.Append(ctx, event.Request{
		StreamType:       req.SubjectType,
		StreamID:         req.SubjectID,
		ExpectedRevision: event.AnyRevision,
		EventType:        EvObservationRecorded,
		Actor:            req.Actor,
		CorrelationID:    req.CorrelationID,
		Payload:          obs,
	})
	if err != nil {
		return Observation{}, err
	}
	obs.EventSeq = env.Seq
	if err := applyObservation(ctx, tx, obs, env.Seq); err != nil {
		return Observation{}, err
	}
	// Новое наблюдение о субъекте будит его ожидания: контур event-driven,
	// а не опрос по таймеру. next_check_at — подсказка планировщика, поэтому
	// пишется напрямую и не порождает события (см. комментарий в projection.go).
	if err := wakeExpectations(ctx, tx, obs.SubjectType, obs.SubjectID, now); err != nil {
		return Observation{}, err
	}
	return obs, nil
}

// confidenceFor задаёт уверенность по умолчанию исходя из качества источника.
func confidenceFor(quality string) float64 {
	switch quality {
	case QualityDirect:
		return 1.0
	case QualityDerived:
		return 0.7
	case QualityReported:
		// Сообщение внешней стороны не является доказательством.
		return 0.5
	default:
		return 0.5
	}
}

// SnapshotRequest — обновление снимка состояния.
type SnapshotRequest struct {
	Scope      string
	Status     string
	Confidence float64
	ObservedAt time.Time
	ValidUntil *time.Time
	Source     string
	Reason     string
	Payload    any
	Actor      event.Actor
}

// UpdateSnapshot записывает новую версию снимка.
//
// Прошлые версии не удаляются: сравнение «что мы думали раньше» является частью
// аудита, а не мусором.
func (r *Runtime) UpdateSnapshot(ctx context.Context, req SnapshotRequest) (Snapshot, error) {
	var out Snapshot
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		var err error
		out, err = r.updateSnapshotTx(ctx, tx, w, req)
		return err
	})
	return out, err
}

func (r *Runtime) updateSnapshotTx(ctx context.Context, tx *sql.Tx, w *event.TxWriter, req SnapshotRequest) (Snapshot, error) {
	if req.Scope == "" || req.Status == "" {
		return Snapshot{}, fmt.Errorf("runtime: снимок без scope или status")
	}
	now := r.clock.Now()
	if req.ObservedAt.IsZero() {
		req.ObservedAt = now
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorRuntime}
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return Snapshot{}, fmt.Errorf("runtime: сериализация снимка %s: %w", req.Scope, err)
	}
	if req.Payload == nil {
		payload = []byte("{}")
	}

	snap := Snapshot{
		ID:         ids.New(ids.Snapshot),
		Scope:      req.Scope,
		Status:     req.Status,
		Confidence: req.Confidence,
		ObservedAt: req.ObservedAt.UTC(),
		ValidUntil: req.ValidUntil,
		Source:     req.Source,
		Reason:     req.Reason,
		Payload:    payload,
	}
	if _, err := w.Append(ctx, event.Request{
		StreamType:       SubjectSystem,
		StreamID:         req.Scope,
		ExpectedRevision: event.AnyRevision,
		EventType:        EvSystemStateUpdated,
		Actor:            req.Actor,
		Payload:          snap,
	}); err != nil {
		return Snapshot{}, err
	}
	if err := applySnapshot(ctx, tx, snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// LatestSnapshot возвращает последний снимок в области.
func (r *Runtime) LatestSnapshot(ctx context.Context, scope string) (Snapshot, bool, error) {
	row := r.db.Reader().QueryRowContext(ctx, selectSnapshotColumns+
		` WHERE scope = ? ORDER BY observed_at DESC, id DESC LIMIT 1`, scope)
	snap, err := scanSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("runtime: чтение снимка %s: %w", scope, err)
	}
	return snap, true, nil
}

// ExpectationRequest — намерение создать ожидание.
type ExpectationRequest struct {
	SubjectType      string
	SubjectID        string
	Kind             string
	Params           any
	Basis            string
	Confidence       float64
	SeverityIfMissed string
	WindowFrom       time.Time
	WindowUntil      *time.Time
	CheckInterval    time.Duration
	FirstCheckAfter  time.Duration
	ProbePolicy      string
	ReactionPolicy   string
	Actor            event.Actor
	CorrelationID    string
}

// CreateExpectation регистрирует ожидание.
func (r *Runtime) CreateExpectation(ctx context.Context, req ExpectationRequest) (Expectation, error) {
	var out Expectation
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		var err error
		out, err = r.createExpectationTx(ctx, tx, w, req)
		return err
	})
	return out, err
}

func (r *Runtime) createExpectationTx(ctx context.Context, tx *sql.Tx, w *event.TxWriter, req ExpectationRequest) (Expectation, error) {
	if _, ok := r.kinds.m[req.Kind]; !ok {
		return Expectation{}, fmt.Errorf("runtime: вид ожидания %q не зарегистрирован; "+
			"известны: %v", req.Kind, r.kinds.Names())
	}
	now := r.clock.Now()
	if req.WindowFrom.IsZero() {
		req.WindowFrom = now
	}
	if req.SeverityIfMissed == "" {
		req.SeverityIfMissed = SeverityWarning
	}
	if req.Confidence == 0 {
		req.Confidence = 0.8
	}
	if req.Actor.Type == "" {
		req.Actor = event.Actor{Type: event.ActorRuntime}
	}
	params, err := MarshalParams(req.Params)
	if err != nil {
		return Expectation{}, err
	}

	next := now
	if req.FirstCheckAfter > 0 {
		next = now.Add(req.FirstCheckAfter)
	} else if req.CheckInterval > 0 {
		next = now.Add(req.CheckInterval)
	}

	exp := Expectation{
		ID:               ids.New(ids.Expectation),
		SubjectType:      req.SubjectType,
		SubjectID:        req.SubjectID,
		Kind:             req.Kind,
		Params:           params,
		Basis:            req.Basis,
		Confidence:       req.Confidence,
		SeverityIfMissed: req.SeverityIfMissed,
		WindowFrom:       req.WindowFrom.UTC(),
		WindowUntil:      req.WindowUntil,
		NextCheckAt:      &next,
		CheckInterval:    req.CheckInterval,
		ProbePolicy:      req.ProbePolicy,
		ReactionPolicy:   req.ReactionPolicy,
		Status:           ExpectationPending,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if _, err := w.Append(ctx, event.Request{
		StreamType:       req.SubjectType,
		StreamID:         req.SubjectID,
		ExpectedRevision: event.AnyRevision,
		EventType:        EvExpectationCreated,
		Actor:            req.Actor,
		CorrelationID:    req.CorrelationID,
		Payload:          exp,
	}); err != nil {
		return Expectation{}, err
	}
	if err := applyExpectationCreated(ctx, tx, exp); err != nil {
		return Expectation{}, err
	}
	return exp, nil
}

// CancelExpectation снимает ожидание, ставшее неприменимым.
func (r *Runtime) CancelExpectation(ctx context.Context, id, reason string) error {
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		exp, err := expectationByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if exp.Status != ExpectationPending {
			return nil
		}
		p := expectationStatusPayload{ID: id, At: r.clock.Now(), Reason: reason, Status: ExpectationCancelled}
		if _, err := w.Append(ctx, event.Request{
			StreamType: exp.SubjectType, StreamID: exp.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvExpectationCancelled,
			Actor:            event.Actor{Type: event.ActorRuntime},
			Payload:          p,
		}); err != nil {
			return err
		}
		return applyExpectationStatus(ctx, tx, p)
	})
	return err
}

// TickResult — итог одного прохода планировщика.
type TickResult struct {
	Checked       int
	Satisfied     int
	Expired       int
	Discrepancies int
	Reflexes      int
	Escalations   int
}

// Tick выполняет один проход: проверяет просроченные ожидания и реагирует.
//
// ADR 0009: это единственная точка, где ожидания превращаются в расхождения.
// Проход детерминирован и не требует LLM.
func (r *Runtime) Tick(ctx context.Context) (TickResult, error) {
	var res TickResult
	now := r.clock.Now()

	due, err := r.dueExpectations(ctx, now)
	if err != nil {
		return res, err
	}
	for _, exp := range due {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		obs, err := r.observationsFor(ctx, exp.SubjectType, exp.SubjectID, exp.WindowFrom)
		if err != nil {
			return res, err
		}
		verdict, err := r.kinds.Evaluate(exp, obs, now)
		if err != nil {
			// Дефект оценки не должен останавливать весь проход: остальные
			// ожидания продолжают проверяться, а этот случай виден в журнале.
			r.log.Error("оценка ожидания не удалась", "expectation", exp.ID, "kind", exp.Kind, "error", err)
			continue
		}
		res.Checked++
		if err := r.applyVerdict(ctx, exp, verdict, now, &res); err != nil {
			return res, err
		}
	}

	n, escalated, err := r.reactToOpenDiscrepancies(ctx, now)
	if err != nil {
		return res, err
	}
	res.Reflexes += n
	res.Escalations += escalated
	return res, nil
}

func (r *Runtime) applyVerdict(ctx context.Context, exp Expectation, v Verdict, now time.Time, res *TickResult) error {
	switch v.Outcome {
	case OutcomeSatisfied:
		res.Satisfied++
		return r.closeExpectation(ctx, exp, ExpectationSatisfied, EvExpectationSatisfied, v, now)
	case OutcomeExpired:
		res.Expired++
		return r.closeExpectation(ctx, exp, ExpectationExpired, EvExpectationExpired, v, now)
	case OutcomeMissed:
		res.Discrepancies++
		return r.recordDiscrepancy(ctx, exp, v, now)
	default:
		return r.reschedule(ctx, exp, v, now)
	}
}

func (r *Runtime) closeExpectation(ctx context.Context, exp Expectation, status, eventType string, v Verdict, now time.Time) error {
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		p := expectationStatusPayload{
			ID: exp.ID, At: now, Status: status, Reason: v.Observed, Expected: v.Expected,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: exp.SubjectType, StreamID: exp.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        eventType,
			Actor:            event.Actor{Type: event.ActorRuntime},
			Payload:          p,
		}); err != nil {
			return err
		}
		if err := applyExpectationStatus(ctx, tx, p); err != nil {
			return err
		}
		// Закрытое ожидание разрешает связанные с ним расхождения.
		return resolveDiscrepanciesOfExpectation(ctx, tx, exp.ID, now, "ожидание закрыто: "+status)
	})
	return err
}

func (r *Runtime) reschedule(ctx context.Context, exp Expectation, v Verdict, now time.Time) error {
	next := now.Add(defaultInterval(exp.CheckInterval))
	if v.NextCheckAt != nil && v.NextCheckAt.After(now) {
		next = *v.NextCheckAt
	}
	if exp.WindowUntil != nil && next.After(*exp.WindowUntil) {
		next = *exp.WindowUntil
	}
	_, err := r.db.Writer().ExecContext(ctx,
		`UPDATE expectations SET next_check_at = ?, updated_at = ? WHERE id = ? AND status = 'pending'`,
		ts(next), ts(now), exp.ID)
	if err != nil {
		return fmt.Errorf("runtime: перенос проверки ожидания %s: %w", exp.ID, err)
	}
	return nil
}

func defaultInterval(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return 15 * time.Second
}

func (r *Runtime) recordDiscrepancy(ctx context.Context, exp Expectation, v Verdict, now time.Time) error {
	severity := v.Severity
	if severity == "" {
		severity = exp.SeverityIfMissed
	}
	dedupe := v.DedupeKey
	if dedupe == "" {
		dedupe = exp.Kind + ":" + exp.ID
	}

	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		existing, found, err := activeDiscrepancyByDedupe(ctx, tx, dedupe)
		if err != nil {
			return err
		}
		if found {
			// Повторный сигнал того же класса не создаёт второе расхождение.
			p := discrepancySeenPayload{
				ID: existing.ID, LastSeen: now, Observed: v.Observed,
				Occurrences: existing.Occurrences + 1,
			}
			if _, err := w.Append(ctx, event.Request{
				StreamType: exp.SubjectType, StreamID: exp.SubjectID,
				ExpectedRevision: event.AnyRevision,
				EventType:        EvDiscrepancyUpdated,
				Actor:            event.Actor{Type: event.ActorRuntime},
				Payload:          p,
			}); err != nil {
				return err
			}
			if err := applyDiscrepancySeen(ctx, tx, p); err != nil {
				return err
			}
			return r.rescheduleTx(ctx, tx, exp, now)
		}

		d := Discrepancy{
			ID:            ids.New(ids.Discrepancy),
			ExpectationID: exp.ID,
			SubjectType:   exp.SubjectType,
			SubjectID:     exp.SubjectID,
			Kind:          exp.Kind,
			Expected:      v.Expected,
			Observed:      v.Observed,
			Severity:      severity,
			Confidence:    exp.Confidence,
			FirstSeen:     now,
			LastSeen:      now,
			Occurrences:   1,
			Status:        DiscrepancyOpen,
			DedupeKey:     dedupe,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: exp.SubjectType, StreamID: exp.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvDiscrepancyDetected,
			Actor:            event.Actor{Type: event.ActorRuntime},
			Payload:          d,
		}); err != nil {
			return err
		}
		if err := applyDiscrepancyDetected(ctx, tx, d); err != nil {
			return err
		}
		return r.rescheduleTx(ctx, tx, exp, now)
	})
	return err
}

func (r *Runtime) rescheduleTx(ctx context.Context, tx *sql.Tx, exp Expectation, now time.Time) error {
	next := now.Add(defaultInterval(exp.CheckInterval))
	if exp.WindowUntil != nil && next.After(*exp.WindowUntil) {
		next = *exp.WindowUntil
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE expectations SET next_check_at = ?, updated_at = ? WHERE id = ? AND status = 'pending'`,
		ts(next), ts(now), exp.ID)
	if err != nil {
		return fmt.Errorf("runtime: перенос проверки ожидания %s: %w", exp.ID, err)
	}
	return nil
}

// ResolveDiscrepancy закрывает расхождение.
func (r *Runtime) ResolveDiscrepancy(ctx context.Context, id, resolution string) error {
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		d, err := discrepancyByID(ctx, tx, id)
		if err != nil {
			return err
		}
		p := discrepancyStatusPayload{
			ID: id, Status: DiscrepancyResolved, At: r.clock.Now(), Resolution: resolution,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: d.SubjectType, StreamID: d.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvDiscrepancyResolved,
			Actor:            event.Actor{Type: event.ActorRuntime},
			Payload:          p,
		}); err != nil {
			return err
		}
		return applyDiscrepancyStatus(ctx, tx, p)
	})
	return err
}

// AcknowledgeDiscrepancy отмечает, что расхождение принято человеком.
func (r *Runtime) AcknowledgeDiscrepancy(ctx context.Context, id, note string, actor event.Actor) error {
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		d, err := discrepancyByID(ctx, tx, id)
		if err != nil {
			return err
		}
		p := discrepancyStatusPayload{
			ID: id, Status: DiscrepancyAcknowledged, At: r.clock.Now(), Resolution: note,
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: d.SubjectType, StreamID: d.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvDiscrepancyAcked,
			Actor:            actor,
			Payload:          p,
		}); err != nil {
			return err
		}
		return applyDiscrepancyStatus(ctx, tx, p)
	})
	return err
}

// reactToOpenDiscrepancies выполняет разрешённые локальные реакции.
func (r *Runtime) reactToOpenDiscrepancies(ctx context.Context, now time.Time) (int, int, error) {
	open, err := r.openDiscrepancies(ctx)
	if err != nil {
		return 0, 0, err
	}
	var acted, escalated int
	for _, d := range open {
		policies := r.reflexes.For(d.Kind)
		if len(policies) == 0 {
			continue
		}
		for _, p := range policies {
			did, esc, err := r.tryReflex(ctx, d, p, now)
			if err != nil {
				r.log.Error("локальная реакция не выполнена",
					"discrepancy", d.ID, "policy", p.ID, "error", err)
				continue
			}
			if did {
				acted++
			}
			if esc {
				escalated++
			}
			if did {
				break
			}
		}
	}
	return acted, escalated, nil
}

func (r *Runtime) tryReflex(ctx context.Context, d Discrepancy, p *ReflexPolicy, now time.Time) (bool, bool, error) {
	budget, err := r.budget(ctx, d.ID, p.ID, p.MaxAttempts)
	if err != nil {
		return false, false, err
	}
	if budget.Exhausted() {
		esc, err := r.escalate(ctx, d, p, budget, now)
		return false, esc, err
	}
	if until := budget.CooldownUntil(p.Cooldown); until != nil && now.Before(*until) {
		return false, false, nil
	}

	decision := r.policy.Check(ctx, PolicyRequest{
		ActionClass: p.ActionClass,
		SubjectType: d.SubjectType,
		SubjectID:   d.SubjectID,
		Actor:       event.Actor{Type: event.ActorRuntime, ID: "reflex:" + p.ID},
		Detail:      "локальная реакция на расхождение " + d.Kind,
	})
	if !decision.Allowed {
		if err := r.recordPolicyDecision(ctx, d, p, decision, now); err != nil {
			return false, false, err
		}
		esc, err := r.escalate(ctx, d, p, budget, now)
		return false, esc, err
	}

	// Попытка резервируется до действия: краш между резервированием и действием
	// тратит попытку, и это безопасное направление ошибки (ADR 0008).
	attempt, err := r.reserveAttempt(ctx, d, p, budget.Used+1, now)
	if err != nil {
		return false, false, err
	}

	outcome, actErr := p.Act(ctx, ReflexInput{Discrepancy: d, AttemptNo: attempt.AttemptNo, Now: now})
	finishedAt := r.clock.Now()

	result := ReflexSucceeded
	detail := outcome.Detail
	if actErr != nil {
		result = ReflexFailed
		detail = actErr.Error()
	} else if !outcome.Succeeded {
		result = ReflexFailed
	}

	err = r.finishAttempt(ctx, d, p, attempt, result, detail, finishedAt, outcome)
	if err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (r *Runtime) reserveAttempt(ctx context.Context, d Discrepancy, p *ReflexPolicy, no int, now time.Time) (ReflexAttempt, error) {
	attempt := ReflexAttempt{
		ID:            ids.New(ids.ReflexAttempt),
		DiscrepancyID: d.ID,
		PolicyID:      p.ID,
		AttemptNo:     no,
		StartedAt:     now,
		Outcome:       ReflexStarted,
	}
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: d.SubjectType, StreamID: d.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvReflexStarted,
			Actor:            event.Actor{Type: event.ActorRuntime, ID: "reflex:" + p.ID},
			Payload:          attempt,
		}); err != nil {
			return err
		}
		// Статус расхождения меняет applyReflexStarted, чтобы пересборка
		// из журнала давала то же состояние.
		return applyReflexStarted(ctx, tx, attempt)
	})
	return attempt, err
}

func (r *Runtime) finishAttempt(ctx context.Context, d Discrepancy, p *ReflexPolicy,
	attempt ReflexAttempt, result, detail string, at time.Time, outcome ReflexOutcome) error {

	eventType := EvReflexCompleted
	if result != ReflexSucceeded {
		eventType = EvReflexFailed
	}
	payload := reflexFinishPayload{
		ID: attempt.ID, DiscrepancyID: d.ID, PolicyID: p.ID,
		AttemptNo: attempt.AttemptNo, Outcome: result, Detail: detail, FinishedAt: at,
	}

	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: d.SubjectType, StreamID: d.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        eventType,
			Actor:            event.Actor{Type: event.ActorRuntime, ID: "reflex:" + p.ID},
			Payload:          payload,
		}); err != nil {
			return err
		}
		if err := applyReflexFinished(ctx, tx, payload); err != nil {
			return err
		}
		for _, o := range outcome.Observations {
			if _, err := r.recordObservationTx(ctx, tx, w, o); err != nil {
				return err
			}
		}
		if result == ReflexSucceeded {
			resolution := outcome.Resolution
			if resolution == "" {
				resolution = "восстановлено реакцией " + p.ID
			}
			sp := discrepancyStatusPayload{
				ID: d.ID, Status: DiscrepancyResolved, At: at, Resolution: resolution,
			}
			if _, err := w.Append(ctx, event.Request{
				StreamType: d.SubjectType, StreamID: d.SubjectID,
				ExpectedRevision: event.AnyRevision,
				EventType:        EvDiscrepancyResolved,
				Actor:            event.Actor{Type: event.ActorRuntime, ID: "reflex:" + p.ID},
				Payload:          sp,
			}); err != nil {
				return err
			}
			return applyDiscrepancyStatus(ctx, tx, sp)
		}
		// Возврат расхождения в open уже выполнен applyReflexFinished.
		return nil
	})
	return err
}

func (r *Runtime) escalate(ctx context.Context, d Discrepancy, p *ReflexPolicy, b BudgetState, now time.Time) (bool, error) {
	if d.Status == DiscrepancyEscalated {
		return false, nil
	}
	payload := escalationPayload{
		DiscrepancyID: d.ID,
		PolicyID:      p.ID,
		AttemptsUsed:  b.Used,
		MaxAttempts:   b.Max,
		Target:        p.EscalateTo,
		Reason: fmt.Sprintf("бюджет реакции %s исчерпан (%d из %d); "+
			"дальнейшее восстановление требует решения", p.ID, b.Used, b.Max),
		At: now,
	}
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: d.SubjectType, StreamID: d.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvEscalationRequested,
			Actor:            event.Actor{Type: event.ActorRuntime, ID: "reflex:" + p.ID},
			Payload:          payload,
		}); err != nil {
			return err
		}
		return applyEscalation(ctx, tx, payload)
	})
	if err != nil {
		return false, err
	}
	r.log.Warn("расхождение эскалировано", "discrepancy", d.ID, "policy", p.ID,
		"attempts", b.Used, "max", b.Max)
	return true, nil
}

func (r *Runtime) recordPolicyDecision(ctx context.Context, d Discrepancy, p *ReflexPolicy, res PolicyResult, now time.Time) error {
	payload := policyDecisionPayload{
		ID: ids.New("pol"), DecidedAt: now,
		ActorType: event.ActorRuntime, ActorID: "reflex:" + p.ID,
		ActionClass: p.ActionClass, SubjectType: d.SubjectType, SubjectID: d.SubjectID,
		Allowed: res.Allowed, Rule: res.Rule, Reason: res.Reason,
	}
	_, err := r.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: d.SubjectType, StreamID: d.SubjectID,
			ExpectedRevision: event.AnyRevision,
			EventType:        EvPolicyDecided,
			Actor:            event.Actor{Type: event.ActorRuntime},
			Payload:          payload,
		}); err != nil {
			return err
		}
		return applyPolicyDecision(ctx, tx, payload)
	})
	return err
}

// Budget возвращает состояние бюджета реакции.
func (r *Runtime) Budget(ctx context.Context, discrepancyID, policyID string) (BudgetState, error) {
	p, ok := r.reflexes.Get(policyID)
	max := 0
	if ok {
		max = p.MaxAttempts
	}
	return r.budget(ctx, discrepancyID, policyID, max)
}

func (r *Runtime) budget(ctx context.Context, discrepancyID, policyID string, max int) (BudgetState, error) {
	var (
		used int
		last sql.NullString
	)
	err := r.db.Reader().QueryRowContext(ctx,
		`SELECT count(*), max(started_at) FROM reflex_attempts
		 WHERE discrepancy_id = ? AND policy_id = ?`, discrepancyID, policyID).Scan(&used, &last)
	if err != nil {
		return BudgetState{}, fmt.Errorf("runtime: чтение бюджета реакции %s/%s: %w",
			discrepancyID, policyID, err)
	}
	b := BudgetState{Used: used, Max: max}
	if last.Valid {
		t, err := parseTS(last.String)
		if err != nil {
			return BudgetState{}, err
		}
		b.LastAttemptAt = &t
	}
	return b, nil
}

// Projections регистрирует проекторы предиктивного контура.
func (r *Runtime) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvObservationRecorded, projectObservation)
	reg.On(EvSystemStateUpdated, projectSnapshot)
	reg.On(EvExpectationCreated, projectExpectationCreated)
	reg.On(EvExpectationSatisfied, projectExpectationStatus)
	reg.On(EvExpectationExpired, projectExpectationStatus)
	reg.On(EvExpectationCancelled, projectExpectationStatus)
	reg.On(EvDiscrepancyDetected, projectDiscrepancyDetected)
	reg.On(EvDiscrepancyUpdated, projectDiscrepancySeen)
	reg.On(EvDiscrepancyResolved, projectDiscrepancyStatus)
	reg.On(EvDiscrepancyAcked, projectDiscrepancyStatus)
	reg.On(EvReflexStarted, projectReflexStarted)
	reg.On(EvReflexCompleted, projectReflexFinished)
	reg.On(EvReflexFailed, projectReflexFinished)
	reg.On(EvProbeRequested, projectProbeRequested)
	reg.On(EvProbeCompleted, projectProbeFinished)
	reg.On(EvProbeFailed, projectProbeFinished)
	reg.On(EvPolicyDecided, projectPolicyDecision)
	reg.On(EvEscalationRequested, projectEscalation)
	reg.OnAudit(EvExpectationChecked)
}
