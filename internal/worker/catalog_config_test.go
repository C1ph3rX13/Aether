package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestCatalogSourceFromConfigPreservesEnvironmentAndLoadsBundledPrompts(t *testing.T) {
	secret := "do-not-copy-this-secret"
	cfg := catalogConfigForTest(secret)
	cfg.Workers[0].SecretVersionIDs = map[string]string{"ANTHROPIC_AUTH_TOKEN": "DEEPSEEK_API_KEY"}
	cfg.Workers[1].SecretVersionIDs = map[string]string{"OPENAI_API_KEY": "QWEN_API_KEY"}
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if source.ImageReference != cfg.ImageReference {
		t.Fatalf("image = %q, want %q", source.ImageReference, cfg.ImageReference)
	}
	if got := source.Prompts[rundomain.TaskBootstrap].Fallback; !strings.Contains(got, "conclude phase") {
		t.Fatalf("bundled bootstrap fallback was not loaded: %q", got)
	}
	if got := source.Prompts[rundomain.TaskExplore].Fallback; !strings.Contains(got, "conclude phase") {
		t.Fatalf("bundled explore fallback was not loaded: %q", got)
	}
	claude := source.Workers[0]
	if claude.Environment["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "390000" {
		t.Fatal("Claude worker did not preserve an Agent environment variable")
	}
	if _, ok := claude.Environment["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatal("Claude secret was copied into environment")
	}
	if len(claude.SecretReferences) != 1 || claude.SecretReferences[0].Key != "DEEPSEEK_API_KEY" {
		t.Fatalf("Claude secrets = %#v", claude.SecretReferences)
	}
	adapter, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	claudeAdapter, err := adapter.AgentAdapter("claude")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := claudeAdapter.Prepare(AgentRequest{
		Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := invocation.Env()["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; got != "390000" {
		t.Fatalf("Agent-specific environment was not passed to Invocation: %q", got)
	}
	if strings.Contains(stringifyCatalogSource(source), secret) {
		t.Fatal("secret value was retained in catalog source")
	}

	codex := source.Workers[1]
	if codex.Environment["CODEX_REASONING_EFFORT"] != "high" {
		t.Fatal("Codex worker did not preserve an Agent environment variable")
	}
	if codex.Model != "gpt-test" || codex.MaxRunning != 3 || codex.Priority != 7 {
		t.Fatalf("Codex mapping = %#v", codex)
	}
	if len(codex.SecretReferences) != 1 || codex.SecretReferences[0].Key != "QWEN_API_KEY" {
		t.Fatalf("Codex secrets = %#v", codex.SecretReferences)
	}
}

func TestNewCatalogFromConfigHasStableRevisionAndPromptHashes(t *testing.T) {
	cfg := catalogConfigForTest("secret")
	first, err := NewCatalogFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCatalogFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workers[0], cfg.Workers[1] = cfg.Workers[1], cfg.Workers[0]
	for index := range cfg.Workers {
		reverseDispatchTasks(cfg.Workers[index].TaskTypes)
	}
	reordered, err := NewCatalogFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision() == "" || first.Revision() != second.Revision() {
		t.Fatalf("revisions are not stable: %q vs %q", first.Revision(), second.Revision())
	}
	if first.Revision() != reordered.Revision() {
		t.Fatalf("revision depends on config order: %q vs %q", first.Revision(), reordered.Revision())
	}
	for _, task := range []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap} {
		firstHash, err := first.PromptTemplateHash(task)
		if err != nil {
			t.Fatal(err)
		}
		secondHash, err := second.PromptTemplateHash(task)
		if err != nil {
			t.Fatal(err)
		}
		if len(firstHash) != 64 || firstHash != strings.ToLower(firstHash) || firstHash != secondHash {
			t.Fatalf("%s prompt hash is not stable lowercase SHA-256: %q vs %q", task, firstHash, secondHash)
		}
	}
}

func TestCatalogConstructorsValidateExplicitRevision(t *testing.T) {
	cfg := catalogConfigForTest("secret")
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		revision string
		valid    bool
	}{
		{"whitespace", "   ", false},
		{"unicode whitespace", "\t\n\u2003", false},
		{"opaque identity", "config-v1", true},
		{"config identity", "sha256:" + strings.Repeat("a", 64), true},
		{"surrounding whitespace", " config-v1 ", true},
		{"256 bytes", strings.Repeat("a", 256), true},
		{"257 bytes", strings.Repeat("a", 257), false},
		{"unicode 256 bytes", strings.Repeat("界", 85) + "a", true},
		{"unicode 258 bytes", strings.Repeat("界", 86), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg.Revision = test.revision
			for _, constructor := range []struct {
				name  string
				build func() (*Catalog, error)
			}{
				{"explicit", func() (*Catalog, error) { return NewCatalogForRevision(test.revision, source) }},
				{"config", func() (*Catalog, error) { return NewCatalogFromConfig(cfg) }},
			} {
				t.Run(constructor.name, func(t *testing.T) {
					catalog, err := constructor.build()
					if !test.valid {
						if err == nil || catalog != nil {
							t.Fatalf("invalid %d-byte Revision: Catalog returned = %t, error = %v", len(test.revision), catalog != nil, err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if catalog.Revision() != test.revision {
						t.Fatalf("Revision = %q, want exact binding %q", catalog.Revision(), test.revision)
					}
				})
			}
		})
	}
}

func TestCatalogConfiguredRevisionIsIndependentOfContentAndPromptIdentity(t *testing.T) {
	cfg := catalogConfigForTest("secret")
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	contentCatalog, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := NewCatalogFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if unbound.Revision() != contentCatalog.Revision() {
		t.Fatalf("unspecified Revision = %q, want content identity %q", unbound.Revision(), contentCatalog.Revision())
	}
	if catalog, err := NewCatalogForRevision("", source); err == nil || catalog != nil {
		t.Fatalf("empty explicit Revision: Catalog returned = %t, error = %v", catalog != nil, err)
	}
	for _, revision := range []string{"config-v1", "config-v2"} {
		cfg.Revision = revision
		bound, err := NewCatalogFromConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if bound.Revision() != revision {
			t.Fatalf("Revision = %q, want binding %q", bound.Revision(), revision)
		}
		for _, task := range []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap} {
			want, err := contentCatalog.PromptTemplateHash(task)
			if err != nil {
				t.Fatal(err)
			}
			got, err := bound.PromptTemplateHash(task)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s Prompt Template Hash changed with binding %q: got %q, want %q", task, revision, got, want)
			}
		}
	}
}

