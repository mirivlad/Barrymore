package thread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

// Как и в предиктивном контуре, каждое изменение существует в двух формах:
// applyX для транзакции сервиса и projectX для пересборки из журнала.
// Обе ведут в одну функцию.

func applyCreated(ctx context.Context, tx *sql.Tx, t Thread) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO threads (id, title, kind, state, summary, origin, importance, sensitivity,
		                     workspace_id, created_at, updated_at, last_meaningful_activity_at,
		                     next_review_at, muted_until, released_reason, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		t.ID, t.Title, t.Kind, t.State, t.Summary, t.Origin, t.Importance, t.Sensitivity,
		nullable(t.WorkspaceID), ts(t.CreatedAt), ts(t.UpdatedAt), tsp(t.LastMeaningfulActivityAt),
		tsp(t.NextReviewAt), tsp(t.MutedUntil), t.ReleasedReason, t.Revision)
	if err != nil {
		return fmt.Errorf("проекция нити %s: %w", t.ID, err)
	}
	return nil
}

func projectCreated(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var t Thread
	if err := env.Decode(&t); err != nil {
		return err
	}
	t.Revision = env.StreamRevision
	return applyCreated(ctx, tx, t)
}

func applyUpdated(ctx context.Context, tx *sql.Tx, p updatePayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE threads
		   SET title = COALESCE(?, title),
		       summary = COALESCE(?, summary),
		       origin = COALESCE(?, origin),
		       importance = COALESCE(?, importance),
		       sensitivity = COALESCE(?, sensitivity),
		       workspace_id = COALESCE(?, workspace_id),
		       next_review_at = COALESCE(?, next_review_at),
		       muted_until = COALESCE(?, muted_until),
		       updated_at = ?,
		       revision = ?
		 WHERE id = ?`,
		strp(p.Title), strp(p.Summary), strp(p.Origin), strp(p.Importance),
		strp(p.Sensitivity), strp(p.WorkspaceID), tsp(p.NextReviewAt), tsp(p.MutedUntil),
		ts(p.UpdatedAt), p.Revision, p.ID)
	if err != nil {
		return fmt.Errorf("проекция изменения нити %s: %w", p.ID, err)
	}
	return nil
}

func projectUpdated(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p updatePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	p.Revision = env.StreamRevision
	return applyUpdated(ctx, tx, p)
}

func applyStateChange(ctx context.Context, tx *sql.Tx, p stateChangePayload) error {
	released := ""
	if p.State == StateReleased {
		released = p.Reason
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE threads
		   SET state = ?, released_reason = ?, updated_at = ?, revision = ?
		 WHERE id = ?`,
		p.State, released, ts(p.ChangedAt), p.Revision, p.ID)
	if err != nil {
		return fmt.Errorf("проекция состояния нити %s: %w", p.ID, err)
	}
	return nil
}

func projectStateChange(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p stateChangePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	p.Revision = env.StreamRevision
	return applyStateChange(ctx, tx, p)
}

func applyPosition(ctx context.Context, tx *sql.Tx, p Position) error {
	// Предыдущая позиция того же владельца не удаляется: она получает срок
	// действия и ссылку на пришедшую на смену. История позиций сохраняется.
	if _, err := tx.ExecContext(ctx, `
		UPDATE thread_positions
		   SET valid_until = ?, superseded_by = ?
		 WHERE thread_id = ? AND owner = ? AND valid_until IS NULL`,
		ts(p.ValidFrom), p.ID, p.ThreadID, p.Owner); err != nil {
		return fmt.Errorf("закрытие предыдущей позиции нити %s: %w", p.ThreadID, err)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO thread_positions (id, thread_id, owner, statement, confidence, basis,
		                              valid_from, valid_until, superseded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?)
		ON CONFLICT (id) DO NOTHING`,
		p.ID, p.ThreadID, p.Owner, p.Statement, p.Confidence, p.Basis,
		ts(p.ValidFrom), ts(p.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция позиции %s: %w", p.ID, err)
	}
	return nil
}

func projectPosition(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p Position
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyPosition(ctx, tx, p)
}

func applyDecision(ctx context.Context, tx *sql.Tx, d Decision) error {
	alt, err := json.Marshal(d.Alternatives)
	if err != nil {
		return fmt.Errorf("сериализация альтернатив решения %s: %w", d.ID, err)
	}
	if d.Alternatives == nil {
		alt = []byte("[]")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO thread_decisions (id, thread_id, statement, decided_by, rationale,
		                              alternatives, consequences, review_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		d.ID, d.ThreadID, d.Statement, d.DecidedBy, d.Rationale, string(alt),
		d.Consequences, tsp(d.ReviewAt), ts(d.DecidedAt))
	if err != nil {
		return fmt.Errorf("проекция решения %s: %w", d.ID, err)
	}
	return nil
}

func projectDecision(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var d Decision
	if err := env.Decode(&d); err != nil {
		return err
	}
	return applyDecision(ctx, tx, d)
}

func applyQuestionOpened(ctx context.Context, tx *sql.Tx, q Question) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO thread_questions (id, thread_id, question, asked_by, status, answer, opened_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		q.ID, q.ThreadID, q.Question, q.AskedBy, q.Status, q.Answer, ts(q.OpenedAt))
	if err != nil {
		return fmt.Errorf("проекция вопроса %s: %w", q.ID, err)
	}
	return nil
}

func projectQuestionOpened(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var q Question
	if err := env.Decode(&q); err != nil {
		return err
	}
	return applyQuestionOpened(ctx, tx, q)
}

func applyQuestionClosed(ctx context.Context, tx *sql.Tx, p questionClosePayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE thread_questions SET status = ?, answer = ?, closed_at = ? WHERE id = ?`,
		p.Status, p.Answer, ts(p.ClosedAt), p.ID)
	if err != nil {
		return fmt.Errorf("проекция закрытия вопроса %s: %w", p.ID, err)
	}
	return nil
}

