package query

import "github.com/C1ph3rX13/Aether/internal/project"

type LogSourceKind string

const (
	LogSourceOperator  LogSourceKind = "operator"
	LogSourceRun       LogSourceKind = "run"
	LogSourceMigration LogSourceKind = "migration"
)

type LogSource struct {
	Kind  LogSourceKind `json:"kind"`
	Name  string        `json:"name"`
	RunID *string       `json:"run_id,omitempty"`
}

type LogFocusType string

const (
	LogFocusFact   LogFocusType = "fact"
	LogFocusIntent LogFocusType = "intent"
)

type LogFocusTarget struct {
	Type LogFocusType `json:"type"`
	ID   string       `json:"id"`
}

type LogEventKind string

const (
	LogEventProjectCreated   LogEventKind = "project_created"
	LogEventProjectRenamed   LogEventKind = "project_renamed"
	LogEventProjectPaused    LogEventKind = "project_paused"
	LogEventProjectResumed   LogEventKind = "project_resumed"
	LogEventProjectReopened  LogEventKind = "project_reopened"
	LogEventProjectCompleted LogEventKind = "project_completed"
	LogEventHintCreated      LogEventKind = "hint_created"
	LogEventHintWithdrawn    LogEventKind = "hint_withdrawn"
	LogEventIntentCreated    LogEventKind = "intent_created"
	LogEventIntentConcluded  LogEventKind = "intent_concluded"
	LogEventIntentCancelled  LogEventKind = "intent_cancelled"
	LogEventFactRetracted    LogEventKind = "fact_retracted"
	LogEventFactSuperseded   LogEventKind = "fact_superseded"
	LogEventMigration        LogEventKind = "migration"
)

type LogEventBase struct {
	EventID     string          `json:"event_id"`
	Kind        LogEventKind    `json:"kind"`
	FocusTarget *LogFocusTarget `json:"focus_target,omitempty"`
}

// LogEvent is sealed so every wire variant remains a typed, closed union.
type LogEvent interface {
	logEvent() LogEventBase
}

type FactReference struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type ProjectCreatedEvent struct {
	LogEventBase
	ProjectID string        `json:"project_id"`
	Title     string        `json:"title"`
	Origin    FactReference `json:"origin"`
	Goal      FactReference `json:"goal"`
}