func TestPromptTemplateHashIncludesSelectedPromptGroup(t *testing.T) {
	defaultCatalog, err := NewCatalogFromConfig(catalogConfigForTest("secret"))
	if err != nil {
		t.Fatal(err)
	}
	mockConfig := catalogConfigForTest("secret")
	mockConfig.PromptGroup = "mock"
	mockCatalog, err := NewCatalogFromConfig(mockConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap} {
		defaultHash, err := defaultCatalog.PromptTemplateHash(task)
		if err != nil {
			t.Fatal(err)
		}
		mockHash, err := mockCatalog.PromptTemplateHash(task)
		if err != nil {
			t.Fatal(err)
		}
		if defaultHash == mockHash {
			t.Fatalf("%s prompt hash does not identify its selected prompt group", task)
		}
	}
}

func TestCatalogSourceFromConfigSupportsBundledMockWithoutSecrets(t *testing.T) {
	cfg := catalogConfigForTest("secret")
	cfg.PromptGroup = "mock"
	cfg.Workers = []WorkerConfig{{
		Name: "mock", Type: WorkerTypeMock,
		TaskTypes: []TaskType{TaskTypeBootstrap}, MaxRunning: 1,
		Env: map[string]string{"MOCK_BOOTSTRAP": `{"outcomes":{"complete":1}}`},
	}}
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Workers) != 1 || len(source.Workers[0].SecretReferences) != 0 {
		t.Fatalf("mock worker = %#v", source.Workers)
	}
	if got := source.Workers[0].Environment["MOCK_BOOTSTRAP"]; got == "" {
		t.Fatal("mock behavior environment was not retained")
	}
	if got := source.Prompts[rundomain.TaskReason].Primary; !strings.Contains(got, "Current Active Hints") ||
		strings.Contains(got, "aether.run-result/v1") {
		t.Fatalf("bundled mock Prompt Group was not selected as a shared task template: %q", got)
	}
	if _, err := NewCatalog(source); err != nil {
		t.Fatalf("mock catalog is invalid: %v", err)
	}
}

