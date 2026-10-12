package worker

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

var testBlackboardPath = rundomain.BlackboardPath("worker-test-run", rundomain.PhaseReason)

func claudeSuccessEnvelope(structured string) string {
	return `{"type":"result","subtype":"success","is_error":false,"result":"ignored text","session_id":"session-id","structured_output":` + structured + `}`
}

func TestReasonFinalizationUsesConcludeAdapterContinuation(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "mock", Type: WorkerTypeMock})
	if err != nil {
		t.Fatal(err)
	}
	executorAdapter, err := newReasonExecutorAdapter(adapter)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := executorAdapter.PrepareReason(rundomain.ReasonRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Prompt:       "reason", InputPath: testBlackboardPath, MaxProposedIntents: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalizer, ok := prepared.(rundomain.ReasonFinalizationInvocation)
	if !ok {
		t.Fatalf("Reason invocation does not expose Finalization continuation: %T", prepared)
	}
	finalizationPrompt := "Re-serialize only the confirmed result."
	finalized, err := finalizer.PrepareReasonFinalization(rundomain.ReasonFinalizationRequest{
		Prompt: finalizationPrompt, InputPath: rundomain.BlackboardPath("reason-run", rundomain.PhaseReasonFinalization),
		MaxProposedIntents: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Environment()["MOCK_PHASE"] != string(PhaseReasonFinalization) {
		t.Fatalf("Finalization phase environment=%q", finalized.Environment()["MOCK_PHASE"])
	}
	if !strings.Contains(strings.Join(finalized.Command(), "\n"), OutcomeSubmissionSchemaV1) {
		t.Fatalf("Finalization command omitted the Mock Adapter submission contract: %v", finalized.Command())
	}
}

func TestReasonFinalizationRejectsUnverifiedPiSession(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi})
	if err != nil {
		t.Fatal(err)
	}
	executorAdapter, err := newReasonExecutorAdapter(adapter)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := executorAdapter.PrepareReason(rundomain.ReasonRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Prompt:       "reason", InputPath: testBlackboardPath, MaxProposedIntents: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalizer, ok := prepared.(rundomain.ReasonFinalizationInvocation)
	if !ok {
		t.Fatalf("Pi Reason invocation does not expose Finalization continuation: %T", prepared)
	}
	if _, err := finalizer.PrepareReasonFinalization(rundomain.ReasonFinalizationRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abd",
		Prompt:       "finalize", InputPath: rundomain.BlackboardPath("reason-run", rundomain.PhaseReasonFinalization),
		MaxProposedIntents: 3,
	}); err == nil {
		t.Fatal("Pi Finalization accepted an unverified Session")
	}
}

