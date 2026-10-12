package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type piBatchFileReader struct {
	files map[string][]byte
}

func (reader *piBatchFileReader) ReadFiles(_ context.Context, requestedPaths []string) map[string]rundomain.StoppedProcessFile {
	result := make(map[string]rundomain.StoppedProcessFile, len(requestedPaths))
	for _, requestedPath := range requestedPaths {
		content, ok := reader.files[requestedPath]
		if !ok {
			result[requestedPath] = rundomain.StoppedProcessFile{Err: errors.New("file unavailable")}
			continue
		}
		result[requestedPath] = rundomain.StoppedProcessFile{Bytes: append([]byte(nil), content...)}
	}
	return result
}

func TestNormalizePiTOMLBuildsCanonicalRunResult(t *testing.T) {
	raw := []byte("[outcome]\nkind = \"propose_intents\"\n[[outcome.intents]]\nfrom = [\"origin\"]\ndescription = \"first line\\nsecond \\\"quoted\\\" \\\\ path\"\n")
	candidate, hash, _, err := normalizePiTOML(raw, ResultContract{Schema: ResultOutputContractSchemaV2, Task: rundomain.TaskReason, Phase: PhaseReason, MaxProposedIntents: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"first line\nsecond \"quoted\" \\ path"}]}}`
	if string(candidate) != want || hash == "" {
		t.Fatalf("candidate=%s hash=%q, want=%s", candidate, hash, want)
	}
}

func TestNormalizePiTOMLAcceptsEveryReasonOutcome(t *testing.T) {
	contract := ResultContract{Schema: ResultOutputContractSchemaV2, Task: rundomain.TaskReason, Phase: PhaseReason, MaxProposedIntents: 3}
	for _, raw := range []string{
		"[outcome]\nkind=\"no_change\"\n",
		"[outcome]\nkind=\"decline\"\nreason=\"outside scope\"\n",
		"[outcome]\nkind=\"complete_project\"\ndescription=\"verified\"\nfrom=[\"origin\"]\n",
	} {
		if _, _, _, err := normalizePiTOML([]byte(raw), contract); err != nil {
			t.Fatalf("valid Reason TOML %q rejected: %v", raw, err)
		}
	}
}

func TestPiExploreAndBootstrapInvocationsUseIndependentTOMLFiles(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "pi", Type: WorkerTypePi,
		Env: map[string]string{"PI_MODEL": "model", "PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []AgentRequest{
		{InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "explore", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}},
		{InvocationID: "12345678-abcd-4abc-8def-123456789abd", Task: rundomain.TaskBootstrap, Phase: PhaseBootstrap, Prompt: "bootstrap", Input: InputReference{Kind: InputBootstrapInline, Inline: "origin: origin\ngoal: goal"}},
	} {
		invocation, err := adapter.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		file := invocation.ProtocolFile()
		if file == nil || file.Filename != piResultFilename || !file.Writable || !strings.Contains(string(file.Bytes), "Allowed kinds:") {
			t.Fatalf("Pi %s protocol file=%+v", request.Task, file)
		}
		if !strings.Contains(strings.Join(invocation.Argv(), "\n"), "Pi Result File Contract") {
			t.Fatalf("Pi %s invocation omitted TOML contract", request.Task)
		}
	}
}

func TestNormalizePiTOMLRejectsMissingUnknownDuplicateAndOversizedResults(t *testing.T) {
	contract := ResultContract{Schema: ResultOutputContractSchemaV2, Task: rundomain.TaskReason, Phase: PhaseReason, MaxProposedIntents: 3}
	for _, test := range []struct {
		name string
		raw  string
		code CaptureErrorCode
	}{
		{name: "missing", raw: "# template only\n", code: CaptureNoCandidate},
		{name: "unknown", raw: "[outcome]\nkind=\"no_change\"\nextra=\"x\"\n", code: CaptureInvalidCandidate},
		{name: "duplicate", raw: "[outcome]\nkind=\"no_change\"\nkind=\"decline\"\n", code: CaptureInvalidCandidate},
		{name: "wrong type", raw: "[outcome]\nkind=42\n", code: CaptureInvalidCandidate},
		{name: "date type", raw: "[outcome]\nkind=\"decline\"\nreason=2026-10-07T12:00:00Z\n", code: CaptureInvalidCandidate},
		{name: "whitespace padded", raw: "[outcome]\nkind=\"no_change\"\n" + strings.Repeat(" ", rundomain.MaximumResultBytes), code: CaptureCandidateTooLarge},
		{name: "oversized", raw: "[outcome]\nkind=\"no_change\"\ncomment=\"" + strings.Repeat("x", rundomain.MaximumResultBytes) + "\"\n", code: CaptureCandidateTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, code, err := normalizePiTOML([]byte(test.raw), contract)
			if err == nil || code != test.code {
				t.Fatalf("error=%v code=%q, want code=%q", err, code, test.code)
			}
		})
	}
}

func TestPiReasonInvocationUsesIndependentResultProtocolFile(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "pi", Type: WorkerTypePi,
		Env: map[string]string{"PI_MODEL": "model", "PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskReason, Phase: PhaseReason,
		Prompt: "reason", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}, MaxProposedIntents: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	file := invocation.ProtocolFile()
	if file == nil || file.Filename != piResultFilename || !strings.HasSuffix(file.Path(), "/result.toml") {
		t.Fatalf("result protocol file=%+v", file)
	}
	if _, _, code, err := normalizePiTOML(file.Bytes, invocation.ResultContract()); err == nil || code != CaptureInvalidCandidate {
		t.Fatalf("untouched template accepted: code=%s err=%v", code, err)
	}
	if strings.Contains(invocation.Argv()[len(invocation.Argv())-1], "Return exactly one raw JSON RunResult") {
		t.Fatal("Pi Reason prompt retained the old JSON submission contract")
	}
}

func TestPiSubmissionPromptExamplesMatchPhaseContracts(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi-examples", Type: WorkerTypePi})
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseCase := range outcomePhaseCases {
		for _, limit := range []int{0, 1} {
			if phaseCase.task != rundomain.TaskReason && limit != 0 {
				continue
			}
			t.Run(fmt.Sprintf("%s/max=%d", phaseCase.phase, limit), func(t *testing.T) {
				request := outcomeMatrixRequest(phaseCase.task, phaseCase.phase)
				request.MaxProposedIntents = limit
				invocation, err := prepareOutcomeMatrixInvocation(t, adapter, request, phaseCase.operation, WorkerTypePi)
				if err != nil {
					t.Fatal(err)
				}
				var kinds []rundomain.OutcomeKind
				for _, block := range strings.Split(invocation.prompt, "```toml\n")[1:] {
					example, _, ok := strings.Cut(block, "```")
					if !ok {
						t.Fatal("unterminated TOML example")
					}
					candidate, _, _, err := normalizePiTOML([]byte(example), invocation.ResultContract())
					if err != nil {
						t.Fatalf("prompt example rejected: %s: %v", example, err)
					}
					result, _, _, err := rundomain.DecodeRunResult(candidate, request.Task, limit)
					if err != nil {
						t.Fatal(err)
					}
					kinds = append(kinds, result.Outcome.Kind)
				}
				want := slices.Clone(phaseCase.allowed)
				if limit == 0 {
					want = slices.DeleteFunc(want, func(kind rundomain.OutcomeKind) bool { return kind == rundomain.OutcomeProposeIntents })
				}
				if !slices.Equal(kinds, want) {
					t.Fatalf("prompt example kinds=%v, want=%v", kinds, want)
				}
			})
		}
	}
}