func TestCatalogSourceFromConfigDoesNotAllowFilesystemPromptOverrides(t *testing.T) {
	groupDir := filepath.Join(t.TempDir(), "prompts", "default")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"reason.md":             "external override {fact_ids} {open_intents} {max_intents}",
		"explore.md":            "external override {intent_id} {intent_description}",
		"explore_conclude.md":   "external override {intent_id} {intent_description}",
		"bootstrap.md":          "external override {origin} {goal} {hints}",
		"bootstrap_conclude.md": "external override {origin} {goal} {hints}",
	} {
		if err := os.WriteFile(filepath.Join(groupDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Dir(filepath.Dir(groupDir)))
	cfg := catalogConfigForTest("secret")
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(source.Prompts[rundomain.TaskReason].Primary, "external override") {
		t.Fatal("filesystem content overrode the bundled default Prompt Group")
	}
}

func TestCatalogSourceFromConfigRejectsUnbundledPromptGroup(t *testing.T) {
	cfg := catalogConfigForTest("secret")
	cfg.PromptGroup = "custom"
	if _, err := CatalogSourceFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unbundled Prompt Group error = %v", err)
	}
}

func catalogConfigForTest(secret string) CatalogConfiguration {
	return CatalogConfiguration{
		PromptGroup:    "default",
		ImageReference: "registry.example/aether@sha256:" + strings.Repeat("a", 64),
		Workers: []WorkerConfig{
			{
				Name: "claude", Type: WorkerTypeClaudeCode,
				TaskTypes: []TaskType{TaskTypeReason}, MaxRunning: 2,
				Env: map[string]string{
					"IS_SANDBOX": "1", "ANTHROPIC_MODEL": "claude-test", "ANTHROPIC_BASE_URL": "https://claude.example",
					"ANTHROPIC_AUTH_TOKEN": secret, "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "390000",
				},
			},
			{
				Name: "codex", Type: WorkerTypeCodex,
				TaskTypes: []TaskType{TaskTypeReason, TaskTypeExplore}, MaxRunning: 3, Priority: 7,
				Env: map[string]string{
					"CODEX_MODEL": "gpt-test", "CODEX_BASE_URL": "https://codex.example",
					"OPENAI_API_KEY": secret, "CODEX_REASONING_EFFORT": "high",
				},
			},
		},
	}
}

func reverseDispatchTasks(tasks []TaskType) {
	for left, right := 0, len(tasks)-1; left < right; left, right = left+1, right-1 {
		tasks[left], tasks[right] = tasks[right], tasks[left]
	}
}

func stringifyCatalogSource(source CatalogSource) string {
	var builder strings.Builder
	for _, worker := range source.Workers {
		builder.WriteString(worker.Name)
		for key, value := range worker.Environment {
			builder.WriteString(key)
			builder.WriteString(value)
		}
		for _, reference := range worker.SecretReferences {
			builder.WriteString(reference.EnvironmentVariable)
			builder.WriteString(reference.Provider)
			builder.WriteString(reference.Key)
		}
	}
	return builder.String()
}