func TestEnabledAgentAdaptersSharePrepareCaptureContract(t *testing.T) {
	configs := []WorkerConfig{
		{Name: "claude", Type: WorkerTypeClaudeCode, Env: map[string]string{"ANTHROPIC_MODEL": "model"}},
		{Name: "codex", Type: WorkerTypeCodex, Env: map[string]string{"CODEX_MODEL": "model"}},
		{Name: "pi", Type: WorkerTypePi, Env: map[string]string{"PI_MODEL": "model"}},
		{Name: "mock", Type: WorkerTypeMock},
	}
	wantConclude := map[WorkerType]bool{
		WorkerTypeClaudeCode: true,
		WorkerTypeCodex:      true,
		WorkerTypePi:         true,
		WorkerTypeMock:       true,
	}
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`
	for _, config := range configs {
		t.Run(string(config.Type), func(t *testing.T) {
			adapter, err := NewAgentAdapter(config)
			if err != nil {
				t.Fatal(err)
			}
			request := AgentRequest{InvocationID: "12345678-abcd-4abc-8def-123456789abc",
				Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason over the input",
				Input:              InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
				MaxProposedIntents: 3,
			}
			invocation, err := adapter.Prepare(request)
			if err != nil {
				t.Fatal(err)
			}
			argv := invocation.Argv()
			joinedArgv := strings.Join(argv, "\n")
			if len(argv) == 0 {
				t.Fatalf("typed invocation does not carry its result capture contract: %+v", argv)
			}
			if config.Type == WorkerTypeClaudeCode &&
				(!strings.Contains(joinedArgv, "--json-schema") || !strings.Contains(joinedArgv, "--output-format\njson")) {
				t.Fatalf("Claude invocation does not carry only its native schema contract: %+v", argv)
			}
			if config.Type == WorkerTypeCodex &&
				(!strings.Contains(joinedArgv, "--output-schema") || invocation.ProtocolFile() == nil) {
				t.Fatalf("Codex invocation does not carry its native schema contract: %+v", argv)
			}
			if config.Type == WorkerTypeMock && (!strings.Contains(joinedArgv, OutcomeSubmissionSchemaV1)) {
				t.Fatalf("Mock invocation does not carry only its outcome submission contract: %+v", argv)
			}
			if config.Type == WorkerTypePi && (!strings.Contains(joinedArgv, "Pi Result File Contract") ||
				!strings.Contains(joinedArgv, "result.toml") || strings.Contains(joinedArgv, ResultOutputContractSchemaV2)) {
				t.Fatalf("Pi invocation does not carry the TOML submission contract: %+v", argv)
			}
			if invocation.ResultContract() != (ResultContract{
				Schema: ResultOutputContractSchemaV2, Task: rundomain.TaskReason, Phase: PhaseReason, MaxProposedIntents: 3,
			}) {
				t.Fatalf("Invocation Result Contract=%+v", invocation.ResultContract())
			}
			if invocation.Input() != request.Input {
				t.Fatalf("invocation input=%+v, want %+v", invocation.Input(), request.Input)
			}
			if config.Type != WorkerTypeMock && !strings.Contains(joinedArgv, testBlackboardPath) {
				t.Fatalf("typed invocation does not reference the Blackboard input: %+v", argv)
			}
			if config.Type == WorkerTypePi && !strings.Contains(joinedArgv, "--mode\njson") {
				t.Fatalf("Pi invocation is not using structured event output: %+v", argv)
			}
			argv[0] = "mutated"
			if invocation.Argv()[0] == "mutated" {
				t.Fatal("Invocation exposed mutable argv")
			}
			stderr := ""
			if config.Type == WorkerTypeCodex {
				stderr = "session id: 12345678-abcd-4abc-8def-123456789abc"
			}
			stdout := "diagnostic\n" + candidate + "\nfinished"
			if config.Type == WorkerTypeClaudeCode {
				stdout = claudeSuccessEnvelope(`{"outcome":{"kind":"no_change"}}`)
			}
			if config.Type == WorkerTypeMock || config.Type == WorkerTypeCodex {
				stdout = `{"outcome":{"kind":"no_change"}}`
			}
			if config.Type == WorkerTypePi {
				stdout = strictPiLifecycle(strictPiHeader(invocation), "Pi Reason result is stored in result.toml")
			}
			output := ProcessOutput{Exited: true, ExitCode: 0, Stderr: stderr, Stdout: stdout}
			if config.Type == WorkerTypePi {
				headerRaw := strictPiHeader(invocation)
				header := mustPiHeader(t, headerRaw)
				sessionPath := piSessionFilePath(invocation.sessionDir, header)
				output.StoppedProcessFileReader = &piBatchFileReader{files: map[string][]byte{
					sessionPath:                      []byte(strictPiSessionFile(headerRaw, strictPiPrompt(invocation), "Pi Reason result is stored in result.toml", "00000001", "00000002")),
					invocation.ProtocolFile().Path(): []byte("[outcome]\nkind = \"no_change\"\n"),
				}}
			}
			captured, err := adapter.Capture(t.Context(), invocation, output)
			if err != nil {
				t.Fatal(err)
			}
			if string(captured.Candidate) != candidate || captured.Diagnostics.StdoutSummary == "" {
				t.Fatalf("capture result: %+v", captured)
			}
			if _, _, _, err := rundomain.DecodeRunResult(captured.Candidate, rundomain.TaskReason, 3); err != nil {
				t.Fatalf("strict RunResult handoff: %v", err)
			}
			_, concludes := adapter.(ConcludeAdapter)
			if concludes != wantConclude[config.Type] {
				t.Fatalf("ConcludeAdapter=%t, want %t", concludes, wantConclude[config.Type])
			}
			if concludes {
				if captured.Session == nil {
					t.Fatal("conclude-capable adapter did not retain a session")
				}
				conclude := adapter.(ConcludeAdapter)
				concludeInvocation, err := conclude.PrepareConclude(ConcludeRequest{
					AgentRequest: AgentRequest{InvocationID: "12345678-abcd-4abc-8def-123456789abc",
						Task: rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "finish exploration",
						Input: request.Input,
					},
					Session: captured.Session,
				})
				if err != nil || len(concludeInvocation.Argv()) == 0 {
					t.Fatalf("valid conclude invocation: argv=%v err=%v", concludeInvocation.Argv(), err)
				}
				if concludeInvocation.ResultContract() != (ResultContract{
					Schema: ResultOutputContractSchemaV2, Task: rundomain.TaskExplore, Phase: PhaseExploreConclude,
				}) || config.Type == WorkerTypePi && (!strings.Contains(strings.Join(concludeInvocation.Argv(), "\n"), "Pi Result File Contract") || concludeInvocation.ProtocolFile() == nil) ||
					config.Type == WorkerTypeMock && !strings.Contains(strings.Join(concludeInvocation.Argv(), "\n"), OutcomeSubmissionSchemaV1) {
					t.Fatalf("Conclude Result Contract=%+v argv=%v", concludeInvocation.ResultContract(), concludeInvocation.Argv())
				}
				if _, err := conclude.PrepareConclude(ConcludeRequest{
					AgentRequest: AgentRequest{InvocationID: "12345678-abcd-4abc-8def-123456789abc",
						Task: rundomain.TaskReason, Phase: PhaseExploreConclude, Prompt: "finish",
						Input: request.Input,
					},
					Session: captured.Session,
				}); err == nil {
					t.Fatal("mismatched conclude phase was accepted")
				}
			}
		})
	}
}

func TestConcludeProcessRecoveryKeepsAdapterSpecificToolPolicy(t *testing.T) {
	templates, err := readBundledPromptTemplates("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		config WorkerConfig
	}{
		{name: "claude", config: WorkerConfig{Name: "claude", Type: WorkerTypeClaudeCode, Env: map[string]string{"ANTHROPIC_MODEL": "model"}}},
		{name: "pi", config: WorkerConfig{Name: "pi", Type: WorkerTypePi, Env: map[string]string{"PI_MODEL": "model"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewAgentAdapter(test.config)
			if err != nil {
				t.Fatal(err)
			}
			recovery, ok := adapter.(ProcessRecoveryAdapter)
			if !ok {
				t.Fatal("Adapter does not expose ProcessRecoveryAdapter")
			}
			session := &Session{value: "same-session"}
			if test.config.Type == WorkerTypePi {
				session = verifiedPiTestSession(t, "00000002")
			}
			invocation, err := recovery.PrepareRecovery(ProcessRecoveryRequest{
				AgentRequest: AgentRequest{
					InvocationID: "12345678-abcd-4abc-8def-123456789abc",
					Task:         rundomain.TaskBootstrap, Phase: PhaseBootstrapConcludeRecovery,
					Prompt: templates["bootstrap_conclude_recovery.md"],
					Input:  InputReference{Kind: InputBootstrapInline, Inline: "origin: origin\ngoal: goal"},
				},
				Session: session,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantSession := "same-session"
			if test.config.Type == WorkerTypePi {
				wantSession = "0192f7e5-7b2a-7c01-8a11-223344556677"
			}
			if invocation.Session() == nil || !strings.Contains(strings.Join(invocation.Argv(), "\n"), wantSession) ||
				invocation.Phase() != PhaseBootstrapConcludeRecovery {
				t.Fatalf("Recovery invocation identity: phase=%s session=%+v", invocation.Phase(), invocation.Session())
			}
			argv := invocation.Argv()
			joined := strings.Join(argv, "\n")
			if !strings.Contains(joined, "Outcome-Constrained Completion Phase") || !strings.Contains(joined, "conclude_intent") {
				t.Fatalf("Conclude Recovery Prompt constraints are missing: %v", argv)
			}
			switch test.config.Type {
			case WorkerTypeClaudeCode:
				if !strings.Contains(joined, "--tools=") || !strings.Contains(joined, "--output-format\njson") ||
					!strings.Contains(joined, "--json-schema") {
					t.Fatalf("Claude Conclude Recovery did not disable tools: %v", argv)
				}
			case WorkerTypePi:
				if !strings.Contains(joined, "--tools\nread,write,edit,bash,grep,find,ls") || strings.Contains(joined, "--no-tools") {
					t.Fatalf("Pi Conclude Recovery did not retain full tools: %v", argv)
				}
			}
		})
	}
}

func TestPiConcludeAndRecoveryRejectUnverifiedSessionCursor(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi, Env: map[string]string{"PI_MODEL": "model"}})
	if err != nil {
		t.Fatal(err)
	}
	unverified := &Session{value: "0192f7e5-7b2a-7c01-8a11-223344556677"}
	if _, err := adapter.(ConcludeAdapter).PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude",
			Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
		},
		Session: unverified,
	}); err == nil {
		t.Fatal("Pi Conclude accepted an unverified Session ID")
	}
	if _, err := adapter.(ProcessRecoveryAdapter).PrepareRecovery(ProcessRecoveryRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreRecovery, Prompt: "recover",
			Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
		},
		Session: unverified,
	}); err == nil {
		t.Fatal("Pi Recovery accepted an unverified Session ID")
	}
}

func TestClaudeCodeUsesNativeStructuredOutputForMainAndConclude(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "claude", Type: WorkerTypeClaudeCode,
		Env: map[string]string{"ANTHROPIC_MODEL": "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mainRequest := AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	}
	mainInvocation, err := adapter.Prepare(mainRequest)
	if err != nil {
		t.Fatal(err)
	}
	mainArgs := strings.Join(mainInvocation.Argv(), "\n")
	if !strings.Contains(mainArgs, "--json-schema") || !strings.Contains(mainArgs, "--output-format\njson") {
		t.Fatalf("Claude main invocation does not use native structured output: %v", mainInvocation.Argv())
	}
	mainCandidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`
	captured, err := adapter.Capture(t.Context(), mainInvocation, ProcessOutput{
		Exited: true, ExitCode: 0, Stdout: claudeSuccessEnvelope(`{"outcome":{"kind":"no_change"}}`),
	})
	if err != nil || string(captured.Candidate) != mainCandidate || captured.ResultHash == "" {
		t.Fatalf("Claude structured capture: result=%+v error=%v", captured, err)
	}

	concludeAdapter, ok := adapter.(ConcludeAdapter)
	if !ok {
		t.Fatal("Claude Code must expose ConcludeAdapter")
	}
	concludeInvocation, err := concludeAdapter.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude",
			Input: mainRequest.Input,
		},
		Session: mainInvocation.Session(),
	})
	if err != nil {
		t.Fatal(err)
	}
	concludeArgs := strings.Join(concludeInvocation.Argv(), "\n")
	if !strings.Contains(concludeArgs, "--json-schema") || !strings.Contains(concludeArgs, "--output-format\njson") ||
		!strings.Contains(concludeArgs, "--tools=") {
		t.Fatalf("Claude conclude invocation does not preserve native structured output and tool boundary: %v", concludeInvocation.Argv())
	}
	concludeCandidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"done"}}`
	captured, err = adapter.Capture(t.Context(), concludeInvocation, ProcessOutput{
		Exited: true, ExitCode: 0, Stdout: claudeSuccessEnvelope(`{"outcome":{"kind":"decline","reason":"done"}}`),
	})
	if err != nil || string(captured.Candidate) != concludeCandidate {
		t.Fatalf("Claude conclude structured result: result=%+v error=%v", captured, err)
	}
	result, _, _, err := rundomain.DecodeRunResult(captured.Candidate, rundomain.TaskExplore, 0)
	if err != nil || result.Outcome.Kind != rundomain.OutcomeDecline {
		t.Fatalf("Claude conclude structured result: result=%+v err=%v", result, err)
	}
}

func TestTimedOutPhaseObservesSessionWithoutResultCapture(t *testing.T) {
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
	header := strictPiHeader(invocation)
	prompt := strictPiPrompt(invocation)
	session := adapter.ObserveSession(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: strictPiLifecycle(header, "not used"), Exited: true, ExitCode: 124, TimedOut: true,
		StoppedProcessFileReader: &piEvidenceFileReader{content: []byte(strictPiSessionFile(
			header, prompt, "not used", "00000001", "00000002",
		))},
	})
	if !adapter.CanContinueSession(session) {
		t.Fatal("verified timeout Session was not continuation-eligible")
	}
	conclude, ok := adapter.(ConcludeAdapter)
	if !ok {
		t.Fatal("Pi adapter does not expose conclude capability")
	}
	if _, err := conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude",
			Input: InputReference{Kind: InputBlackboardFile, Path: rundomain.BlackboardPath("run-timeout", rundomain.PhaseExploreConclude)},
		},
		Session: session,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAbnormalPiPhaseRequiresVerifiedSessionBeforeProcessRecovery(t *testing.T) {
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
	header := strictPiHeader(invocation)
	prompt := strictPiPrompt(invocation)
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"abnormal candidate must not commit"}}}`
	session := adapter.ObserveSession(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: strictPiLifecycle(header, candidate), Exited: true, ExitCode: 9,
		StoppedProcessFileReader: &piEvidenceFileReader{content: []byte(strictPiSessionFile(
			header, prompt, candidate, "00000001", "00000002",
		))},
	})
	if !adapter.CanContinueSession(session) {
		t.Fatal("verified abnormal Pi Invocation did not authorize Process Recovery")
	}
	recovery, ok := adapter.(ProcessRecoveryAdapter)
	if !ok {
		t.Fatal("Pi adapter does not expose Process Recovery")
	}
	recovered, err := recovery.PrepareRecovery(ProcessRecoveryRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreRecovery, Prompt: "recover",
			Input: InputReference{Kind: InputBlackboardFile, Path: rundomain.BlackboardPath("run-abnormal", rundomain.PhaseExploreRecovery)},
		},
		Session: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(recovered.Argv(), "\n"), "--session\n0192f7e5-7b2a-7c01-8a11-223344556677") {
		t.Fatalf("Recovery did not resume the verified abnormal Session: %v", recovered.Argv())
	}
}

