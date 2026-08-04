package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

type decisionPayload struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	At        time.Time `json:"at"`
	DecidedBy string    `json:"decided_by"`
	Note      string    `json:"note,omitempty"`
}

type revokePayload struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

func applyCandidate(ctx context.Context, tx *sql.Tx, c Candidate) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO memory_candidates (id, type, content, reason, proposed_by, thread_id,
		                               conversation_id, message_id, sensitivity, confidence,
		                               status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Type, c.Content, c.Reason, c.ProposedBy, nullable(c.ThreadID),
		nullable(c.ConversationID), nullable(c.MessageID), c.Sensitivity, c.Confidence,
		c.Status, ts(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция кандидата %s: %w", c.ID, err)
	}
	return nil
}

func projectCandidate(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var c Candidate
	if err := env.Decode(&c); err != nil {
		return err
	}
	return applyCandidate(ctx, tx, c)
}

func applyDecision(ctx context.Context, tx *sql.Tx, p decisionPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE memory_candidates
		   SET status = ?, decided_at = ?, decided_by = ?, decision_note = ?
		 WHERE id = ?`,
		p.Status, ts(p.At), p.DecidedBy, p.Note, p.ID)
	if err != nil {
		return fmt.Errorf("проекция решения по кандидату %s: %w", p.ID, err)
	}
	return nil
}

func projectDecision(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p decisionPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyDecision(ctx, tx, p)
}

func applyItem(ctx context.Context, tx *sql.Tx, i Item) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO memory_items (id, type, content, provenance, candidate_id, thread_id,
		                          sensitivity, confidence, valid_from, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		i.ID, i.Type, i.Content, marshalProvenance(i.Provenance), nullable(i.CandidateID),
		nullable(i.ThreadID), i.Sensitivity, i.Confidence, ts(i.ValidFrom), ts(i.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция записи памяти %s: %w", i.ID, err)
	}
	rowID, err := res.LastInsertId()
	if err != nil || rowID == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_fts (rowid, content) VALUES (?, ?)`, rowID, i.Content); err != nil {
		return fmt.Errorf("индексация записи памяти %s: %w", i.ID, err)
	}
	return nil
}

func projectItem(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var i Item
	if err := env.Decode(&i); err != nil {
		return err
	}
	return applyItem(ctx, tx, i)
}

// applyForget затирает содержание, оставляя надгробие.
func applyForget(ctx context.Context, tx *sql.Tx, p revokePayload) error {
	var rowID int64
	err := tx.QueryRowContext(ctx, `SELECT rowid FROM memory_items WHERE id = ?`, p.ID).Scan(&rowID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("поиск записи памяти %s: %w", p.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE memory_items
		   SET content = '[удалено владельцем]', provenance = '{}',
		       forgotten_at = ?, revoked_at = COALESCE(revoked_at, ?), revoke_reason = ?
		 WHERE id = ?`,
		ts(p.At), ts(p.At), p.Reason, p.ID); err != nil {
		return fmt.Errorf("проекция удаления записи %s: %w", p.ID, err)
	}
	if rowID != 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memory_fts WHERE rowid = ?`, rowID); err != nil {
			return fmt.Errorf("удаление записи из индекса %s: %w", p.ID, err)
		}
	}
	return nil
}

func projectForget(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p revokePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyForget(ctx, tx, p)
}

func applyRevoke(ctx context.Context, tx *sql.Tx, p revokePayload) error {
	// Запись остаётся в базе с отметкой отзыва: след того, что она была,
	// сохраняется, но из извлечения она исчезает.
	var rowID int64
	err := tx.QueryRowContext(ctx, `SELECT rowid FROM memory_items WHERE id = ?`, p.ID).Scan(&rowID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("поиск записи памяти %s: %w", p.ID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE memory_items SET revoked_at = ?, revoke_reason = ? WHERE id = ?`,
		ts(p.At), p.Reason, p.ID); err != nil {
		return fmt.Errorf("проекция отзыва записи %s: %w", p.ID, err)
	}
	if rowID != 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memory_fts WHERE rowid = ?`, rowID); err != nil {
			return fmt.Errorf("удаление записи из индекса %s: %w", p.ID, err)
		}
	}
	return nil
}

func projectRevoke(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p revokePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyRevoke(ctx, tx, p)
}

// ---------- чтение ----------

const selectCandidateColumns = `
	SELECT id, type, content, reason, proposed_by, COALESCE(thread_id, ''),
	       COALESCE(conversation_id, ''), COALESCE(message_id, ''), sensitivity, confidence,
	       status, decided_at, decided_by, decision_note, auto_decision, created_at
	FROM memory_candidates`

