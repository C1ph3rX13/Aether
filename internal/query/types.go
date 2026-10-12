package query

import (
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type ActiveCounts struct {
	Total  int `json:"total"`
	Active int `json:"active"`
}

type IntentCounts struct {
	Total int `json:"total"`
	Open  int `json:"open"`
}

type BlackboardSummary struct {
	Facts   ActiveCounts `json:"facts"`
	Intents IntentCounts `json:"intents"`
	Hints   ActiveCounts `json:"hints"`
}

type ExecutionSummary struct {
	Status rundomain.ProjectExecutionStatus `json:"status"`
}

type ProjectCatalogEntry struct {
	Project    project.ProjectMeta `json:"project"`
	Blackboard BlackboardSummary   `json:"blackboard"`
	Execution  ExecutionSummary    `json:"execution"`
}
