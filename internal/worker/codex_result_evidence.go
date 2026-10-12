package worker

import "context"

// Codex stdout is the CLI's final JSON response. Session discovery retains
// its existing stderr boundary; result capture never scans those diagnostics.
type codexResultEvidence struct{}

func (codexResultEvidence) NewOutputCapture(*Session) streamingCapture { return nil }
func (codexResultEvidence) SessionDirectory([]string) string           { return "" }
func (codexResultEvidence) CanContinueSession(session *Session) bool {
	return session != nil && session.value != ""
}

func (codexResultEvidence) ObserveSession(_ context.Context, invocation Invocation, output ProcessOutput) *Session {
	var current *string
	if invocation.session != nil {
		current = &invocation.session.value
	}
	id := NewCodexDriver().ExtractSession(current, output.Stdout, output.Stderr)
	if id == nil {
		return nil
	}
	return &Session{value: *id}
}

func (e codexResultEvidence) Capture(ctx context.Context, invocation Invocation, output ProcessOutput) (CaptureResult, error) {
	return captureOutcomeSubmission(invocation, output, e.ObserveSession(ctx, invocation, output))
}