func (event ProjectCreatedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type ProjectRenamedEvent struct {
	LogEventBase
	ProjectID     string `json:"project_id"`
	PreviousTitle string `json:"previous_title"`
	Title         string `json:"title"`
}

func (event ProjectRenamedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type ProjectPausedEvent struct {
	LogEventBase
	ProjectID  string                `json:"project_id"`
	FromStatus project.ProjectStatus `json:"from_status"`
	ToStatus   project.ProjectStatus `json:"to_status"`
}

func (event ProjectPausedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type ProjectResumedEvent struct {
	LogEventBase
	ProjectID  string                `json:"project_id"`
	FromStatus project.ProjectStatus `json:"from_status"`
	ToStatus   project.ProjectStatus `json:"to_status"`
}

func (event ProjectResumedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type ProjectReopenedEvent struct {
	LogEventBase
	ProjectID          string `json:"project_id"`
	PreviousGeneration int    `json:"previous_generation"`
	CurrentGeneration  int    `json:"current_generation"`
	FeedbackHintID     string `json:"feedback_hint_id"`
}

func (event ProjectReopenedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type ProjectCompletedEvent struct {
	LogEventBase
	ProjectID            string   `json:"project_id"`
	CompletionIntentID   string   `json:"completion_intent_id"`
	Description          string   `json:"description"`
	SourceFactIDs        []string `json:"source_fact_ids"`
	CompletionGeneration int      `json:"completion_generation"`
	AffectedRunCount     *int     `json:"affected_run_count,omitempty"`
}

func (event ProjectCompletedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type HintCreatedEvent struct {
	LogEventBase
	HintID           string `json:"hint_id"`
	Content          string `json:"content"`
	Creator          string `json:"creator"`
	Generation       int    `json:"generation"`
	Status           string `json:"status"`
	AffectedRunCount *int   `json:"affected_run_count,omitempty"`
}

func (event HintCreatedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type HintWithdrawnEvent struct {
	LogEventBase
	HintID           string `json:"hint_id"`
	Content          string `json:"content"`
	Generation       int    `json:"generation"`
	WithdrawnBy      string `json:"withdrawn_by"`
	Reason           string `json:"reason"`
	AffectedRunCount *int   `json:"affected_run_count,omitempty"`
}

func (event HintWithdrawnEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type IntentCreatedEvent struct {
	LogEventBase
	IntentID      string               `json:"intent_id"`
	IntentKind    project.IntentKind   `json:"intent_kind"`
	Status        project.IntentStatus `json:"status"`
	Description   string               `json:"description"`
	Creator       string               `json:"creator"`
	SourceFactIDs []string             `json:"source_fact_ids"`
	TargetFactID  *string              `json:"target_fact_id,omitempty"`
}

func (event IntentCreatedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type IntentConcludedEvent struct {
	LogEventBase
	IntentID      string             `json:"intent_id"`
	IntentKind    project.IntentKind `json:"intent_kind"`
	Description   string             `json:"description"`
	SourceFactIDs []string           `json:"source_fact_ids"`
	ProducedFact  FactReference      `json:"produced_fact"`
}

func (event IntentConcludedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type IntentCancelledEvent struct {
	LogEventBase
	IntentID         string             `json:"intent_id"`
	IntentKind       project.IntentKind `json:"intent_kind"`
	Description      string             `json:"description"`
	SourceFactIDs    []string           `json:"source_fact_ids"`
	CancelledBy      string             `json:"cancelled_by"`
	Reason           string             `json:"reason"`
	AffectedRunCount *int               `json:"affected_run_count,omitempty"`
}

func (event IntentCancelledEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type FactRetractedEvent struct {
	LogEventBase
	FactID            string  `json:"fact_id"`
	Description       string  `json:"description"`
	RetractedBy       string  `json:"retracted_by"`
	Reason            string  `json:"reason"`
	ReplacementFactID *string `json:"replacement_fact_id,omitempty"`
	AffectedRunCount  *int    `json:"affected_run_count,omitempty"`
}

func (event FactRetractedEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type FactSupersededEvent struct {
	LogEventBase
	OriginalFact     FactReference `json:"original_fact"`
	ReplacementFact  FactReference `json:"replacement_fact"`
	Actor            string        `json:"actor"`
	Reason           string        `json:"reason"`
	AffectedRunCount *int          `json:"affected_run_count,omitempty"`
}

func (event FactSupersededEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type MigrationEvent struct {
	LogEventBase
	OriginalChangeKind project.ChangeKind `json:"original_change_kind"`
	SchemaVersion      int                `json:"schema_version"`
	EntityType         string             `json:"entity_type"`
	EntityID           *string            `json:"entity_id,omitempty"`
}

func (event MigrationEvent) logEvent() LogEventBase {
	return event.LogEventBase
}

type LogRevision struct {
	Revision   int64              `json:"revision"`
	Generation int                `json:"generation"`
	ChangeKind project.ChangeKind `json:"change_kind"`
	OccurredAt string             `json:"occurred_at"`
	Source     LogSource          `json:"source"`
	Events     []LogEvent         `json:"events"`
}

type LogPage struct {
	ProjectID                 string        `json:"project_id"`
	AvailableFromRevision     int64         `json:"available_from_revision"`
	AvailableFromOccurredAt   string        `json:"available_from_occurred_at"`
	CurrentRevision           int64         `json:"current_revision"`
	CurrentRevisionOccurredAt string        `json:"current_revision_occurred_at"`
	Revisions                 []LogRevision `json:"revisions"`
	NextBeforeRevision        *int64        `json:"next_before_revision,omitempty"`
}

type ProjectLogQuery struct {
	BeforeRevision int64
	Limit          int
}
