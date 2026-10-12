package run

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDecodeRunResultAcceptsTaskSpecificOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		task      TaskType
		candidate string
		want      OutcomeKind
	}{
		{
			name: "reason completion", task: TaskReason, want: OutcomeCompleteProject,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","from":["f002","f001"]}}`,
		},
		{
			name: "reason proposals", task: TaskReason, want: OutcomeProposeIntents,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"investigate","from":["origin"]}]}}`,
		},
		{
			name: "reason no change", task: TaskReason, want: OutcomeNoChange,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`,
		},
		{
			name: "explore conclusion", task: TaskExplore, want: OutcomeConcludeIntent,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"evidence"}}}`,
		},
		{
			name: "bootstrap completion", task: TaskBootstrap, want: OutcomeCompleteProject,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"goal reached","fact":{"description":"answer"}}}`,
		},
		{
			name: "bootstrap conclusion", task: TaskBootstrap, want: OutcomeConcludeIntent,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"partial answer"}}}`,
		},
		{
			name: "decline", task: TaskExplore, want: OutcomeDecline,
			candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"unsupported domain"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, canonical, hash, err := DecodeRunResult([]byte(test.candidate), test.task, 3)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome.Kind != test.want || len(canonical) == 0 || len(hash) != 64 {
				t.Fatalf("decoded result: result=%+v canonical=%s hash=%q", result, canonical, hash)
			}
		})
	}
}

func TestDecodeRunResultRejectsAmbiguousAndIncompatiblePayloads(t *testing.T) {
	tests := []struct {
		name      string
		task      TaskType
		candidate string
		max       int
	}{
		{name: "cairn wrapper", task: TaskReason, candidate: `{"accepted":true,"data":{}}`, max: 3},
		{name: "agent ownership", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","run_id":"run_x","outcome":{"kind":"no_change"}}`, max: 3},
		{name: "duplicate schema", task: TaskReason, candidate: `{"schema":"old","schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`, max: 3},
		{name: "duplicate kind", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","kind":"no_change","reason":"hidden"}}`, max: 3},
		{name: "trailing value", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}} {}`, max: 3},
		{name: "unknown outcome", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"guess"}}`, max: 3},
		{name: "unknown outcome field", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change","note":"x"}}`, max: 3},
		{name: "wrong task outcome", task: TaskExplore, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`, max: 0},
		{name: "reason missing sources", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done"}}`, max: 3},
		{name: "reason fact field", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","from":["origin"],"fact":null}}`, max: 3},
		{name: "goal source", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","from":["goal"]}}`, max: 3},
		{name: "duplicate source", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","from":["origin","origin"]}}`, max: 3},
		{name: "empty proposals", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[]}}`, max: 3},
		{name: "proposal limit", task: TaskReason, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"a","from":["origin"]},{"description":"b","from":["origin"]}]}}`, max: 1},
		{name: "bootstrap from field", task: TaskBootstrap, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","from":[],"fact":{"description":"answer"}}}`, max: 0},
		{name: "bootstrap missing fact", task: TaskBootstrap, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done"}}`, max: 0},
		{name: "empty decline", task: TaskExplore, candidate: `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":" "}}`, max: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := DecodeRunResult([]byte(test.candidate), test.task, test.max)
			var domainErr *DomainError
			if !errors.As(err, &domainErr) || domainErr.Code != ErrorInvalidResult {
				t.Fatalf("error=%v, want invalid_result", err)
			}
		})
	}
}

func TestDecodeRunResultRejectsGoalAsProposedIntentSource(t *testing.T) {
	candidate := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"valid direction","from":["origin"]},{"description":"invalid direction","from":["goal"]}]}}`)
	_, _, _, err := DecodeRunResult(candidate, TaskReason, 2)
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorInvalidResult {
		t.Fatalf("error=%v, want invalid_result for goal source", err)
	}
}

func TestDecodeRunResultCanonicalizesEquivalentResults(t *testing.T) {
	first := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":" done ","from":["f002","f001"]}}`)
	second := []byte("{\n  \"outcome\": {\"from\": [\"f001\", \"f002\"], \"description\": \"done\", \"kind\": \"complete_project\"},\n  \"schema\": \"aether.run-result/v1\"\n}")
	_, canonicalA, hashA, err := DecodeRunResult(first, TaskReason, 3)
	if err != nil {
		t.Fatal(err)
	}
	_, canonicalB, hashB, err := DecodeRunResult(second, TaskReason, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonicalA, canonicalB) || hashA != hashB {
		t.Fatalf("canonical mismatch:\n%s (%s)\n%s (%s)", canonicalA, hashA, canonicalB, hashB)
	}
}

func TestDecodeRunResultRejectsOversizedCandidate(t *testing.T) {
	candidate := strings.Repeat("x", MaximumResultBytes+1)
	_, _, _, err := DecodeRunResult([]byte(candidate), TaskReason, 3)
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorInvalidResult {
		t.Fatalf("error=%v, want invalid_result", err)
	}
}
