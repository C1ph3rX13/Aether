package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type piEvidenceFileReader struct {
	content []byte
	err     error
	path    string
	reads   int
}

func (reader *piEvidenceFileReader) ReadFiles(_ context.Context, requestedPaths []string) map[string]rundomain.StoppedProcessFile {
	reader.reads++
	files := make(map[string]rundomain.StoppedProcessFile, len(requestedPaths))
	for _, requestedPath := range requestedPaths {
		reader.path = requestedPath
		files[requestedPath] = rundomain.StoppedProcessFile{Bytes: append([]byte(nil), reader.content...), Err: reader.err}
	}
	return files
}

func TestConfiguredPiAdapterExposesOnlyVerifiedContinuationSessions(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi})
	if err != nil {
		t.Fatal(err)
	}
	unverified := &Session{value: "0192f7e5-7b2a-7c01-8a11-223344556677"}
	if adapter.CanContinueSession(unverified) {
		t.Fatal("unverified Pi Session was continuation-eligible")
	}
	if !adapter.CanContinueSession(verifiedPiTestSession(t, "00000002")) {
		t.Fatal("verified Pi Session was not continuation-eligible")
	}
}

func TestPiAllExecutionPhasesUseUniformFullToolPolicy(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "pi", Type: WorkerTypePi, Env: map[string]string{"PI_MODEL": "model"}})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "primary",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := verifiedPiTestSession(t, "00000002")
	conclude := adapter.(ConcludeAdapter)
	recovery := adapter.(ProcessRecoveryAdapter)
	invocations := []Invocation{primary}
	for index, request := range []ConcludeRequest{
		{AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "conclude", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}}, Session: session},
		{AgentRequest: AgentRequest{
			InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: "finalize", Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}}, Session: session},
	} {
		request.InvocationID = fmt.Sprintf("12345678-abcd-4abc-8def-%012d", index+2)
		invocation, err := conclude.PrepareConclude(request)
		if err != nil {
			t.Fatal(err)
		}
		invocations = append(invocations, invocation)
	}
	for index, phase := range []Phase{PhaseExploreRecovery, PhaseExploreConcludeRecovery} {
		invocation, err := recovery.PrepareRecovery(ProcessRecoveryRequest{
			AgentRequest: AgentRequest{
				InvocationID: fmt.Sprintf("12345678-abcd-4abc-8def-%012d", index+4), Task: rundomain.TaskExplore, Phase: phase, Prompt: string(phase), Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}},
			Session: session,
		})
		if err != nil {
			t.Fatal(err)
		}
		invocations = append(invocations, invocation)
	}
	for _, invocation := range invocations {
		joined := strings.Join(invocation.Argv(), "\n")
		if !strings.Contains(joined, "--tools\nread,write,edit,bash,grep,find,ls") || strings.Contains(joined, "--no-tools") {
			t.Fatalf("Pi phase %s command policy=%v", invocation.Phase(), invocation.Argv())
		}
		if !strings.Contains(joined, "Pi Result File Contract") || invocation.ProtocolFile() == nil || invocation.ResultContract() != (ResultContract{
			Schema: ResultOutputContractSchemaV2, Task: rundomain.TaskExplore, Phase: invocation.Phase(),
		}) {
			t.Fatalf("Pi phase %s Result Contract=%+v command=%v", invocation.Phase(), invocation.ResultContract(), invocation.Argv())
		}
	}
}

