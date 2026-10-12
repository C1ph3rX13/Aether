package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestPiOutputCaptureRetainsSessionAndLatestAssistantWithoutTranscript(t *testing.T) {
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	events := []any{
		map[string]any{"type": "message_update", "message": piTestMessage("assistant", "intermediate")},
		map[string]any{"type": "thinking_start"},
		map[string]any{"type": "tool_execution_start", "toolName": "bash"},
		map[string]any{"type": "tool_execution_end", "toolName": "bash"},
		map[string]any{"type": "message_end", "message": piTestMessage("assistant", "intermediate")},
		map[string]any{"type": "turn_end", "message": piTestMessage("assistant", candidate)},
		map[string]any{
			"type":     "agent_end",
			"messages": []any{piTestMessage("assistant", strings.Repeat("discarded transcript ", 40000)), piTestMessage("assistant", candidate)},
		},
	}
	var stream []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		stream = append(stream, line...)
		stream = append(stream, '\n')
	}
	for index := 0; index < len(stream); index += 37 {
		end := index + 37
		if end > len(stream) {
			end = len(stream)
		}
		capture.WriteStdout(stream[index:end])
	}

	result := capture.Result()
	if result.Overflowed {
		t.Fatal("irrelevant oversized agent_end event overflowed a valid final response")
	}
	if result.Stdout != candidate {
		t.Fatalf("captured response=%q, want final assistant text", result.Stdout)
	}
	session := capture.Session()
	if session == nil || session.value != "0192f7e5-7b2a-7c01-8a11-223344556677" {
		t.Fatalf("session=%v, want validated Session UUID", session)
	}
}

func TestPiStdoutEvidenceRequiresCoherentTerminalLifecycle(t *testing.T) {
	message := func(text, stopReason string) string {
		return `{"role":"assistant","stopReason":` + jsonString(stopReason) + `,"content":[{"type":"text","text":` + jsonString(text) + `}]}`
	}
	final := message("final", "stop")
	tests := []struct {
		name   string
		events []string
		state  piEvidenceState
	}{
		{name: "complete", state: piEvidenceComplete, events: []string{
			`{"type":"message_end","message":` + final + `}`,
			`{"type":"turn_end","message":` + final + `}`,
			`{"type":"agent_end","messages":[` + final + `]}`,
		}},
		{name: "isolated message end", state: piEvidenceUnavailable, events: []string{
			`{"type":"message_end","message":` + final + `}`,
		}},
		{name: "missing agent end", state: piEvidenceUnavailable, events: []string{
			`{"type":"message_end","message":` + final + `}`,
			`{"type":"turn_end","message":` + final + `}`,
		}},
		{name: "terminal text disagreement", state: piEvidenceBoundaryError, events: []string{
			`{"type":"message_end","message":` + final + `}`,
			`{"type":"turn_end","message":` + message("different", "stop") + `}`,
			`{"type":"agent_end","messages":[` + final + `]}`,
		}},
		{name: "terminal stop disagreement", state: piEvidenceBoundaryError, events: []string{
			`{"type":"message_end","message":` + final + `}`,
			`{"type":"turn_end","message":` + message("final", "length") + `}`,
			`{"type":"agent_end","messages":[` + final + `]}`,
		}},
		{name: "terminal events out of order", state: piEvidenceBoundaryError, events: []string{
			`{"type":"turn_end","message":` + final + `}`,
			`{"type":"message_end","message":` + final + `}`,
			`{"type":"agent_end","messages":[` + final + `]}`,
		}},
		{name: "duplicate prelude", state: piEvidenceBoundaryError, events: []string{
			strictPiPrelude("/workspace"),
		}},
		{name: "duplicate header", state: piEvidenceBoundaryError, events: []string{
			strictPiStreamHeader,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := newPiOutputCapture(nil)
			primePiStream(capture)
			for _, event := range test.events {
				capture.WriteStdout([]byte(event + "\n"))
			}
			if evidence := capture.Evidence(); evidence.state != test.state {
				t.Fatalf("stdout evidence state=%d, want %d", evidence.state, test.state)
			}
		})
	}
}