func TestAbnormalPiPhaseRejectsUnavailableSessionEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		reader rundomain.StoppedProcessFileReader
	}{
		{name: "missing reader"},
		{name: "read failure", reader: &piEvidenceFileReader{err: errors.New("Session unavailable")}},
		{name: "read timeout", reader: &piEvidenceFileReader{err: context.DeadlineExceeded}},
		{name: "malformed", reader: &piEvidenceFileReader{content: []byte(`{"type":"session"}`)}},
		{name: "oversized", reader: &piEvidenceFileReader{content: []byte(strings.Repeat("x", maximumPiSessionBytes+1))}},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			session := adapter.ObserveSession(t.Context(), invocation, rundomain.ProcessOutput{
				Stdout: strictPiLifecycle(strictPiHeader(invocation), "abnormal candidate"), Exited: true, ExitCode: 9,
				StoppedProcessFileReader: test.reader,
			})
			if adapter.CanContinueSession(session) {
				t.Fatal("unavailable abnormal Pi Session evidence authorized Process Recovery")
			}
		})
	}
}

func TestTimedOutPiPhaseDoesNotUseUnverifiedSession(t *testing.T) {
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
	session := adapter.ObserveSession(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: strictPiLifecycle(strictPiHeader(invocation), "not used"), Exited: true, ExitCode: 124, TimedOut: true,
		StoppedProcessFileReader: &piEvidenceFileReader{err: errors.New("Session unavailable")},
	})
	if adapter.CanContinueSession(session) {
		t.Fatal("unverified Pi Session authorized continuation")
	}
}

