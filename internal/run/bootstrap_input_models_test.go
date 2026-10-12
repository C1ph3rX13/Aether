package run

import (
	"strings"
	"testing"
)

func TestBootstrapInputIsDeterministicAndBounded(t *testing.T) {
	first := BootstrapInput{
		Generation: 2, Revision: 7, TargetIntentID: "i001", Origin: "origin", Goal: "goal",
		Hints: []InputHint{
			{ID: "h002", Content: "second", Creator: "operator", Status: "active"},
			{ID: "h001", Content: "first", Creator: "operator", Status: "active"},
		},
	}
	second := first
	second.Hints = []InputHint{first.Hints[1], first.Hints[0]}
	firstYAML, firstHash, err := first.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	secondYAML, secondHash, err := second.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstYAML) != string(secondYAML) || firstHash != secondHash ||
		!strings.Contains(string(firstYAML), "schema: aether.bootstrap/v1") {
		t.Fatalf("non-deterministic Bootstrap input:\n%s\n%s", firstYAML, secondYAML)
	}

	invalid := first
	invalid.TargetIntentID = ""
	if _, _, err := invalid.MarshalYAML(); err == nil {
		t.Fatal("missing Bootstrap target was accepted")
	}
	oversized := first
	oversized.Origin = strings.Repeat("x", MaximumBootstrapInputBytes)
	if _, _, err := oversized.MarshalYAML(); err == nil {
		t.Fatal("oversized Bootstrap input was accepted")
	}
}