type scanner interface{ Scan(dest ...any) error }

func scanCandidate(row scanner) (Candidate, error) {
	var (
		c         Candidate
		decidedAt sql.NullString
		createdAt string
	)
	err := row.Scan(&c.ID, &c.Type, &c.Content, &c.Reason, &c.ProposedBy, &c.ThreadID,
		&c.ConversationID, &c.MessageID, &c.Sensitivity, &c.Confidence, &c.Status,
		&decidedAt, &c.DecidedBy, &c.DecisionNote, &c.AutoDecision, &createdAt)
	if err != nil {
		return Candidate{}, err
	}
	if c.CreatedAt, err = parseTS(createdAt); err != nil {
		return Candidate{}, err
	}
	if decidedAt.Valid && decidedAt.String != "" {
		t, err := parseTS(decidedAt.String)
		if err != nil {
			return Candidate{}, err
		}
		c.DecidedAt = &t
	}
	return c, nil
}

func candidateByID(ctx context.Context, tx *sql.Tx, id string) (Candidate, error) {
	row := tx.QueryRowContext(ctx, selectCandidateColumns+` WHERE id = ?`, id)
	c, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, fmt.Errorf("%w: кандидат %s", ErrNotFound, id)
	}
	return c, err
}

// Candidates возвращает кандидатов; при pendingOnly — только ожидающие решения.
func (s *Service) Candidates(ctx context.Context, pendingOnly bool, limit int) ([]Candidate, error) {
	if limit <= 0 {
		limit = 100
	}
	query := selectCandidateColumns
	if pendingOnly {
		query += ` WHERE status = 'pending'`
	}
	query += ` ORDER BY created_at DESC LIMIT ?`

	rows, err := s.db.Reader().QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение кандидатов: %w", err)
	}
	defer rows.Close()

	out := []Candidate{}
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const selectItemColumns = `
	SELECT id, type, content, provenance, COALESCE(candidate_id, ''), COALESCE(thread_id, ''),
	       sensitivity, confidence, valid_from, valid_until, COALESCE(superseded_by, ''),
	       revoked_at, revoke_reason, forgotten_at, created_at
	FROM memory_items`

func scanItem(row scanner) (Item, error) {
	var (
		i                                  Item
		provenance                         string
		validFrom, createdAt               string
		validUntil, revokedAt, forgottenAt sql.NullString
	)
	err := row.Scan(&i.ID, &i.Type, &i.Content, &provenance, &i.CandidateID, &i.ThreadID,
		&i.Sensitivity, &i.Confidence, &validFrom, &validUntil, &i.SupersededBy,
		&revokedAt, &i.RevokeReason, &forgottenAt, &createdAt)
	if err != nil {
		return Item{}, err
	}
	_ = json.Unmarshal([]byte(provenance), &i.Provenance)
	if i.ValidFrom, err = parseTS(validFrom); err != nil {
		return Item{}, err
	}
	if i.CreatedAt, err = parseTS(createdAt); err != nil {
		return Item{}, err
	}
	if i.ValidUntil, err = parseTSPtr(validUntil); err != nil {
		return Item{}, err
	}
	if i.RevokedAt, err = parseTSPtr(revokedAt); err != nil {
		return Item{}, err
	}
	if i.ForgottenAt, err = parseTSPtr(forgottenAt); err != nil {
		return Item{}, err
	}
	return i, nil
}

// Active возвращает действующие записи памяти.
//
// Отозванные записи не попадают в извлечение: после отзыва Бэрримор их
// не использует (сценарий B).
func (s *Service) Active(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		selectItemColumns+` WHERE revoked_at IS NULL ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение памяти: %w", err)
	}
	defer rows.Close()

	out := []Item{}
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// All возвращает все записи, включая отозванные.
func (s *Service) All(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		selectItemColumns+` ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение памяти: %w", err)
	}
	defer rows.Close()

	out := []Item{}
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Search ищет по подтверждённой памяти полнотекстовым индексом.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]Item, error) {
	if query == "" {
		return s.Active(ctx, limit)
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT m.id, m.type, m.content, m.provenance, COALESCE(m.candidate_id, ''),
		       COALESCE(m.thread_id, ''), m.sensitivity, m.confidence, m.valid_from,
		       m.valid_until, COALESCE(m.superseded_by, ''), m.revoked_at, m.revoke_reason,
		       m.forgotten_at, m.created_at
		  FROM memory_fts f JOIN memory_items m ON m.rowid = f.rowid
		 WHERE memory_fts MATCH ? AND m.revoked_at IS NULL
		 ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("поиск по памяти: %w", err)
	}
	defer rows.Close()

	out := []Item{}
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

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