func TestPiCaptureFailureDoesNotRetainUnverifiedStdoutSession(t *testing.T) {
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
	captured, err := adapter.Capture(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: strictPiLifecycle(strictPiHeader(invocation), "not a result"), Exited: true, ExitCode: 0,
		StoppedProcessFileReader: &piEvidenceFileReader{err: errors.New("Session unavailable")},
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureNoCandidate ||
		captured.CaptureFailureReason != rundomain.CaptureFailureNoCandidate {
		t.Fatalf("unverified capture=%+v error=%v", captured, err)
	}
	if captured.Session != nil || adapter.CanContinueSession(captured.Session) {
		t.Fatalf("unverified stdout Session remained continuation-eligible: %+v", captured.Session)
	}
	conclude, ok := adapter.(ConcludeAdapter)
	if !ok {
		t.Fatal("Pi adapter does not expose conclude capability")
	}
	if _, err := conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude",
			Input: InputReference{Kind: InputBlackboardFile, Path: rundomain.BlackboardPath("run-unverified", rundomain.PhaseExploreConclude)},
		},
		Session: captured.Session,
	}); err == nil {
		t.Fatal("unverified stdout Session authorized Conclude")
	}
}

func TestPiStructurallyIncompleteSessionAdvancesCursorForConclude(t *testing.T) {
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
	header := strictPiHeader(invocation)
	stdout := strictPiPrelude("/workspace") + "\n" + header + "\n" + `{"type":"agent_start"}` + "\n"
	captured, err := adapter.Capture(t.Context(), invocation, rundomain.ProcessOutput{
		Stdout: stdout, Exited: true, ExitCode: 0,
		StoppedProcessFileReader: &piEvidenceFileReader{content: []byte(strictPiIncompleteSessionFile(header, prompt))},
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureNoCandidate ||
		captured.CaptureFailureReason != rundomain.CaptureFailureNoCandidate ||
		!adapter.CanContinueSession(captured.Session) {
		t.Fatalf("incomplete capture=%+v error=%v", captured, err)
	}
	conclude, ok := adapter.(ConcludeAdapter)
	if !ok {
		t.Fatal("Pi adapter does not expose conclude capability")
	}
	fallback, err := conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc",
			Task:         rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude",
			Input: InputReference{Kind: InputBlackboardFile, Path: rundomain.BlackboardPath("run-incomplete", rundomain.PhaseExploreConclude)},
		},
		Session: captured.Session,
	})
	if err != nil || !strings.Contains(strings.Join(fallback.Argv(), "\n"), "--session\n0192f7e5-7b2a-7c01-8a11-223344556677") {
		t.Fatalf("verified incomplete Session did not authorize Conclude: command=%v error=%v", fallback, err)
	}
}

