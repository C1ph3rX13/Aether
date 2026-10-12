package input

import (
	"fmt"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// BootstrapInputForRun extracts only the initial fast-path context from a Project projection.
func BootstrapInputForRun(snapshot project.ExecutionInputProjection, targetIntentID string) (rundomain.BootstrapInput, error) {
	if targetIntentID == "" {
		return rundomain.BootstrapInput{}, fmt.Errorf("Bootstrap target intent is required")
	}
	input := rundomain.BootstrapInput{
		Schema: rundomain.BootstrapInputSchema, Generation: snapshot.Project.Generation,
		Revision: snapshot.Project.Revision, TargetIntentID: targetIntentID,
	}
	for _, fact := range snapshot.Facts {
		if !fact.IsActive() {
			continue
		}
		switch fact.ID {
		case "origin":
			input.Origin = fact.Description
		case "goal":
			input.Goal = fact.Description
		}
	}
	targetFound := false
	for _, intent := range snapshot.Intents {
		if intent.ID == targetIntentID && intent.IsOpen() {
			targetFound = true
			break
		}
	}
	if !targetFound {
		return rundomain.BootstrapInput{}, fmt.Errorf("Bootstrap target intent %q is not open in the Project projection", targetIntentID)
	}
	for _, hint := range snapshot.Hints {
		if hint.Generation != snapshot.Project.Generation || !hint.IsActive() {
			continue
		}
		input.Hints = append(input.Hints, rundomain.InputHint{
			ID: hint.ID, Content: hint.Content, Creator: hint.Creator, CreatedAt: hint.CreatedAt, Status: "active",
		})
	}
	return input.Normalize()
}
