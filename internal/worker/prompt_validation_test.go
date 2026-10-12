package worker

import (
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestBundledPromptGroupsAreCompleteAndMapped(t *testing.T) {
	groups, err := loadBundledPromptGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != len(bundledPromptGroupNames) {
		t.Fatalf("bundled groups = %d, want %d", len(groups), len(bundledPromptGroupNames))
	}
	for _, group := range bundledPromptGroupNames {
		prompts := groups[group]
		if len(prompts) != 3 {
			t.Fatalf("%s task mapping = %#v", group, prompts)
		}
		if strings.TrimSpace(prompts[rundomain.TaskReason].Primary) == "" || prompts[rundomain.TaskReason].Fallback != "" ||
			strings.TrimSpace(prompts[rundomain.TaskReason].Finalization) == "" {
			t.Fatalf("%s Reason mapping = %#v", group, prompts[rundomain.TaskReason])
		}
		for _, task := range []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskBootstrap} {
			if strings.TrimSpace(prompts[task].Primary) == "" || strings.TrimSpace(prompts[task].Fallback) == "" ||
				strings.TrimSpace(prompts[task].Finalization) == "" {
				t.Fatalf("%s %s mapping = %#v", group, task, prompts[task])
			}
		}
	}
}

func TestSharedTaskTemplatesDoNotContainAdapterSubmissionEnvelope(t *testing.T) {
	defaultTemplates, err := readBundledPromptTemplates("default")
	if err != nil {
		t.Fatal(err)
	}
	mockTemplates, err := readBundledPromptTemplates("mock")
	if err != nil {
		t.Fatal(err)
	}
	for name, prompt := range defaultTemplates {
		if prompt != mockTemplates[name] {
			t.Fatalf("default and mock task template %s diverged", name)
		}
		for _, forbidden := range []string{
			"aether.run-result/v1",
			"accepted/data", "JSON RunResult", "Escape quotes, backslashes",
			"result.toml", "[outcome]", "TOML", "--output-schema", "--json-schema",
		} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("%s contains Adapter submission instruction %q", name, forbidden)
			}
		}
	}
}

func TestMockEffectivePromptAddsOnlyOutcomeSubmissionContract(t *testing.T) {
	adapter, err := NewAgentAdapter(WorkerConfig{Name: "mock", Type: WorkerTypeMock})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "decide",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}, MaxProposedIntents: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := invocation.prompt
	if !strings.Contains(prompt, "JSON Schema for this invocation") || !strings.Contains(prompt, `"propose_intents"`) ||
		!strings.Contains(prompt, "outcome") || !strings.Contains(prompt, OutcomeSubmissionSchemaV1) ||
		strings.Contains(prompt, ResultOutputContractSchemaV2) {
		t.Fatalf("Mock Adapter submission contract is not isolated: %s", prompt)
	}
	explore, err := adapter.Prepare(AgentRequest{
		Task: rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "explore",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	exploreSchema := explore.prompt
	if strings.Contains(exploreSchema, `"propose_intents"`) || !strings.Contains(exploreSchema, `"conclude_intent"`) {
		t.Fatalf("Explore schema is not task constrained: %s", exploreSchema)
	}
}

func TestBundledFinalizationPromptsPreserveTaskOutcomeContracts(t *testing.T) {
	for _, group := range bundledPromptGroupNames {
		templates, err := readBundledPromptTemplates(group)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"reason_finalization.md", "explore_finalization.md", "bootstrap_finalization.md"} {
			prompt := templates[name]
			if !strings.Contains(prompt, "Outcome-Constrained Completion Phase") ||
				!strings.Contains(strings.ToLower(prompt), "phase deadline") {
				t.Fatalf("%s/%s does not preserve the bounded Finalization contract", group, name)
			}
		}
		if !strings.Contains(templates["reason_finalization.md"], "propose_intents") ||
			!strings.Contains(templates["reason_finalization.md"], "{max_intents}") {
			t.Fatalf("%s Reason Finalization lost the admitted Reason outcomes", group)
		}
		for _, name := range []string{"explore_finalization.md", "bootstrap_finalization.md"} {
			if !strings.Contains(templates[name], "conclude_intent") || !strings.Contains(templates[name], "decline") ||
				strings.Contains(templates[name], "complete_project") {
				t.Fatalf("%s/%s widened the admitted completion outcomes", group, name)
			}
		}
	}
}