func TestCaptureRequiresObservedSuccessfulExit(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "mock", Type: WorkerTypeMock})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{Exited: false, ExitCode: 0, Stdout: candidate})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureProcessFailed {
		t.Fatalf("capture error=%v, want %s", err, CaptureProcessFailed)
	}
	if len(captured.Candidate) != 0 {
		t.Fatalf("unobserved process exit produced candidate: %s", captured.Candidate)
	}
}

func TestCaptureRejectsMissingMultipleMalformedAndOversizedCandidates(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "mock", Type: WorkerTypeMock})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		output        ProcessOutput
		code          CaptureErrorCode
		failureReason rundomain.CaptureFailureReason
	}{
		{name: "no outcome", output: ProcessOutput{Exited: true, ExitCode: 0, Stdout: `{}`}, code: CaptureInvalidCandidate, failureReason: rundomain.CaptureFailureInvalidCandidate},
		{name: "empty", output: ProcessOutput{Exited: true, ExitCode: 0}, code: CaptureNoCandidate, failureReason: rundomain.CaptureFailureNoCandidate},
		{name: "legacy envelope", output: ProcessOutput{Exited: true, ExitCode: 0, Stdout: `{"accepted":true,"data":{}}`}, code: CaptureInvalidCandidate, failureReason: rundomain.CaptureFailureInvalidCandidate},
		{name: "unknown field", output: ProcessOutput{Exited: true, ExitCode: 0, Stdout: `{"outcome":{"kind":"no_change","extra":true}}`}, code: CaptureInvalidCandidate, failureReason: rundomain.CaptureFailureInvalidCandidate},
		{name: "duplicate field", output: ProcessOutput{Exited: true, ExitCode: 0, Stdout: `{"outcome":{"kind":"no_change","kind":"no_change"}}`}, code: CaptureInvalidCandidate, failureReason: rundomain.CaptureFailureInvalidCandidate},
		{name: "multiple values", output: ProcessOutput{Exited: true, ExitCode: 0, Stdout: `{"outcome":{"kind":"no_change"}} {}`}, code: CaptureInvalidCandidate, failureReason: rundomain.CaptureFailureInvalidCandidate},
		{name: "oversized", output: ProcessOutput{Exited: true, ExitCode: 0, Stdout: `{"outcome":{"kind":"decline","reason":"` + strings.Repeat("x", rundomain.MaximumResultBytes) + `"}}`}, code: CaptureCandidateTooLarge, failureReason: rundomain.CaptureFailureCandidateTooLarge},
		{name: "collector overflow", output: ProcessOutput{Exited: true, ExitCode: 0, ErrorCode: "output_too_large"}, code: CaptureOutputTooLarge},
		{name: "timed out", output: ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true}, code: CaptureTimedOut, failureReason: rundomain.CaptureFailureTimedOut},
		{name: "cancelled", output: ProcessOutput{Exited: true, ExitCode: 130, Cancelled: true}, code: CaptureCancelled},
		{name: "process failed", output: ProcessOutput{Exited: true, ExitCode: 9}, code: CaptureProcessFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			captured, err := adapter.Capture(t.Context(), invocation, test.output)
			var captureErr *CaptureError
			if !errors.As(err, &captureErr) || captureErr.Code != test.code {
				t.Fatalf("error=%v, want %s", err, test.code)
			}
			if captured.CaptureFailureReason != test.failureReason {
				t.Fatalf("failure reason=%q, want %q", captured.CaptureFailureReason, test.failureReason)
			}
		})
	}
}

