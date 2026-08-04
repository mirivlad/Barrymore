package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

// Каждое изменение проекции существует в двух формах:
//
//	applyX   — вызывается сервисом внутри транзакции записи;
//	projectX — вызывается при пересборке из журнала.
//
// Обе формы ведут в одну и ту же функцию, поэтому состояние, восстановленное из
// журнала, совпадает с состоянием, накопленным по ходу работы. Это проверяется
// тестом пересборки, а не принимается на веру.
//
// Единственное исключение — expectations.next_check_at: это подсказка
// планировщика, а не доменное состояние. Событие на каждый перенос проверки
// раздуло бы журнал без пользы. После пересборки поле указывает на момент
// создания ожидания, поэтому ближайший тик просто перепроверит его.

// ---------- payload-структуры событий ----------

type expectationStatusPayload struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason,omitempty"`
	Expected string    `json:"expected,omitempty"`
}

type discrepancySeenPayload struct {
	ID          string    `json:"id"`
	LastSeen    time.Time `json:"last_seen"`
	Observed    string    `json:"observed"`
	Occurrences int       `json:"occurrences"`
}

type discrepancyStatusPayload struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	At         time.Time `json:"at"`
	Resolution string    `json:"resolution,omitempty"`
}

type reflexFinishPayload struct {
	ID            string    `json:"id"`
	DiscrepancyID string    `json:"discrepancy_id"`
	PolicyID      string    `json:"policy_id"`
	AttemptNo     int       `json:"attempt_no"`
	Outcome       string    `json:"outcome"`
	Detail        string    `json:"detail,omitempty"`
	FinishedAt    time.Time `json:"finished_at"`
}

type escalationPayload struct {
	DiscrepancyID string    `json:"discrepancy_id"`
	PolicyID      string    `json:"policy_id"`
	AttemptsUsed  int       `json:"attempts_used"`
	MaxAttempts   int       `json:"max_attempts"`
	Target        string    `json:"target,omitempty"`
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
}

type probeFinishPayload struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	CompletedAt time.Time       `json:"completed_at"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type policyDecisionPayload struct {
	ID          string    `json:"id"`
	DecidedAt   time.Time `json:"decided_at"`
	ActorType   string    `json:"actor_type"`
	ActorID     string    `json:"actor_id,omitempty"`
	ActionClass string    `json:"action_class"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Allowed     bool      `json:"allowed"`
	Rule        string    `json:"rule"`
	Reason      string    `json:"reason,omitempty"`
	Detail      string    `json:"detail,omitempty"`
}

// ---------- наблюдения ----------

func applyObservation(ctx context.Context, tx *sql.Tx, o Observation, seq int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO observations (id, kind, subject_type, subject_id, observed_at, recorded_at,
		                          source, source_quality, confidence, dedupe_key, payload, event_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		o.ID, o.Kind, o.SubjectType, o.SubjectID, ts(o.ObservedAt), ts(o.RecordedAt),
		o.Source, o.SourceQuality, o.Confidence, nullable(o.DedupeKey), string(o.Payload), seq)
	if err != nil {
		return fmt.Errorf("проекция наблюдения %s: %w", o.ID, err)
	}
	return nil
}

func projectObservation(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var o Observation
	if err := env.Decode(&o); err != nil {
		return err
	}
	return applyObservation(ctx, tx, o, env.Seq)
}

// ---------- снимки ----------

func applySnapshot(ctx context.Context, tx *sql.Tx, s Snapshot) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO system_snapshots (id, scope, status, confidence, observed_at, valid_until,
		                              source, reason, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		s.ID, s.Scope, s.Status, s.Confidence, ts(s.ObservedAt), tsp(s.ValidUntil),
		s.Source, s.Reason, string(s.Payload))
	if err != nil {
		return fmt.Errorf("проекция снимка %s: %w", s.ID, err)
	}
	return nil
}

func projectSnapshot(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var s Snapshot
	if err := env.Decode(&s); err != nil {
		return err
	}
	return applySnapshot(ctx, tx, s)
}

// ---------- ожидания ----------

