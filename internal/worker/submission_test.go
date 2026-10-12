package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestClaudeSubmissionOptionsPrecedePrint(t *testing.T) {
	adapter, err := NewAgentAdapter(makeClaudeCodeWorker())
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{PhaseReason, PhaseExploreConclude} {
		t.Run(string(phase), func(t *testing.T) {
			request := AgentRequest{
				Task: rundomain.TaskReason, Phase: phase, Prompt: "submit the outcome",
				Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
			}
			var invocation Invocation
			var err error
			if phase == PhaseReason {
				invocation, err = adapter.Prepare(request)
			} else {
				request.Task = rundomain.TaskExplore
				invocation, err = adapter.(ConcludeAdapter).PrepareConclude(ConcludeRequest{
					AgentRequest: request, Session: &Session{value: "session-id"},
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			argv := invocation.Argv()
			printIndex := slices.Index(argv, "-p")
			schemaIndex := slices.Index(argv, "--json-schema")
			formatIndex := slices.Index(argv, "--output-format")
			if schemaIndex < 0 || schemaIndex+1 >= printIndex || formatIndex < 0 ||
				formatIndex+1 >= printIndex || argv[formatIndex+1] != "json" {
				t.Fatalf("native submission options must precede -p: %v", argv)
			}
			if !json.Valid([]byte(argv[schemaIndex+1])) || !strings.Contains(argv[schemaIndex+1], `"outcome"`) || invocation.ProtocolFile() != nil {
				t.Fatalf("Claude must use an inline outcome schema: %v", argv)
			}
		})
	}
}

func TestClaudePrimaryRecoveryResumesSameSessionWithFullTools(t *testing.T) {
	adapter, err := NewAgentAdapter(makeClaudeCodeWorker())
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskBootstrap} {
		t.Run(string(task), func(t *testing.T) {
			request := AgentRequest{Task: task, Phase: PhaseExplore, Prompt: "verify the assigned evidence", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}}
			if task == rundomain.TaskBootstrap {
				request.Phase = PhaseBootstrap
				request.Input = InputReference{Kind: InputBootstrapInline, Inline: "origin: start\ngoal: finish"}
			}
			primary, err := adapter.Prepare(request)
			if err != nil {
				t.Fatal(err)
			}
			session := adapter.ObserveSession(t.Context(), primary, ProcessOutput{Exited: true, ExitCode: 1})
			if !adapter.CanContinueSession(session) {
				t.Fatal("failed primary lost its continuation Session")
			}
			primaryArgs := primary.Argv()
			sessionID := primaryArgs[slices.Index(primaryArgs, "--session-id")+1]
			request.Phase = PhaseExploreRecovery
			if task == rundomain.TaskBootstrap {
				request.Phase = PhaseBootstrapRecovery
			}
			invocation, err := adapter.(ProcessRecoveryAdapter).PrepareRecovery(ProcessRecoveryRequest{AgentRequest: request, Session: session})
			if err != nil {
				t.Fatal(err)
			}
			argv := invocation.Argv()
			resumeIndex := slices.Index(argv, "-r")
			if resumeIndex < 0 || resumeIndex+1 >= len(argv) || argv[resumeIndex+1] != sessionID || slices.Contains(argv, "--session-id") {
				t.Fatalf("Primary Recovery must resume the existing Session: %v", argv)
			}
			if slices.Contains(argv, "--tools=") || !slices.Contains(argv, "-p") || !slices.Contains(argv, "--json-schema") {
				t.Fatalf("Primary Recovery lost tools or native Schema submission: %v", argv)
			}
			formatIndex := slices.Index(argv, "--output-format")
			if formatIndex < 0 || formatIndex+1 >= len(argv) || argv[formatIndex+1] != "json" {
				t.Fatalf("Primary Recovery omitted JSON result envelope: %v", argv)
			}
		})
	}
}

func TestFileSubmissionMaterialsAcrossPhases(t *testing.T) {
	phases := []struct {
		task  rundomain.TaskType
		phase Phase
	}{
		{rundomain.TaskReason, PhaseReason},
		{rundomain.TaskReason, PhaseReasonFinalization},
		{rundomain.TaskExplore, PhaseExplore},
		{rundomain.TaskExplore, PhaseExploreConclude},
		{rundomain.TaskExplore, PhaseExploreRecovery},
		{rundomain.TaskExplore, PhaseExploreConcludeRecovery},
		{rundomain.TaskBootstrap, PhaseBootstrap},
		{rundomain.TaskBootstrap, PhaseBootstrapConclude},
		{rundomain.TaskBootstrap, PhaseBootstrapRecovery},
		{rundomain.TaskBootstrap, PhaseBootstrapConcludeRecovery},
	}
	for _, workerType := range []WorkerType{WorkerTypeCodex, WorkerTypePi} {
		t.Run(string(workerType), func(t *testing.T) {
			adapter, err := NewAgentAdapter(WorkerConfig{Name: string(workerType), Type: workerType})
			if err != nil {
				t.Fatal(err)
			}
			session := &Session{value: "session-id"}
			if workerType == WorkerTypePi {
				session = verifiedPiTestSession(t, "00000002")
			}
			paths := make(map[string]bool)
			for index, test := range phases {
				t.Run(string(test.phase), func(t *testing.T) {
					request := AgentRequest{
						InvocationID: fmt.Sprintf("12345678-abcd-4abc-8def-%012d", index+1),
						Task:         test.task, Phase: test.phase, Prompt: "submit the outcome",
						Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
					}
					if test.task == rundomain.TaskReason {
						request.MaxProposedIntents = 1
					}
					if test.task == rundomain.TaskBootstrap {
						request.Input = InputReference{Kind: InputBootstrapInline, Inline: "origin: origin\ngoal: goal"}
					}
					var invocation Invocation
					var err error
					switch test.phase {
					case PhaseReasonFinalization, PhaseExploreConclude, PhaseBootstrapConclude:
						invocation, err = adapter.(ConcludeAdapter).PrepareConclude(ConcludeRequest{AgentRequest: request, Session: session})
					case PhaseExploreRecovery, PhaseExploreConcludeRecovery, PhaseBootstrapRecovery, PhaseBootstrapConcludeRecovery:
						invocation, err = adapter.(ProcessRecoveryAdapter).PrepareRecovery(ProcessRecoveryRequest{AgentRequest: request, Session: session})
					default:
						invocation, err = adapter.Prepare(request)
					}
					if err != nil {
						t.Fatal(err)
					}
					file := invocation.ProtocolFile()
					if file == nil || file.Validate() != nil || len(file.Bytes) == 0 {
						t.Fatalf("missing or invalid submission file: %+v", file)
					}
					if paths[file.Path()] || !strings.Contains(invocation.prompt, file.Path()) {
						t.Fatalf("submission prompt must reference this invocation's independent file: %s", file.Path())
					}
					paths[file.Path()] = true
					argv := invocation.Argv()
					if !slices.Contains(argv, invocation.prompt) {
						t.Fatalf("command lost the prepared submission prompt: %v", argv)
					}
					if workerType == WorkerTypeCodex {
						fileIndex := slices.Index(argv, "--output-schema")
						if fileIndex < 0 || fileIndex+1 >= slices.Index(argv, "--") || argv[fileIndex+1] != file.Path() || file.Writable || !json.Valid(file.Bytes) {
							t.Fatalf("Codex command must use the prepared schema file: %v file=%+v", argv, file)
						}
					} else if file.Filename != "result.toml" || !file.Writable || !strings.Contains(string(file.Bytes), `kind = ""`) {
						t.Fatalf("Pi must prepare a writable file without a valid default outcome: %+v", file)
					}
					definition, err := LookupAdapterDefinition(string(workerType))
					if err != nil {
						t.Fatal(err)
					}
					identity := definition.submission.submissionIdentity(test.task)
					if !strings.Contains(invocation.prompt, identity.Contract) {
						t.Fatal("invocation and static identity disagree on the submission contract")
					}
					if workerType == WorkerTypePi && identity.Templates[test.phase] != string(file.Bytes) {
						t.Fatal("invocation and static identity disagree on the phase file template")
					}
					originalBytes := string(file.Bytes)
					originalPath := file.Path()
					file.Bytes[0] = '!'
					file.Filename = "mutated"
					file.Writable = !file.Writable
					fresh := invocation.ProtocolFile()
					if string(fresh.Bytes) != originalBytes || fresh.Path() != originalPath || fresh.Writable == file.Writable {
						t.Fatal("Invocation exposed mutable submission file material")
					}
				})
			}
		})
	}
}

func TestFileSubmissionRejectsInvalidInvocationIdentity(t *testing.T) {
	for _, workerType := range []WorkerType{WorkerTypeCodex, WorkerTypePi} {
		t.Run(string(workerType), func(t *testing.T) {
			adapter, err := NewAgentAdapter(WorkerConfig{Name: string(workerType), Type: workerType})
			if err != nil {
				t.Fatal(err)
			}
			for _, invocationID := range []string{"", "not-an-invocation-id"} {
				_, err := adapter.Prepare(AgentRequest{
					InvocationID: invocationID, Task: rundomain.TaskReason, Phase: PhaseReason,
					Prompt: "submit", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
				})
				var captureError *CaptureError
				if !errors.As(err, &captureError) || captureError.Code != CaptureBoundaryError {
					t.Fatalf("invalid invocation identity %q must fail preparation: %v", invocationID, err)
				}
			}
		})
	}
}

func TestNativeSubmissionRejectsInvalidCommand(t *testing.T) {
	request := AgentRequest{InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskReason, Phase: PhaseReason}
	contract, err := resultContractForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claudecode", "codex"} {
		t.Run(name, func(t *testing.T) {
			definition, err := LookupAdapterDefinition(name)
			if err != nil {
				t.Fatal(err)
			}
			material, err := definition.submission.prepareSubmission(request, contract)
			if err != nil {
				t.Fatal(err)
			}
			_, err = definition.submission.applySubmission(DriverResult{Argv: []string{name}}, material)
			var captureError *CaptureError
			if !errors.As(err, &captureError) || captureError.Code != CaptureBoundaryError {
				t.Fatalf("missing native option insertion point must fail: %v", err)
			}
		})
	}
}
