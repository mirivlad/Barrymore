package experience

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/projection"
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func replaceFTS(ctx context.Context, tx *sql.Tx, entityType, entityID, text string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM experience_fts WHERE entity_type = ? AND entity_id = ?`, entityType, entityID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO experience_fts (entity_type, entity_id, text) VALUES (?, ?, ?)`,
		entityType, entityID, strings.TrimSpace(text))
	return err
}

func applyEpisodeStarted(ctx context.Context, tx *sql.Tx, ep Episode) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO episodes (
			id, goal, scope, thread_id, conversation_id, status, outcome,
			initial_context, result, verification, started_at, finished_at,
			created_at, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, '', ?, '', '{}', ?, NULL, ?, ?)`,
		ep.ID, ep.Goal, ep.Scope, ep.ThreadID, ep.ConversationID, ep.Status,
		string(ep.InitialContext), ts(ep.StartedAt), ts(ep.CreatedAt), ts(ep.UpdatedAt))
	if err != nil {
		return fmt.Errorf("проекция начала эпизода %s: %w", ep.ID, err)
	}
	if err := replaceFTS(ctx, tx, "episode", ep.ID, ep.Goal+" "+ep.Scope); err != nil {
		return fmt.Errorf("индекс эпизода %s: %w", ep.ID, err)
	}
	return nil
}

func projectEpisodeStarted(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var ep Episode
	if err := env.Decode(&ep); err != nil {
		return err
	}
	return applyEpisodeStarted(ctx, tx, ep)
}

func sourceStability(src Source) (string, error) {
	stability := strings.TrimSpace(src.Stability)
	if stability == "" {
		// Events recorded before source freshness became explicit do not carry
		// the field. They must remain replayable after migration 0016.
		return StabilityStable, nil
	}
	switch stability {
	case StabilityImmutable, StabilityStable, StabilityVolatile, StabilityRealtime:
		return stability, nil
	default:
		return "", fmt.Errorf("неизвестная свежесть источника %q", stability)
	}
}

func applySourceRecorded(ctx context.Context, tx *sql.Tx, src Source) error {
	stability, err := sourceStability(src)
	if err != nil {
		return fmt.Errorf("проекция источника %s: %w", src.ID, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO experience_sources (
			id, episode_id, kind, locator, title, evidence, confidence, stability, observed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.ID, src.EpisodeID, src.Kind, src.Locator, src.Title, src.Evidence,
		src.Confidence, stability, ts(src.ObservedAt), ts(src.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция источника %s: %w", src.ID, err)
	}
	if err := replaceFTS(ctx, tx, "source", src.ID,
		strings.Join([]string{src.Kind, src.Title, src.Locator, src.Evidence}, " ")); err != nil {
		return fmt.Errorf("индекс источника %s: %w", src.ID, err)
	}
	return nil
}

func projectSourceRecorded(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var src Source
	if err := env.Decode(&src); err != nil {
		return err
	}
	return applySourceRecorded(ctx, tx, src)
}

func applyEpisodeCompleted(ctx context.Context, tx *sql.Tx, p completedPayload) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE episodes
		SET status = ?, outcome = ?, result = ?, verification = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`,
		EpisodeCompleted, p.Outcome, p.Result, string(p.Verification), ts(p.FinishedAt), ts(p.FinishedAt),
		p.EpisodeID, EpisodeOpen)
	if err != nil {
		return fmt.Errorf("проекция завершения эпизода %s: %w", p.EpisodeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("эпизод %s не открыт", p.EpisodeID)
	}
	var goal, scope string
	if err := tx.QueryRowContext(ctx,
		`SELECT goal, scope FROM episodes WHERE id = ?`, p.EpisodeID).Scan(&goal, &scope); err != nil {
		return err
	}
	if err := replaceFTS(ctx, tx, "episode", p.EpisodeID,
		strings.Join([]string{goal, scope, p.Result}, " ")); err != nil {
		return fmt.Errorf("индекс завершённого эпизода %s: %w", p.EpisodeID, err)
	}
	return nil
}

