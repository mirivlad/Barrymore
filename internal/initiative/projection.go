package initiative

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

func applyNotice(ctx context.Context, tx *sql.Tx, p noticePayload) error {
	n := p.Notice
	_, err := tx.ExecContext(ctx, `
		INSERT INTO initiative_notices (id, kind, subject_type, subject_id, level,
		    title, why, status, created_at, deliver_at, dedupe_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (dedupe_key) DO NOTHING`,
		n.ID, n.Kind, n.SubjectType, n.SubjectID, n.Level, n.Title, n.Why,
		n.Status, ts(n.CreatedAt), ts(n.DeliverAt), n.DedupeKey)
	if err != nil {
		return fmt.Errorf("проекция обращения %s: %w", n.ID, err)
	}
	return nil
}

func projectNotice(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p noticePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyNotice(ctx, tx, p)
}

func applyStatus(ctx context.Context, tx *sql.Tx, p statusPayload) error {
	var err error
	switch p.Status {
	case StatusDelivered:
		_, err = tx.ExecContext(ctx,
			`UPDATE initiative_notices SET status = ?, delivered_at = ? WHERE id = ?`,
			p.Status, ts(p.At), p.NoticeID)
	case StatusRead:
		_, err = tx.ExecContext(ctx,
			`UPDATE initiative_notices SET status = ?, read_at = ? WHERE id = ?`,
			p.Status, ts(p.At), p.NoticeID)
	default:
		_, err = tx.ExecContext(ctx,
			`UPDATE initiative_notices SET status = ? WHERE id = ?`, p.Status, p.NoticeID)
	}
	if err != nil {
		return fmt.Errorf("проекция состояния обращения %s: %w", p.NoticeID, err)
	}
	return nil
}

func projectStatus(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p statusPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyStatus(ctx, tx, p)
}

const selectNoticeColumns = `
	SELECT id, kind, subject_type, subject_id, level, title, why, status,
	       created_at, deliver_at, delivered_at, read_at, dedupe_key
	FROM initiative_notices`

type scanner interface{ Scan(dest ...any) error }

func scanNotice(row scanner) (Notice, error) {
	var (
		n                       Notice
		createdAt, deliverAt    string
		deliveredAt, readAtNull sql.NullString
	)
	err := row.Scan(&n.ID, &n.Kind, &n.SubjectType, &n.SubjectID, &n.Level,
		&n.Title, &n.Why, &n.Status, &createdAt, &deliverAt,
		&deliveredAt, &readAtNull, &n.DedupeKey)
	if err != nil {
		return Notice{}, err
	}
	if n.CreatedAt, err = parseTS(createdAt); err != nil {
		return Notice{}, err
	}
	if n.DeliverAt, err = parseTS(deliverAt); err != nil {
		return Notice{}, err
	}
	if n.DeliveredAt, err = parseTSPtr(deliveredAt); err != nil {
		return Notice{}, err
	}
	if n.ReadAt, err = parseTSPtr(readAtNull); err != nil {
		return Notice{}, err
	}
	return n, nil
}

func (s *Service) byStatus(ctx context.Context, status string, limit int) ([]Notice, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		selectNoticeColumns+` WHERE status = ? ORDER BY deliver_at LIMIT ?`, status, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение обращений: %w", err)
	}
	defer rows.Close()

	out := []Notice{}
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// List отдаёт обращения для показа, свежие сверху.
func (s *Service) List(ctx context.Context, limit int) ([]Notice, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		selectNoticeColumns+` ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение обращений: %w", err)
	}
	defer rows.Close()

	out := []Notice{}
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) hasNotice(ctx context.Context, dedupeKey string) (bool, error) {
	var one int
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT 1 FROM initiative_notices WHERE dedupe_key = ?`, dedupeKey).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("проверка повтора обращения: %w", err)
	}
	return true, nil
}

func (s *Service) byDedupeKey(ctx context.Context, key string) (Notice, error) {
	row := s.db.Reader().QueryRowContext(ctx,
		selectNoticeColumns+` WHERE dedupe_key = ?`, key)
	n, err := scanNotice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Notice{}, ErrNotFound
	}
	if err != nil {
		return Notice{}, fmt.Errorf("чтение обращения: %w", err)
	}
	return n, nil
}

// deliveredToday считает обращения за текущие сутки.
//
// Считаются именно показанные: удержанные не расходуют лимит, иначе один
// шумный день заглушил бы следующий.
func (s *Service) deliveredToday(ctx context.Context, now time.Time) (int, error) {
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var n int
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM initiative_notices WHERE delivered_at >= ?`, ts(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("подсчёт обращений за сутки: %w", err)
	}
	return n, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("разбор времени %q: %w", s, err)
	}
	return t.UTC(), nil
}

func parseTSPtr(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := parseTS(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