func TestPiReasonCaptureUsesTOMLFileAndKeepsSessionAsBoundaryEvidence(t *testing.T) {
	adapter, invocation := newStrictPiInvocationWithID(t, "reason with file")
	header := strictPiHeader(invocation)
	sessionPath := piSessionFilePath(invocation.sessionDir, mustPiHeader(t, header))
	resultFile := invocation.ProtocolFile()
	reader := &piBatchFileReader{files: map[string][]byte{
		sessionPath:       []byte(strictPiSessionFile(header, strictPiPrompt(invocation), "finished", "00000001", "00000002")),
		resultFile.Path(): []byte("[outcome]\nkind = \"no_change\"\n"),
	}}
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{
		Stdout: strictPiLifecycle(header, "finished"), Exited: true, ExitCode: 0,
		StoppedProcessFileReader: reader,
	})
	if err != nil || captured.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile ||
		string(captured.Candidate) != `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}` {
		t.Fatalf("capture=%+v err=%v", captured, err)
	}
	if captured.Session == nil || captured.Session.cursor != "00000002" {
		t.Fatalf("session boundary evidence=%+v", captured.Session)
	}
}

func TestPiReasonCaptureClassifiesEmptyResultFile(t *testing.T) {
	adapter, invocation := newStrictPiInvocationWithID(t, "empty result file")
	headerRaw := strictPiHeader(invocation)
	header := mustPiHeader(t, headerRaw)
	sessionPath := piSessionFilePath(invocation.sessionDir, header)
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{
		Stdout: strictPiLifecycle(headerRaw, "result file is empty"), Exited: true, ExitCode: 0,
		StoppedProcessFileReader: &piBatchFileReader{files: map[string][]byte{
			sessionPath:                      []byte(strictPiSessionFile(headerRaw, strictPiPrompt(invocation), "result file is empty", "00000001", "00000002")),
			invocation.ProtocolFile().Path(): nil,
		}},
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureNoCandidate ||
		captured.CaptureFailureReason != rundomain.CaptureFailureNoCandidate ||
		captured.CaptureDiagnostic != rundomain.CaptureDiagnosticResultFileInvalid || captured.Session == nil {
		t.Fatalf("empty result capture=%+v err=%v", captured, err)
	}
}

func TestPiReasonCaptureAcceptsFileWithUnavailableSessionWhenStdoutIsComplete(t *testing.T) {
	adapter, invocation := newStrictPiInvocationWithID(t, "stdout proves termination")
	headerRaw := strictPiHeader(invocation)
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{
		Stdout: strictPiLifecycle(headerRaw, "result file is authoritative"), Exited: true, ExitCode: 0,
		StoppedProcessFileReader: &piBatchFileReader{files: map[string][]byte{
			invocation.ProtocolFile().Path(): []byte("[outcome]\nkind = \"no_change\"\n"),
		}},
	})
	if err != nil || captured.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile || captured.Session != nil ||
		string(captured.Candidate) != `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}` {
		t.Fatalf("stdout-only file capture=%+v err=%v", captured, err)
	}
}

