package input

import (
	"bytes"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/project"
)

func TestBlackboardInputForRunIsStableAndExcludesExecutionFields(t *testing.T) {
	target := "i001"
	snapshot := project.ExecutionInputProjection{
		Project: project.ProjectMeta{ID: "proj", Title: "Project", Generation: 2, Revision: 9, BootstrapEnabled: true},
		Facts: []project.Fact{
			{ID: "goal", Description: "goal", Status: project.FactStatusActive},
			{ID: "origin", Description: "origin", Status: project.FactStatusActive},
			{ID: "old", Description: "superseded", RetractedAt: stringPointer("2026-01-01T00:00:00Z")},
		},
		Intents: []project.Intent{{
			ID: target, From: []string{"origin"}, Description: "inspect", Creator: "reasoner",
		}},
		Hints: []project.Hint{
			{ID: "h-old", Content: "old", Creator: "operator", Generation: 1},
			{ID: "h-current", Content: "current", Creator: "operator", Generation: 2},
		},
	}
	input, err := BlackboardInputForRun(snapshot, &target)
	if err != nil {
		t.Fatal(err)
	}
	data, hash, err := input.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || !bytes.Contains(data, []byte("target_intent_id: i001")) ||
		!bytes.Contains(data, []byte("h-current")) || !bytes.Contains(data, []byte("status: retracted")) ||
		!bytes.Contains(data, []byte("title: Project")) || !bytes.Contains(data, []byte("origin: origin")) ||
		!bytes.Contains(data, []byte("goal: goal")) || !bytes.Contains(data, []byte("bootstrap_enabled: true")) {
		t.Fatalf("run input: hash=%q\n%s", hash, data)
	}
	for _, excluded := range [][]byte{[]byte("worker:"), []byte("last_heartbeat"), []byte("h-old")} {
		if bytes.Contains(data, excluded) {
			t.Fatalf("execution or old-generation data leaked into input: %q\n%s", excluded, data)
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestBlackboardInputForRunRejectsMissingTarget(t *testing.T) {
	target := "i404"
	_, err := BlackboardInputForRun(project.ExecutionInputProjection{Project: project.ProjectMeta{Generation: 1}}, &target)
	if err == nil {
		t.Fatal("expected missing target rejection")
	}
}