func TestCaptureDiagnosticsRetainNoRawAgentOutput(t *testing.T) {
	const secret = "sk-super-secret"
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "codex", Type: WorkerTypeCodex,
		Env: map[string]string{
			"CODEX_MODEL": "model", "CODEX_BASE_URL": "https://example.invalid", "OPENAI_API_KEY": secret,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task: rundomain.TaskBootstrap, Phase: PhaseBootstrap, Prompt: "private bootstrap prompt",
		Input: InputReference{Kind: InputBootstrapInline, Inline: "origin: confidential"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout := secret + " private bootstrap prompt origin: confidential " + strings.Repeat("诊", 4096)
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{Exited: true, ExitCode: 1, Stdout: stdout, Stderr: secret})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) {
		t.Fatal("expected process failure")
	}
	for _, value := range []string{captured.Diagnostics.StdoutSummary, captured.Diagnostics.StderrSummary} {
		if strings.Contains(value, secret) || strings.Contains(value, "private bootstrap prompt") ||
			strings.Contains(value, "origin: confidential") || !strings.Contains(value, "redacted agent output") {
			t.Fatalf("diagnostics were not redacted: %q", value)
		}
		if len(value) > 64 {
			t.Fatalf("diagnostics summary is not bounded: %q", value)
		}
	}
}

