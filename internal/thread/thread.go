// Package thread реализует главную смысловую сущность Бэрримора.
//
// ADR 0001: нить, а не задача и не чат. Нить может не иметь срока, чек-листа и
// измеримого результата: идея, спор или сомнение — полноценные нити.
package thread

import (
	"fmt"
	"time"
)

// Виды нитей (02_DOMAIN_MODEL §2).
const (
	KindProject      = "project"
	KindIdea         = "idea"
	KindProblem      = "problem"
	KindDecision     = "decision"
	KindConversation = "conversation"
	KindResearch     = "research"
	KindWaiting      = "waiting"
	KindPersonal     = "personal"
	KindRelationship = "relationship"
	KindOther        = "other"
)

// Состояния нити.
const (
	StateActive   = "active"
	StateMaturing = "maturing"
	StateWaiting  = "waiting"
	StateBlocked  = "blocked"
	StatePaused   = "paused"
	StateResolved = "resolved"
	StateReleased = "released"
	StateArchived = "archived"
)

// Владельцы позиции.
const (
	OwnerPerson    = "person"
	OwnerBarrymore = "barrymore"
)

// Виды связей между нитями.
const (
	LinkDependsOn     = "depends_on"
	LinkConflictsWith = "conflicts_with"
	LinkDerivedFrom   = "derived_from"
	LinkRelatedTo     = "related_to"
	LinkSupersedes    = "supersedes"
	LinkBlocks        = "blocks"
	LinkInspiredBy    = "inspired_by"
)

// Статусы открытого вопроса.
const (
	QuestionOpen     = "open"
	QuestionAnswered = "answered"
	QuestionDropped  = "dropped"
)

var (
	validKinds = set(KindProject, KindIdea, KindProblem, KindDecision, KindConversation,
		KindResearch, KindWaiting, KindPersonal, KindRelationship, KindOther)
	validStates = set(StateActive, StateMaturing, StateWaiting, StateBlocked,
		StatePaused, StateResolved, StateReleased, StateArchived)
	validOwners = set(OwnerPerson, OwnerBarrymore)
	validLinks  = set(LinkDependsOn, LinkConflictsWith, LinkDerivedFrom, LinkRelatedTo,
		LinkSupersedes, LinkBlocks, LinkInspiredBy)
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// ValidateKind проверяет вид нити.
func ValidateKind(kind string) error {
	if !validKinds[kind] {
		return fmt.Errorf("недопустимый вид нити %q", kind)
	}
	return nil
}

// ValidateState проверяет состояние нити.
func ValidateState(state string) error {
	if !validStates[state] {
		return fmt.Errorf("недопустимое состояние нити %q", state)
	}
	return nil
}

// ValidateOwner проверяет владельца позиции.
func ValidateOwner(owner string) error {
	if !validOwners[owner] {
		return fmt.Errorf("недопустимый владелец позиции %q: ожидается person или barrymore", owner)
	}
	return nil
}

// ValidateLinkKind проверяет вид связи.
func ValidateLinkKind(kind string) error {
	if !validLinks[kind] {
		return fmt.Errorf("недопустимый вид связи %q", kind)
	}
	return nil
}

// Thread — каноническая долгоживущая линия.
type Thread struct {
	ID                       string     `json:"id"`
	Title                    string     `json:"title"`
	Kind                     string     `json:"kind"`
	State                    string     `json:"state"`
	Summary                  string     `json:"summary,omitempty"`
	Origin                   string     `json:"origin,omitempty"`
	Importance               string     `json:"importance"`
	Sensitivity              string     `json:"sensitivity"`
	WorkspaceID              string     `json:"workspace_id,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	LastMeaningfulActivityAt *time.Time `json:"last_meaningful_activity_at,omitempty"`
	NextReviewAt             *time.Time `json:"next_review_at,omitempty"`
	MutedUntil               *time.Time `json:"muted_until,omitempty"`
	ReleasedReason           string     `json:"released_reason,omitempty"`
	// Revision — ревизия потока событий нити, основа оптимистичной конкурентности.
	Revision int64 `json:"revision"`
}

// Position — позиция участника по нити.
type Position struct {
	ID           string     `json:"id"`
	ThreadID     string     `json:"thread_id"`
	Owner        string     `json:"owner"`
	Statement    string     `json:"statement"`
	Confidence   float64    `json:"confidence"`
	Basis        string     `json:"basis,omitempty"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Decision — зафиксированное решение.
type Decision struct {
	ID           string     `json:"id"`
	ThreadID     string     `json:"thread_id"`
	Statement    string     `json:"statement"`
	DecidedBy    string     `json:"decided_by"`
	Rationale    string     `json:"rationale,omitempty"`
	Alternatives []string   `json:"alternatives,omitempty"`
	Consequences string     `json:"consequences,omitempty"`
	ReviewAt     *time.Time `json:"review_at,omitempty"`
	DecidedAt    time.Time  `json:"decided_at"`
}

// Question — открытый вопрос.
type Question struct {
	ID       string     `json:"id"`
	ThreadID string     `json:"thread_id"`
	Question string     `json:"question"`
	AskedBy  string     `json:"asked_by"`
	Status   string     `json:"status"`
	Answer   string     `json:"answer,omitempty"`
	OpenedAt time.Time  `json:"opened_at"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// Link — связь между нитями.
type Link struct {
	ID        string    `json:"id"`
	FromID    string    `json:"from_id"`
	ToID      string    `json:"to_id"`
	Kind      string    `json:"kind"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Detail — нить со всем, что к ней относится.
type Detail struct {
	Thread    Thread     `json:"thread"`
	Positions []Position `json:"positions"`
	Decisions []Decision `json:"decisions"`
	Questions []Question `json:"questions"`
	Links     []Link     `json:"links"`
}

// Типы событий нитей (08_API_AND_EVENTS §4).
const (
	EvCreated          = "thread.created"
	EvUpdated          = "thread.updated"
	EvStateChanged     = "thread.state.changed"
	EvPositionUpdated  = "thread.position.updated"
	EvDecisionRecorded = "thread.decision.recorded"
	EvQuestionOpened   = "thread.question.opened"
	EvQuestionClosed   = "thread.question.closed"
	EvLinked           = "thread.linked"
	EvReleased         = "thread.released"
)

// StreamType — тип потока событий нити.
const StreamType = "thread"

// ProjectionTables — таблицы проекций нитей в порядке внешних ключей.
var ProjectionTables = []string{
	"threads",
	"thread_positions",
	"thread_decisions",
	"thread_questions",
	"thread_links",
}
