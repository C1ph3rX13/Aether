package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

var continuationPartial = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"confirmed"}}}`)
var continuationComplete = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","fact":{"description":"confirmed"}}}`)
var continuationDecline = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"no confirmed fact"}}`)

func TestContinuationProtocol(t *testing.T) {
	normal := ProcessOutput{Exited: true}
	timedOut := ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true}
	// An apparent candidate from a failed process must never enter Capture.
	abnormal := ProcessOutput{Exited: true, ExitCode: 9, Stdout: string(continuationDecline)}
	partial := CapturedResult{Candidate: continuationPartial}
	invalid := CapturedResult{Candidate: protocolInvalidConcludeResult}
	malformed := CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate, CaptureDiagnostic: CaptureDiagnosticResultFileInvalid}
	for _, task := range []TaskType{TaskExplore, TaskBootstrap} {
		t.Run(string(task), func(t *testing.T) {
			for _, test := range []struct {
				name       string
				outputs    []ProcessOutput
				captures   []CapturedResult
				configure  func(*continuationTestHarness)
				stages     []string
				trace      []string
				kind       CompletionKind
				code       ExecutionErrorCode
				outcome    OutcomeKind
				failure    CaptureFailureReason
				diagnostic CaptureDiagnostic
			}{
				{name: "normal primary decline", outputs: []ProcessOutput{normal}, captures: []CapturedResult{{Candidate: continuationDecline}},
					stages: []string{"primary"}, trace: []string{"capture:primary"}, kind: CompletionDeclined, outcome: OutcomeDecline},
				{name: "timeout authorizes twice without recovery", outputs: []ProcessOutput{timedOut, normal}, captures: []CapturedResult{partial},
					stages: []string{"primary", "conclude"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "capture:conclude"}, kind: CompletionResultReady, outcome: OutcomeConcludeIntent},
				{name: "timeout without verified session", outputs: []ProcessOutput{timedOut}, configure: func(h *continuationTestHarness) { h.blockContinuation = true },
					stages: []string{"primary"}, trace: []string{"authorize", "observe:primary"}, kind: CompletionFailed, code: ExecutionDeadline},
				{name: "timeout loses authorization after observation", outputs: []ProcessOutput{timedOut}, configure: func(h *continuationTestHarness) { h.authorizationErrorAt = 2 },
					stages: []string{"primary"}, trace: []string{"authorize", "observe:primary", "authorize"}, kind: CompletionInterrupted, code: ExecutionLeaseExpired},
				{name: "primary recovery accepts partial directly", outputs: []ProcessOutput{abnormal, normal}, captures: []CapturedResult{partial},
					stages: []string{"primary", "primary-recovery"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary", "reserve", "capture:primary-recovery"}, kind: CompletionResultReady, code: ExecutionProcessFailed, outcome: OutcomeConcludeIntent},
				{name: "recovery authorization precedes observation", outputs: []ProcessOutput{abnormal}, configure: func(h *continuationTestHarness) { h.authorizationErrorAt = 1 },
					stages: []string{"primary"}, trace: []string{"authorize"}, kind: CompletionInterrupted, code: ExecutionLeaseExpired},
				{name: "recovery requires verified session", outputs: []ProcessOutput{abnormal}, configure: func(h *continuationTestHarness) { h.blockContinuation = true },
					stages: []string{"primary"}, trace: []string{"authorize", "observe:primary"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "insufficient recovery deadline", outputs: []ProcessOutput{abnormal}, configure: func(h *continuationTestHarness) { h.started.Run.StartedAt = time.Now().Add(-28 * time.Second) },
					stages: []string{"primary"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "recovery prepare fails before reservation", outputs: []ProcessOutput{abnormal}, configure: func(h *continuationTestHarness) { h.recoveryPrepareError = true },
					stages: []string{"primary"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "recovery quota unavailable", outputs: []ProcessOutput{abnormal}, configure: func(h *continuationTestHarness) { h.reservationError = true },
					stages: []string{"primary"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary", "reserve"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "reservation identity mismatch", outputs: []ProcessOutput{abnormal}, configure: func(h *continuationTestHarness) { h.badReservation = true },
					stages: []string{"primary"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary", "reserve"}, kind: CompletionFailed, code: ExecutionPrepareFailed},
				{name: "primary recovery process failure is terminal", outputs: []ProcessOutput{abnormal, abnormal},
					stages: []string{"primary", "primary-recovery"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary", "reserve"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "invalid primary recovery concludes from original invocation", outputs: []ProcessOutput{abnormal, normal, normal}, captures: []CapturedResult{invalid, partial},
					stages: []string{"primary", "primary-recovery", "conclude"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary", "reserve", "capture:primary-recovery", "authorize", "prepare-conclude:primary", "capture:conclude"}, kind: CompletionResultReady, code: ExecutionProcessFailed, outcome: OutcomeConcludeIntent},
				{name: "primary and conclude share persistent recovery quota", outputs: []ProcessOutput{abnormal, normal, abnormal}, captures: []CapturedResult{malformed},
					stages: []string{"primary", "primary-recovery", "conclude"}, trace: []string{"authorize", "observe:primary", "prepare-recovery:primary", "reserve", "capture:primary-recovery", "authorize", "prepare-conclude:primary", "authorize", "observe:conclude", "prepare-recovery:conclude", "reserve"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "invalid primary goes to conclude", outputs: []ProcessOutput{normal, normal}, captures: []CapturedResult{invalid, partial},
					stages: []string{"primary", "conclude"}, trace: []string{"capture:primary", "authorize", "prepare-conclude:primary", "capture:conclude"}, kind: CompletionResultReady, outcome: OutcomeConcludeIntent},
				{name: "malformed conclude finalizes once from original invocation", outputs: []ProcessOutput{timedOut, normal, normal}, captures: []CapturedResult{malformed, partial},
					stages: []string{"primary", "conclude", "finalization"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "capture:conclude", "authorize", "prepare-finalization:primary", "capture:finalization"}, kind: CompletionResultReady, outcome: OutcomeConcludeIntent},
				{name: "invalid finalization stops", outputs: []ProcessOutput{timedOut, normal, normal}, captures: []CapturedResult{invalid, invalid},
					stages: []string{"primary", "conclude", "finalization"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "capture:conclude", "authorize", "prepare-finalization:primary", "capture:finalization"}, kind: CompletionFailed, code: ExecutionInvalidResult},
				{name: "malformed finalization preserves failure diagnostics", outputs: []ProcessOutput{timedOut, normal, normal}, captures: []CapturedResult{invalid, malformed},
					stages: []string{"primary", "conclude", "finalization"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "capture:conclude", "authorize", "prepare-finalization:primary", "capture:finalization"}, kind: CompletionFailed, code: ExecutionCaptureFailed, failure: CaptureFailureInvalidCandidate, diagnostic: CaptureDiagnosticResultFileInvalid},
				{name: "conclude recovery uses latest invocation", outputs: []ProcessOutput{timedOut, abnormal, normal}, captures: []CapturedResult{partial},
					stages: []string{"primary", "conclude", "conclude-recovery"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "authorize", "observe:conclude", "prepare-recovery:conclude", "reserve", "capture:conclude-recovery"}, kind: CompletionResultReady, code: ExecutionProcessFailed, outcome: OutcomeConcludeIntent},
				{name: "conclude recovery process failure is terminal", outputs: []ProcessOutput{timedOut, abnormal, abnormal},
					stages: []string{"primary", "conclude", "conclude-recovery"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "authorize", "observe:conclude", "prepare-recovery:conclude", "reserve"}, kind: CompletionFailed, code: ExecutionProcessFailed},
				{name: "invalid conclude recovery does not finalize", outputs: []ProcessOutput{timedOut, abnormal, normal}, captures: []CapturedResult{invalid},
					stages: []string{"primary", "conclude", "conclude-recovery"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "authorize", "observe:conclude", "prepare-recovery:conclude", "reserve", "capture:conclude-recovery"}, kind: CompletionFailed, code: ExecutionInvalidResult},
				{name: "complete project forbidden in conclude and finalization", outputs: []ProcessOutput{timedOut, normal, normal}, captures: []CapturedResult{{Candidate: continuationComplete}, {Candidate: continuationComplete}},
					stages: []string{"primary", "conclude", "finalization"}, trace: []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary", "capture:conclude", "authorize", "prepare-finalization:primary", "capture:finalization"}, kind: CompletionFailed, code: ExecutionInvalidResult},
			} {
				t.Run(test.name, func(t *testing.T) {
					h := newContinuationTestHarness(t, task)
					h.captures = test.captures
					if test.configure != nil {
						test.configure(h)
					}
					for _, output := range test.outputs {
						h.runtime.QueueOutput(output)
					}
					completion := h.execute()
					if completion.Kind != test.kind || completion.Code != test.code || completion.OutcomeKind != test.outcome ||
						completion.CaptureFailureReason != test.failure || completion.CaptureDiagnostic != test.diagnostic {
						t.Fatalf("completion=%+v", completion)
					}
					if !reflect.DeepEqual(h.trace, test.trace) {
						t.Fatalf("trace=%v, want %v", h.trace, test.trace)
					}
					h.assertLaunches(test.stages)
					request, err := completion.FinishRequest()
					if err != nil || request.RunID != h.started.Run.ID || request.FencingToken != h.started.Run.FencingToken {
						t.Fatalf("finish request=%+v err=%v", request, err)
					}
					if test.outcome == "" {
						if len(completion.Candidate) != 0 || len(request.Candidate) != 0 || request.ResultEvidenceSource != "" || request.Termination == nil || request.Termination.Code != test.code {
							t.Fatalf("failed result leaked evidence: completion=%+v request=%+v", completion, request)
						}
					} else if len(completion.Candidate) == 0 || !bytes.Equal(request.Candidate, completion.Candidate) || request.Termination != nil {
						t.Fatalf("accepted result missing from finish request: %+v", request)
					}
				})
			}
		})
	}
}

func TestContinuationTechnicalFailuresDoNotRecover(t *testing.T) {
	for _, task := range []TaskType{TaskExplore, TaskBootstrap} {
		for _, stage := range []string{"primary", "conclude"} {
			for _, test := range []struct {
				name   string
				output ProcessOutput
				err    error
				kind   CompletionKind
				code   ExecutionErrorCode
			}{
				{"cancelled", ProcessOutput{Exited: true, ExitCode: 9, Cancelled: true}, nil, CompletionInterrupted, ""},
				{"overflow", ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorOutputTooLarge}, nil, CompletionFailed, ExecutionOutputTooLarge},
				{"stream", ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorStreamRead}, nil, CompletionFailed, ExecutionProcessStream},
				{"container", ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorContainerState}, nil, CompletionFailed, ExecutionProcessFailed},
				{"wait error", ProcessOutput{Exited: true, ExitCode: 9}, errors.New("wait failed"), CompletionFailed, ExecutionProcessFailed},
				{"unconfirmed stop", ProcessOutput{ExitCode: 9}, nil, CompletionFailed, ExecutionProcessFailed},
				{"timeout with stream error", ProcessOutput{Exited: true, TimedOut: true, ErrorCode: ProcessErrorStreamRead}, nil, CompletionFailed, ExecutionProcessStream},
			} {
				t.Run(string(task)+"/"+stage+"/"+test.name, func(t *testing.T) {
					h := newContinuationTestHarness(t, task)
					stages := []string{"primary"}
					var trace []string
					if stage == "conclude" {
						h.runtime.QueueOutput(ProcessOutput{Exited: true, TimedOut: true})
						stages = append(stages, "conclude")
						trace = []string{"authorize", "observe:primary", "authorize", "prepare-conclude:primary"}
					}
					h.runtime.QueueResult(test.output, test.err)
					completion := h.execute()
					if completion.Kind != test.kind || completion.Code != test.code || !reflect.DeepEqual(h.trace, trace) {
						t.Fatalf("completion=%+v trace=%v", completion, h.trace)
					}
					h.assertLaunches(stages)
				})
			}
		}
	}
}

func TestContinuationUnrepairableConcludeCapture(t *testing.T) {
	for _, task := range []TaskType{TaskExplore, TaskBootstrap} {
		for _, reason := range []CaptureFailureReason{CaptureFailureMultipleCandidates, CaptureFailureCandidateTooLarge, CaptureFailureBoundaryError} {
			t.Run(string(task)+"/"+string(reason), func(t *testing.T) {
				h := newContinuationTestHarness(t, task)
				h.runtime.QueueOutput(ProcessOutput{Exited: true, TimedOut: true})
				h.runtime.QueueOutput(ProcessOutput{Exited: true})
				h.captures = []CapturedResult{{CaptureFailureReason: reason}}
				completion := h.execute()
				if completion.Kind != CompletionFailed || completion.Code != ExecutionCaptureFailed || completion.CaptureFailureReason != reason || h.reservations != 0 {
					t.Fatalf("completion=%+v reservations=%d", completion, h.reservations)
				}
				h.assertLaunches([]string{"primary", "conclude"})
			})
		}
	}
}

func TestContinuationPrimaryOutcomeAndEvidence(t *testing.T) {
	for _, task := range []TaskType{TaskExplore, TaskBootstrap} {
		t.Run(string(task), func(t *testing.T) {
			h := newContinuationTestHarness(t, task)
			h.captures = []CapturedResult{{Candidate: continuationPartial, ResultEvidenceSource: ResultEvidenceSourceFile}}
			stages := []string{"primary"}
			if task == TaskBootstrap {
				h.captures = append(h.captures, CapturedResult{Candidate: continuationDecline, ResultEvidenceSource: ResultEvidenceSourceFile})
				stages = append(stages, "conclude")
			}
			completion := h.execute()
			request, err := completion.FinishRequest()
			if err != nil || request.Termination != nil || !bytes.Equal(request.Candidate, completion.Candidate) {
				t.Fatalf("completion=%+v request=%+v err=%v", completion, request, err)
			}
			if task == TaskExplore {
				if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent || request.CaptureDiagnostic != "" || request.ResultEvidenceSource != ResultEvidenceSourceFile {
					t.Fatalf("Explore Primary evidence=%+v", completion)
				}
			} else if completion.Kind != CompletionDeclined || completion.OutcomeKind != OutcomeDecline || request.CaptureDiagnostic != "" || request.ResultEvidenceSource != ResultEvidenceSourceFile {
				t.Fatalf("Bootstrap Conclude evidence=%+v", completion)
			}
			h.assertLaunches(stages)
		})
	}
	t.Run("bootstrap technical conclude failure retains only prior diagnostics in finish", func(t *testing.T) {
		h := newContinuationTestHarness(t, TaskBootstrap)
		h.runtime.QueueOutput(ProcessOutput{Exited: true})
		h.runtime.QueueOutput(ProcessOutput{Exited: true, ErrorCode: ProcessErrorStreamRead})
		h.captures = []CapturedResult{{Candidate: continuationPartial, ResultEvidenceSource: ResultEvidenceSourceFile}}
		completion := h.execute()
		request, err := completion.FinishRequest()
		if err != nil || completion.Kind != CompletionFailed || completion.Code != ExecutionProcessStream || completion.CaptureDiagnostic != "" || completion.ResultEvidenceSource != ResultEvidenceSourceFile ||
			len(completion.Candidate) != 0 || completion.OutcomeKind != "" || len(request.Candidate) != 0 || request.ResultEvidenceSource != "" || request.CaptureDiagnostic != "" || request.Termination == nil || request.Termination.Code != ExecutionProcessStream {
			t.Fatalf("completion=%+v request=%+v err=%v", completion, request, err)
		}
		h.assertLaunches([]string{"primary", "conclude"})
	})
	t.Run("bootstrap complete project returns from primary", func(t *testing.T) {
		h := newContinuationTestHarness(t, TaskBootstrap)
		h.captures = []CapturedResult{{Candidate: continuationComplete, ResultEvidenceSource: ResultEvidenceSourceFile}}
		completion := h.execute()
		request, err := completion.FinishRequest()
		if err != nil || completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeCompleteProject || request.Termination != nil || request.ResultEvidenceSource != ResultEvidenceSourceFile {
			t.Fatalf("completion=%+v request=%+v err=%v", completion, request, err)
		}
		h.assertLaunches([]string{"primary"})
	})
}

// The harness observes the existing Binding, Runtime and authorization seams;
// it deliberately gives each prepared Invocation a distinct identity.
type continuationTestHarness struct {
	t                    *testing.T
	started              StartedRun
	input                executionYAMLInput
	runtime              *runtimeTestDouble
	captures             []CapturedResult
	trace                []string
	blockContinuation    bool
	authorizations       int
	authorizationErrorAt int
	reservations         int
	reservationError     bool
	badReservation       bool
	recoveryPrepareError bool
}

func newContinuationTestHarness(t *testing.T, task TaskType) *continuationTestHarness {
	t.Helper()
	h := &continuationTestHarness{t: t, runtime: newRuntimeTestDouble()}
	if task == TaskExplore {
		h.started, h.input, _, _ = exploreExecutorFixture(t)
	} else {
		h.started, h.input, _, _ = bootstrapExecutorFixture(t)
	}
	h.started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	h.started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	return h
}

func (h *continuationTestHarness) execute() Completion {
	h.t.Helper()
	binding := newTestExecutionBinding(h.t, h.runtime, nil, nil, h, h)
	var executor Executor
	var err error
	if h.started.Run.Task == TaskExplore {
		h.started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery("primary", "conclude", "primary-recovery", "conclude-recovery")
		executor, err = binding.BindExplore(h.started, ExploreExecution{
			Input: h.input.(BlackboardInput), Adapter: h, PromptTemplateHash: h.started.Run.Spec.PromptTemplateHash,
			Prompt: "primary", FallbackPrompt: "conclude", RecoveryPrompt: "primary-recovery", RecoveryConcludePrompt: "conclude-recovery", FinalizationPrompt: "finalization",
		})
	} else {
		h.started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes("primary", "conclude", "primary-recovery", "conclude-recovery")
		executor, err = binding.BindBootstrap(h.started, BootstrapExecution{
			Input: h.input.(BootstrapInput), Adapter: h, PromptTemplateHash: h.started.Run.Spec.PromptTemplateHash,
			Prompt: "primary", FallbackPrompt: "conclude", RecoveryPrompt: "primary-recovery", RecoveryConcludePrompt: "conclude-recovery", FinalizationPrompt: "finalization",
		})
	}
	if err != nil {
		h.t.Fatal(err)
	}
	completion := executor.Execute(h.t.Context(), h.started)
	if len(h.captures) != 0 {
		h.t.Fatalf("unused captures: %d", len(h.captures))
	}
	return completion
}

func (h *continuationTestHarness) assertLaunches(stages []string) {
	h.t.Helper()
	phases := map[string]ExecutionPhase{"primary": PhaseExplore, "conclude": PhaseExploreConclude, "finalization": PhaseExploreConclude, "primary-recovery": PhaseExploreRecovery, "conclude-recovery": PhaseExploreConcludeRecovery}
	if h.started.Run.Task == TaskBootstrap {
		phases = map[string]ExecutionPhase{"primary": PhaseBootstrap, "conclude": PhaseBootstrapConclude, "finalization": PhaseBootstrapConclude, "primary-recovery": PhaseBootstrapRecovery, "conclude-recovery": PhaseBootstrapConcludeRecovery}
	}
	launches := h.runtime.Snapshot().Launches
	if len(launches) != len(stages) {
		h.t.Fatalf("launches=%+v, want stages %v", launches, stages)
	}
	inputBytes, inputHash, err := h.input.MarshalYAML()
	if err != nil {
		h.t.Fatal(err)
	}
	for index, stage := range stages {
		launch := launches[index]
		duration := 5 * time.Second
		if stage == "primary-recovery" || stage == "conclude-recovery" {
			duration = 2 * time.Second
		}
		path := ""
		if h.started.Run.Task == TaskExplore || stage == "primary-recovery" || stage == "conclude-recovery" {
			path = BlackboardPath(h.started.Run.ID, phases[stage])
		}
		if launch.RunID != h.started.Run.ID || launch.FencingToken != h.started.Run.FencingToken || launch.Task != h.started.Run.Task || launch.Phase != phases[stage] || launch.PhaseTimeout != duration ||
			launch.RenderedInput == nil || launch.RenderedInput.Path != path || launch.RenderedInput.Hash != inputHash || !bytes.Equal(launch.RenderedInput.Bytes, inputBytes) {
			h.t.Fatalf("%s launch=%+v input=%+v", stage, launch, launch.RenderedInput)
		}
	}
}

func (h *continuationTestHarness) TypeName() string { return "test" }
func (h *continuationTestHarness) PrepareExplore(ExploreRequest) (AgentInvocation, error) {
	return &continuationTestInvocation{h: h, stage: "primary"}, nil
}
func (h *continuationTestHarness) PrepareBootstrap(BootstrapRequest) (AgentInvocation, error) {
	return &continuationTestInvocation{h: h, stage: "primary"}, nil
}
func (h *continuationTestHarness) AuthorizeRunContinuation(_ context.Context, request RunContinuationRequest) error {
	h.trace = append(h.trace, "authorize")
	h.authorizations++
	if request.RunID != h.started.Run.ID || request.FencingToken != h.started.Run.FencingToken {
		h.t.Fatalf("continuation identity=%+v", request)
	}
	if h.authorizations == h.authorizationErrorAt {
		return &DomainError{Code: ErrorFenced, Detail: "lease changed"}
	}
	return nil
}
func (h *continuationTestHarness) BeginProcessRecovery(_ context.Context, request BeginProcessRecoveryRequest) (ProcessRecoveryReservation, error) {
	h.trace = append(h.trace, "reserve")
	h.reservations++
	if request.RunID != h.started.Run.ID || request.FencingToken != h.started.Run.FencingToken {
		h.t.Fatalf("recovery identity=%+v", request)
	}
	if h.reservationError || h.reservations > 1 {
		return ProcessRecoveryReservation{}, errors.New("recovery quota exhausted")
	}
	phase, mode := PhaseExploreRecovery, ProcessRecoveryExplorePrimary
	if h.started.Run.Task == TaskBootstrap {
		phase, mode = PhaseBootstrapRecovery, ProcessRecoveryBootstrapPrimary
	}
	if request.FailedPhase == PhaseExploreConclude {
		phase, mode = PhaseExploreConcludeRecovery, ProcessRecoveryExploreConclude
	} else if request.FailedPhase == PhaseBootstrapConclude {
		phase, mode = PhaseBootstrapConcludeRecovery, ProcessRecoveryBootstrapConclude
	}
	if request.Mode != mode {
		h.t.Fatalf("recovery mode=%s, want %s", request.Mode, mode)
	}
	reservation := ProcessRecoveryReservation{RunID: request.RunID, FencingToken: request.FencingToken, Mode: mode, Phase: phase, ReservedAt: time.Now(), PhaseTimeout: 2 * time.Second}
	if h.badReservation {
		reservation.RunID = "another-run"
	}
	return reservation, nil
}

type continuationTestInvocation struct {
	h     *continuationTestHarness
	stage string
}

func (invocation *continuationTestInvocation) Command() []string {
	return []string{"agent", invocation.stage}
}
func (invocation *continuationTestInvocation) Environment() map[string]string { return nil }
func (invocation *continuationTestInvocation) CanContinueSession() bool {
	return !invocation.h.blockContinuation
}
func (invocation *continuationTestInvocation) ObserveContinuationSession(context.Context, ProcessOutput) bool {
	invocation.h.trace = append(invocation.h.trace, "observe:"+invocation.stage)
	return !invocation.h.blockContinuation
}
func (invocation *continuationTestInvocation) Capture(_ context.Context, output ProcessOutput) (CapturedResult, error) {
	h := invocation.h
	h.trace = append(h.trace, "capture:"+invocation.stage)
	if !output.Exited || output.ExitCode != 0 || output.TimedOut || output.Cancelled || output.ErrorCode != "" || len(h.captures) == 0 {
		h.t.Fatalf("unexpected Capture from %s: %+v", invocation.stage, output)
	}
	captured := h.captures[0]
	h.captures = h.captures[1:]
	if captured.CaptureFailureReason != "" {
		return captured, errors.New("capture failed")
	}
	return captured, nil
}
func (invocation *continuationTestInvocation) PrepareExploreConclude(request ExploreConcludeRequest) (AgentInvocation, error) {
	if request.InputPath != BlackboardPath(invocation.h.started.Run.ID, PhaseExploreConclude) {
		invocation.h.t.Fatalf("Explore conclude request=%+v", request)
	}
	return invocation.prepareConclude(request.Prompt), nil
}
func (invocation *continuationTestInvocation) PrepareBootstrapConclude(request BootstrapConcludeRequest) (AgentInvocation, error) {
	inputBytes, _, _ := invocation.h.input.MarshalYAML()
	if request.InlineInput != string(inputBytes) {
		invocation.h.t.Fatalf("Bootstrap conclude input differs from admitted input")
	}
	return invocation.prepareConclude(request.Prompt), nil
}
func (invocation *continuationTestInvocation) prepareConclude(prompt string) AgentInvocation {
	invocation.h.trace = append(invocation.h.trace, "prepare-"+prompt+":"+invocation.stage)
	return &continuationTestInvocation{h: invocation.h, stage: prompt}
}
func (invocation *continuationTestInvocation) PrepareProcessRecovery(request ProcessRecoveryRequest) (AgentInvocation, error) {
	h := invocation.h
	h.trace = append(h.trace, "prepare-recovery:"+invocation.stage)
	inputBytes, _, _ := h.input.MarshalYAML()
	if request.Task != h.started.Run.Task || request.InputPath != BlackboardPath(h.started.Run.ID, request.Phase) ||
		h.started.Run.Task == TaskBootstrap && request.InlineInput != string(inputBytes) || h.started.Run.Task == TaskExplore && request.InlineInput != "" {
		h.t.Fatalf("recovery request=%+v", request)
	}
	if h.recoveryPrepareError {
		return nil, errors.New("cannot prepare recovery")
	}
	return &continuationTestInvocation{h: h, stage: request.Prompt}, nil
}
