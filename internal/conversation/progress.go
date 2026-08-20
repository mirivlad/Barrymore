package conversation

import (
	"context"
	"sync"
	"time"

	"github.com/mirivlad/barrymore/internal/model"
)

type turnReporter struct {
	service             *Service
	run                 *TurnRun
	started             time.Time
	response            model.Response
	lastProviderElapsed time.Duration
}

func (r *turnReporter) stage(ctx context.Context, stage, label string) error {
	if r == nil {
		return nil
	}
	now := r.service.clock.Now()
	r.run.Stage = stage
	r.run.StageLabel = label
	r.run.UpdatedAt = now
	if err := r.service.writeTurnRun(ctx, *r.run, EvTurnStageChanged); err != nil {
		return err
	}
	r.publish(stage, label, 0, 0, false, now.Sub(r.started))
	return nil
}

func (r *turnReporter) generation(progress model.Progress, completedOutput int) {
	if r == nil {
		return
	}
	if r.lastProviderElapsed > 0 && progress.Elapsed-r.lastProviderElapsed < 200*time.Millisecond {
		return
	}
	r.lastProviderElapsed = progress.Elapsed
	rate := float64(0)
	if progress.Elapsed > 0 {
		rate = float64(progress.OutputUnits) / progress.Elapsed.Seconds()
	}
	r.publish(StageProviderGeneration, "Формирую ответ", completedOutput+progress.OutputUnits,
		rate, true, r.service.clock.Now().Sub(r.started))
}

func (r *turnReporter) beginGeneration() {
	if r != nil {
		r.lastProviderElapsed = 0
	}
}

func (r *turnReporter) publish(stage, label string, outputTokens int, rate float64,
	approximate bool, elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	r.service.progress.Publish(TurnProgress{
		TurnID: r.run.ID, ConversationID: r.run.ConversationID,
		Stage: stage, Label: label, ElapsedMS: elapsed.Milliseconds(),
		OutputTokens: outputTokens, GenerationTokensPerSecond: rate,
		Approximate: approximate, UpdatedAt: r.service.clock.Now(),
	})
}

// TurnProgress is an ephemeral latest-value snapshot. Durable lifecycle state
// remains in TurnRun; this type is for inexpensive live UI updates only.
type TurnProgress struct {
	TurnID                    string    `json:"turn_id"`
	ConversationID            string    `json:"conversation_id"`
	Stage                     string    `json:"stage"`
	Label                     string    `json:"label"`
	ElapsedMS                 int64     `json:"elapsed_ms"`
	OutputTokens              int       `json:"output_tokens,omitempty"`
	GenerationTokensPerSecond float64   `json:"generation_tokens_per_second,omitempty"`
	Approximate               bool      `json:"approximate,omitempty"`
	UpdatedAt                 time.Time `json:"updated_at,omitempty"`
}

// ProgressBroker stores one latest snapshot per turn and one newest pending
// update per subscriber. A slow observer cannot block or grow an event queue.
type ProgressBroker struct {
	mu          sync.Mutex
	latest      map[string]TurnProgress
	subscribers map[uint64]chan TurnProgress
	nextID      uint64
}

func NewProgressBroker() *ProgressBroker {
	return &ProgressBroker{
		latest:      make(map[string]TurnProgress),
		subscribers: make(map[uint64]chan TurnProgress),
	}
}

func (b *ProgressBroker) Publish(progress TurnProgress) {
	if b == nil || progress.TurnID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latest[progress.TurnID] = progress
	for _, updates := range b.subscribers {
		select {
		case <-updates:
		default:
		}
		select {
		case updates <- progress:
		default:
		}
	}
}

func (b *ProgressBroker) Latest(turnID string) (TurnProgress, bool) {
	if b == nil {
		return TurnProgress{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	progress, ok := b.latest[turnID]
	return progress, ok
}

func (b *ProgressBroker) Forget(turnID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.latest, turnID)
	b.mu.Unlock()
}

func (b *ProgressBroker) Subscribe() (<-chan TurnProgress, func()) {
	updates := make(chan TurnProgress, 1)
	if b == nil {
		close(updates)
		return updates, func() {}
	}
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = updates
	b.mu.Unlock()

	var once sync.Once
	closeSubscription := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subscribers[id]; ok {
				delete(b.subscribers, id)
				close(updates)
			}
			b.mu.Unlock()
		})
	}
	return updates, closeSubscription
}
