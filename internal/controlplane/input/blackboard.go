package input

import (
	"fmt"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// BlackboardInputForRun removes transport and execution fields from a Project projection.
func BlackboardInputForRun(snapshot project.ExecutionInputProjection, targetIntentID *string) (rundomain.BlackboardInput, error) {
	input := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: snapshot.Project.Generation,
		Revision: snapshot.Project.Revision,
		Project: rundomain.InputProject{
			Title: snapshot.Project.Title, BootstrapEnabled: snapshot.Project.BootstrapEnabled,
		},
	}
	if targetIntentID != nil {
		input.TargetIntentID = *targetIntentID
	}
	for _, fact := range snapshot.Facts {
		switch fact.ID {
		case "origin":
			input.Project.Origin = fact.Description
		case "goal":
			input.Project.Goal = fact.Description
		}
		entry := rundomain.InputFact{ID: fact.ID, Description: fact.Description, Status: string(fact.Status)}
		if fact.RetractedAt != nil {
			entry.Status = "retracted"
		} else if entry.Status == "" {
			entry.Status = "active"
		}
		if fact.Supersedes != nil {
			entry.Supersedes = *fact.Supersedes
		}
		if fact.ReplacementFactID != nil {
			entry.ReplacementFactID = *fact.ReplacementFactID
		}
		input.Facts = append(input.Facts, entry)
	}
	targetFound := targetIntentID == nil
	for _, intent := range snapshot.Intents {
		status := "open"
		if intent.CancelledAt != nil {
			status = "cancelled"
		} else if intent.To != nil {
			status = "concluded"
		}
		entry := rundomain.InputIntent{
			ID: intent.ID, From: intent.From, Description: intent.Description, Creator: intent.Creator, Status: status,
		}
		if intent.To != nil {
			entry.To = *intent.To
		}
		input.Intents = append(input.Intents, entry)
		if targetIntentID != nil && intent.ID == *targetIntentID {
			targetFound = true
		}
	}
	for _, hint := range snapshot.Hints {
		if hint.Generation != snapshot.Project.Generation {
			continue
		}
		status := "active"
		if hint.WithdrawnAt != nil {
			status = "withdrawn"
		}
		input.Hints = append(input.Hints, rundomain.InputHint{
			ID: hint.ID, Content: hint.Content, Creator: hint.Creator, CreatedAt: hint.CreatedAt, Status: status,
		})
	}
	if !targetFound {
		return rundomain.BlackboardInput{}, fmt.Errorf("target intent %q is not in the Blackboard projection", *targetIntentID)
	}
	return input.Normalize()
}
