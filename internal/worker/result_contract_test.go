package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/pelletier/go-toml/v2"
)

// Expectations describe the admitted behavior independently of the production
// rule matrix, so changing that matrix alone cannot make these tests pass.
var outcomePhaseCases = []struct {
	task      rundomain.TaskType
	phase     Phase
	operation string
	allowed   []rundomain.OutcomeKind
}{
	{rundomain.TaskReason, PhaseReason, "primary", []rundomain.OutcomeKind{rundomain.OutcomeCompleteProject, rundomain.OutcomeProposeIntents, rundomain.OutcomeNoChange, rundomain.OutcomeDecline}},
	{rundomain.TaskReason, PhaseReasonFinalization, "conclude", []rundomain.OutcomeKind{rundomain.OutcomeCompleteProject, rundomain.OutcomeProposeIntents, rundomain.OutcomeNoChange, rundomain.OutcomeDecline}},
	{rundomain.TaskExplore, PhaseExplore, "primary", []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskExplore, PhaseExploreConclude, "conclude", []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskExplore, PhaseExploreRecovery, "recovery", []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskExplore, PhaseExploreConcludeRecovery, "recovery", []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskBootstrap, PhaseBootstrap, "primary", []rundomain.OutcomeKind{rundomain.OutcomeCompleteProject, rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskBootstrap, PhaseBootstrapConclude, "conclude", []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskBootstrap, PhaseBootstrapRecovery, "recovery", []rundomain.OutcomeKind{rundomain.OutcomeCompleteProject, rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
	{rundomain.TaskBootstrap, PhaseBootstrapConcludeRecovery, "recovery", []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}},
}

func TestAdaptersGenerateAndCaptureTheSamePhaseOutcomes(t *testing.T) {
	for _, agentType := range []WorkerType{WorkerTypeMock, WorkerTypeClaudeCode, WorkerTypeCodex, WorkerTypePi} {
		for _, phaseCase := range outcomePhaseCases {
			t.Run(string(agentType)+"/"+string(phaseCase.phase), func(t *testing.T) {
				adapter, err := NewAgentAdapter(WorkerConfig{Name: "matrix", Type: agentType})
				if err != nil {
					t.Fatal(err)
				}
				request := outcomeMatrixRequest(phaseCase.task, phaseCase.phase)
				invocation, err := prepareOutcomeMatrixInvocation(t, adapter, request, phaseCase.operation, agentType)
				if err != nil {
					t.Fatal(err)
				}
				if contract := invocation.ResultContract(); contract.Task != request.Task || contract.Phase != request.Phase || contract.MaxProposedIntents != request.MaxProposedIntents {
					t.Fatalf("Invocation did not fix the request contract: %+v", contract)
				}
				generated := generatedOutcomeKinds(t, agentType, invocation)
				if len(generated) != len(phaseCase.allowed) {
					t.Fatalf("generated kinds=%v, want=%v", generated, phaseCase.allowed)
				}
				for _, kind := range []rundomain.OutcomeKind{rundomain.OutcomeCompleteProject, rundomain.OutcomeProposeIntents, rundomain.OutcomeNoChange, rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline, "unknown"} {
					t.Run(string(kind), func(t *testing.T) {
						allowed := slices.Contains(phaseCase.allowed, kind)
						if slices.Contains(generated, kind) != allowed {
							t.Fatalf("generated eligibility for %s disagrees with phase", kind)
						}
						outcome := outcomeMatrixPayload(phaseCase.task, kind)
						captured, err := captureOutcomeMatrix(t, adapter, invocation, agentType, outcome)
						if !allowed {
							var captureErr *CaptureError
							if !errors.As(err, &captureErr) || captureErr.Code != CaptureInvalidCandidate || len(captured.Candidate) != 0 || captured.ResultHash != "" || captured.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate {
								t.Fatalf("forbidden %s capture=%+v err=%v", kind, captured, err)
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						encoded, _ := json.Marshal(outcome)
						_, canonical, hash, err := rundomain.DecodeRunResult([]byte(`{"schema":"aether.run-result/v1","outcome":`+string(encoded)+`}`), phaseCase.task, request.MaxProposedIntents)
						if err != nil || string(captured.Candidate) != string(canonical) || captured.ResultHash != hash {
							t.Fatalf("canonical result mismatch: capture=%+v err=%v", captured, err)
						}
					})
				}
			})
		}
	}
}

func TestReasonProposalLimitsApplyToEveryAdapterAndPhase(t *testing.T) {
	for _, agentType := range []WorkerType{WorkerTypeMock, WorkerTypeClaudeCode, WorkerTypeCodex, WorkerTypePi} {
		for _, phaseCase := range outcomePhaseCases[:2] {
			for _, maxIntents := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/max=%d", agentType, phaseCase.phase, maxIntents), func(t *testing.T) {
					adapter, err := NewAgentAdapter(WorkerConfig{Name: "limits", Type: agentType})
					if err != nil {
						t.Fatal(err)
					}
					request := outcomeMatrixRequest(phaseCase.task, phaseCase.phase)
					request.MaxProposedIntents = maxIntents
					invocation, err := prepareOutcomeMatrixInvocation(t, adapter, request, phaseCase.operation, agentType)
					if err != nil {
						t.Fatal(err)
					}
					if got := slices.Contains(generatedOutcomeKinds(t, agentType, invocation), rundomain.OutcomeProposeIntents); got != (maxIntents > 0) {
						t.Fatalf("generated proposal eligibility=%t for max=%d", got, maxIntents)
					}
					for _, count := range []int{1, 2} {
						outcome := outcomeMatrixPayload(rundomain.TaskReason, rundomain.OutcomeProposeIntents)
						intents := []any{map[string]any{"from": []string{"origin"}, "description": "first direction"}}
						if count == 2 {
							intents = append(intents, map[string]any{"from": []string{"origin"}, "description": "second direction"})
						}
						outcome["intents"] = intents
						captured, err := captureOutcomeMatrix(t, adapter, invocation, agentType, outcome)
						if count <= maxIntents {
							if err != nil || len(captured.Candidate) == 0 {
								t.Fatalf("admitted proposal rejected: %v", err)
							}
						} else {
							var captureErr *CaptureError
							if !errors.As(err, &captureErr) || captureErr.Code != CaptureInvalidCandidate || len(captured.Candidate) != 0 {
								t.Fatalf("over-limit proposal capture=%+v err=%v", captured, err)
							}
						}
					}
				})
			}
		}
	}
}

func TestPreparationRejectsMismatchedTaskPhaseAndOperation(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "eligibility", Type: WorkerTypeMock})
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseCase := range outcomePhaseCases {
		for _, task := range []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap, "unknown"} {
			for _, operation := range []string{"primary", "conclude", "recovery"} {
				request := outcomeMatrixRequest(task, phaseCase.phase)
				_, err := prepareOutcomeMatrixInvocation(t, adapter, request, operation, WorkerTypeMock)
				valid := task == phaseCase.task && operation == phaseCase.operation
				if (err == nil) != valid {
					t.Fatalf("task=%s phase=%s operation=%s err=%v", task, phaseCase.phase, operation, err)
				}
			}
		}
	}
	for _, request := range []AgentRequest{
		outcomeMatrixRequest(rundomain.TaskReason, "unknown"),
		outcomeMatrixRequest(rundomain.TaskReason, ""),
	} {
		if _, err := adapter.Prepare(request); err == nil {
			t.Fatal("invalid phase accepted")
		}
	}
	for _, task := range []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap} {
		phase := PhaseReason
		if task == rundomain.TaskExplore {
			phase = PhaseExplore
		}
		if task == rundomain.TaskBootstrap {
			phase = PhaseBootstrap
		}
		for _, limit := range []int{-1, 1} {
			if task == rundomain.TaskReason && limit == 1 {
				continue
			}
			request := outcomeMatrixRequest(task, phase)
			request.MaxProposedIntents = limit
			if _, err := adapter.Prepare(request); err == nil {
				t.Fatalf("task=%s invalid limit=%d accepted", task, limit)
			}
		}
	}
}