func TestMockAdapterInvocationEmitsTypedRunResult(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "mock", Type: WorkerTypeMock,
		Env: map[string]string{
			"MOCK_REASON":  `{"delay":[0,0],"outcomes":{"noop":1}}`,
			"MOCK_CONTEXT": `{"fact_ids_count":2,"open_intents_empty":false,"max_intents":3}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := invocation.Argv()
	command := exec.Command(argv[0], argv[1:]...)
	var stderr strings.Builder
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		t.Fatalf("execute typed mock: %v: %s", err, stderr.String())
	}
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{Exited: true, Stdout: string(stdout)})
	if err != nil {
		t.Fatal(err)
	}
	result, _, _, err := rundomain.DecodeRunResult(captured.Candidate, rundomain.TaskReason, 3)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome.Kind != rundomain.OutcomeNoChange {
		t.Fatalf("outcome=%q, want %q", result.Outcome.Kind, rundomain.OutcomeNoChange)
	}
}

func TestMockAdapterConsumesOutcomeSubmissionPackage(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "mock", Type: WorkerTypeMock,
		Env: map[string]string{
			"MOCK_REASON":  `{"delay":[0,0],"outcomes":{"noop":1}}`,
			"MOCK_CONTEXT": `{"fact_ids_count":1,"open_intents_empty":true,"max_intents":3}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(invocation.Argv(), "\n")
	if strings.Contains(joined, ResultOutputContractSchemaV2) ||
		!strings.Contains(joined, OutcomeSubmissionSchemaV1) {
		t.Fatalf("Mock invocation mixed shared and Adapter output contracts: %v", invocation.Argv())
	}
	command := exec.Command(invocation.Argv()[0], invocation.Argv()[1:]...)
	stdout, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stdout), `"schema"`) || strings.Contains(string(stdout), "accepted") {
		t.Fatalf("Mock emitted the legacy or backend envelope: %s", stdout)
	}
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{Exited: true, Stdout: string(stdout)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`
	if string(captured.Candidate) != want || captured.ResultHash == "" {
		t.Fatalf("Mock canonical candidate=%s hash=%q, want %s", captured.Candidate, captured.ResultHash, want)
	}
}

func TestCapturedCandidateStillRequiresStrictRunResultValidation(t *testing.T) {
	for _, candidate := range []string{
		`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change","inferred":true}}`,
		`{"accepted":true,"data":{}}`,
	} {
		captured := []byte(candidate)
		_, _, _, err := rundomain.DecodeRunResult(captured, rundomain.TaskReason, 3)
		var domainErr *rundomain.DomainError
		if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorInvalidResult {
			t.Fatalf("error=%v, want invalid_result", err)
		}
	}
}