func applyExpectationCreated(ctx context.Context, tx *sql.Tx, e Expectation) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO expectations (id, subject_type, subject_id, kind, params, basis, confidence,
		                          severity_if_missed, window_from, window_until, next_check_at,
		                          check_interval_ms, probe_policy, reaction_policy, status,
		                          created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.SubjectType, e.SubjectID, e.Kind, string(e.Params), e.Basis, e.Confidence,
		e.SeverityIfMissed, ts(e.WindowFrom), tsp(e.WindowUntil), tsp(e.NextCheckAt),
		e.CheckInterval.Milliseconds(), e.ProbePolicy, e.ReactionPolicy, e.Status,
		ts(e.CreatedAt), ts(e.UpdatedAt))
	if err != nil {
		return fmt.Errorf("проекция ожидания %s: %w", e.ID, err)
	}
	return nil
}

func projectExpectationCreated(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var e Expectation
	if err := env.Decode(&e); err != nil {
		return err
	}
	return applyExpectationCreated(ctx, tx, e)
}

func applyExpectationStatus(ctx context.Context, tx *sql.Tx, p expectationStatusPayload) error {
	var satisfied, expired any
	switch p.Status {
	case ExpectationSatisfied:
		satisfied = ts(p.At)
	case ExpectationExpired:
		expired = ts(p.At)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE expectations
		   SET status = ?, satisfied_at = COALESCE(?, satisfied_at),
		       expired_at = COALESCE(?, expired_at), next_check_at = NULL, updated_at = ?
		 WHERE id = ?`,
		p.Status, satisfied, expired, ts(p.At), p.ID)
	if err != nil {
		return fmt.Errorf("проекция статуса ожидания %s: %w", p.ID, err)
	}
	return nil
}

func projectExpectationStatus(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p expectationStatusPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyExpectationStatus(ctx, tx, p)
}

// ---------- расхождения ----------

func applyDiscrepancyDetected(ctx context.Context, tx *sql.Tx, d Discrepancy) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO discrepancies (id, expectation_id, subject_type, subject_id, kind, expected,
		                           observed, severity, confidence, first_seen, last_seen,
		                           occurrences, status, resolution, dedupe_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		d.ID, nullable(d.ExpectationID), d.SubjectType, d.SubjectID, d.Kind, d.Expected,
		d.Observed, d.Severity, d.Confidence, ts(d.FirstSeen), ts(d.LastSeen),
		d.Occurrences, d.Status, d.Resolution, nullable(d.DedupeKey), ts(d.CreatedAt), ts(d.UpdatedAt))
	if err != nil {
		return fmt.Errorf("проекция расхождения %s: %w", d.ID, err)
	}
	return nil
}

func projectDiscrepancyDetected(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var d Discrepancy
	if err := env.Decode(&d); err != nil {
		return err
	}
	return applyDiscrepancyDetected(ctx, tx, d)
}

func applyDiscrepancySeen(ctx context.Context, tx *sql.Tx, p discrepancySeenPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE discrepancies
		   SET last_seen = ?, observed = ?, occurrences = ?, updated_at = ?
		 WHERE id = ?`,
		ts(p.LastSeen), p.Observed, p.Occurrences, ts(p.LastSeen), p.ID)
	if err != nil {
		return fmt.Errorf("проекция повтора расхождения %s: %w", p.ID, err)
	}
	return nil
}

func projectDiscrepancySeen(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p discrepancySeenPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyDiscrepancySeen(ctx, tx, p)
}

func applyDiscrepancyStatus(ctx context.Context, tx *sql.Tx, p discrepancyStatusPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE discrepancies SET status = ?, resolution = ?, updated_at = ? WHERE id = ?`,
		p.Status, p.Resolution, ts(p.At), p.ID)
	if err != nil {
		return fmt.Errorf("проекция статуса расхождения %s: %w", p.ID, err)
	}
	return nil
}

func projectDiscrepancyStatus(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p discrepancyStatusPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyDiscrepancyStatus(ctx, tx, p)
}

func setDiscrepancyStatus(ctx context.Context, tx *sql.Tx, id, status string, at time.Time) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE discrepancies SET status = ?, updated_at = ? WHERE id = ?`, status, ts(at), id)
	if err != nil {
		return fmt.Errorf("проекция статуса расхождения %s: %w", id, err)
	}
	return nil
}

