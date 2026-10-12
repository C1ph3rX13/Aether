package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func normalizeOutcomeSubmission(raw []byte, contract ResultContract) (json.RawMessage, string, error) {
	if len(raw) == 0 {
		return nil, "", &CaptureError{Code: CaptureNoCandidate, Detail: "outcome submission is empty"}
	}
	if len(raw) > rundomain.MaximumResultBytes {
		return nil, "", &CaptureError{Code: CaptureCandidateTooLarge, Detail: "outcome submission exceeds its limit"}
	}
	if !utf8.Valid(raw) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome submission is not valid UTF-8"}
	}
	if !isSingleJSONObject(raw) || hasDuplicateObjectKeys(raw) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome submission must be one JSON object"}
	}
	var fields any
	if json.Unmarshal(raw, &fields) != nil || hasNonLowercaseField(fields) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome submission has unknown fields"}
	}

	var envelope struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome submission has invalid fields"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome submission contains multiple values"}
	}
	if len(envelope.Outcome) == 0 || string(envelope.Outcome) == "null" ||
		!isSingleJSONObject(envelope.Outcome) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome object is required"}
	}

	return contract.normalizeOutcome(bytes.TrimSpace(envelope.Outcome))
}

// All submission field names are lowercase. encoding/json otherwise accepts
// case-insensitive aliases of known fields, which the protocol does not admit.
func hasNonLowercaseField(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if strings.ToLower(key) != key || hasNonLowercaseField(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if hasNonLowercaseField(child) {
				return true
			}
		}
	}
	return false
}

func captureOutcomeSubmission(invocation Invocation, output ProcessOutput, session *Session) (CaptureResult, error) {
	diagnostics := Diagnostics{
		StdoutSummary: diagnosticSummary(output.Stdout),
		StderrSummary: diagnosticSummary(output.Stderr),
	}
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

	candidate, resultHash, err := normalizeOutcomeSubmission(
		[]byte(strings.TrimSpace(output.Stdout)), invocation.resultContract,
	)
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
