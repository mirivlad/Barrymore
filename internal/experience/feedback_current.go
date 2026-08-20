package experience

import (
	"context"
	"database/sql"
	"errors"
)

// CurrentFeedback returns the latest explicit owner judgement in projection
// insertion order. Projection rebuild replays journal events in that same
// order, so this remains stable even when several feedback events share one
// timestamp. Absence of feedback is represented by (nil, nil), not a neutral
// synthetic record.
func (s *Service) CurrentFeedback(ctx context.Context, episodeID string) (*Feedback, error) {
	var fb Feedback
	var created string
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT id, episode_id, value, note, actor_type, actor_id, created_at
		  FROM experience_feedback
		 WHERE episode_id = ?
		 ORDER BY rowid DESC LIMIT 1`, episodeID).
		Scan(&fb.ID, &fb.EpisodeID, &fb.Value, &fb.Note, &fb.ActorType, &fb.ActorID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fb.CreatedAt, err = parseTS(created)
	if err != nil {
		return nil, err
	}
	return &fb, nil
}