func resolveDiscrepanciesOfExpectation(ctx context.Context, tx *sql.Tx, expectationID string, at time.Time, resolution string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE discrepancies
		   SET status = 'resolved', resolution = ?, updated_at = ?
		 WHERE expectation_id = ? AND status IN ('open','probing','reacting','escalated')`,
		resolution, ts(at), expectationID)
	if err != nil {
		return fmt.Errorf("закрытие расхождений ожидания %s: %w", expectationID, err)
	}
	return nil
}

func applyEscalation(ctx context.Context, tx *sql.Tx, p escalationPayload) error {
	return setDiscrepancyStatus(ctx, tx, p.DiscrepancyID, DiscrepancyEscalated, p.At)
}

func projectEscalation(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p escalationPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyEscalation(ctx, tx, p)
}

// ---------- локальные реакции ----------

func applyReflexStarted(ctx context.Context, tx *sql.Tx, a ReflexAttempt) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO reflex_attempts (id, discrepancy_id, policy_id, attempt_no, started_at, outcome)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		a.ID, a.DiscrepancyID, a.PolicyID, a.AttemptNo, ts(a.StartedAt), a.Outcome)
	if err != nil {
		return fmt.Errorf("проекция попытки реакции %s: %w", a.ID, err)
	}
	return setDiscrepancyStatus(ctx, tx, a.DiscrepancyID, DiscrepancyReacting, a.StartedAt)
}

func projectReflexStarted(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var a ReflexAttempt
	if err := env.Decode(&a); err != nil {
		return err
	}
	return applyReflexStarted(ctx, tx, a)
}

func applyReflexFinished(ctx context.Context, tx *sql.Tx, p reflexFinishPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE reflex_attempts SET finished_at = ?, outcome = ?, detail = ? WHERE id = ?`,
		ts(p.FinishedAt), p.Outcome, p.Detail, p.ID)
	if err != nil {
		return fmt.Errorf("проекция итога реакции %s: %w", p.ID, err)
	}
	if p.Outcome != ReflexSucceeded {
		// Неудачная попытка возвращает расхождение в открытое состояние:
		// следующий тик решит, есть ли ещё бюджет.
		return setDiscrepancyStatus(ctx, tx, p.DiscrepancyID, DiscrepancyOpen, p.FinishedAt)
	}
	return nil
}

func projectReflexFinished(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p reflexFinishPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyReflexFinished(ctx, tx, p)
}

// ---------- probes ----------

func applyProbeRequested(ctx context.Context, tx *sql.Tx, p Probe) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO probes (id, kind, subject_type, subject_id, requested_by, discrepancy_id,
		                    params, status, requested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		p.ID, p.Kind, p.SubjectType, p.SubjectID, p.RequestedBy, nullable(p.DiscrepancyID),
		string(p.Params), p.Status, ts(p.RequestedAt))
	if err != nil {
		return fmt.Errorf("проекция probe %s: %w", p.ID, err)
	}
	return nil
}

func projectProbeRequested(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p Probe
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyProbeRequested(ctx, tx, p)
}

