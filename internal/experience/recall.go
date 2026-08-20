package experience

import (
	"context"
	"fmt"

	"github.com/mirivlad/barrymore/internal/retrieval"
)

// RecalledEpisode contains the old result together with its provenance and
// explicit owner feedback. The caller must still decide whether the result is
// fresh enough to reuse.
type RecalledEpisode struct {
	Episode  Episode    `json:"episode"`
	Sources  []Source   `json:"sources,omitempty"`
	Feedback []Feedback `json:"feedback,omitempty"`
	Rank     float64    `json:"rank"`
}

// RecalledProcedure is procedural memory: a known route for finding or
// changing something again. It is useful even when the old result is stale.
type RecalledProcedure struct {
	Procedure Procedure `json:"procedure"`
	Rank      float64   `json:"rank"`
}

type RecallResult struct {
	Episodes   []RecalledEpisode   `json:"episodes,omitempty"`
	Procedures []RecalledProcedure `json:"procedures,omitempty"`
}

// Recall searches episodes, their sources and procedures with one natural
// language question. Source hits are folded back into their parent episode so
// the model sees an episode, not disconnected evidence fragments.
func (s *Service) Recall(ctx context.Context, question string, limit int) (RecallResult, error) {
	match := retrieval.FTS(question, 8)
	if match == "" {
		return RecallResult{}, nil
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT entity_type, entity_id, bm25(experience_fts)
		  FROM experience_fts
		 WHERE experience_fts MATCH ?
		 ORDER BY bm25(experience_fts)
		 LIMIT ?`, match, limit*3)
	if err != nil {
		return RecallResult{}, fmt.Errorf("recall опыта: %w", err)
	}
	type rawHit struct {
		kind string
		id   string
		rank float64
	}
	raw := []rawHit{}
	for rows.Next() {
		var h rawHit
		if err := rows.Scan(&h.kind, &h.id, &h.rank); err != nil {
			rows.Close()
			return RecallResult{}, err
		}
		raw = append(raw, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RecallResult{}, err
	}
	rows.Close()

	out := RecallResult{}
	seenEpisodes := map[string]bool{}
	seenProcedures := map[string]bool{}
	for _, h := range raw {
		switch h.kind {
		case "episode":
			if seenEpisodes[h.id] || len(out.Episodes) >= limit {
				continue
			}
			recalled, err := s.recalledEpisode(ctx, h.id, h.rank)
			if err != nil {
				return RecallResult{}, err
			}
			seenEpisodes[h.id] = true
			out.Episodes = append(out.Episodes, recalled)
		case "source":
			var episodeID string
			if err := s.db.Reader().QueryRowContext(ctx,
				`SELECT episode_id FROM experience_sources WHERE id = ?`, h.id).Scan(&episodeID); err != nil {
				return RecallResult{}, err
			}
			if seenEpisodes[episodeID] || len(out.Episodes) >= limit {
				continue
			}
			recalled, err := s.recalledEpisode(ctx, episodeID, h.rank)
			if err != nil {
				return RecallResult{}, err
			}
			seenEpisodes[episodeID] = true
			out.Episodes = append(out.Episodes, recalled)
		case "procedure":
			if seenProcedures[h.id] || len(out.Procedures) >= limit {
				continue
			}
			p, err := s.Procedure(ctx, h.id)
			if err != nil {
				return RecallResult{}, err
			}
			seenProcedures[h.id] = true
			out.Procedures = append(out.Procedures, RecalledProcedure{Procedure: p, Rank: h.rank})
		}
	}
	return out, nil
}

func (s *Service) recalledEpisode(ctx context.Context, id string, rank float64) (RecalledEpisode, error) {
	ep, err := s.Episode(ctx, id)
	if err != nil {
		return RecalledEpisode{}, err
	}
	sources, err := s.Sources(ctx, id)
	if err != nil {
		return RecalledEpisode{}, err
	}
	feedback, err := s.Feedback(ctx, id)
	if err != nil {
		return RecalledEpisode{}, err
	}
	return RecalledEpisode{Episode: ep, Sources: sources, Feedback: feedback, Rank: rank}, nil
}
