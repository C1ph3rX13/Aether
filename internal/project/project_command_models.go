package project

type CreateProjectRequest struct {
	Title            string             `json:"title"`
	Origin           string             `json:"origin"`
	Goal             string             `json:"goal"`
	BootstrapEnabled bool               `json:"bootstrap_enabled"`
	Hints            []CreateHintInline `json:"hints"`
}

type CreateHintInline struct {
	Content string `json:"content"`
}

type CreateHintRequest struct {
	Content          string `json:"content"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type CompleteRequest struct {
	From             []string `json:"from"`
	Description      string   `json:"description"`
	ExpectedRevision int64    `json:"expected_revision"`
}

type ProjectLifecycleRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type ProjectLifecycleReceipt struct {
	Project         ProjectMeta `json:"project"`
	ProjectRevision int64       `json:"project_revision"`
}

type DeleteProjectRequest struct {
	ExpectedTitle    string `json:"expected_title"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type DeleteProjectReceipt struct {
	ProjectID        string `json:"project_id"`
	ProjectRevision  int64  `json:"project_revision"`
	AffectedRunCount int    `json:"affected_run_count"`
	CleanupJobID     string `json:"cleanup_job_id"`
}

type UpdateProjectTitleRequest struct {
	Title            string `json:"title"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type RenameProjectReceipt struct {
	Project         ProjectMeta `json:"project"`
	ProjectRevision int64       `json:"project_revision"`
}

type ReopenRequest struct {
	Feedback         string `json:"feedback"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type ReopenResponse struct {
	Project         ProjectMeta      `json:"project"`
	ProjectRevision int64            `json:"project_revision"`
	Hint            Hint             `json:"hint"`
	Previous        CompletionRecord `json:"previous_completion"`
}

type RetractFactRequest struct {
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type SupersedeFactRequest struct {
	Description      string `json:"description"`
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type CancelIntentRequest struct {
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type WithdrawHintRequest struct {
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type ImpactAction string

const (
	ImpactHintChange      ImpactAction = "hint_change"
	ImpactRetractFact     ImpactAction = "retract_fact"
	ImpactSupersedeFact   ImpactAction = "supersede_fact"
	ImpactCancelIntent    ImpactAction = "cancel_intent"
	ImpactCompleteProject ImpactAction = "complete_project"
)

type ImpactRequest struct {
	Action           ImpactAction `json:"action"`
	EntityID         string       `json:"entity_id,omitempty"`
	ExpectedRevision int64        `json:"expected_revision"`
}

type ImpactPreview struct {
	ProjectRevision  int64 `json:"project_revision"`
	AffectedRunCount int   `json:"affected_run_count"`
}