func TestPiOutputCapturePreservesInvocationCursor(t *testing.T) {
	session := &Session{value: "0192f7e5-7b2a-7c01-8a11-223344556677", cursor: "00000007", hasCursor: true}
	capture := newPiOutputCapture(session)
	primePiStream(capture)
	observed := capture.Session()
	if observed == nil || !observed.hasCursor || observed.cursor != session.cursor {
		t.Fatalf("capture Session=%+v, want preserved cursor", observed)
	}
}
func TestPiOutputCaptureRejectsResumedSessionHeaderMismatch(t *testing.T) {
	mismatched := "0192f7e5-7b2a-7c01-8a11-223344556678"
	capture := newPiOutputCapture(&Session{value: mismatched})
	primePiStream(capture)
	if evidence := capture.Evidence(); evidence.state != piEvidenceBoundaryError {
		t.Fatalf("resumed header mismatch state=%d, want boundary error", evidence.state)
	}
}
func TestPiOutputCaptureRejectsNonPreludeBeforeLaunchBoundary(t *testing.T) {
	capture := newPiOutputCapture(nil)
	capture.WriteStdout([]byte(strings.Repeat("x", maximumPiEventLineBytes+1)))
	capture.WriteStdout([]byte("\n"))
	primePiStream(capture)
	if evidence := capture.Evidence(); evidence.state != piEvidenceBoundaryError {
		t.Fatalf("prelude-after-overflow evidence state=%d, want boundary error", evidence.state)
	}
}

