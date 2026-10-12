package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func claudeEnvelopeForOutcome(outcome string) string {
	return claudeSuccessEnvelope(outcome)
}

func TestClaudeStructuredOutputMapsTaskOutcomesToCanonicalRunResults(t *testing.T) {
	tests := []struct {
		name    string
		task    rundomain.TaskType
		phase   Phase
		outcome string
		want    rundomain.OutcomeKind
	}{
		{
			name: "reason", task: rundomain.TaskReason, phase: PhaseReason,
			outcome: `{"outcome":{"kind":"no_change"}}`, want: rundomain.OutcomeNoChange,
		},
		{
			name: "explore", task: rundomain.TaskExplore, phase: PhaseExplore,
			outcome: `{"outcome":{"kind":"conclude_intent","fact":{"description":"confirmed quote \\\"x\\\" and line\\nnext"}}}`, want: rundomain.OutcomeConcludeIntent,
		},
		{
			name: "bootstrap", task: rundomain.TaskBootstrap, phase: PhaseBootstrap,
			outcome: `{"outcome":{"kind":"complete_project","description":"goal confirmed","fact":{"description":"evidence\\nwith\\\\path"}}}`, want: rundomain.OutcomeCompleteProject,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewAgentAdapter(WorkerConfig{Name: "claude", Type: WorkerTypeClaudeCode})
			if err != nil {
				t.Fatal(err)
			}
			request := AgentRequest{
				Task: test.task, Phase: test.phase, Prompt: "task",
				Input: testInputForTask(test.task), MaxProposedIntents: maxProposedIntentsForTest(test.task),
			}
			invocation, err := adapter.Prepare(request)
			if err != nil {
				t.Fatal(err)
			}
			captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{
				Exited: true, ExitCode: 0, Stdout: claudeEnvelopeForOutcome(test.outcome),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, canonical, hash, err := rundomain.DecodeRunResult(captured.Candidate, test.task, 3)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome.Kind != test.want || string(canonical) != string(captured.Candidate) || hash != captured.ResultHash {
				t.Fatalf("captured result=%+v canonical=%s hash=%q", result, captured.Candidate, captured.ResultHash)
			}
		})
	}
}

func TestClaudeStructuredOutputRejectsTextFallbackAndInvalidSources(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "claude", Type: WorkerTypeClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: testInputForTask(rundomain.TaskReason), MaxProposedIntents: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	validOutcome := `{"outcome":{"kind":"no_change"}}`
	tests := []struct {
		name string
		text string
		code CaptureErrorCode
	}{
		{name: "missing structured output", text: `{"type":"result","subtype":"success","is_error":false,"result":"fallback"}`, code: CaptureNoCandidate},
		{name: "null structured output", text: claudeSuccessEnvelope("null"), code: CaptureNoCandidate},
		{name: "missing success subtype", text: `{"type":"result","is_error":false,"structured_output":` + validOutcome + `}`, code: CaptureInvalidCandidate},
		{name: "non success subtype", text: `{"type":"result","subtype":"error","is_error":false,"structured_output":` + validOutcome + `}`, code: CaptureInvalidCandidate},
		{name: "null error flag", text: `{"type":"result","subtype":"success","is_error":null,"structured_output":` + validOutcome + `}`, code: CaptureInvalidCandidate},
		{name: "error result", text: `{"type":"result","subtype":"success","is_error":true,"structured_output":` + validOutcome + `}`, code: CaptureInvalidCandidate},
		{name: "fenced structured output is not an envelope", text: "```json\n" + claudeSuccessEnvelope(validOutcome) + "\n```", code: CaptureMultipleCandidates},
		{name: "unknown structured field", text: claudeSuccessEnvelope(`{"outcome":{"kind":"no_change","extra":true}}`), code: CaptureInvalidCandidate},
		{name: "duplicate envelope field", text: `{"type":"result","type":"result","subtype":"success","is_error":false,"structured_output":` + validOutcome + `}`, code: CaptureInvalidCandidate},
		{name: "duplicate structured field", text: claudeSuccessEnvelope(`{"outcome":{"kind":"no_change","kind":"no_change"}}`), code: CaptureInvalidCandidate},
		{name: "multiple envelopes", text: claudeSuccessEnvelope(validOutcome) + "\n" + claudeSuccessEnvelope(validOutcome), code: CaptureMultipleCandidates},
		{name: "oversized envelope", text: claudeSuccessEnvelope(`{"outcome":{"kind":"decline","reason":"` + strings.Repeat("x", rundomain.MaximumResultBytes) + `"}}`), code: CaptureCandidateTooLarge},
		{name: "result text only", text: `{"type":"result","subtype":"success","is_error":false,"result":"` + strings.ReplaceAll(validOutcome, `"`, `\"`) + `"}`, code: CaptureNoCandidate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{Exited: true, ExitCode: 0, Stdout: test.text})
			var captureErr *CaptureError
			if !errors.As(err, &captureErr) || captureErr.Code != test.code {
				t.Fatalf("error=%v, want %s", err, test.code)
			}
			if len(captured.Candidate) != 0 || captured.ResultHash != "" {
				t.Fatalf("invalid Claude result produced candidate=%+v", captured)
			}
		})
	}
}

func TestClaudeStructuredOutputIgnoresResultTextAndMetadata(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "claude", Type: WorkerTypeClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: testInputForTask(rundomain.TaskReason), MaxProposedIntents: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	output := `{"type":"result","subtype":"success","is_error":false,"result":"legacy text ignored","unknown_metadata":{"kept":true},"structured_output":{"outcome":{"kind":"no_change"}}}`
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{Exited: true, ExitCode: 0, Stdout: output})
	if err != nil {
		t.Fatal(err)
	}
	if string(captured.Candidate) != `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}` {
		t.Fatalf("Claude used ignored result text: %s", captured.Candidate)
	}
}

func TestClaudeStructuredOutputNeverCommitsAfterProcessFailure(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "claude", Type: WorkerTypeClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		Task: rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "explore",
		Input: testInputForTask(rundomain.TaskExplore),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{
		Exited: true, ExitCode: 1, Stdout: claudeEnvelopeForOutcome(`{"outcome":{"kind":"decline","reason":"ignored"}}`),
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureProcessFailed {
		t.Fatalf("error=%v, want process failure", err)
	}
	if len(captured.Candidate) != 0 {
		t.Fatalf("process failure exposed structured candidate: %s", captured.Candidate)
	}
}

func TestClaudeNativeSchemaIsTaskAndPhaseSpecific(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "claude", Type: WorkerTypeClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: testInputForTask(rundomain.TaskReason), MaxProposedIntents: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := invocation.Argv()
	var schema string
	for index, arg := range args {
		if arg == "--json-schema" && index+1 < len(args) {
			schema = args[index+1]
		}
	}
	if schema == "" || !strings.Contains(strings.Join(args, "\n"), "--output-format\njson") {
		t.Fatalf("Claude invocation omitted native schema flags: %v", args)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "object" || decoded["additionalProperties"] != false || decoded["properties"] == nil {
		t.Fatalf("schema does not describe the strict outcome package: %s", schema)
	}
}