func projectEpisodeCompleted(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p completedPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyEpisodeCompleted(ctx, tx, p)
}

func applyProcedureSaved(ctx context.Context, tx *sql.Tx, p Procedure) error {
	preconditions, err := jsonText(p.Preconditions)
	if err != nil {
		return err
	}
	caps, err := jsonText(p.RequiredCapabilities)
	if err != nil {
		return err
	}
	verification, err := jsonText(p.Verification)
	if err != nil {
		return err
	}
	var lastUsed any
	if p.LastUsedAt != nil {
		lastUsed = ts(*p.LastUsedAt)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO procedures (
			id, intent, title, scope, source_episode_id, preconditions,
			required_capabilities, expected_result, verification,
			risk_class, status, succeeded, failed, last_used_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			intent = excluded.intent,
			title = excluded.title,
			scope = excluded.scope,
			source_episode_id = excluded.source_episode_id,
			preconditions = excluded.preconditions,
			required_capabilities = excluded.required_capabilities,
			expected_result = excluded.expected_result,
			verification = excluded.verification,
			risk_class = excluded.risk_class,
			status = excluded.status,
			updated_at = excluded.updated_at`,
		p.ID, p.Intent, p.Title, p.Scope, p.SourceEpisodeID, preconditions, caps,
		p.ExpectedResult, verification, p.RiskClass, p.Status,
		p.Succeeded, p.Failed, lastUsed, ts(p.CreatedAt), ts(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("проекция процедуры %s: %w", p.ID, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM procedure_steps WHERE procedure_id = ?`, p.ID); err != nil {
		return fmt.Errorf("очистка шагов процедуры %s: %w", p.ID, err)
	}
	insertSteps := func(rollback int, steps []Step) error {
		for i, step := range steps {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO procedure_steps (procedure_id, rollback, ordinal, capability, purpose, args)
				VALUES (?, ?, ?, ?, ?, ?)`,
				p.ID, rollback, i, step.Capability, step.Purpose, string(step.Args)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := insertSteps(0, p.Steps); err != nil {
		return fmt.Errorf("шаги процедуры %s: %w", p.ID, err)
	}
	if err := insertSteps(1, p.Rollback); err != nil {
		return fmt.Errorf("rollback процедуры %s: %w", p.ID, err)
	}

	text := strings.Join([]string{
		p.Intent, p.Title, p.Scope, strings.Join(p.Preconditions, " "),
		strings.Join(p.RequiredCapabilities, " "), p.ExpectedResult,
		strings.Join(p.Verification, " "),
	}, " ")
	if err := replaceFTS(ctx, tx, "procedure", p.ID, text); err != nil {
		return fmt.Errorf("индекс процедуры %s: %w", p.ID, err)
	}
	return nil
}

func projectProcedureSaved(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p Procedure
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyProcedureSaved(ctx, tx, p)
}

func applyFeedbackRecorded(ctx context.Context, tx *sql.Tx, fb Feedback) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO experience_feedback (
			id, episode_id, value, note, actor_type, actor_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		fb.ID, fb.EpisodeID, fb.Value, fb.Note, fb.ActorType, fb.ActorID, ts(fb.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция оценки %s: %w", fb.ID, err)
	}
	return nil
}

func projectFeedbackRecorded(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var fb Feedback
	if err := env.Decode(&fb); err != nil {
		return err
	}
	return applyFeedbackRecorded(ctx, tx, fb)
}

func applyArtifactRecorded(ctx context.Context, tx *sql.Tx, a Artifact) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO experience_artifacts (id, episode_id, name, path, kind, size, checksum, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.EpisodeID, a.Name, a.Path, a.Kind, a.Size, a.Checksum, ts(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция артефакта %s: %w", a.ID, err)
	}
	return nil
}

func projectArtifactRecorded(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var a Artifact
	if err := env.Decode(&a); err != nil {
		return err
	}
	return applyArtifactRecorded(ctx, tx, a)
}

const selectEpisode = `
	SELECT id, goal, scope, COALESCE(thread_id, ''), COALESCE(conversation_id, ''),
	       status, outcome, initial_context, result, verification, started_at,
	       COALESCE(finished_at, ''), created_at, updated_at
	FROM episodes`

func scanEpisode(row interface{ Scan(...any) error }) (Episode, error) {
	var ep Episode
	var initial, verification, started, finished, created, updated string
	if err := row.Scan(&ep.ID, &ep.Goal, &ep.Scope, &ep.ThreadID, &ep.ConversationID,
		&ep.Status, &ep.Outcome, &initial, &ep.Result, &verification, &started,
		&finished, &created, &updated); err != nil {
		return Episode{}, err
	}
	ep.InitialContext = json.RawMessage(initial)
	ep.Verification = json.RawMessage(verification)
	var err error
	if ep.StartedAt, err = parseTS(started); err != nil {
		return Episode{}, err
	}
	if ep.CreatedAt, err = parseTS(created); err != nil {
		return Episode{}, err
	}
	if ep.UpdatedAt, err = parseTS(updated); err != nil {
		return Episode{}, err
	}
	if finished != "" {
		t, err := parseTS(finished)
		if err != nil {
			return Episode{}, err
		}
		ep.FinishedAt = &t
	}
	return ep, nil
}

func (s *Service) Episode(ctx context.Context, id string) (Episode, error) {
	ep, err := scanEpisode(s.db.Reader().QueryRowContext(ctx, selectEpisode+` WHERE id = ?`, id))
	if err != nil {
		return Episode{}, fmt.Errorf("эпизод %s: %w", id, err)
	}
	return ep, nil
}

func (s *Service) Sources(ctx context.Context, episodeID string) ([]Source, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, episode_id, kind, locator, title, evidence, confidence, stability, observed_at, created_at
		FROM experience_sources WHERE episode_id = ? ORDER BY observed_at, id`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		var src Source
		var observed, created string
		if err := rows.Scan(&src.ID, &src.EpisodeID, &src.Kind, &src.Locator, &src.Title,
			&src.Evidence, &src.Confidence, &src.Stability, &observed, &created); err != nil {
			return nil, err
		}
		if src.ObservedAt, err = parseTS(observed); err != nil {
			return nil, err
		}
		if src.CreatedAt, err = parseTS(created); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

const selectProcedure = `
	SELECT id, intent, title, scope, COALESCE(source_episode_id, ''), preconditions,
	       required_capabilities, expected_result, verification, risk_class, status,
	       succeeded, failed, COALESCE(last_used_at, ''), created_at, updated_at
	FROM procedures`

func scanProcedure(row interface{ Scan(...any) error }) (Procedure, error) {
	var p Procedure
	var preconditions, caps, verification, lastUsed, created, updated string
	if err := row.Scan(&p.ID, &p.Intent, &p.Title, &p.Scope, &p.SourceEpisodeID,
		&preconditions, &caps, &p.ExpectedResult, &verification, &p.RiskClass,
		&p.Status, &p.Succeeded, &p.Failed, &lastUsed, &created, &updated); err != nil {
		return Procedure{}, err
	}
	if err := json.Unmarshal([]byte(preconditions), &p.Preconditions); err != nil {
		return Procedure{}, err
	}
	if err := json.Unmarshal([]byte(caps), &p.RequiredCapabilities); err != nil {
		return Procedure{}, err
	}
	if err := json.Unmarshal([]byte(verification), &p.Verification); err != nil {
		return Procedure{}, err
	}
	var err error
	if p.CreatedAt, err = parseTS(created); err != nil {
		return Procedure{}, err
	}
	if p.UpdatedAt, err = parseTS(updated); err != nil {
		return Procedure{}, err
	}
	if lastUsed != "" {
		t, err := parseTS(lastUsed)
		if err != nil {
			return Procedure{}, err
		}
		p.LastUsedAt = &t
	}
	return p, nil
}

func (s *Service) loadProcedureSteps(ctx context.Context, p *Procedure) error {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT rollback, ordinal, capability, purpose, args
		FROM procedure_steps WHERE procedure_id = ? ORDER BY rollback, ordinal`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rollback, ordinal int
		var step Step
		var args string
		if err := rows.Scan(&rollback, &ordinal, &step.Capability, &step.Purpose, &args); err != nil {
			return err
		}
		_ = ordinal
		step.Args = json.RawMessage(args)
		if rollback == 0 {
			p.Steps = append(p.Steps, step)
		} else {
			p.Rollback = append(p.Rollback, step)
		}
	}
	return rows.Err()
}

func (s *Service) Procedure(ctx context.Context, id string) (Procedure, error) {
	p, err := scanProcedure(s.db.Reader().QueryRowContext(ctx, selectProcedure+` WHERE id = ?`, id))
	if err != nil {
		return Procedure{}, fmt.Errorf("процедура %s: %w", id, err)
	}
	if err := s.loadProcedureSteps(ctx, &p); err != nil {
		return Procedure{}, fmt.Errorf("шаги процедуры %s: %w", id, err)
	}
	return p, nil
}

func (s *Service) ProceduresByIntent(ctx context.Context, intent string, limit int) ([]Procedure, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		selectProcedure+` WHERE intent = ? AND status = ? ORDER BY updated_at DESC LIMIT ?`,
		strings.TrimSpace(intent), ProcedureActive, limit)
	if err != nil {
		return nil, err
	}
	out := []Procedure{}
	for rows.Next() {
		p, err := scanProcedure(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := s.loadProcedureSteps(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) Feedback(ctx context.Context, episodeID string) ([]Feedback, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, episode_id, value, note, actor_type, actor_id, created_at
		FROM experience_feedback WHERE episode_id = ? ORDER BY created_at, id`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		var fb Feedback
		var created string
		if err := rows.Scan(&fb.ID, &fb.EpisodeID, &fb.Value, &fb.Note,
			&fb.ActorType, &fb.ActorID, &created); err != nil {
			return nil, err
		}
		if fb.CreatedAt, err = parseTS(created); err != nil {
			return nil, err
		}
		out = append(out, fb)
	}
	return out, rows.Err()
}

func (s *Service) Artifacts(ctx context.Context, episodeID string) ([]Artifact, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, episode_id, name, path, kind, size, checksum, created_at
		FROM experience_artifacts WHERE episode_id = ? ORDER BY created_at, id`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		var created string
		if err := rows.Scan(&a.ID, &a.EpisodeID, &a.Name, &a.Path, &a.Kind,
			&a.Size, &a.Checksum, &created); err != nil {
			return nil, err
		}
		if a.CreatedAt, err = parseTS(created); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) Search(ctx context.Context, q string, limit int) ([]SearchHit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	match := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT entity_type, entity_id, bm25(experience_fts)
		FROM experience_fts WHERE experience_fts MATCH ? ORDER BY bm25(experience_fts) LIMIT ?`,
		match, limit)
	if err != nil {
		return nil, fmt.Errorf("поиск по опыту: %w", err)
	}
	defer rows.Close()
	out := []SearchHit{}
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.EntityType, &hit.EntityID, &hit.Rank); err != nil {
			return nil, err
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

func (s *Service) Projections(reg *projection.Registry) {
	reg.On(EvEpisodeStarted, projectEpisodeStarted)
	reg.On(EvSourceRecorded, projectSourceRecorded)
	reg.On(EvEpisodeCompleted, projectEpisodeCompleted)
	reg.On(EvProcedureSaved, projectProcedureSaved)
	reg.On(EvFeedbackRecorded, projectFeedbackRecorded)
	reg.On(EvArtifactRecorded, projectArtifactRecorded)
	reg.Tables("episodes", "experience_sources", "procedures", "procedure_steps",
		"experience_feedback", "experience_artifacts", "experience_fts")
}
