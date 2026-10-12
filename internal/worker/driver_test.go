package worker

import (
	"slices"
	"strings"
	"testing"
)

func makeClaudeCodeWorker() WorkerConfig {
	return WorkerConfig{
		Name:       "claudecode_test",
		Type:       WorkerTypeClaudeCode,
		TaskTypes:  []TaskType{"bootstrap", "reason", "explore"},
		MaxRunning: 2,
		Priority:   0,
		Env: map[string]string{
			"ANTHROPIC_MODEL":      "deepseek-v3",
			"ANTHROPIC_BASE_URL":   "https://api.deepseek.com/anthropic",
			"ANTHROPIC_AUTH_TOKEN": "sk-test",
		},
	}
}

func TestClaudeCodeBuildExecute(t *testing.T) {
	d := NewClaudeCodeDriver()
	w := makeClaudeCodeWorker()
	session := "abc123"
	result := d.BuildExecute(w, "test prompt", &session)

	if result.Argv[0] != "claude" {
		t.Errorf("first arg: got %s, want claude", result.Argv[0])
	}
	if result.Argv[2] != "abc123" {
		t.Errorf("session arg: got %s, want abc123", result.Argv[2])
	}
	if *result.Session != "abc123" {
		t.Errorf("session: got %s", *result.Session)
	}
}

func TestClaudeCodeBuildConclude(t *testing.T) {
	d := NewClaudeCodeDriver()
	w := makeClaudeCodeWorker()
	argv := d.BuildConclude(w, "conclude prompt", "abc123")

	if argv[0] != "claude" {
		t.Errorf("first arg: got %s, want claude", argv[0])
	}
	if argv[1] != "-r" {
		t.Errorf("second arg: got %s, want -r", argv[1])
	}
	if argv[2] != "abc123" {
		t.Errorf("session arg: got %s, want abc123", argv[2])
	}
	if slices.Contains(argv, "") || !slices.Contains(argv, "--tools=") {
		t.Fatalf("Claude conclude command must disable tools without an empty argv: %v", argv)
	}
}

func TestCodexBuildExecute(t *testing.T) {
	d := NewCodexDriver()
	w := WorkerConfig{
		Name: "codex_test", Type: WorkerTypeCodex,
		Env: map[string]string{
			"CODEX_MODEL":    "qwen3.6-plus",
			"CODEX_BASE_URL": "https://dashscope.aliyuncs.com/compatible-mode/v1",
			"OPENAI_API_KEY": "sk-test",
		},
	}
	result := d.BuildExecute(w, "test prompt", nil)

	if result.Argv[0] != "codex" {
		t.Errorf("first arg: got %s, want codex", result.Argv[0])
	}
	if result.Argv[1] != "exec" {
		t.Errorf("second arg: got %s, want exec", result.Argv[1])
	}
	if slices.Contains(result.Argv, "--quiet") || !slices.Contains(result.Argv, `model_provider="aether"`) {
		t.Fatalf("Codex command does not use the controlled provider contract: %v", result.Argv)
	}
}

func TestCodexBuildConcludeUsesResumeSubcommand(t *testing.T) {
	d := NewCodexDriver()
	w := WorkerConfig{Env: map[string]string{
		"CODEX_MODEL": "model", "CODEX_BASE_URL": "https://example.invalid", "OPENAI_API_KEY": "secret",
	}}
	argv := d.BuildConclude(w, "conclude prompt", "session-id")
	if len(argv) < 5 || strings.Join(argv[:4], " ") != "codex exec resume session-id" {
		t.Fatalf("Codex conclude command: %v", argv)
	}
	if strings.Contains(strings.Join(argv, "\n"), "secret") {
		t.Fatal("Codex API key leaked into process arguments")
	}
}

func TestPiBuildConcludeResumesCapturedSessionInWorkerDirectory(t *testing.T) {
	driver := NewPiDriver()
	worker := WorkerConfig{Name: "pi/worker", Env: map[string]string{
		"PI_MODEL": "model", "PI_BASE_URL": "https://example.invalid",
		"PI_PROVIDER_API": "openai-completions", "PI_API_KEY": "secret",
	}}
	result := driver.BuildExecute(worker, "primary", nil)
	joined := strings.Join(result.Argv, "\n")
	if result.Session != nil || !strings.Contains(joined, "/tmp/aether-pi/pi-worker-") ||
		strings.Contains(joined, "/tmp/aether-pi/pi/worker") || strings.Contains(joined, "secret") {
		t.Fatalf("Pi primary command does not isolate its worker session safely: session=%v argv=%v", result.Session, result.Argv)
	}
	session := "0192f7e5-7b2a-7c01-8a11-223344556677"
	conclude := driver.BuildConclude(worker, "conclude", session)
	if !slices.Contains(conclude, "--session") || !slices.Contains(conclude, "0192f7e5-7b2a-7c01-8a11-223344556677") ||
		!slices.Contains(conclude, "--tools") || slices.Contains(conclude, "--no-tools") {
		t.Fatalf("Pi conclude command does not resume its session with full tools: %v", conclude)
	}
}

func TestPiBuildExecuteDisablesThinkingForBoundedRun(t *testing.T) {
	driver := NewPiDriver()
	worker := WorkerConfig{Type: WorkerTypePi, Name: "pi_bounded", Env: map[string]string{
		"PI_MODEL": "model", "PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions",
	}}
	argv := driver.BuildExecute(worker, "bounded", nil).Argv
	thinkingIndex := slices.Index(argv, "--thinking")
	if thinkingIndex < 0 || thinkingIndex+1 >= len(argv) || argv[thinkingIndex+1] != "off" {
		t.Fatalf("Pi command must disable unbounded thinking: %v", argv)
	}
}

func TestDefinitionsComposeDriverCapabilities(t *testing.T) {
	driverNames := []string{"claudecode", "codex", "pi", "mock"}
	for _, name := range driverNames {
		definition, err := LookupAdapterDefinition(name)
		if err != nil {
			t.Errorf("get Adapter definition %s: %v", name, err)
		}
		d := definition.driver
		if d.TypeName() != name {
			t.Errorf("driver type name: got %s, want %s", d.TypeName(), name)
		}
	}
}

func TestDefinitionsComposeOptionalCapabilities(t *testing.T) {
	piDefinition, err := LookupAdapterDefinition("pi")
	if err != nil {
		t.Fatal(err)
	}
	pi := piDefinition.driver
	if pi.resultEvidence == nil {
		t.Fatal("Pi evidence must be provided by its Result Evidence module")
	}
	if !pi.hasConclude() {
		t.Fatal("Pi must expose conclude capability")
	}
	if pi.resultEvidence == nil {
		t.Fatal("Pi must expose session capture capability")
	}
	if pi.prepareSession != nil {
		t.Fatal("Pi must not expose session preparation capability")
	}

	claudeDefinition, err := LookupAdapterDefinition("claudecode")
	if err != nil {
		t.Fatal(err)
	}
	claude := claudeDefinition.driver
	if !claude.hasConclude() || claude.resultEvidence == nil || claude.prepareSession == nil {
		t.Fatal("Claude Code must expose conclude and session capabilities")
	}
	codexDefinition, err := LookupAdapterDefinition("codex")
	if err != nil {
		t.Fatal(err)
	}
	codex := codexDefinition.driver
	if codex.prepareSession != nil {
		t.Fatal("Codex must not expose session preparation capability")
	}
}

func TestDefinitionsRejectUnknownDriver(t *testing.T) {
	_, err := LookupAdapterDefinition("unknown")
	if err == nil {
		t.Error("expected error for unknown driver")
	}
}
