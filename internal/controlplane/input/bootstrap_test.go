package input

import (
	"testing"

	"github.com/C1ph3rX13/Aether/internal/project"
)

func TestBootstrapInputForRunUsesCurrentActiveBusinessContext(t *testing.T) {
	withdrawn := "2026-07-21T00:00:00Z"
	closedFact := "f003"
	snapshot := project.ExecutionInputProjection{
		Project: project.ProjectMeta{ID: "project", Generation: 2, Revision: 9},
		Facts: []project.Fact{
			{ID: "goal", Description: "goal", Status: project.FactStatusActive},
			{ID: "origin", Description: "origin", Status: project.FactStatusActive},
			{ID: "f003", Description: "irrelevant", Status: project.FactStatusActive},
		},
		Intents: []project.Intent{
			{ID: "i001", From: []string{"origin"}, Description: "bootstrap", Creator: project.BootstrapIntentCreator},
			{ID: "i002", From: []string{"origin"}, To: &closedFact, Description: "closed", Creator: "operator"},
		},
		Hints: []project.Hint{
			{ID: "h003", Content: "withdrawn", Creator: "operator", Generation: 2, WithdrawnAt: &withdrawn},
			{ID: "h002", Content: "old generation", Creator: "operator", Generation: 1},
			{ID: "h001", Content: "current", Creator: "operator", Generation: 2},
		},
	}
	input, err := BootstrapInputForRun(snapshot, "i001")
	if err != nil {
		t.Fatal(err)
	}
	if input.Origin != "origin" || input.Goal != "goal" || input.TargetIntentID != "i001" ||
		len(input.Hints) != 1 || input.Hints[0].ID != "h001" {
		t.Fatalf("Bootstrap input: %+v", input)
	}
	if _, err := BootstrapInputForRun(snapshot, "i002"); err == nil {
		t.Fatal("closed Bootstrap target was accepted")
	}
}
