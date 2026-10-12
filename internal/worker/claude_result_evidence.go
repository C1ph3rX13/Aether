package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// claudeResultEvidence accepts only Claude Code's successful JSON result
// envelope. The CLI's result text and metadata remain diagnostics at this
// boundary; the business payload is structured_output alone.
type claudeResultEvidence struct{}

func (claudeResultEvidence) NewOutputCapture(*Session) streamingCapture { return nil }

func (claudeResultEvidence) SessionDirectory([]string) string { return "" }

func (claudeResultEvidence) ObserveSession(_ context.Context, invocation Invocation, _ ProcessOutput) *Session {
	if invocation.session == nil {
		return nil
	}
	session := *invocation.session
	return &session
}

func (claudeResultEvidence) CanContinueSession(session *Session) bool {
	return session != nil && session.value != ""
}

func (e claudeResultEvidence) Capture(
	ctx context.Context,
	invocation Invocation,
	output ProcessOutput,
) (CaptureResult, error) {
	diagnostics := Diagnostics{
		StdoutSummary: diagnosticSummary(output.Stdout),
		StderrSummary: diagnosticSummary(output.Stderr),
	}
	session := e.ObserveSession(ctx, invocation, output)
	if output.ErrorCode == rundomain.ProcessErrorOutputTooLarge {
		return captureErrorResult(session, diagnostics, CaptureOutputTooLarge,
			"process output exceeded its configured limit")
	}
	if output.Cancelled || output.TimedOut {
		code := CaptureCancelled
		if output.TimedOut {
			code = CaptureTimedOut
		}
		return captureErrorResult(session, diagnostics, code, "process did not produce a usable result")
	}
	if output.ErrorCode != "" || !output.Exited || output.ExitCode != 0 {
		return captureErrorResult(session, diagnostics, CaptureProcessFailed,
			"agent process did not exit successfully before result capture")
	}

	candidate, resultHash, err := normalizeClaudeResultEnvelope([]byte(output.Stdout), invocation.resultContract)
	if err != nil {
		capture := CaptureResult{Session: session, Diagnostics: diagnostics}
		var captureErr *CaptureError
		if asCaptureError(err, &captureErr) {
			capture.CaptureFailureReason = captureFailureReason(captureErr.Code)
		}
		return capture, err
	}
	return CaptureResult{
		Candidate: candidate, ResultHash: resultHash,
		Session: session, Diagnostics: diagnostics,
	}, nil
}

func normalizeClaudeResultEnvelope(raw []byte, contract ResultContract) (json.RawMessage, string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, "", &CaptureError{Code: CaptureNoCandidate, Detail: "Claude Code returned no result envelope"}
	}
	if len(trimmed) > rundomain.MaximumResultBytes {
		return nil, "", &CaptureError{Code: CaptureCandidateTooLarge, Detail: "Claude Code result envelope exceeds its limit"}
	}
	if !utf8.Valid(trimmed) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result envelope is not valid UTF-8"}
	}
	if hasDuplicateObjectKeys(trimmed) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result envelope contains duplicate fields"}
	}
	if !json.Valid(trimmed) {
		if hasMultipleTopLevelValues(trimmed) {
			return nil, "", &CaptureError{Code: CaptureMultipleCandidates, Detail: "Claude Code returned multiple result envelopes"}
		}
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result envelope is invalid JSON"}
	}
	if !isSingleJSONObject(trimmed) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result envelope must be one JSON object"}
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result envelope is invalid"}
	}
	if !claudeStringFieldEquals(envelope, "type", "result") ||
		!claudeStringFieldEquals(envelope, "subtype", "success") {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result is not a successful result object"}
	}
	if raw, present := envelope["is_error"]; present {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result has an invalid error flag"}
		}
		isError, ok := value.(bool)
		if !ok {
			return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result has an invalid error flag"}
		}
		if isError {
			return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code result reports an error"}
		}
	}
	structured, ok := envelope["structured_output"]
	if !ok || bytes.Equal(bytes.TrimSpace(structured), []byte("null")) {
		return nil, "", &CaptureError{Code: CaptureNoCandidate, Detail: "Claude Code result has no structured output"}
	}
	structured = bytes.TrimSpace(structured)
	if !isSingleJSONObject(structured) || hasDuplicateObjectKeys(structured) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "Claude Code structured output must be one JSON object"}
	}
	return normalizeOutcomeSubmission(structured, contract)
}

func claudeStringFieldEquals(fields map[string]json.RawMessage, name, want string) bool {
	raw, ok := fields[name]
	if !ok {
		return false
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && value == want
}

var _ resultEvidenceProvider = (*ClaudeCodeDriver)(nil)
