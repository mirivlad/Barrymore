// Package experience keeps durable episodes, procedures, sources and explicit
// owner feedback. It complements memory and practices instead of replacing them:
// confirmed facts remain memory_items, while an Episode records what happened
// and a Procedure records how to obtain or change something again.
package experience

import (
	"encoding/json"
	"time"
)

const (
	StreamEpisode   = "episode"
	StreamProcedure = "procedure"
)

const (
	EvEpisodeStarted   = "experience.episode.started"
	EvSourceRecorded   = "experience.source.recorded"
	EvEpisodeCompleted = "experience.episode.completed"
	EvProcedureSaved   = "experience.procedure.saved"
	EvFeedbackRecorded = "experience.feedback.recorded"
	EvArtifactRecorded = "experience.artifact.recorded"
)

const (
	EpisodeOpen      = "open"
	EpisodeCompleted = "completed"
)

const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomePartial = "partial"
	OutcomeUnknown = "unknown"
)

const (
	FeedbackLike    = "like"
	FeedbackDislike = "dislike"
)

const (
	ProcedureActive  = "active"
	ProcedureStale   = "stale"
	ProcedureRetired = "retired"
)

const (
	RiskReadOnly        = "read_only"
	RiskLocalChange     = "local_change"
	RiskWorkspaceChange = "workspace_change"
	RiskRemoteChange    = "remote_change"
	RiskDestructive     = "destructive"
)

type Episode struct {
	ID             string          `json:"id"`
	Goal           string          `json:"goal"`
	Scope          string          `json:"scope,omitempty"`
	ThreadID       string          `json:"thread_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Status         string          `json:"status"`
	Outcome        string          `json:"outcome,omitempty"`
	InitialContext json.RawMessage `json:"initial_context,omitempty"`
	Result         string          `json:"result,omitempty"`
	Verification   json.RawMessage `json:"verification,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type Source struct {
	ID         string    `json:"id"`
	EpisodeID  string    `json:"episode_id"`
	Kind       string    `json:"kind"`
	Locator    string    `json:"locator,omitempty"`
	Title      string    `json:"title,omitempty"`
	Evidence   string    `json:"evidence"`
	Confidence float64   `json:"confidence"`
	ObservedAt time.Time `json:"observed_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type Step struct {
	Capability string          `json:"capability"`
	Purpose    string          `json:"purpose,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
}

type Procedure struct {
	ID                   string     `json:"id"`
	Intent               string     `json:"intent"`
	Title                string     `json:"title"`
	Scope                string     `json:"scope,omitempty"`
	SourceEpisodeID      string     `json:"source_episode_id,omitempty"`
	Preconditions        []string   `json:"preconditions,omitempty"`
	Steps                []Step     `json:"steps"`
	RequiredCapabilities []string   `json:"required_capabilities,omitempty"`
	ExpectedResult       string     `json:"expected_result,omitempty"`
	Verification         []string   `json:"verification,omitempty"`
	Rollback             []Step     `json:"rollback,omitempty"`
	RiskClass            string     `json:"risk_class"`
	Status               string     `json:"status"`
	Succeeded            int        `json:"succeeded"`
	Failed               int        `json:"failed"`
	LastUsedAt           *time.Time `json:"last_used_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type Artifact struct {
	ID        string    `json:"id"`
	EpisodeID string    `json:"episode_id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	Size      int64     `json:"size"`
	Checksum  string    `json:"checksum,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type SearchHit struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	Rank       float64 `json:"rank"`
}

type Feedback struct {
	ID        string    `json:"id"`
	EpisodeID string    `json:"episode_id"`
	Value     string    `json:"value"`
	Note      string    `json:"note,omitempty"`
	ActorType string    `json:"actor_type"`
	ActorID   string    `json:"actor_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type StartRequest struct {
	Goal           string
	Scope          string
	ThreadID       string
	ConversationID string
	InitialContext json.RawMessage
}

type CompleteRequest struct {
	Outcome      string
	Result       string
	Verification json.RawMessage
}
