package worker

import (
	"context"
)

// mockResultEvidence is the Mock Adapter's submission boundary. The mock
// process writes the Adapter-owned outcome package; this module strictly
// decodes it and creates the backend RunResult envelope.
type mockResultEvidence struct{}

func (mockResultEvidence) NewOutputCapture(*Session) streamingCapture { return nil }

func (mockResultEvidence) SessionDirectory([]string) string { return "" }

func (mockResultEvidence) ObserveSession(_ context.Context, invocation Invocation, _ ProcessOutput) *Session {
	return mockResultSession(invocation)
}

func (mockResultEvidence) CanContinueSession(session *Session) bool {
	return session != nil && session.value != ""
}

func (mockResultEvidence) Capture(
	_ context.Context,
	invocation Invocation,
	output ProcessOutput,
) (CaptureResult, error) {
	return captureOutcomeSubmission(invocation, output, mockResultSession(invocation))
}

func mockResultSession(invocation Invocation) *Session {
	if invocation.session == nil {
		return nil
	}
	session := *invocation.session
	return &session
}

var _ resultEvidenceProvider = (*MockDriver)(nil)
