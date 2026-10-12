package run

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestBlackboardInputIsStableAcrossProjectionOrdering(t *testing.T) {
	project := InputProject{Title: "Project", Origin: "origin", Goal: "goal"}
	first := BlackboardInput{
		Generation: 1, Revision: 7,
		Project: project,
		Facts:   []InputFact{{ID: "f2", Description: "second"}, {ID: "f1", Description: "first"}},
		Intents: []InputIntent{{ID: "i1", From: []string{"f2", "f1"}, Description: "inspect", Creator: "reasoner"}},
		Hints:   []InputHint{{ID: "h2", Content: "later", Creator: "operator"}, {ID: "h1", Content: "first", Creator: "operator"}},
	}
	second := BlackboardInput{
		Schema: BlackboardInputSchema, Generation: 1, Revision: 7,
		Project: project,
		Facts:   []InputFact{{ID: "f1", Description: "first"}, {ID: "f2", Description: "second"}},
		Intents: []InputIntent{{ID: "i1", From: []string{"f1", "f2"}, Description: "inspect", Creator: "reasoner"}},
		Hints:   []InputHint{{ID: "h1", Content: "first", Creator: "operator"}, {ID: "h2", Content: "later", Creator: "operator"}},
	}
	firstYAML, firstHash, err := first.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	secondYAML, secondHash, err := second.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstYAML, secondYAML) || firstHash != secondHash {
		t.Fatalf("unstable input:\n%s (%s)\n%s (%s)", firstYAML, firstHash, secondYAML, secondHash)
	}
	if !bytes.Contains(firstYAML, []byte("schema: aether.blackboard/v1\n")) ||
		!bytes.Contains(firstYAML, []byte("origin: origin")) || !bytes.Contains(firstYAML, []byte("goal: goal")) {
		t.Fatalf("schema missing from YAML: %s", firstYAML)
	}
}

func TestBlackboardInputRejectsOversizedAggregate(t *testing.T) {
	facts := make([]InputFact, 65)
	for index := range facts {
		facts[index] = InputFact{
			ID: fmt.Sprintf("f%03d", index), Description: strings.Repeat("x", maximumInputTextBytes),
		}
	}
	if _, _, err := (BlackboardInput{
		Generation: 1, Project: InputProject{Title: "Project", Origin: "origin", Goal: "goal"}, Facts: facts,
	}).MarshalYAML(); err == nil {
		t.Fatal("expected aggregate input size rejection")
	}
}

func TestBlackboardInputRejectsInvalidIdentityAndRecords(t *testing.T) {
	project := InputProject{Title: "Project", Origin: "origin", Goal: "goal"}
	tests := []BlackboardInput{
		{Generation: 0, Revision: 1, Project: project},
		{Generation: 1, Revision: -1, Project: project},
		{Schema: "other/v1", Generation: 1, Project: project},
		{Generation: 1},
		{Generation: 1, Project: project, Facts: []InputFact{{ID: "", Description: "fact"}}},
		{Generation: 1, Project: project, Intents: []InputIntent{{ID: "i1", Description: "intent", Creator: "reasoner", From: []string{""}}}},
		{Generation: 1, Project: project, Hints: []InputHint{{ID: "h1", Content: "hint"}}},
		{Generation: 1, Project: project, Facts: []InputFact{{ID: "f1", Description: "a"}, {ID: "f1", Description: "b"}}},
		{Generation: 1, Project: project, Intents: []InputIntent{{ID: "i1", Description: "intent", Creator: "reasoner", From: []string{"f1", "f1"}}}},
		{Generation: 1, Project: project, Intents: []InputIntent{{ID: "i1", Description: "intent", Creator: "reasoner", Status: "concluded"}}},
	}
	for index, input := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			if _, _, err := input.MarshalYAML(); err == nil {
				t.Fatal("expected invalid input")
			}
		})
	}
}
