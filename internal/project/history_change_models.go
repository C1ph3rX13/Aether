package project

import "encoding/json"

type ChangeKind string

const (
	ChangeProjectBaseline    ChangeKind = "project_baseline"
	ChangeProjectCreated     ChangeKind = "project_created"
	ChangeProjectRenamed     ChangeKind = "project_renamed"
	ChangeProjectPaused      ChangeKind = "project_paused"
	ChangeProjectResumed     ChangeKind = "project_resumed"
	ChangeIntentCreated      ChangeKind = "intent_created"
	ChangeIntentConcluded    ChangeKind = "intent_concluded"
	ChangeIntentCancelled    ChangeKind = "intent_cancelled"
	ChangeHintCreated        ChangeKind = "hint_created"
	ChangeHintWithdrawn      ChangeKind = "hint_withdrawn"
	ChangeFactRetracted      ChangeKind = "fact_retracted"
	ChangeFactSuperseded     ChangeKind = "fact_superseded"
	ChangeProjectCompleted   ChangeKind = "project_completed"
	ChangeProjectReopened    ChangeKind = "project_reopened"
	ChangeIntentsProposed    ChangeKind = "intents_proposed"
	ChangeBootstrapCompleted ChangeKind = "bootstrap_completed"
)

type ChangeSource struct {
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	RunID *string `json:"run_id,omitempty"`
}

type BlackboardChange struct {
	ProjectID  string     `json:"project_id"`
	Revision   int64      `json:"revision"`
	Generation int        `json:"generation"`
	Kind       ChangeKind `json:"kind"`
	EntityType string     `json:"entity_type"`
	EntityID   *string    `json:"entity_id,omitempty"`
	// Data is the retained legacy payload for legacy rows and a compact
	// envelope for delta rows. Delta is the authoritative replay input for
	// new-format rows.
	Data          json.RawMessage `json:"data"`
	PayloadSchema string          `json:"payload_schema,omitempty"`
	Source        ChangeSource    `json:"source,omitempty"`
	Delta         json.RawMessage `json:"delta,omitempty"`
	PreviousHash  string          `json:"previous_hash,omitempty"`
	ChangeHash    string          `json:"change_hash,omitempty"`
	CreatedAt     string          `json:"created_at"`
}

type ChangeReplay struct {
	ProjectID        string             `json:"project_id"`
	AfterRevision    int64              `json:"after_revision"`
	CurrentRevision  int64              `json:"current_revision"`
	EarliestRevision *int64             `json:"earliest_revision,omitempty"`
	ReplayRequired   bool               `json:"replay_required"`
	Changes          []BlackboardChange `json:"changes"`
}