func outcomeMatrixRequest(task rundomain.TaskType, phase Phase) AgentRequest {
	request := AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: task, Phase: phase, Prompt: "verify this task",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	}
	if task == rundomain.TaskReason {
		request.MaxProposedIntents = 1
	}
	if task == rundomain.TaskBootstrap {
		request.Input = InputReference{Kind: InputBootstrapInline, Inline: "origin: start\ngoal: finish"}
	}
	return request
}

func prepareOutcomeMatrixInvocation(t *testing.T, adapter AgentAdapter, request AgentRequest, operation string, agentType WorkerType) (Invocation, error) {
	t.Helper()
	session := &Session{value: "0192f7e5-7b2a-7c01-8a11-223344556677"}
	if agentType == WorkerTypePi {
		session = verifiedPiTestSession(t, "00000002")
	}
	switch operation {
	case "conclude":
		return adapter.(ConcludeAdapter).PrepareConclude(ConcludeRequest{AgentRequest: request, Session: session})
	case "recovery":
		return adapter.(ProcessRecoveryAdapter).PrepareRecovery(ProcessRecoveryRequest{AgentRequest: request, Session: session})
	default:
		return adapter.Prepare(request)
	}
}

func generatedOutcomeKinds(t *testing.T, agentType WorkerType, invocation Invocation) []rundomain.OutcomeKind {
	t.Helper()
	var schema []byte
	switch agentType {
	case WorkerTypePi:
		for _, line := range strings.Split(string(invocation.ProtocolFile().Bytes), "\n") {
			if kinds, ok := strings.CutPrefix(line, "# Allowed kinds: "); ok {
				var result []rundomain.OutcomeKind
				for _, kind := range strings.Split(strings.TrimSuffix(kinds, "."), ", ") {
					result = append(result, rundomain.OutcomeKind(kind))
				}
				return result
			}
		}
		t.Fatal("Pi template omitted allowed kinds")
	case WorkerTypeCodex:
		schema = invocation.ProtocolFile().Bytes
	case WorkerTypeClaudeCode:
		argv := invocation.Argv()
		for index, arg := range argv {
			if arg == "--json-schema" && index+1 < len(argv) {
				schema = []byte(argv[index+1])
			}
		}
	case WorkerTypeMock:
		_, raw, ok := strings.Cut(invocation.prompt, "JSON Schema for this invocation:\n")
		if !ok {
			t.Fatal("Mock prompt omitted schema")
		}
		schema = []byte(raw)
	}
	var decoded struct {
		Properties struct {
			Outcome struct {
				AnyOf []struct {
					Properties struct {
						Kind struct {
							Enum []rundomain.OutcomeKind `json:"enum"`
						} `json:"kind"`
					} `json:"properties"`
				} `json:"anyOf"`
			} `json:"outcome"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("generated schema=%s err=%v", schema, err)
	}
	var result []rundomain.OutcomeKind
	for _, branch := range decoded.Properties.Outcome.AnyOf {
		result = append(result, branch.Properties.Kind.Enum...)
	}
	return result
}

func outcomeMatrixPayload(task rundomain.TaskType, kind rundomain.OutcomeKind) map[string]any {
	outcome := map[string]any{"kind": kind}
	switch kind {
	case rundomain.OutcomeCompleteProject:
		outcome["description"] = "verified completion"
		if task == rundomain.TaskReason {
			outcome["from"] = []string{"origin"}
		} else {
			outcome["fact"] = map[string]any{"description": "verified evidence"}
		}
	case rundomain.OutcomeConcludeIntent:
		outcome["fact"] = map[string]any{"description": "verified evidence"}
	case rundomain.OutcomeDecline:
		outcome["reason"] = "outside scope"
	case rundomain.OutcomeProposeIntents:
		outcome["intents"] = []any{map[string]any{"from": []string{"origin"}, "description": "verify a new direction"}}
	}
	return outcome
}

func captureOutcomeMatrix(t *testing.T, adapter AgentAdapter, invocation Invocation, agentType WorkerType, outcome map[string]any) (CaptureResult, error) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"outcome": outcome})
	if err != nil {
		t.Fatal(err)
	}
	output := ProcessOutput{Exited: true, ExitCode: 0, Stdout: string(encoded)}
	switch agentType {
	case WorkerTypeClaudeCode:
		output.Stdout = claudeSuccessEnvelope(string(encoded))
	case WorkerTypePi:
		header := strictPiHeader(invocation)
		sessionFile := strictPiSessionFile(header, strictPiPrompt(invocation), "finished", "00000001", "00000002")
		if invocation.Session() != nil {
			sessionFile = strictPiContinuedSessionFile(header, "previous prompt", "previous finish", strictPiPrompt(invocation), "finished")
		}
		// Serialize exactly the same outcome values through the existing TOML
		// library rather than hand-maintaining a second set of payload fixtures.
		tomlBytes, err := toml.Marshal(map[string]any{"outcome": outcome})
		if err != nil {
			t.Fatal(err)
		}
		output.Stdout = strictPiLifecycle(header, "finished")
		output.StoppedProcessFileReader = &piBatchFileReader{files: map[string][]byte{
			piSessionFilePath(invocation.sessionDir, mustPiHeader(t, header)): []byte(sessionFile),
			invocation.ProtocolFile().Path():                                  tomlBytes,
		}}
	}
	return adapter.Capture(t.Context(), invocation, output)
}
