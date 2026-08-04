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

func applyMessage(ctx context.Context, tx *sql.Tx, m Message) error {
	trace, _ := json.Marshal(orEmpty(m.RetrievalTrace))
	_, err := tx.ExecContext(ctx, `
		INSERT INTO messages (id, conversation_id, thread_id, role, content, provider, model,
		                      prompt_tokens, output_tokens, latency_ms, retrieval_trace, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		m.ID, m.ConversationID, nullable(m.ThreadID), m.Role, m.Content, m.Provider, m.Model,
		m.PromptTokens, m.OutputTokens, m.LatencyMS, string(trace), ts(m.CreatedAt))
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
		SELECT id, conversation_id, COALESCE(thread_id, ''), role, content, provider, model,
		       prompt_tokens, output_tokens, latency_ms, retrieval_trace, created_at
		  FROM messages WHERE conversation_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, conversationID, limit)
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
			&trace, &createdAt); err != nil {
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