func TestPiInvocationSuffixRequiresExactCursorAncestryAndPromptTurn(t *testing.T) {
	headerText := `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
	header, err := decodePiSessionHeader([]byte(headerText))
	if err != nil {
		t.Fatal(err)
	}
	const prompt = "complete successor prompt"
	prior := &Session{value: header.ID, header: header, hasHeader: true, cursor: "00000002", hasCursor: true}
	base := []string{
		headerText,
		`{"type":"message","id":"00000001","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":"primary prompt"}]}}`,
		`{"type":"message","id":"00000002","parentId":"00000001","timestamp":"2026-08-27T13:15:30.125Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"primary result"}]}}`,
	}
	tests := []struct {
		name   string
		prompt string
		lines  []string
		state  piEvidenceState
	}{
		{name: "verified successor", prompt: prompt, state: piEvidenceComplete, lines: []string{
			`{"type":"message","id":"00000003","parentId":"00000002","timestamp":"2026-08-27T13:15:30.126Z","message":{"role":"user","content":[{"type":"text","text":"complete successor prompt"}]}}`,
			`{"type":"message","id":"00000004","parentId":"00000003","timestamp":"2026-08-27T13:15:30.127Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"successor result"}]}}`,
		}},
		{name: "cursor missing from active ancestry", prompt: prompt, state: piEvidenceBoundaryError, lines: []string{
			`{"type":"message","id":"00000003","parentId":"00000001","timestamp":"2026-08-27T13:15:30.126Z","message":{"role":"user","content":[{"type":"text","text":"complete successor prompt"}]}}`,
			`{"type":"message","id":"00000004","parentId":"00000003","timestamp":"2026-08-27T13:15:30.127Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"wrong branch"}]}}`,
		}},
		{name: "extra user turn", prompt: prompt, state: piEvidenceBoundaryError, lines: []string{
			`{"type":"message","id":"00000003","parentId":"00000002","timestamp":"2026-08-27T13:15:30.126Z","message":{"role":"user","content":[{"type":"text","text":"complete successor prompt"}]}}`,
			`{"type":"message","id":"00000004","parentId":"00000003","timestamp":"2026-08-27T13:15:30.127Z","message":{"role":"user","content":[{"type":"text","text":"injected turn"}]}}`,
			`{"type":"message","id":"00000005","parentId":"00000004","timestamp":"2026-08-27T13:15:30.128Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"ambiguous result"}]}}`,
		}},
		{name: "prompt mismatch", prompt: prompt + " changed", state: piEvidenceBoundaryError, lines: []string{
			`{"type":"message","id":"00000003","parentId":"00000002","timestamp":"2026-08-27T13:15:30.126Z","message":{"role":"user","content":[{"type":"text","text":"complete successor prompt"}]}}`,
			`{"type":"message","id":"00000004","parentId":"00000003","timestamp":"2026-08-27T13:15:30.127Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"mismatched result"}]}}`,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := parsePiSessionDocument([]byte(strings.Join(append(append([]string(nil), base...), test.lines...), "\n") + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			evidence := verifyPiInvocationSuffix(Invocation{prompt: test.prompt, session: prior}, document, prior)
			if evidence.state != test.state {
				t.Fatalf("evidence state=%d, want %d", evidence.state, test.state)
			}
			if test.state == piEvidenceComplete && (evidence.session == nil || evidence.session.cursor != "00000004") {
				t.Fatalf("successor Cursor=%+v", evidence.session)
			}
		})
	}
}

func TestPiSessionHeaderRequiresCanonicalV3Identity(t *testing.T) {
	canonical := piSessionHeader{
		Type: "session", Version: 3, ID: "0192f7e5-7b2a-7c01-8a11-223344556677",
		Timestamp: "2026-08-27T13:15:30.123Z", CWD: "/workspace",
	}
	tests := []struct {
		name   string
		mutate func(*piSessionHeader)
	}{
		{name: "unsupported version", mutate: func(header *piSessionHeader) { header.Version = 2 }},
		{name: "noncanonical UUID", mutate: func(header *piSessionHeader) { header.ID = strings.ToUpper(header.ID) }},
		{name: "invalid UUID", mutate: func(header *piSessionHeader) { header.ID = "not-a-uuid" }},
		{name: "timestamp without milliseconds", mutate: func(header *piSessionHeader) { header.Timestamp = "2026-08-27T13:15:30Z" }},
		{name: "timestamp with offset", mutate: func(header *piSessionHeader) { header.Timestamp = "2026-08-27T13:15:30.123+00:00" }},
		{name: "relative cwd", mutate: func(header *piSessionHeader) { header.CWD = "workspace" }},
		{name: "noncanonical cwd", mutate: func(header *piSessionHeader) { header.CWD = "/workspace/../tmp" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := canonical
			test.mutate(&header)
			if err := validatePiSessionHeader(header); err == nil {
				t.Fatal("noncanonical Pi Session header was accepted")
			}
		})
	}
	if err := validatePiSessionHeader(canonical); err != nil {
		t.Fatalf("canonical Pi Session header rejected: %v", err)
	}
}

func TestPiInvocationTerminalAssistantStates(t *testing.T) {
	headerText := `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
	header, err := decodePiSessionHeader([]byte(headerText))
	if err != nil {
		t.Fatal(err)
	}
	const prompt = "terminal state prompt"
	tests := []struct {
		name       string
		stopReason string
		text       string
		state      piEvidenceState
	}{
		{name: "stop", stopReason: "stop", text: "result", state: piEvidenceComplete},
		{name: "tool use", stopReason: "toolUse", text: "intermediate", state: piEvidenceIncomplete},
		{name: "length", stopReason: "length", text: "truncated", state: piEvidenceIncomplete},
		{name: "error", stopReason: "error", text: "failed", state: piEvidenceIncomplete},
		{name: "aborted", stopReason: "aborted", text: "cancelled", state: piEvidenceIncomplete},
		{name: "empty", stopReason: "stop", text: "", state: piEvidenceIncomplete},
		{name: "whitespace", stopReason: "stop", text: "  \n", state: piEvidenceIncomplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := strictPiSessionFile(headerText, prompt, test.text, "00000001", "00000002")
			file = strings.Replace(file, `"stopReason":"stop"`, `"stopReason":"`+test.stopReason+`"`, 1)
			document, err := parsePiSessionDocument([]byte(file))
			if err != nil {
				t.Fatal(err)
			}
			observed := &Session{value: header.ID, header: header, hasHeader: true, hasCursor: true}
			evidence := verifyPiInvocationSuffix(Invocation{prompt: prompt}, document, observed)
			if evidence.state != test.state {
				t.Fatalf("terminal state=%d, want %d", evidence.state, test.state)
			}
		})
	}
}

func TestPiSessionDecoderRejectsDuplicateFieldsAndInvalidTree(t *testing.T) {
	header := `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "duplicate field", body: header + `
{"type":"message","id":"00000001","id":"00000002","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":"prompt"}]}}`},
		{name: "UUID entry ID", body: header + `
{"type":"message","id":"0192f7e5-7b2a-7c01-8a11-223344556677","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":"prompt"}]}}`},
		{name: "missing parent", body: header + `
{"type":"message","id":"00000001","parentId":"00000002","timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":"prompt"}]}}`},
		{name: "cycle", body: header + `
{"type":"custom","id":"00000001","parentId":"00000002","timestamp":"2026-08-27T13:15:30.124Z"}
{"type":"custom","id":"00000002","parentId":"00000001","timestamp":"2026-08-27T13:15:30.125Z"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parsePiSessionDocument([]byte(test.body + "\n")); err == nil {
				t.Fatal("malformed Session was accepted")
			}
		})
	}
}

func TestPiSessionDecoderRejectsIncompleteFinalLine(t *testing.T) {
	header := `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
	content := header + `
{"type":"custom","id":"00000001","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z"}`
	if _, err := parsePiSessionDocument([]byte(content)); err == nil {
		t.Fatal("Session with an incomplete final line was accepted")
	}
}

func TestPiSessionDecoderRejectsInvalidUTF8(t *testing.T) {
	content := []byte(`{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}` + "\n")
	content[len(content)-2] = 0xff
	if _, err := parsePiSessionDocument(content); err == nil {
		t.Fatal("Session with invalid UTF-8 was accepted")
	}
}
func TestPiSessionDecoderKeepsOpaqueLeafBeforeAssistantCheck(t *testing.T) {
	header := `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
	content := header + "\n" +
		`{"type":"message","id":"00000001","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":"prompt"}]}}` + "\n" +
		`{"type":"message","id":"00000002","parentId":"00000001","timestamp":"2026-08-27T13:15:30.125Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"result"}]}}` + "\n" +
		`{"type":"custom","id":"00000003","parentId":"00000002","timestamp":"2026-08-27T13:15:30.126Z","data":{"opaque":true}}` + "\n"
	document, err := parsePiSessionDocument([]byte(content))
	if err != nil || document.leaf == nil || document.leaf.id != "00000003" || document.leaf.message != nil {
		t.Fatalf("opaque Session leaf=%+v error=%v", document.leaf, err)
	}
}
func TestPiAssistantTextBlocksAreConcatenatedWithoutSeparators(t *testing.T) {
	message, err := decodePiSessionMessage([]byte(`{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"left"},{"type":"text","text":"right"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if message.text != "leftright" {
		t.Fatalf("assistant text=%q, want concatenated blocks", message.text)
	}
}

func verifiedPiTestSession(t *testing.T, cursor string) *Session {
	t.Helper()
	header, err := decodePiSessionHeader([]byte(`{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`))
	if err != nil {
		t.Fatal(err)
	}
	return &Session{value: header.ID, header: header, hasHeader: true, cursor: cursor, hasCursor: true}
}

func newStrictPiInvocation(t *testing.T, prompt string) (AgentAdapter, Invocation) {
	t.Helper()
	adapter, err := NewAgentAdapter(WorkerConfig{
		Name: "strict-pi", Type: WorkerTypePi,
		Env: map[string]string{"PI_MODEL": "model", "PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskExplore, Phase: PhaseExplore, Prompt: prompt,
		Input: InputReference{Kind: InputBlackboardFile, Path: rundomain.BlackboardPath("worker-test-run", rundomain.PhaseExplore)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, invocation
}

func strictPiPrompt(invocation Invocation) string {
	argv := invocation.Argv()
	if len(argv) == 0 {
		return ""
	}
	return argv[len(argv)-1]
}

func strictPiPrelude(cwd string) string {
	return fmt.Sprintf(`{"schema":"%s","cwd":%q}`, piCwdPreludeSchema, cwd)
}

func strictPiHeader(invocation Invocation) string {
	return `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
}

func strictPiLifecycle(header, final string) string {
	message := `{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":` + fmt.Sprintf("%q", final) + `}]}`
	return strictPiPrelude("/workspace") + "\n" + header + "\n" +
		`{"type":"agent_start"}` + "\n" +
		`{"type":"message_end","message":` + message + "}\n" +
		`{"type":"turn_end","message":` + message + "}\n" +
		`{"type":"agent_end","messages":[` + message + "]}\n"
}

func strictPiSessionFile(header, prompt, final, userID, assistantID string) string {
	return header + "\n" +
		`{"type":"message","id":"` + userID + `","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":` + fmt.Sprintf("%q", prompt) + `}]}}` + "\n" +
		`{"type":"message","id":"` + assistantID + `","parentId":"` + userID + `","timestamp":"2026-08-27T13:15:30.125Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":` + fmt.Sprintf("%q", final) + `}]}}` + "\n"
}

func strictPiIncompleteSessionFile(header, prompt string) string {
	return header + "\n" +
		`{"type":"message","id":"00000001","parentId":null,"timestamp":"2026-08-27T13:15:30.124Z","message":{"role":"user","content":[{"type":"text","text":` + fmt.Sprintf("%q", prompt) + `}]}}` + "\n" +
		`{"type":"message","id":"00000002","parentId":"00000001","timestamp":"2026-08-27T13:15:30.125Z","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"toolCall","name":"read"}]}}` + "\n" +
		`{"type":"message","id":"00000003","parentId":"00000002","timestamp":"2026-08-27T13:15:30.126Z","message":{"role":"toolResult","content":[{"type":"text","text":"observed artifact"}]}}` + "\n"
}

func strictPiContinuedSessionFile(header, firstPrompt, firstFinal, secondPrompt, secondFinal string) string {
	return strictPiSessionFile(header, firstPrompt, firstFinal, "00000001", "00000002") +
		`{"type":"message","id":"00000003","parentId":"00000002","timestamp":"2026-08-27T13:15:30.126Z","message":{"role":"user","content":[{"type":"text","text":` + fmt.Sprintf("%q", secondPrompt) + `}]}}` + "\n" +
		`{"type":"message","id":"00000004","parentId":"00000003","timestamp":"2026-08-27T13:15:30.127Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":` + fmt.Sprintf("%q", secondFinal) + `}]}}` + "\n"
}
