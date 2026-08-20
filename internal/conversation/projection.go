package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

func applyConversation(ctx context.Context, tx *sql.Tx, c Conversation) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversations (id, title, thread_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Title, nullable(c.ThreadID), ts(c.CreatedAt), ts(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("проекция разговора %s: %w", c.ID, err)
	}
	return nil
}

func projectConversation(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var c Conversation
	if err := env.Decode(&c); err != nil {
		return err
	}
	return applyConversation(ctx, tx, c)
}

// applyThreadLink переносит связь разговора с нитью.
//
// Прошлые сообщения получают ту же нить: разговор целиком относится к ней,
// и половина реплик, оставшаяся без нити, выпала бы из её ленты.
func applyThreadLink(ctx context.Context, tx *sql.Tx, p threadLinkPayload) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE conversations SET thread_id = ?, updated_at = ? WHERE id = ?`,
		nullable(p.ThreadID), ts(p.At), p.ConversationID); err != nil {
		return fmt.Errorf("связь разговора %s с нитью: %w", p.ConversationID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET thread_id = ? WHERE conversation_id = ?`,
		nullable(p.ThreadID), p.ConversationID); err != nil {
		return fmt.Errorf("перенос сообщений разговора %s: %w", p.ConversationID, err)
	}
	return nil
}

func projectThreadLink(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p threadLinkPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyThreadLink(ctx, tx, p)
}

// deriveEpisodeID correlates a Barrymore reply with the completed Episode that
// immediately produced it. The message event itself need not know the ID:
// projections replay Episode completion before the final reply in the same
// order as the live turn. Matching result + conversation avoids attaching an
// unrelated episode; ordering disambiguates repeated identical answers.
func deriveEpisodeID(ctx context.Context, tx *sql.Tx, m Message) (string, error) {
	if m.EpisodeID != "" || m.Role != RoleBarrymore {
		return m.EpisodeID, nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM episodes
		 WHERE conversation_id = ? AND status = 'completed' AND result = ?
		   AND finished_at <= ?
		 ORDER BY finished_at DESC, id DESC LIMIT 1`,
		m.ConversationID, m.Content, ts(m.CreatedAt)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("поиск episode для сообщения %s: %w", m.ID, err)
	}
	return id, nil
}

func applyMessage(ctx context.Context, tx *sql.Tx, m Message) error {
	trace, _ := json.Marshal(orEmpty(m.RetrievalTrace))
	episodeID, err := deriveEpisodeID(ctx, tx, m)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages (id, conversation_id, thread_id, role, content, provider, model,
		                      prompt_tokens, output_tokens, latency_ms, episode_id, retrieval_trace, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		m.ID, m.ConversationID, nullable(m.ThreadID), m.Role, m.Content, m.Provider, m.Model,
		m.PromptTokens, m.OutputTokens, m.LatencyMS, episodeID, string(trace), ts(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("проекция сообщения %s: %w", m.ID, err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE conversations SET updated_at = ? WHERE id = ?`, ts(m.CreatedAt), m.ConversationID)
	if err != nil {
		return fmt.Errorf("отметка активности разговора %s: %w", m.ConversationID, err)
	}
	return nil
}

func projectMessage(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var m Message
	if err := env.Decode(&m); err != nil {
		return err
	}
	return applyMessage(ctx, tx, m)
}

// ---------- чтение ----------

const selectConversationColumns = `
	SELECT id, title, COALESCE(thread_id, ''), created_at, updated_at FROM conversations`

// Get возвращает разговор.
func (s *Service) Get(ctx context.Context, id string) (Conversation, error) {
	var (
		c                    Conversation
		createdAt, updatedAt string
	)
	err := s.db.Reader().QueryRowContext(ctx, selectConversationColumns+` WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &c.ThreadID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("чтение разговора %s: %w", id, err)
	}
	if c.CreatedAt, err = parseTS(createdAt); err != nil {
		return Conversation{}, err
	}
	if c.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return Conversation{}, err
	}
	return c, nil
}

// List возвращает разговоры, свежие сверху.
func (s *Service) List(ctx context.Context, limit int) ([]Conversation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		selectConversationColumns+` ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение разговоров: %w", err)
	}
	defer rows.Close()

	out := []Conversation{}
	for rows.Next() {
		var (
			c                    Conversation
			createdAt, updatedAt string
		)
		if err := rows.Scan(&c.ID, &c.Title, &c.ThreadID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if c.CreatedAt, err = parseTS(createdAt); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTS(updatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Messages возвращает последние реплики разговора в хронологическом порядке.
func (s *Service) Messages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT m.id, m.conversation_id, COALESCE(m.thread_id, ''), m.role, m.content, m.provider, m.model,
		       m.prompt_tokens, m.output_tokens, m.latency_ms, m.episode_id,
		       COALESCE((
		           SELECT f.value FROM experience_feedback f
		            WHERE f.episode_id = m.episode_id
		            ORDER BY f.created_at DESC, f.id DESC LIMIT 1
		       ), ''),
		       m.retrieval_trace, m.created_at
		  FROM messages m WHERE m.conversation_id = ?
		 ORDER BY m.created_at DESC, m.id DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение сообщений %s: %w", conversationID, err)
	}
	defer rows.Close()

	var reversed []Message
	for rows.Next() {
		var (
			m         Message
			trace     string
			createdAt string
		)
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.ThreadID, &m.Role, &m.Content,
			&m.Provider, &m.Model, &m.PromptTokens, &m.OutputTokens, &m.LatencyMS,
			&m.EpisodeID, &m.Feedback, &trace, &createdAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(trace), &m.RetrievalTrace)
		if m.CreatedAt, err = parseTS(createdAt); err != nil {
			return nil, err
		}
		reversed = append(reversed, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Message, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		out = append(out, reversed[i])
	}
	return out, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("разбор времени %q: %w", s, err)
	}
	return t.UTC(), nil
}