func TestPiReasonCaptureRejectsResultFileAfterRuntimeOutputError(t *testing.T) {
	adapter, invocation := newStrictPiInvocationWithID(t, "runtime output error")
	headerRaw := strictPiHeader(invocation)
	header := mustPiHeader(t, headerRaw)
	sessionPath := piSessionFilePath(invocation.sessionDir, header)
	reader := &piBatchFileReader{files: map[string][]byte{
		sessionPath:                      []byte(strictPiSessionFile(headerRaw, strictPiPrompt(invocation), "complete", "00000001", "00000002")),
		invocation.ProtocolFile().Path(): []byte("[outcome]\nkind = \"no_change\"\n"),
	}}
	captured, err := adapter.Capture(t.Context(), invocation, ProcessOutput{
		Stdout: strictPiLifecycle(headerRaw, "complete"), Exited: true, ExitCode: 0,
		ErrorCode: rundomain.ProcessErrorOutputTooLarge, StoppedProcessFileReader: reader,
	})
	var captureErr *CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != CaptureProcessFailed ||
		captured.Candidate != nil || captured.ResultEvidenceSource != "" {
		t.Fatalf("runtime output error accepted result file: capture=%+v err=%v", captured, err)
	}
}

func newStrictPiInvocationWithID(t *testing.T, prompt string) (AgentAdapter, Invocation) {
	t.Helper()
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "strict-pi", Type: WorkerTypePi,
		Env: map[string]string{"PI_MODEL": "model", "PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskReason, Phase: PhaseReason,
		Prompt: prompt, Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}, MaxProposedIntents: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, invocation
}

func mustPiHeader(t *testing.T, raw string) piSessionHeader {
	t.Helper()
	header, err := decodePiSessionHeader([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return header
}
