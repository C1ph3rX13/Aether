package project

// CompletionRecord is an immutable record of a completed Project generation.
// The linked completion Intent remains in the Blackboard and carries source facts.
type CompletionRecord struct {
	Generation  int    `json:"generation" yaml:"generation"`
	IntentID    string `json:"intent_id" yaml:"intent_id"`
	Description string `json:"description" yaml:"description"`
	Actor       string `json:"actor" yaml:"actor"`
	CompletedAt string `json:"completed_at" yaml:"completed_at"`
}
