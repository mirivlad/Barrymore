package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/retrieval"
)

// RecallItem is a memory candidate retrieved for one concrete question.
// Freshness is deliberately returned next to content: an old realtime fact is
// useful as a hint about how to investigate, not as today's answer.
type RecallItem struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Content     string     `json:"content"`
	Provenance  Provenance `json:"provenance"`
	ThreadID    string     `json:"thread_id,omitempty"`
	Sensitivity string     `json:"sensitivity"`
	Confidence  float64    `json:"confidence"`
	Stability   string     `json:"stability"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
	ValidFrom   time.Time  `json:"valid_from"`
	CreatedAt   time.Time  `json:"created_at"`
	Rank        float64    `json:"rank"`
}

// Recall searches only memory relevant to the current question. It does not
// return "the latest N memories" as a substitute for relevance.
func (s *Service) Recall(ctx context.Context, question string, limit int) ([]RecallItem, error) {
	match := retrieval.FTS(question, 8)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 12
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT m.id, m.type, m.content, m.provenance, COALESCE(m.thread_id, ''),
		       m.sensitivity, m.confidence, m.stability, COALESCE(m.verified_at, ''),
		       m.valid_from, m.created_at, bm25(memory_fts)
		  FROM memory_fts
		  JOIN memory_items m ON m.rowid = memory_fts.rowid
		 WHERE memory_fts MATCH ?
		   AND m.revoked_at IS NULL
		   AND m.forgotten_at IS NULL
		 ORDER BY bm25(memory_fts), m.created_at DESC
		 LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("recall памяти: %w", err)
	}
	defer rows.Close()

	out := []RecallItem{}
	for rows.Next() {
		var r RecallItem
		var provenance string
		var verified, valid, created string
		if err := rows.Scan(&r.ID, &r.Type, &r.Content, &provenance, &r.ThreadID,
			&r.Sensitivity, &r.Confidence, &r.Stability, &verified,
			&valid, &created, &r.Rank); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(provenance), &r.Provenance)
		if r.ValidFrom, err = parseTS(valid); err != nil {
			return nil, err
		}
		if r.CreatedAt, err = parseTS(created); err != nil {
			return nil, err
		}
		if verified != "" {
			t, err := parseTS(verified)
			if err != nil {
				return nil, err
			}
			r.VerifiedAt = &t
		}
		if r.Stability == "" {
			r.Stability = "stable"
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
