// Package research executes bounded, typed read-only investigations.
//
// The language model may request a capability by ID, but it never supplies an
// arbitrary command to execute. Runtime owns the catalog, validates arguments
// and invokes only registered handlers. This is the boundary between
// "I need to find out" and shell-by-model.
package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/model"
)

const (
	StabilityImmutable = "immutable"
	StabilityStable    = "stable"
	StabilityVolatile  = "volatile"
	StabilityRealtime  = "realtime"
)

// Capability describes one observation Barrymore is allowed to make by
// himself. The first research layer contains read-only capabilities only.
type Capability struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Question    string `json:"question"`
	Description string `json:"description,omitempty"`
	Stability   string `json:"stability"`
}

// Request is what deliberation asks runtime to investigate.
type Request struct {
	CapabilityID string          `json:"capability_id"`
	Args         json.RawMessage `json:"args"`
	Why          string          `json:"why,omitempty"`
}

// Result is direct evidence returned by a registered capability.
type Result struct {
	CapabilityID string          `json:"capability_id"`
	Title        string          `json:"title"`
	Evidence     string          `json:"evidence"`
	Locator      string          `json:"locator,omitempty"`
	Confidence   float64         `json:"confidence"`
	Stability    string          `json:"stability"`
	ObservedAt   time.Time       `json:"observed_at"`
	Data         json.RawMessage `json:"data,omitempty"`
}

type Handler func(context.Context, json.RawMessage) (Result, error)

type entry struct {
	cap Capability
	fn  Handler
}

// Registry is the runtime-owned read-only capability catalog.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
}

func New() *Registry { return &Registry{entries: map[string]entry{}} }

func validStability(v string) bool {
	switch v {
	case StabilityImmutable, StabilityStable, StabilityVolatile, StabilityRealtime:
		return true
	default:
		return false
	}
}

// Register adds a capability. Duplicate IDs are refused instead of silently
// replacing behavior behind a name already shown to the model.
func (r *Registry) Register(cap Capability, fn Handler) error {
	cap.ID = strings.TrimSpace(cap.ID)
	cap.Title = strings.TrimSpace(cap.Title)
	cap.Question = strings.TrimSpace(cap.Question)
	if cap.ID == "" || cap.Title == "" || cap.Question == "" {
		return errors.New("research capability requires id, title and question")
	}
	if !validStability(cap.Stability) {
		return fmt.Errorf("research capability %s has unknown stability %q", cap.ID, cap.Stability)
	}
	if fn == nil {
		return fmt.Errorf("research capability %s has no handler", cap.ID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[cap.ID]; exists {
		return fmt.Errorf("research capability %s already registered", cap.ID)
	}
	r.entries[cap.ID] = entry{cap: cap, fn: fn}
	return nil
}

// Catalog returns capabilities in stable order for prompt caching and tests.
func (r *Registry) Catalog() []Capability {
	r.mu.RLock()
	out := make([]Capability, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.cap)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Execute invokes only a registered capability and validates its evidence.
func (r *Registry) Execute(ctx context.Context, req Request) (Result, error) {
	id := strings.TrimSpace(req.CapabilityID)
	r.mu.RLock()
	e, ok := r.entries[id]
	r.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("research capability %q is not registered", id)
	}
	args := req.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return Result{}, fmt.Errorf("research capability %s received invalid JSON args", id)
	}
	res, err := e.fn(ctx, args)
	if err != nil {
		return Result{}, err
	}
	res.CapabilityID = e.cap.ID
	if strings.TrimSpace(res.Title) == "" {
		res.Title = e.cap.Title
	}
	res.Evidence = strings.TrimSpace(res.Evidence)
	if res.Evidence == "" {
		return Result{}, fmt.Errorf("research capability %s returned no evidence", id)
	}
	if res.Stability == "" {
		res.Stability = e.cap.Stability
	}
	if !validStability(res.Stability) {
		return Result{}, fmt.Errorf("research capability %s returned unknown stability %q", id, res.Stability)
	}
	if res.Confidence == 0 {
		res.Confidence = 1
	}
	if res.Confidence < 0 || res.Confidence > 1 {
		return Result{}, fmt.Errorf("research capability %s returned confidence %.3f", id, res.Confidence)
	}
	return res, nil
}

// RegisterProviderInspector exposes the conversation provider as a direct
// realtime observation. It does not infer model identity from memory: every
// execution probes the provider again.
func RegisterProviderInspector(r *Registry, provider model.Provider, clk clock.Clock) error {
	if provider == nil {
		return nil
	}
	return r.Register(Capability{
		ID:          "runtime.provider.inspect",
		Title:       "Проверить разговорную модель",
		Question:    "Какая разговорная модель и провайдер реально отвечают сейчас?",
		Description: "Проверяет текущий provider через его read-only probe; не использует старый ответ из памяти.",
		Stability:   StabilityRealtime,
	}, func(ctx context.Context, _ json.RawMessage) (Result, error) {
		st := provider.Probe(ctx)
		now := time.Now().UTC()
		if clk != nil {
			now = clk.Now()
		}
		data, err := json.Marshal(map[string]any{
			"provider": provider.Describe(),
			"status": st.Status,
			"endpoint": st.Endpoint,
			"model": st.Model,
			"reason": st.Reason,
		})
		if err != nil {
			return Result{}, err
		}
		evidence := fmt.Sprintf("provider=%s; status=%s", provider.Describe(), st.Status)
		if st.Model != "" {
			evidence += "; model=" + st.Model
		}
		if st.Endpoint != "" {
			evidence += "; endpoint=" + st.Endpoint
		}
		if st.Reason != "" {
			evidence += "; reason=" + st.Reason
		}
		confidence := 1.0
		if !st.Ready() {
			// Failure to reach a provider is still direct evidence about current
			// availability, but not strong evidence about model identity.
			confidence = 0.8
		}
		return Result{
			Evidence: evidence, Locator: st.Endpoint, Confidence: confidence,
			Stability: StabilityRealtime, ObservedAt: now, Data: data,
		}, nil
	})
}
