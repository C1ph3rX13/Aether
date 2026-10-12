package project

type ProjectStatus string

const (
	ProjectStatusActive    ProjectStatus = "active"
	ProjectStatusPaused    ProjectStatus = "paused"
	ProjectStatusCompleted ProjectStatus = "completed"
)

type ProjectMeta struct {
	ID               string        `json:"id" yaml:"id"`
	Title            string        `json:"title" yaml:"title"`
	Status           ProjectStatus `json:"status" yaml:"status"`
	BootstrapEnabled bool          `json:"bootstrap_enabled" yaml:"bootstrap_enabled"`
	CreatedAt        string        `json:"created_at" yaml:"created_at"`
	Generation       int           `json:"generation" yaml:"generation"`
	Revision         int64         `json:"revision" yaml:"revision"`
	ConfigRevision   string        `json:"config_revision,omitempty" yaml:"config_revision,omitempty"`
}

type ProjectDetail struct {
	Project           ProjectMeta        `json:"project" yaml:"project"`
	Facts             []Fact             `json:"facts" yaml:"facts"`
	Intents           []Intent           `json:"intents" yaml:"intents"`
	Hints             []Hint             `json:"hints" yaml:"hints"`
	CompletionRecords []CompletionRecord `json:"completion_records" yaml:"completion_records"`
}

// ExecutionInputProjection contains only authoritative records that can
// contribute to a normalized Run input. Completion history is audit-only.
type ExecutionInputProjection struct {
	Project ProjectMeta
	Facts   []Fact
	Intents []Intent
	Hints   []Hint
}

func ExecutionInputProjectionFromDetail(detail ProjectDetail) ExecutionInputProjection {
	return ExecutionInputProjection{
		Project: detail.Project, Facts: detail.Facts, Intents: detail.Intents, Hints: detail.Hints,
	}
}