func projectQuestionClosed(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p questionClosePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyQuestionClosed(ctx, tx, p)
}

func applyLink(ctx context.Context, tx *sql.Tx, l Link) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO thread_links (id, from_id, to_id, kind, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (from_id, to_id, kind) DO NOTHING`,
		l.ID, l.FromID, l.ToID, l.Kind, l.Note, ts(l.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция связи %s: %w", l.ID, err)
	}
	return nil
}

func projectLink(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var l Link
	if err := env.Decode(&l); err != nil {
		return err
	}
	return applyLink(ctx, tx, l)
}

// ---------- чтение ----------

const selectThreadColumns = `
	SELECT id, title, kind, state, summary, origin, importance, sensitivity,
	       COALESCE(workspace_id, ''), created_at, updated_at, last_meaningful_activity_at,
	       next_review_at, muted_until, released_reason, revision
	FROM threads`

type scanner interface{ Scan(dest ...any) error }

func scanThread(row scanner) (Thread, error) {
	var (
		t                                    Thread
		createdAt, updatedAt                 string
		lastActivity, nextReview, mutedUntil sql.NullString
	)
	err := row.Scan(&t.ID, &t.Title, &t.Kind, &t.State, &t.Summary, &t.Origin,
		&t.Importance, &t.Sensitivity, &t.WorkspaceID, &createdAt, &updatedAt,
		&lastActivity, &nextReview, &mutedUntil, &t.ReleasedReason, &t.Revision)
	if err != nil {
		return Thread{}, err
	}
	if t.CreatedAt, err = parseTS(createdAt); err != nil {
		return Thread{}, err
	}
	if t.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return Thread{}, err
	}
	if t.LastMeaningfulActivityAt, err = parseTSPtr(lastActivity); err != nil {
		return Thread{}, err
	}
	if t.NextReviewAt, err = parseTSPtr(nextReview); err != nil {
		return Thread{}, err
	}
	if t.MutedUntil, err = parseTSPtr(mutedUntil); err != nil {
		return Thread{}, err
	}
	return t, nil
}

func loadThread(ctx context.Context, tx *sql.Tx, id string) (Thread, error) {
	row := tx.QueryRowContext(ctx, selectThreadColumns+` WHERE id = ?`, id)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Thread{}, fmt.Errorf("чтение нити %s: %w", id, err)
	}
	return t, nil
}

// Get возвращает нить.
func (s *Service) Get(ctx context.Context, id string) (Thread, error) {
	row := s.db.Reader().QueryRowContext(ctx, selectThreadColumns+` WHERE id = ?`, id)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Thread{}, fmt.Errorf("чтение нити %s: %w", id, err)
	}
	return t, nil
}

// ListFilter — условия выборки нитей.
type ListFilter struct {
	States []string
	Kind   string
	Limit  int
}

// List возвращает нити, свежие сверху.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Thread, error) {
	query := selectThreadColumns + ` WHERE 1 = 1`
	var args []any
	if len(f.States) > 0 {
		query += ` AND state IN (` + placeholders(len(f.States)) + `)`
		for _, st := range f.States {
			args = append(args, st)
		}
	}
	if f.Kind != "" {
		query += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.Limit <= 0 {
		f.Limit = 200
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("выборка нитей: %w", err)
	}
	defer rows.Close()

	out := []Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Detail возвращает нить со всем содержимым.
func (s *Service) Detail(ctx context.Context, id string) (Detail, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Thread: t, Positions: []Position{}, Decisions: []Decision{},
		Questions: []Question{}, Links: []Link{}}

	if d.Positions, err = s.positions(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Decisions, err = s.decisions(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Questions, err = s.questions(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Links, err = s.links(ctx, id); err != nil {
		return Detail{}, err
	}
	return d, nil
}

func (s *Service) positions(ctx context.Context, threadID string) ([]Position, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, thread_id, owner, statement, confidence, basis, valid_from, valid_until,
		       COALESCE(superseded_by, ''), created_at
		  FROM thread_positions WHERE thread_id = ? ORDER BY valid_from DESC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("чтение позиций нити %s: %w", threadID, err)
	}
	defer rows.Close()

	out := []Position{}
	for rows.Next() {
		var (
			p                    Position
			validFrom, createdAt string
			validUntil           sql.NullString
		)
		if err := rows.Scan(&p.ID, &p.ThreadID, &p.Owner, &p.Statement, &p.Confidence,
			&p.Basis, &validFrom, &validUntil, &p.SupersededBy, &createdAt); err != nil {
			return nil, err
		}
		if p.ValidFrom, err = parseTS(validFrom); err != nil {
			return nil, err
		}
		if p.ValidUntil, err = parseTSPtr(validUntil); err != nil {
			return nil, err
		}
		if p.CreatedAt, err = parseTS(createdAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) decisions(ctx context.Context, threadID string) ([]Decision, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, thread_id, statement, decided_by, rationale, alternatives, consequences,
		       review_at, decided_at
		  FROM thread_decisions WHERE thread_id = ? ORDER BY decided_at DESC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("чтение решений нити %s: %w", threadID, err)
	}
	defer rows.Close()

	out := []Decision{}
	for rows.Next() {
		var (
			d         Decision
			alt       string
			reviewAt  sql.NullString
			decidedAt string
		)
		if err := rows.Scan(&d.ID, &d.ThreadID, &d.Statement, &d.DecidedBy, &d.Rationale,
			&alt, &d.Consequences, &reviewAt, &decidedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(alt), &d.Alternatives); err != nil {
			return nil, fmt.Errorf("разбор альтернатив решения %s: %w", d.ID, err)
		}
		if d.ReviewAt, err = parseTSPtr(reviewAt); err != nil {
			return nil, err
		}
		if d.DecidedAt, err = parseTS(decidedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) questions(ctx context.Context, threadID string) ([]Question, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, thread_id, question, asked_by, status, answer, opened_at, closed_at
		  FROM thread_questions WHERE thread_id = ? ORDER BY opened_at DESC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("чтение вопросов нити %s: %w", threadID, err)
	}
	defer rows.Close()

	out := []Question{}
	for rows.Next() {
		var (
			q        Question
			openedAt string
			closedAt sql.NullString
		)
		if err := rows.Scan(&q.ID, &q.ThreadID, &q.Question, &q.AskedBy, &q.Status,
			&q.Answer, &openedAt, &closedAt); err != nil {
			return nil, err
		}
		if q.OpenedAt, err = parseTS(openedAt); err != nil {
			return nil, err
		}
		if q.ClosedAt, err = parseTSPtr(closedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Service) links(ctx context.Context, threadID string) ([]Link, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, from_id, to_id, kind, note, created_at
		  FROM thread_links WHERE from_id = ? OR to_id = ? ORDER BY created_at`,
		threadID, threadID)
	if err != nil {
		return nil, fmt.Errorf("чтение связей нити %s: %w", threadID, err)
	}
	defer rows.Close()

	out := []Link{}
	for rows.Next() {
		var (
			l         Link
			createdAt string
		)
		if err := rows.Scan(&l.ID, &l.FromID, &l.ToID, &l.Kind, &l.Note, &createdAt); err != nil {
			return nil, err
		}
		if l.CreatedAt, err = parseTS(createdAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Timeline возвращает события нити в хронологическом порядке.
func (s *Service) Timeline(ctx context.Context, threadID string) ([]event.Envelope, error) {
	return s.journal.Stream(ctx, StreamType, threadID)
}

// ---------- вспомогательное ----------

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func strp(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
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

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*2-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}