func TestPrepareEnforcesTaskInputAndOptionalConcludeCapabilities(t *testing.T) {
	pi, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi})
	if err != nil {
		t.Fatal(err)
	}
	badRequests := []AgentRequest{
		{Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "x", Input: InputReference{Kind: InputBootstrapInline, Inline: "x"}},
		{Task: rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "x", Input: InputReference{Kind: InputBlackboardFile, Path: "/tmp/graph.yaml"}},
		{Task: rundomain.TaskBootstrap, Phase: PhaseBootstrap, Prompt: "x", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}},
		{Task: rundomain.TaskBootstrap, Phase: PhaseBootstrap, Prompt: "x", Input: InputReference{Kind: InputBootstrapInline, Inline: strings.Repeat("x", maximumInlineSize+1)}},
	}
	for _, request := range badRequests {
		if _, err := pi.Prepare(request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	if _, ok := pi.(ConcludeAdapter); !ok {
		t.Fatal("Pi must expose its supported ConcludeAdapter")
	}
	bootstrap, err := pi.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskBootstrap, Phase: PhaseBootstrap, Prompt: "bootstrap",
		Input: InputReference{Kind: InputBootstrapInline, Inline: "origin: start\ngoal: answer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(bootstrap.Argv(), "\n"), "origin: start\ngoal: answer") {
		t.Fatal("Bootstrap inline input was not included in the controlled prompt")
	}
}
