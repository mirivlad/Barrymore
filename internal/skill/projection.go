package skill

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

type retiredPayload struct {
	SkillID string    `json:"skill_id"`
	Why     string    `json:"why"`
	At      time.Time `json:"at"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

func applyRun(ctx context.Context, tx *sql.Tx, r Run) error {
	steps, err := json.Marshal(r.Steps)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO skill_runs (id, skill_id, skill_title, target, thread_id,
		    conversation_id, status, answer, failure, steps, started_at,
		    finished_at, took_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		r.ID, r.SkillID, r.SkillTitle, r.Target, r.ThreadID, r.ConversationID,
		r.Status, r.Answer, r.Failure, string(steps), ts(r.StartedAt),
		ts(r.FinishedAt), r.TookMS)
	if err != nil {
		return fmt.Errorf("проекция применения умения %s: %w", r.ID, err)
	}
	return nil
}

func projectRun(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var r Run
	if err := env.Decode(&r); err != nil {
		return err
	}
	return applyRun(ctx, tx, r)
}

func applyLearned(ctx context.Context, tx *sql.Tx, sk Skill) error {
	steps, err := json.Marshal(sk.Steps)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO skills (id, title, question, needs_target, steps, origin,
		    enabled, retired_why)
		VALUES (?, ?, ?, ?, ?, ?, 1, '')
		ON CONFLICT (id) DO UPDATE SET
		    title = excluded.title, question = excluded.question,
		    needs_target = excluded.needs_target, steps = excluded.steps`,
		sk.ID, sk.Title, sk.Question, sk.NeedsTarget, string(steps), sk.Origin)
	if err != nil {
		return fmt.Errorf("проекция умения %s: %w", sk.ID, err)
	}
	return nil
}

func projectLearned(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var sk Skill
	if err := env.Decode(&sk); err != nil {
		return err
	}
	return applyLearned(ctx, tx, sk)
}

// applyRetired помнит и снятые встроенные умения.
//
// Встроенного умения в таблице может не быть вовсе — оно живёт в коде.
// Поэтому запись создаётся при необходимости: иначе причина снятия пропала бы
// вместе с перезапуском, и умение вернулось бы само.
func applyRetired(ctx context.Context, tx *sql.Tx, p retiredPayload) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO skills (id, title, question, needs_target, steps, origin,
		    enabled, retired_why)
		VALUES (?, '', '', 0, '[]', ?, 0, ?)
		ON CONFLICT (id) DO UPDATE SET enabled = 0, retired_why = excluded.retired_why`,
		p.SkillID, OriginBuiltin, p.Why)
	if err != nil {
		return fmt.Errorf("проекция снятия умения %s: %w", p.SkillID, err)
	}
	return nil
}

func projectRetired(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p retiredPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyRetired(ctx, tx, p)
}

// storedSkills читает умения из проекции.
func (s *Service) storedSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, title, question, needs_target, steps, origin, enabled, retired_why
		FROM skills ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("чтение умений: %w", err)
	}
	defer rows.Close()

	out := []Skill{}
	for rows.Next() {
		var (
			sk    Skill
			steps string
		)
		if err := rows.Scan(&sk.ID, &sk.Title, &sk.Question, &sk.NeedsTarget,
			&steps, &sk.Origin, &sk.Enabled, &sk.RetiredWhy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(steps), &sk.Steps); err != nil {
			return nil, fmt.Errorf("шаги умения %s: %w", sk.ID, err)
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// Runs возвращает последние применения умений.
func (s *Service) Runs(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, skill_id, skill_title, target, thread_id, conversation_id,
		       status, answer, failure, steps, started_at, finished_at, took_ms
		FROM skill_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение применений: %w", err)
	}
	defer rows.Close()

	out := []Run{}
	for rows.Next() {
		var (
			r                     Run
			steps                 string
			startedAt, finishedAt string
		)
		if err := rows.Scan(&r.ID, &r.SkillID, &r.SkillTitle, &r.Target, &r.ThreadID,
			&r.ConversationID, &r.Status, &r.Answer, &r.Failure, &steps,
			&startedAt, &finishedAt, &r.TookMS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(steps), &r.Steps); err != nil {
			return nil, err
		}
		if r.StartedAt, err = parseTS(startedAt); err != nil {
			return nil, err
		}
		if r.FinishedAt, err = parseTS(finishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
