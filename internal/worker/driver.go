package worker

import (
	"context"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// invocationBuilder provides an agent's command, submission protocol and
// result evidence. Session and continuation capabilities remain optional.
type invocationBuilder interface {
	submissionProtocol
	BuildExecute(w WorkerConfig, prompt string, session *string) DriverResult
	ResultEvidence() resultEvidenceModule
}

// sessionPreparer is optional. Stateless drivers omit it instead of exposing
// a nil-returning placeholder method.
type sessionPreparer interface {
	PrepareSession() *string
}

type recoveryBuilder interface {
	BuildRecovery(w WorkerConfig, prompt, session string, phase Phase) []string
}

// streamingCapture is the Worker-side output boundary passed from Runtime to
// an Adapter-owned evidence module. Pi-specific state stays behind the
// concrete module implementation.
type streamingCapture interface {
	rundomain.ProcessOutputCapture
}

// resultEvidenceModule owns an Adapter's complete result evidence decision.
// Pi uses this deep module for bounded stdout lifecycle evidence, Session
// verification, TOML result-file normalization, and continuation eligibility.
type resultEvidenceModule interface {
	NewOutputCapture(*Session) streamingCapture
	SessionDirectory([]string) string
	ObserveSession(context.Context, Invocation, ProcessOutput) *Session
	Capture(context.Context, Invocation, ProcessOutput) (CaptureResult, error)
	CanContinueSession(*Session) bool
}

type resultEvidenceProvider interface {
	ResultEvidence() resultEvidenceModule
}

// concludeBuilder is optional. Its presence is the source of truth for
// whether an adapter can run a conclude phase.
type concludeBuilder interface {
	BuildConclude(w WorkerConfig, prompt string, session string) []string
}

// driverCapabilities composes the independent capabilities a worker adapter
// needs. Optional command builders omit unsupported CLI operations; the
// result evidence module owns the complete capture and transport policy.
type driverCapabilities struct {
	typeName       string
	prepareSession func() *string
	buildExecute   func(WorkerConfig, string, *string) DriverResult
	resultEvidence resultEvidenceModule
	buildRecovery  func(WorkerConfig, string, string, Phase) []string
	buildConclude  func(WorkerConfig, string, string) []string
}

func (d driverCapabilities) TypeName() string { return d.typeName }

func (d driverCapabilities) hasConclude() bool { return d.buildConclude != nil }

func (d driverCapabilities) hasRecovery() bool { return d.buildRecovery != nil }

// DriverResult contains the constructed command and optional session.
type DriverResult struct {
	Argv         []string
	Session      *string
	ProtocolFile *rundomain.ProtocolFile
}