func applyProbeFinished(ctx context.Context, tx *sql.Tx, p probeFinishPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE probes SET status = ?, completed_at = ?, result = ?, error = ? WHERE id = ?`,
		p.Status, ts(p.CompletedAt), string(p.Result), p.Error, p.ID)
	if err != nil {
		return fmt.Errorf("проекция итога probe %s: %w", p.ID, err)
	}
	return nil
}

func projectProbeFinished(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p probeFinishPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyProbeFinished(ctx, tx, p)
}

// ---------- решения политик ----------

func applyPolicyDecision(ctx context.Context, tx *sql.Tx, p policyDecisionPayload) error {
	allowed := 0
	if p.Allowed {
		allowed = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO policy_decisions (id, decided_at, actor_type, actor_id, action_class,
		                              subject_type, subject_id, allowed, rule, reason, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		p.ID, ts(p.DecidedAt), p.ActorType, p.ActorID, p.ActionClass,
		p.SubjectType, p.SubjectID, allowed, p.Rule, p.Reason, p.Detail)
	if err != nil {
		return fmt.Errorf("проекция решения политики %s: %w", p.ID, err)
	}
	return nil
}

func projectPolicyDecision(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p policyDecisionPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyPolicyDecision(ctx, tx, p)
}

// ---------- чтение проекций ----------

const selectObservationColumns = `
	SELECT id, kind, subject_type, subject_id, observed_at, recorded_at, source,
	       source_quality, confidence, COALESCE(dedupe_key, ''), payload, COALESCE(event_seq, 0)
	FROM observations`

type scanner interface{ Scan(dest ...any) error }

func scanObservation(row scanner) (Observation, error) {
	var (
		o                      Observation
		observedAt, recordedAt string
		payload                string
	)
	err := row.Scan(&o.ID, &o.Kind, &o.SubjectType, &o.SubjectID, &observedAt, &recordedAt,
		&o.Source, &o.SourceQuality, &o.Confidence, &o.DedupeKey, &payload, &o.EventSeq)
	if err != nil {
		return Observation{}, err
	}
	if o.ObservedAt, err = parseTS(observedAt); err != nil {
		return Observation{}, err
	}
	if o.RecordedAt, err = parseTS(recordedAt); err != nil {
		return Observation{}, err
	}
	o.Payload = json.RawMessage(payload)
	return o, nil
}

func observationByDedupe(ctx context.Context, tx *sql.Tx, key string) (Observation, bool, error) {
	row := tx.QueryRowContext(ctx, selectObservationColumns+` WHERE dedupe_key = ?`, key)
	o, err := scanObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Observation{}, false, nil
	}
	if err != nil {
		return Observation{}, false, fmt.Errorf("поиск наблюдения по ключу дедупликации: %w", err)
	}
	return o, true, nil
}

// observationsFor возвращает наблюдения о субъекте начиная с указанного момента.
func (r *Runtime) observationsFor(ctx context.Context, subjectType, subjectID string, since time.Time) ([]Observation, error) {
	rows, err := r.db.Reader().QueryContext(ctx,
		selectObservationColumns+`
		 WHERE subject_type = ? AND subject_id = ? AND observed_at >= ?
		 ORDER BY observed_at`,
		subjectType, subjectID, ts(since))
	if err != nil {
		return nil, fmt.Errorf("runtime: чтение наблюдений %s/%s: %w", subjectType, subjectID, err)
	}
	defer rows.Close()

	var out []Observation
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Observations возвращает наблюдения о субъекте (для интерфейса и диагностики).
func (r *Runtime) Observations(ctx context.Context, subjectType, subjectID string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.db.Reader().QueryContext(ctx,
		selectObservationColumns+`
		 WHERE subject_type = ? AND subject_id = ?
		 ORDER BY observed_at DESC LIMIT ?`, subjectType, subjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("runtime: чтение наблюдений: %w", err)
	}
	defer rows.Close()

	var out []Observation
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

const selectSnapshotColumns = `
	SELECT id, scope, status, confidence, observed_at, valid_until, source, reason, payload
	FROM system_snapshots`

func scanSnapshot(row scanner) (Snapshot, error) {
	var (
		s          Snapshot
		observedAt string
		validUntil sql.NullString
		payload    string
	)
	err := row.Scan(&s.ID, &s.Scope, &s.Status, &s.Confidence, &observedAt, &validUntil,
		&s.Source, &s.Reason, &payload)
	if err != nil {
		return Snapshot{}, err
	}
	if s.ObservedAt, err = parseTS(observedAt); err != nil {
		return Snapshot{}, err
	}
	if s.ValidUntil, err = parseTSPtr(validUntil); err != nil {
		return Snapshot{}, err
	}
	s.Payload = json.RawMessage(payload)
	return s, nil
}

const selectExpectationColumns = `
	SELECT id, subject_type, subject_id, kind, params, basis, confidence, severity_if_missed,
	       window_from, window_until, next_check_at, check_interval_ms, probe_policy,
	       reaction_policy, status, satisfied_at, expired_at, COALESCE(superseded_by, ''),
	       created_at, updated_at
	FROM expectations`

func scanExpectation(row scanner) (Expectation, error) {
	var (
		e                                              Expectation
		params                                         string
		windowFrom                                     string
		windowUntil, nextCheck, satisfiedAt, expiredAt sql.NullString
		intervalMS                                     int64
		createdAt, updatedAt                           string
	)
	err := row.Scan(&e.ID, &e.SubjectType, &e.SubjectID, &e.Kind, &params, &e.Basis,
		&e.Confidence, &e.SeverityIfMissed, &windowFrom, &windowUntil, &nextCheck,
		&intervalMS, &e.ProbePolicy, &e.ReactionPolicy, &e.Status, &satisfiedAt,
		&expiredAt, &e.SupersededBy, &createdAt, &updatedAt)
	if err != nil {
		return Expectation{}, err
	}
	e.Params = json.RawMessage(params)
	e.CheckInterval = time.Duration(intervalMS) * time.Millisecond
	if e.WindowFrom, err = parseTS(windowFrom); err != nil {
		return Expectation{}, err
	}
	if e.WindowUntil, err = parseTSPtr(windowUntil); err != nil {
		return Expectation{}, err
	}
	if e.NextCheckAt, err = parseTSPtr(nextCheck); err != nil {
		return Expectation{}, err
	}
	if e.SatisfiedAt, err = parseTSPtr(satisfiedAt); err != nil {
		return Expectation{}, err
	}
	if e.ExpiredAt, err = parseTSPtr(expiredAt); err != nil {
		return Expectation{}, err
	}
	if e.CreatedAt, err = parseTS(createdAt); err != nil {
		return Expectation{}, err
	}
	if e.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return Expectation{}, err
	}
	return e, nil
}

func expectationByID(ctx context.Context, tx *sql.Tx, id string) (Expectation, error) {
	row := tx.QueryRowContext(ctx, selectExpectationColumns+` WHERE id = ?`, id)
	e, err := scanExpectation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Expectation{}, fmt.Errorf("runtime: ожидание %s не найдено", id)
	}
	if err != nil {
		return Expectation{}, fmt.Errorf("runtime: чтение ожидания %s: %w", id, err)
	}
	return e, nil
}

func (r *Runtime) dueExpectations(ctx context.Context, now time.Time) ([]Expectation, error) {
	rows, err := r.db.Reader().QueryContext(ctx, selectExpectationColumns+`
		 WHERE status = 'pending' AND (next_check_at IS NULL OR next_check_at <= ?)
		 ORDER BY next_check_at LIMIT ?`, ts(now), r.maxBatch)
	if err != nil {
		return nil, fmt.Errorf("runtime: выборка просроченных ожиданий: %w", err)
	}
	defer rows.Close()

	var out []Expectation
	for rows.Next() {
		e, err := scanExpectation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Expectations возвращает ожидания о субъекте.
func (r *Runtime) Expectations(ctx context.Context, subjectType, subjectID string) ([]Expectation, error) {
	rows, err := r.db.Reader().QueryContext(ctx, selectExpectationColumns+`
		 WHERE subject_type = ? AND subject_id = ? ORDER BY created_at`, subjectType, subjectID)
	if err != nil {
		return nil, fmt.Errorf("runtime: чтение ожиданий: %w", err)
	}
	defer rows.Close()

	var out []Expectation
	for rows.Next() {
		e, err := scanExpectation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExpectationByID возвращает ожидание.
func (r *Runtime) ExpectationByID(ctx context.Context, id string) (Expectation, error) {
	row := r.db.Reader().QueryRowContext(ctx, selectExpectationColumns+` WHERE id = ?`, id)
	e, err := scanExpectation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Expectation{}, fmt.Errorf("runtime: ожидание %s не найдено", id)
	}
	return e, err
}

const selectDiscrepancyColumns = `
	SELECT id, COALESCE(expectation_id, ''), subject_type, subject_id, kind, expected, observed,
	       severity, confidence, first_seen, last_seen, occurrences, status, resolution,
	       COALESCE(dedupe_key, ''), created_at, updated_at
	FROM discrepancies`

func scanDiscrepancy(row scanner) (Discrepancy, error) {
	var (
		d                                         Discrepancy
		firstSeen, lastSeen, createdAt, updatedAt string
	)
	err := row.Scan(&d.ID, &d.ExpectationID, &d.SubjectType, &d.SubjectID, &d.Kind,
		&d.Expected, &d.Observed, &d.Severity, &d.Confidence, &firstSeen, &lastSeen,
		&d.Occurrences, &d.Status, &d.Resolution, &d.DedupeKey, &createdAt, &updatedAt)
	if err != nil {
		return Discrepancy{}, err
	}
	if d.FirstSeen, err = parseTS(firstSeen); err != nil {
		return Discrepancy{}, err
	}
	if d.LastSeen, err = parseTS(lastSeen); err != nil {
		return Discrepancy{}, err
	}
	if d.CreatedAt, err = parseTS(createdAt); err != nil {
		return Discrepancy{}, err
	}
	if d.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return Discrepancy{}, err
	}
	return d, nil
}

func activeDiscrepancyByDedupe(ctx context.Context, tx *sql.Tx, key string) (Discrepancy, bool, error) {
	row := tx.QueryRowContext(ctx, selectDiscrepancyColumns+`
		 WHERE dedupe_key = ? AND status IN ('open','probing','reacting','escalated')`, key)
	d, err := scanDiscrepancy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Discrepancy{}, false, nil
	}
	if err != nil {
		return Discrepancy{}, false, fmt.Errorf("поиск активного расхождения: %w", err)
	}
	return d, true, nil
}

func discrepancyByID(ctx context.Context, tx *sql.Tx, id string) (Discrepancy, error) {
	row := tx.QueryRowContext(ctx, selectDiscrepancyColumns+` WHERE id = ?`, id)
	d, err := scanDiscrepancy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Discrepancy{}, fmt.Errorf("runtime: расхождение %s не найдено", id)
	}
	return d, err
}

func (r *Runtime) openDiscrepancies(ctx context.Context) ([]Discrepancy, error) {
	rows, err := r.db.Reader().QueryContext(ctx, selectDiscrepancyColumns+`
		 WHERE status IN ('open','probing','reacting') ORDER BY first_seen LIMIT ?`, r.maxBatch)
	if err != nil {
		return nil, fmt.Errorf("runtime: выборка открытых расхождений: %w", err)
	}
	defer rows.Close()

	var out []Discrepancy
	for rows.Next() {
		d, err := scanDiscrepancy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Discrepancies возвращает расхождения, при filterOpen — только незакрытые.
func (r *Runtime) Discrepancies(ctx context.Context, filterOpen bool, limit int) ([]Discrepancy, error) {
	if limit <= 0 {
		limit = 200
	}
	query := selectDiscrepancyColumns
	if filterOpen {
		query += ` WHERE status IN ('open','probing','reacting','escalated')`
	}
	query += ` ORDER BY last_seen DESC LIMIT ?`

	rows, err := r.db.Reader().QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("runtime: чтение расхождений: %w", err)
	}
	defer rows.Close()

	var out []Discrepancy
	for rows.Next() {
		d, err := scanDiscrepancy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Attempts возвращает историю попыток реакции по расхождению.
func (r *Runtime) Attempts(ctx context.Context, discrepancyID string) ([]ReflexAttempt, error) {
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT id, discrepancy_id, policy_id, attempt_no, started_at, finished_at, outcome, detail
		  FROM reflex_attempts WHERE discrepancy_id = ? ORDER BY attempt_no`, discrepancyID)
	if err != nil {
		return nil, fmt.Errorf("runtime: чтение попыток реакции: %w", err)
	}
	defer rows.Close()

	var out []ReflexAttempt
	for rows.Next() {
		var (
			a          ReflexAttempt
			startedAt  string
			finishedAt sql.NullString
		)
		if err := rows.Scan(&a.ID, &a.DiscrepancyID, &a.PolicyID, &a.AttemptNo,
			&startedAt, &finishedAt, &a.Outcome, &a.Detail); err != nil {
			return nil, err
		}
		var err error
		if a.StartedAt, err = parseTS(startedAt); err != nil {
			return nil, err
		}
		if a.FinishedAt, err = parseTSPtr(finishedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------- вспомогательное ----------

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("разбор времени %q: %w", s, err)
	}
	return t.UTC(), nil
}

func parseTSPtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := parseTS(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// wakeExpectations делает ожидания субъекта готовыми к немедленной проверке.
//
// Планировщик остаётся единственным местом, где ожидания превращаются в
// расхождения (ADR 0009); наблюдение лишь ускоряет ближайшую проверку.
func wakeExpectations(ctx context.Context, tx *sql.Tx, subjectType, subjectID string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE expectations SET next_check_at = ?
		 WHERE subject_type = ? AND subject_id = ? AND status = 'pending'
		   AND (next_check_at IS NULL OR next_check_at > ?)`,
		ts(at), subjectType, subjectID, ts(at))
	if err != nil {
		return fmt.Errorf("пробуждение ожиданий %s/%s: %w", subjectType, subjectID, err)
	}
	return nil
}