func TestPiOutputCaptureRejectsUnboundedEventAndKeepsDraining(t *testing.T) {
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	capture.WriteStdout([]byte(strings.Repeat("x", maximumPiEventLineBytes+1)))
	capture.WriteStdout([]byte("\n"))
	capture.WriteStdout([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"later"}]}}` + "\n"))
	result := capture.Result()
	if result.Overflowed {
		t.Fatal("a discarded oversized transport line should not block a later final message")
	}
	if result.Stdout != "later" {
		t.Fatalf("response=%q, want later assistant text", result.Stdout)
	}
}

func TestPiOutputCaptureKeepsFinalResponseAfterMoreThanOneMiBOfTransportNoise(t *testing.T) {
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	for range 2 {
		capture.WriteStdout([]byte(strings.Repeat("x", maximumPiEventLineBytes+1)))
		capture.WriteStdout([]byte("\n"))
	}
	message, err := json.Marshal(map[string]any{
		"type": "message_end", "message": piTestMessage("assistant", candidate),
	})
	if err != nil {
		t.Fatal(err)
	}
	capture.WriteStdout(append(message, '\n'))
	result := capture.Result()
	if result.Overflowed || result.Stdout != candidate {
		t.Fatalf("capture=%+v, want final assistant text without transport overflow", result)
	}
	if session := capture.Session(); session == nil || session.value != "0192f7e5-7b2a-7c01-8a11-223344556677" {
		t.Fatalf("session=%v, want validated Session UUID", session)
	}
}

func TestPiOutputCaptureDecodesLargeAgentEndTranscriptBoundedly(t *testing.T) {
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	messages := make([]any, 0, 7000)
	for range 7000 {
		messages = append(messages, piTestMessage("user", strings.Repeat("discarded transcript ", 12)))
	}
	messages = append(messages, piTestMessage("assistant", candidate))
	line, err := json.Marshal(map[string]any{"type": "agent_end", "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	if len(line) <= maximumPiEventLineBytes {
		t.Fatalf("agent_end test line=%d, want larger than streaming line limit", len(line))
	}
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	capture.WriteStdout(append(line, '\n'))
	result := capture.Result()
	if result.Overflowed || result.Stdout != candidate {
		t.Fatalf("capture=%+v, want final assistant from large agent_end", result)
	}
}

func TestPiOutputCaptureUsesLatestAssistantFromAgentEnd(t *testing.T) {
	intermediate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	final := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"final"}}`
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	capture.WriteStdout([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text":` +
		jsonString(intermediate) + `}]}}` + "\n"))
	agentEnd, err := json.Marshal(map[string]any{
		"type": "agent_end", "messages": []any{
			piTestMessage("assistant", intermediate),
			piTestMessage("assistant", final),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	capture.WriteStdout(append(agentEnd, '\n'))
	if result := capture.Result(); result.Stdout != final {
		t.Fatalf("response=%q, want latest agent_end assistant", result.Stdout)
	}
}

func TestPiOutputCaptureTreatsEmptyAgentEndAssistantAsAuthoritative(t *testing.T) {
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	capture.WriteStdout([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text":` +
		jsonString(candidate) + `}]}}` + "\n"))
	capture.WriteStdout([]byte(`{"type":"agent_end","messages":[` +
		`{"role":"assistant","content":[{"type":"text":` + jsonString(candidate) + `}]},` +
		`{"role":"assistant","content":[]}]}` + "\n"))
	if result := capture.Result(); result.Stdout != "" {
		t.Fatalf("response=%q, want empty authoritative assistant text", result.Stdout)
	}
}

func TestPiOutputCaptureReportsOversizedAssistantResponseAfterTransportLineLimit(t *testing.T) {
	capture := newPiOutputCapture(nil)
	primePiStream(capture)
	line, err := json.Marshal(map[string]any{
		"type":    "message_end",
		"message": piTestMessage("assistant", strings.Repeat("x", maximumPiEventLineBytes)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(line) <= maximumPiEventLineBytes {
		t.Fatalf("test event line=%d, want larger than transport limit", len(line))
	}
	capture.WriteStdout(append(line, '\n'))
	if capture.Evidence().state != piEvidenceUnavailable {
		t.Fatal("oversized assistant event supplied complete lifecycle evidence")
	}
	if result := capture.Result(); result.Overflowed {
		t.Fatal("oversized final assistant event was misclassified as transport overflow")
	}
}

func TestPiCaptureFailurePreservesSessionForConclude(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "explore",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := strictPiPrompt(invocation)
	candidate := "not a result"
	captured, err := adapter.Capture(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: strictPiLifecycle(strictPiHeader(invocation), candidate), Exited: true, ExitCode: 0,
		StoppedProcessFileReader: &piEvidenceFileReader{content: []byte(strictPiSessionFile(
			strictPiHeader(invocation), prompt, candidate, "00000001", "00000002",
		))},
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureInvalidCandidate {
		t.Fatalf("capture=%+v error=%v, want invalid result file", captured, err)
	}
	if captured.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate ||
		captured.CaptureDiagnostic != rundomain.CaptureDiagnosticResultFileInvalid || captured.Session == nil {
		t.Fatalf("capture=%+v, want invalid result file with verified Session", captured)
	}
	conclude, ok := adapter.(ConcludeAdapter)
	if !ok {
		t.Fatal("Pi adapter does not expose conclude capability")
	}
	fallback, err := conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abd",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude",
			Input: InputReference{Kind: InputBlackboardFile, Path: rundomain.BlackboardPath("run-capture-failure", rundomain.PhaseExploreConclude)},
		},
		Session: captured.Session,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fallback.Argv(), "\n"), "--session\n0192f7e5-7b2a-7c01-8a11-223344556677") {
		t.Fatalf("conclude did not resume capture Session: %v", fallback.Argv())
	}
}

func TestPiStreamingCaptureStillRequiresSuccessfulProcessExit(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	captured, err := adapter.Capture(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: primedPiStream() + `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text":` +
			jsonString(candidate) + `}]}}` + "\n",
		Exited: true, ExitCode: 9,
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureProcessFailed {
		t.Fatalf("capture=%+v error=%v, want process_failed", captured, err)
	}
	if len(captured.Candidate) != 0 {
		t.Fatal("assistant text observed before non-zero exit was accepted")
	}
}

func primedPiStream() string {
	return strictPiPrelude("/workspace") + "\n" + strictPiStreamHeader + "\n" + `{"type":"agent_start"}` + "\n"
}

func primePiStream(capture *piOutputCapture) {
	capture.WriteStdout([]byte(primedPiStream()))
}

const strictPiStreamHeader = `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`

func piTestMessage(role, text string) map[string]any {
	return map[string]any{
		"role":    role,
		"content": []any{map[string]any{"type": "text", "text": text}},
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
