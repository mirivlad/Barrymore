// Package event реализует append-only журнал доменных событий.
//
// ADR 0003: событие — источник аудита и восстановления, проекции обновляются
// в той же транзакции. ADR 0010: seq — глобальная монотонная последовательность,
// на которой строится возобновляемый SSE.
package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/store"
)

// ErrConcurrency возвращается, когда поток изменился между чтением и записью.
var ErrConcurrency = errors.New("event: поток изменён другим писателем")

// Тип действующего лица.
const (
	ActorPerson    = "person"
	ActorBarrymore = "barrymore"
	ActorRuntime   = "runtime"
	ActorWorker    = "worker"
	ActorSystem    = "system"
)

// AnyRevision отключает проверку оптимистичной конкурентности.
const AnyRevision int64 = -1

// Actor — кто вызвал изменение.
type Actor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// Envelope — записанное событие.
type Envelope struct {
	Seq            int64           `json:"seq"`
	ID             string          `json:"id"`
	StreamType     string          `json:"stream_type"`
	StreamID       string          `json:"stream_id"`
	StreamRevision int64           `json:"stream_revision"`
	EventType      string          `json:"event_type"`
	SchemaVersion  int             `json:"schema_version"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Actor          Actor           `json:"actor"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	CausationID    string          `json:"causation_id,omitempty"`
	Payload        json.RawMessage `json:"payload"`
}

// Decode распаковывает payload события в v.
func (e Envelope) Decode(v any) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("event %s (%s): разбор payload: %w", e.ID, e.EventType, err)
	}
	return nil
}

// Request описывает намерение записать событие.
type Request struct {
	StreamType string
	StreamID   string
	// ExpectedRevision — ревизия потока, которую писатель считает текущей.
	// AnyRevision отключает проверку, 0 означает «поток ещё не существует».
	ExpectedRevision int64
	EventType        string
	SchemaVersion    int
	Actor            Actor
	CorrelationID    string
	CausationID      string
	// IdempotencyKey делает повтор запроса безопасным: при совпадении ключа
	// возвращается ранее записанное событие, новое не создаётся.
	IdempotencyKey string
	Payload        any
}

// Journal пишет и читает события.
type Journal struct {
	db     *store.DB
	clock  clock.Clock
	broker *Broker
}

// NewJournal создаёт журнал поверх базы.
func NewJournal(db *store.DB, clk clock.Clock) *Journal {
	return &Journal{db: db, clock: clk, broker: NewBroker(db)}
}

// Broker возвращает шину подписки на новые события.
func (j *Journal) Broker() *Broker { return j.broker }

// TxWriter накапливает события внутри транзакции.
//
// Публикация подписчикам происходит только после успешного commit: подписчик
// не должен увидеть событие, которого нет в базе.
type TxWriter struct {
	j       *Journal
	tx      *sql.Tx
	written []Envelope
}

// Written возвращает события, записанные в этой транзакции.
func (w *TxWriter) Written() []Envelope { return w.written }

// Write выполняет fn в транзакции и публикует записанные события после commit.
func (j *Journal) Write(ctx context.Context, fn func(tx *sql.Tx, w *TxWriter) error) ([]Envelope, error) {
	w := &TxWriter{j: j, tx: nil}
	err := j.db.Tx(ctx, func(tx *sql.Tx) error {
		w.tx = tx
		w.written = w.written[:0]
		return fn(tx, w)
	})
	if err != nil {
		return nil, err
	}
	for _, env := range w.written {
		j.broker.publish(env)
	}
	return w.written, nil
}

// Append записывает событие в рамках текущей транзакции.
func (w *TxWriter) Append(ctx context.Context, req Request) (Envelope, error) {
	env, err := w.j.append(ctx, w.tx, req)
	if err != nil {
		return Envelope{}, err
	}
	w.written = append(w.written, env)
	return env, nil
}

func (j *Journal) append(ctx context.Context, tx *sql.Tx, req Request) (Envelope, error) {
	if req.StreamType == "" || req.StreamID == "" || req.EventType == "" {
		return Envelope{}, fmt.Errorf("event: неполный запрос записи (%q/%q/%q)",
			req.StreamType, req.StreamID, req.EventType)
	}
	if req.Actor.Type == "" {
		return Envelope{}, fmt.Errorf("event %s: не указан actor", req.EventType)
	}
	if req.SchemaVersion == 0 {
		req.SchemaVersion = 1
	}

	if req.IdempotencyKey != "" {
		existing, found, err := loadByIdempotency(ctx, tx, req.IdempotencyKey)
		if err != nil {
			return Envelope{}, err
		}
		if found {
			return existing, nil
		}
	}

	current, err := currentRevision(ctx, tx, req.StreamType, req.StreamID)
	if err != nil {
		return Envelope{}, err
	}
	if req.ExpectedRevision != AnyRevision && req.ExpectedRevision != current {
		return Envelope{}, fmt.Errorf("%w: поток %s/%s имеет ревизию %d, ожидалась %d",
			ErrConcurrency, req.StreamType, req.StreamID, current, req.ExpectedRevision)
	}
	next := current + 1

	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("event %s: сериализация payload: %w", req.EventType, err)
	}
	if req.Payload == nil {
		payload = []byte("{}")
	}

	env := Envelope{
		ID:             ids.New(ids.Event),
		StreamType:     req.StreamType,
		StreamID:       req.StreamID,
		StreamRevision: next,
		EventType:      req.EventType,
		SchemaVersion:  req.SchemaVersion,
		OccurredAt:     j.clock.Now(),
		Actor:          req.Actor,
		CorrelationID:  req.CorrelationID,
		CausationID:    req.CausationID,
		Payload:        payload,
	}

	var idem any
	if req.IdempotencyKey != "" {
		idem = req.IdempotencyKey
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, stream_type, stream_id, stream_revision, event_type,
		                    schema_version, occurred_at, actor_type, actor_id,
		                    correlation_id, causation_id, idempotency_key, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		env.ID, env.StreamType, env.StreamID, env.StreamRevision, env.EventType,
		env.SchemaVersion, formatTime(env.OccurredAt), env.Actor.Type, env.Actor.ID,
		env.CorrelationID, env.CausationID, idem, string(env.Payload))
	if err != nil {
		return Envelope{}, fmt.Errorf("event %s: запись в журнал: %w", env.EventType, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Envelope{}, fmt.Errorf("event %s: получение seq: %w", env.EventType, err)
	}
	env.Seq = seq

	_, err = tx.ExecContext(ctx, `
		INSERT INTO streams (stream_type, stream_id, revision, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (stream_type, stream_id) DO UPDATE SET revision = excluded.revision,
		                                                   updated_at = excluded.updated_at`,
		env.StreamType, env.StreamID, env.StreamRevision, formatTime(env.OccurredAt))
	if err != nil {
		return Envelope{}, fmt.Errorf("event %s: обновление головы потока: %w", env.EventType, err)
	}
	return env, nil
}

