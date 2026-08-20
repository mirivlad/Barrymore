package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
)

func applyTurnRun(ctx context.Context, tx *sql.Tx, run TurnRun) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_turn_runs (
			id, conversation_id, thread_id, user_message_id, reply_message_id,
			status, stage, stage_label, provider, model, prompt_tokens, output_tokens,
			prompt_ms, generation_ms, prompt_tokens_per_second,
			generation_tokens_per_second, total_latency_ms, error_code, error_message,
			result_json, created_at, started_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			reply_message_id=excluded.reply_message_id, status=excluded.status,
			stage=excluded.stage, stage_label=excluded.stage_label,
			provider=excluded.provider, model=excluded.model,
			prompt_tokens=excluded.prompt_tokens, output_tokens=excluded.output_tokens,
			prompt_ms=excluded.prompt_ms, generation_ms=excluded.generation_ms,
			prompt_tokens_per_second=excluded.prompt_tokens_per_second,
			generation_tokens_per_second=excluded.generation_tokens_per_second,
			total_latency_ms=excluded.total_latency_ms, error_code=excluded.error_code,
			error_message=excluded.error_message, result_json=excluded.result_json,
			started_at=excluded.started_at, updated_at=excluded.updated_at,
			finished_at=excluded.finished_at`,
		run.ID, run.ConversationID, nullable(run.ThreadID), run.UserMessageID,
		nullable(run.ReplyMessageID), run.Status, run.Stage, run.StageLabel,
		run.Provider, run.Model, run.PromptTokens, run.OutputTokens, run.PromptMS,
		run.GenerationMS, run.PromptTokensPerSecond, run.GenerationTokensPerSecond,
		run.TotalLatencyMS, run.ErrorCode, run.ErrorMessage, encodeTurnResult(run.Result),
		ts(run.CreatedAt), nullableTime(run.StartedAt), ts(run.UpdatedAt), nullableTime(run.FinishedAt))
	if err != nil {
		return fmt.Errorf("проекция turn %s: %w", run.ID, err)
	}
	return nil
}

func projectTurnRun(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var run TurnRun
	if err := env.Decode(&run); err != nil {
		return err
	}
	return applyTurnRun(ctx, tx, run)
}

const selectTurnRunColumns = `
	SELECT id, conversation_id, COALESCE(thread_id, ''), user_message_id,
	       COALESCE(reply_message_id, ''), status, stage, stage_label, provider,
	       model, prompt_tokens, output_tokens, prompt_ms, generation_ms,
	       prompt_tokens_per_second, generation_tokens_per_second,
	       total_latency_ms, error_code, error_message, result_json,
	       created_at, COALESCE(started_at, ''), updated_at, COALESCE(finished_at, '')
	  FROM conversation_turn_runs`

type turnRowScanner interface {
	Scan(...any) error
}

func scanTurnRun(row turnRowScanner) (TurnRun, error) {
	var run TurnRun
	var result, createdAt, startedAt, updatedAt, finishedAt string
	err := row.Scan(&run.ID, &run.ConversationID, &run.ThreadID, &run.UserMessageID,
		&run.ReplyMessageID, &run.Status, &run.Stage, &run.StageLabel, &run.Provider,
		&run.Model, &run.PromptTokens, &run.OutputTokens, &run.PromptMS,
		&run.GenerationMS, &run.PromptTokensPerSecond, &run.GenerationTokensPerSecond,
		&run.TotalLatencyMS, &run.ErrorCode, &run.ErrorMessage, &result,
		&createdAt, &startedAt, &updatedAt, &finishedAt)
	if err != nil {
		return TurnRun{}, err
	}
	if run.CreatedAt, err = parseTS(createdAt); err != nil {
		return TurnRun{}, err
	}
	if run.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return TurnRun{}, err
	}
	if run.StartedAt, err = parseOptionalTS(startedAt); err != nil {
		return TurnRun{}, err
	}
	if run.FinishedAt, err = parseOptionalTS(finishedAt); err != nil {
		return TurnRun{}, err
	}
	if result != "" && result != "{}" {
		if err := json.Unmarshal([]byte(result), &run.Result); err != nil {
			return TurnRun{}, fmt.Errorf("разбор результата turn %s: %w", run.ID, err)
		}
	}
	return run, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseOptionalTS(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseTS(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
