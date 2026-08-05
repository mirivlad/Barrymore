package learning

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

type stalePayload struct {
	PracticeID string    `json:"practice_id"`
	Why        string    `json:"why"`
	At         time.Time `json:"at"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

// applyOutcome накапливает практику.
//
// Среднее считается сразу и на месте: хранить все исходы ради него значило бы
// держать журнал дважды, а журнал уже есть.
func applyOutcome(ctx context.Context, tx *sql.Tx, o Outcome) error {
	good, bad, streak := 1, 0, 0
	if o.Result == OutcomeBad {
		good, bad, streak = 0, 1, 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO practices (id, kind, ref, title, question, applied, succeeded,
		    failed, streak, avg_ms, last_at, last_outcome, last_note, stale, stale_why)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, 0, '')
		ON CONFLICT (id) DO UPDATE SET
		    title = CASE WHEN excluded.title != '' THEN excluded.title ELSE practices.title END,
		    question = CASE WHEN excluded.question != '' THEN excluded.question ELSE practices.question END,
		    applied = practices.applied + 1,
		    succeeded = practices.succeeded + excluded.succeeded,
		    failed = practices.failed + excluded.failed,
		    streak = CASE WHEN excluded.failed = 1 THEN practices.streak + 1 ELSE 0 END,
		    avg_ms = (practices.avg_ms * practices.applied + excluded.avg_ms)
		             / (practices.applied + 1),
		    last_at = excluded.last_at,
		    last_outcome = excluded.last_outcome,
		    last_note = excluded.last_note`,
		key(o.Kind, o.Ref), o.Kind, o.Ref, o.Title, o.Question,
		good, bad, streak, o.TookMS, ts(o.At), o.Result, o.Evidence)
	if err != nil {
		return fmt.Errorf("проекция исхода %s: %w", o.Ref, err)
	}
	return nil
}

func projectOutcome(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var o Outcome
	if err := env.Decode(&o); err != nil {
		return err
	}
	return applyOutcome(ctx, tx, o)
}

func applyStale(ctx context.Context, tx *sql.Tx, p stalePayload) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE practices SET stale = 1, stale_why = ? WHERE id = ?`, p.Why, p.PracticeID)
	if err != nil {
		return fmt.Errorf("проекция негодного способа %s: %w", p.PracticeID, err)
	}
	return nil
}

func projectStale(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p stalePayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyStale(ctx, tx, p)
}

const selectPractice = `
	SELECT id, kind, ref, title, question, applied, succeeded, failed, streak,
	       avg_ms, last_at, last_outcome, last_note, stale, stale_why
	FROM practices`

func scanPractice(row interface{ Scan(...any) error }) (Practice, error) {
	var (
		p      Practice
		lastAt string
	)
	err := row.Scan(&p.ID, &p.Kind, &p.Ref, &p.Title, &p.Question, &p.Applied,
		&p.Succeeded, &p.Failed, &p.Streak, &p.AvgMS, &lastAt, &p.LastOutcome,
		&p.LastNote, &p.Stale, &p.StaleWhy)
	if err != nil {
		return Practice{}, err
	}
	p.LastAt, err = parseTS(lastAt)
	return p, err
}

// Practice возвращает одну практику.
func (s *Service) Practice(ctx context.Context, id string) (Practice, error) {
	row := s.db.Reader().QueryRowContext(ctx, selectPractice+` WHERE id = ?`, id)
	p, err := scanPractice(row)
	if err != nil {
		return Practice{}, fmt.Errorf("практика %s: %w", id, err)
	}
	return p, nil
}

// Practices возвращает всё, чему научил опыт.
func (s *Service) Practices(ctx context.Context) ([]Practice, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		selectPractice+` ORDER BY applied DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("чтение практик: %w", err)
	}
	defer rows.Close()

	out := []Practice{}
	for rows.Next() {
		p, err := scanPractice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