func TestBundledPiContinuationPromptsKeepToolsAndBoundedPhaseContracts(t *testing.T) {
	for _, group := range bundledPromptGroupNames {
		templates, err := readBundledPromptTemplates(group)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{
			"explore_recovery.md", "bootstrap_recovery.md",
			"explore_conclude_recovery.md", "bootstrap_conclude_recovery.md",
		} {
			prompt := templates[name]
			lower := strings.ToLower(prompt)
			for _, prohibition := range []string{"without tools", "do not use tools", "must not use tools", "tools are unavailable", "--no-tools"} {
				if strings.Contains(lower, prohibition) {
					t.Fatalf("%s/%s contains tool prohibition %q", group, name, prohibition)
				}
			}
			if !strings.Contains(lower, "phase deadline") {
				t.Fatalf("%s/%s does not preserve the bounded phase deadline", group, name)
			}
			if !strings.Contains(lower, "overrides any earlier instruction") {
				t.Fatalf("%s/%s does not state its phase override", group, name)
			}
		}
	}

	defaultTemplates, err := readBundledPromptTemplates("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"explore_conclude.md", "bootstrap_conclude.md", "explore_conclude_recovery.md", "bootstrap_conclude_recovery.md"} {
		prompt := defaultTemplates[name]
		if !strings.Contains(prompt, "Outcome-Constrained Completion Phase") ||
			!strings.Contains(prompt, "conclude_intent") || !strings.Contains(prompt, "decline") {
			t.Fatalf("%s does not preserve completion outcome constraints", name)
		}
	}
	if !strings.Contains(defaultTemplates["explore_conclude.md"], "{intent_id}") ||
		!strings.Contains(defaultTemplates["explore_conclude.md"], "overrides any earlier instruction") {
		t.Fatal("Explore conclude Prompt lost target Intent or phase override")
	}
	for _, name := range []string{"explore_recovery.md", "explore_conclude_recovery.md"} {
		if !strings.Contains(defaultTemplates[name], "{intent_id}") || !strings.Contains(defaultTemplates[name], "{intent_description}") {
			t.Fatalf("%s lost the target Explore Intent", name)
		}
	}
	for _, name := range []string{"bootstrap_conclude.md", "bootstrap_recovery.md", "bootstrap_conclude_recovery.md"} {
		for _, placeholder := range []string{"{intent_id}", "{origin}", "{goal}", "{hints}"} {
			if !strings.Contains(defaultTemplates[name], placeholder) {
				t.Fatalf("%s lost Bootstrap scope placeholder %s", name, placeholder)
			}
		}
	}
	if !strings.Contains(defaultTemplates["bootstrap_conclude.md"], "Do not output `complete_project`") {
		t.Fatal("Bootstrap conclude Prompt permits complete_project")
	}
}

func primaryPhase(task rundomain.TaskType) Phase {
	switch task {
	case rundomain.TaskReason:
		return PhaseReason
	case rundomain.TaskExplore:
		return PhaseExplore
	default:
		return PhaseBootstrap
	}
}

func testInputForTask(task rundomain.TaskType) InputReference {
	if task == rundomain.TaskBootstrap {
		return InputReference{Kind: InputBootstrapInline, Inline: "origin: start\ngoal: finish"}
	}
	return InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath}
}

func maxProposedIntentsForTest(task rundomain.TaskType) int {
	if task == rundomain.TaskReason {
		return 3
	}
	return 0
}

func TestValidatePromptTemplatesRejectsMissingPlaceholder(t *testing.T) {
	templates, err := readBundledPromptTemplates("default")
	if err != nil {
		t.Fatal(err)
	}
	templates["reason.md"] = strings.ReplaceAll(templates["reason.md"], "{max_intents}", "max intents")
	err = validatePromptTemplates("default", templates)
	if err == nil || !strings.Contains(err.Error(), "reason.md") || !strings.Contains(err.Error(), "{max_intents}") {
		t.Fatalf("missing placeholder error = %v", err)
	}
}

func TestValidatePromptTemplatesRejectsInlineBlackboardToken(t *testing.T) {
	templates, err := readBundledPromptTemplates("default")
	if err != nil {
		t.Fatal(err)
	}
	templates["reason.md"] += "\n{graph_yaml}"
	if err := validatePromptTemplates("default", templates); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("inline Blackboard error = %v", err)
	}
}

func TestValidatePromptTemplatesRejectsMissingAndUnexpectedFiles(t *testing.T) {
	templates, err := readBundledPromptTemplates("mock")
	if err != nil {
		t.Fatal(err)
	}
	delete(templates, "reason.md")
	templates["extra.md"] = "unexpected"
	err = validatePromptTemplates("mock", templates)
	if err == nil || !strings.Contains(err.Error(), "missing template reason.md") || !strings.Contains(err.Error(), "unexpected template extra.md") {
		t.Fatalf("template set error = %v", err)
	}
}

func TestValidatePromptTemplatesRejectsEmptyAndOversizedTemplates(t *testing.T) {
	templates, err := readBundledPromptTemplates("default")
	if err != nil {
		t.Fatal(err)
	}
	templates["reason.md"] = ""
	templates["explore.md"] = strings.Repeat("x", maxPromptTemplateBytes+1)
	err = validatePromptTemplates("default", templates)
	if err == nil || !strings.Contains(err.Error(), "reason.md is empty") || !strings.Contains(err.Error(), "explore.md exceeds") {
		t.Fatalf("template size error = %v", err)
	}
}

func TestValidatePromptGroupRejectsUnknownGroup(t *testing.T) {
	if err := ValidatePromptGroup("custom"); err == nil || !strings.Contains(err.Error(), "available: default, mock") {
		t.Fatalf("unknown group error = %v", err)
	}
}