// Revision возвращает текущую ревизию потока (0 — потока нет).
func (j *Journal) Revision(ctx context.Context, streamType, streamID string) (int64, error) {
	var rev int64
	err := j.db.Reader().QueryRowContext(ctx,
		`SELECT revision FROM streams WHERE stream_type = ? AND stream_id = ?`,
		streamType, streamID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("event: чтение ревизии потока %s/%s: %w", streamType, streamID, err)
	}
	return rev, nil
}

func currentRevision(ctx context.Context, tx *sql.Tx, streamType, streamID string) (int64, error) {
	var rev int64
	err := tx.QueryRowContext(ctx,
		`SELECT revision FROM streams WHERE stream_type = ? AND stream_id = ?`,
		streamType, streamID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("event: чтение ревизии потока: %w", err)
	}
	return rev, nil
}

func loadByIdempotency(ctx context.Context, tx *sql.Tx, key string) (Envelope, bool, error) {
	row := tx.QueryRowContext(ctx, selectEventColumns+` WHERE idempotency_key = ?`, key)
	env, err := scanEnvelope(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Envelope{}, false, nil
	}
	if err != nil {
		return Envelope{}, false, fmt.Errorf("event: поиск по idempotency key: %w", err)
	}
	return env, true, nil
}

const selectEventColumns = `
	SELECT seq, id, stream_type, stream_id, stream_revision, event_type, schema_version,
	       occurred_at, actor_type, actor_id, correlation_id, causation_id, payload
	FROM events`

type rowScanner interface{ Scan(dest ...any) error }

func scanEnvelope(row rowScanner) (Envelope, error) {
	var (
		env        Envelope
		occurredAt string
		payload    string
	)
	err := row.Scan(&env.Seq, &env.ID, &env.StreamType, &env.StreamID, &env.StreamRevision,
		&env.EventType, &env.SchemaVersion, &occurredAt, &env.Actor.Type, &env.Actor.ID,
		&env.CorrelationID, &env.CausationID, &payload)
	if err != nil {
		return Envelope{}, err
	}
	env.OccurredAt, err = parseTime(occurredAt)
	if err != nil {
		return Envelope{}, fmt.Errorf("event %s: разбор occurred_at: %w", env.ID, err)
	}
	env.Payload = json.RawMessage(payload)
	return env, nil
}

// Stream читает события потока по возрастанию ревизии.
func (j *Journal) Stream(ctx context.Context, streamType, streamID string) ([]Envelope, error) {
	rows, err := j.db.Reader().QueryContext(ctx,
		selectEventColumns+` WHERE stream_type = ? AND stream_id = ? ORDER BY stream_revision`,
		streamType, streamID)
	if err != nil {
		return nil, fmt.Errorf("event: чтение потока %s/%s: %w", streamType, streamID, err)
	}
	return collect(rows)
}

// Since читает события с seq больше указанного, не более limit штук.
func (j *Journal) Since(ctx context.Context, seq int64, limit int) ([]Envelope, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := j.db.Reader().QueryContext(ctx,
		selectEventColumns+` WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil, fmt.Errorf("event: чтение журнала с seq %d: %w", seq, err)
	}
	return collect(rows)
}

// All читает журнал целиком в порядке seq. Используется при пересборке проекций.
func (j *Journal) All(ctx context.Context) ([]Envelope, error) {
	rows, err := j.db.Reader().QueryContext(ctx, selectEventColumns+` ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("event: чтение всего журнала: %w", err)
	}
	return collect(rows)
}

// Head возвращает наибольший seq в журнале.
func (j *Journal) Head(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := j.db.Reader().QueryRowContext(ctx, `SELECT max(seq) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("event: чтение головы журнала: %w", err)
	}
	return seq.Int64, nil
}

func collect(rows *sql.Rows) ([]Envelope, error) {
	defer rows.Close()
	var out []Envelope
	for rows.Next() {
		env, err := scanEnvelope(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
