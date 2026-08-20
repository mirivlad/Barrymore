package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
)

var ErrTurnActive = errors.New("в разговоре уже выполняется ход")
var ErrNoActiveTurn = errors.New("в разговоре нет выполняющегося хода")

const (
	TurnQueued      = "queued"
	TurnRunning     = "running"
	TurnCompleted   = "completed"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

const (
	StageQueued             = "queued"
	StageRecall             = "recall"
	StageContext            = "context"
	StageResearch           = "research"
	StageCapability         = "capability"
	StageProviderPrompt     = "provider_prompt"
	StageProviderGeneration = "provider_generation"
	StageVerification       = "verification"
	StageFinalization       = "finalization"
)

type TurnRun struct {
	ID                        string     `json:"id"`
	ConversationID            string     `json:"conversation_id"`
	ThreadID                  string     `json:"thread_id,omitempty"`
	UserMessageID             string     `json:"user_message_id"`
	ReplyMessageID            string     `json:"reply_message_id,omitempty"`
	Status                    string     `json:"status"`
	Stage                     string     `json:"stage"`
	StageLabel                string     `json:"stage_label,omitempty"`
	Provider                  string     `json:"provider,omitempty"`
	Model                     string     `json:"model,omitempty"`
	PromptTokens              int        `json:"prompt_tokens,omitempty"`
	OutputTokens              int        `json:"output_tokens,omitempty"`
	PromptMS                  float64    `json:"prompt_ms,omitempty"`
	GenerationMS              float64    `json:"generation_ms,omitempty"`
	PromptTokensPerSecond     float64    `json:"prompt_tokens_per_second,omitempty"`
	GenerationTokensPerSecond float64    `json:"generation_tokens_per_second,omitempty"`
	TotalLatencyMS            int64      `json:"total_latency_ms,omitempty"`
	ErrorCode                 string     `json:"error_code,omitempty"`
	ErrorMessage              string     `json:"error_message,omitempty"`
	Result                    Turn       `json:"result,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	StartedAt                 *time.Time `json:"started_at,omitempty"`
	UpdatedAt                 time.Time  `json:"updated_at"`
	FinishedAt                *time.Time `json:"finished_at,omitempty"`
}

func (s *Service) BeginTurn(ctx context.Context, conversationID, text string) (TurnRun, error) {
	if s.provider == nil {
		return TurnRun{}, ErrNoProvider
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return TurnRun{}, fmt.Errorf("пустая реплика")
	}
	conv, err := s.Get(ctx, conversationID)
	if err != nil {
		return TurnRun{}, err
	}
	if _, err := s.ActiveTurn(ctx, conversationID); err == nil {
		return TurnRun{}, ErrTurnActive
	} else if !errors.Is(err, ErrNoActiveTurn) {
		return TurnRun{}, err
	}

	now := s.clock.Now()
	msg := Message{
		ID: ids.New(ids.Message), ConversationID: conv.ID, ThreadID: conv.ThreadID,
		Role: RolePerson, Content: text, CreatedAt: now,
	}
	run := TurnRun{
		ID: ids.New(ids.TurnRun), ConversationID: conv.ID, ThreadID: conv.ThreadID,
		UserMessageID: msg.ID, Status: TurnQueued, Stage: StageQueued,
		StageLabel: "Готовлю ход", CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: conv.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvMessageRecorded, Actor: event.Actor{Type: event.ActorPerson},
			CorrelationID: conv.ThreadID, Payload: msg,
		}); err != nil {
			return err
		}
		if err := applyMessage(ctx, tx, msg); err != nil {
			return err
		}
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: conv.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvTurnQueued, Actor: event.Actor{Type: event.ActorPerson},
			CorrelationID: run.ID, Payload: run,
		}); err != nil {
			return err
		}
		return applyTurnRun(ctx, tx, run)
	})
	if err != nil {
		if strings.Contains(err.Error(), "conversation_turn_runs.conversation_id") {
			return TurnRun{}, ErrTurnActive
		}
		return TurnRun{}, err
	}
	return run, nil
}

func (s *Service) ExecuteTurn(ctx context.Context, turnID string) (TurnRun, error) {
	run, err := s.turnRunByID(ctx, turnID)
	if err != nil {
		return TurnRun{}, err
	}
	if run.Status != TurnQueued {
		return run, fmt.Errorf("turn %s нельзя запустить из состояния %s", turnID, run.Status)
	}
	conv, err := s.Get(ctx, run.ConversationID)
	if err != nil {
		return run, err
	}
	msgs, err := s.Messages(ctx, run.ConversationID, 200)
	if err != nil {
		return run, err
	}
	var userMsg Message
	for _, msg := range msgs {
		if msg.ID == run.UserMessageID {
			userMsg = msg
			break
		}
	}
	if userMsg.ID == "" {
		return run, fmt.Errorf("реплика %s для turn %s не найдена", run.UserMessageID, turnID)
	}

	started := s.clock.Now()
	run.Status = TurnRunning
	run.Stage = StageRecall
	run.StageLabel = "Вспоминаю похожий опыт"
	run.StartedAt = &started
	run.UpdatedAt = started
	if err := s.writeTurnRun(ctx, run, EvTurnStarted); err != nil {
		return run, err
	}
	reporter := &turnReporter{service: s, run: &run, started: started}
	reporter.publish(run.Stage, run.StageLabel, 0, 0, false, 0)

	turn, executeErr := s.executeRecordedTurn(ctx, conv, userMsg.Content, userMsg, reporter)
	finished := s.clock.Now()
	run.UpdatedAt = finished
	run.FinishedAt = &finished
	run.TotalLatencyMS = finished.Sub(started).Milliseconds()
	if executeErr != nil {
		run.Status = TurnFailed
		run.ErrorCode = "turn_failed"
		run.ErrorMessage = executeErr.Error()
		if err := s.writeTurnRun(ctx, run, EvTurnFailed); err != nil {
			return run, errors.Join(executeErr, err)
		}
		s.progress.Forget(run.ID)
		return run, executeErr
	}

	run.Status = TurnCompleted
	run.Stage = StageFinalization
	run.StageLabel = "Ответ готов"
	run.ReplyMessageID = turn.Reply.ID
	run.Provider = turn.Reply.Provider
	run.Model = turn.Reply.Model
	run.PromptTokens = turn.Reply.PromptTokens
	run.OutputTokens = turn.Reply.OutputTokens
	run.PromptMS = float64(reporter.response.PromptDuration) / float64(time.Millisecond)
	run.GenerationMS = float64(reporter.response.GenerationDuration) / float64(time.Millisecond)
	run.PromptTokensPerSecond = reporter.response.PromptTokensPerSecond
	run.GenerationTokensPerSecond = reporter.response.GenerationTokensPerSecond
	run.Result = turn
	if err := s.writeTurnRun(ctx, run, EvTurnCompleted); err != nil {
		return run, err
	}
	s.progress.Forget(run.ID)
	return run, nil
}

func (s *Service) writeTurnRun(ctx context.Context, run TurnRun, eventType string) error {
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: run.ConversationID,
			ExpectedRevision: event.AnyRevision, EventType: eventType,
			Actor: event.Actor{Type: event.ActorRuntime}, CorrelationID: run.ID,
			Payload: run,
		}); err != nil {
			return err
		}
		return applyTurnRun(ctx, tx, run)
	})
	return err
}

func (s *Service) turnRunByID(ctx context.Context, turnID string) (TurnRun, error) {
	run, err := s.readTurnRun(ctx, ` WHERE id = ?`, turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnRun{}, fmt.Errorf("%w: turn %s", ErrNotFound, turnID)
	}
	return run, err
}

func (s *Service) InterruptUnfinished(ctx context.Context) (int, error) {
	rows, err := s.db.Reader().QueryContext(ctx, selectTurnRunColumns+
		` WHERE status IN ('queued', 'running') ORDER BY created_at`)
	if err != nil {
		return 0, fmt.Errorf("чтение незавершённых turn: %w", err)
	}
	var runs []TurnRun
	for rows.Next() {
		run, err := scanTurnRun(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for i := range runs {
		finished := s.clock.Now()
		runs[i].Status = TurnInterrupted
		runs[i].StageLabel = "Ход прерван перезапуском"
		runs[i].ErrorCode = "runtime_restarted"
		runs[i].ErrorMessage = "Barrymore перезапущен до завершения хода; вызов не повторён"
		runs[i].UpdatedAt = finished
		runs[i].FinishedAt = &finished
		if err := s.writeTurnRun(ctx, runs[i], EvTurnInterrupted); err != nil {
			return i, err
		}
	}
	return len(runs), nil
}

// FailTurn terminalizes an active turn at an outer runtime boundary, such as
// panic recovery in App. Ordinary execution errors are handled by ExecuteTurn.
func (s *Service) FailTurn(ctx context.Context, turnID, code, message string) error {
	run, err := s.turnRunByID(ctx, turnID)
	if err != nil {
		return err
	}
	if run.Status != TurnQueued && run.Status != TurnRunning {
		return nil
	}
	finished := s.clock.Now()
	run.Status = TurnFailed
	run.StageLabel = "Ход завершился внутренней ошибкой"
	run.ErrorCode = strings.TrimSpace(code)
	run.ErrorMessage = strings.TrimSpace(message)
	run.UpdatedAt = finished
	run.FinishedAt = &finished
	if err := s.writeTurnRun(ctx, run, EvTurnFailed); err != nil {
		return err
	}
	s.progress.Forget(run.ID)
	return nil
}

func (s *Service) TurnRun(ctx context.Context, conversationID, turnID string) (TurnRun, error) {
	run, err := s.readTurnRun(ctx, ` WHERE id = ? AND conversation_id = ?`, turnID, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnRun{}, fmt.Errorf("%w: turn %s", ErrNotFound, turnID)
	}
	return run, err
}

func (s *Service) ActiveTurn(ctx context.Context, conversationID string) (TurnRun, error) {
	run, err := s.readTurnRun(ctx,
		` WHERE conversation_id = ? AND status IN ('queued', 'running') ORDER BY created_at DESC LIMIT 1`,
		conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnRun{}, ErrNoActiveTurn
	}
	return run, err
}

func (s *Service) readTurnRun(ctx context.Context, suffix string, args ...any) (TurnRun, error) {
	row := s.db.Reader().QueryRowContext(ctx, selectTurnRunColumns+suffix, args...)
	return scanTurnRun(row)
}

func encodeTurnResult(turn Turn) string {
	raw, err := json.Marshal(turn)
	if err != nil {
		return `{}`
	}
	return string(raw)
}
