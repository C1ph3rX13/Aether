package worker

import (
	"embed"
	"fmt"
	"slices"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

const maxPromptTemplateBytes = 256 * 1024

var requiredPromptTemplates = []string{
	"bootstrap.md",
	"bootstrap_conclude.md",
	"bootstrap_finalization.md",
	"reason.md",
	"reason_finalization.md",
	"explore.md",
	"explore_conclude.md",
	"explore_finalization.md",
	"explore_recovery.md",
	"explore_conclude_recovery.md",
	"bootstrap_recovery.md",
	"bootstrap_conclude_recovery.md",
}

var requiredPromptPlaceholders = map[string][]string{
	"reason.md":                      {"{fact_ids}", "{open_intents}", "{max_intents}", "{hints}"},
	"reason_finalization.md":         {"{fact_ids}", "{open_intents}", "{max_intents}", "{hints}"},
	"explore.md":                     {"{intent_id}", "{intent_description}"},
	"explore_conclude.md":            {"{intent_id}", "{intent_description}"},
	"explore_finalization.md":        {"{intent_id}", "{intent_description}"},
	"explore_recovery.md":            {"{intent_id}", "{intent_description}"},
	"explore_conclude_recovery.md":   {"{intent_id}", "{intent_description}"},
	"bootstrap.md":                   {"{origin}", "{goal}", "{hints}"},
	"bootstrap_conclude.md":          {"{intent_id}", "{origin}", "{goal}", "{hints}"},
	"bootstrap_finalization.md":      {"{intent_id}", "{origin}", "{goal}", "{hints}"},
	"bootstrap_recovery.md":          {"{intent_id}", "{origin}", "{goal}", "{hints}"},
	"bootstrap_conclude_recovery.md": {"{intent_id}", "{origin}", "{goal}", "{hints}"},
}

var bundledPromptGroupNames = []string{"default", "mock"}

// BundledPromptGroups returns the supported read-only Prompt Group names in
// their stable display order. Configuration clients use these values to build
// an explicit selector instead of guessing from a current Draft value.
func BundledPromptGroups() []string {
	return append([]string(nil), bundledPromptGroupNames...)
}

//go:embed prompts/default/*.md prompts/mock/*.md
var bundledPromptFiles embed.FS

func ValidatePromptGroup(group string) error {
	_, err := bundledCatalogPrompts(group)
	return err
}

func bundledCatalogPrompts(group string) (map[rundomain.TaskType]PromptSet, error) {
	groups, err := loadBundledPromptGroups()
	if err != nil {
		return nil, err
	}
	prompts, ok := groups[group]
	if !ok {
		return nil, fmt.Errorf(
			"runtime.prompt_group %q is unknown (available: %s)",
			group,
			strings.Join(bundledPromptGroupNames, ", "),
		)
	}
	return prompts, nil
}

func loadBundledPromptGroups() (map[string]map[rundomain.TaskType]PromptSet, error) {
	groups := make(map[string]map[rundomain.TaskType]PromptSet, len(bundledPromptGroupNames))
	for _, group := range bundledPromptGroupNames {
		templates, err := readBundledPromptTemplates(group)
		if err != nil {
			return nil, err
		}
		if err := validatePromptTemplates(group, templates); err != nil {
			return nil, err
		}
		groups[group] = map[rundomain.TaskType]PromptSet{
			rundomain.TaskReason: {
				Primary: templates["reason.md"], Finalization: templates["reason_finalization.md"],
			},
			rundomain.TaskExplore: {
				Primary: templates["explore.md"], Fallback: templates["explore_conclude.md"],
				RecoveryPrimary: templates["explore_recovery.md"], RecoveryConclude: templates["explore_conclude_recovery.md"],
				Finalization: templates["explore_finalization.md"],
			},
			rundomain.TaskBootstrap: {
				Primary: templates["bootstrap.md"], Fallback: templates["bootstrap_conclude.md"],
				RecoveryPrimary: templates["bootstrap_recovery.md"], RecoveryConclude: templates["bootstrap_conclude_recovery.md"],
				Finalization: templates["bootstrap_finalization.md"],
			},
		}
	}
	return groups, nil
}

func readBundledPromptTemplates(group string) (map[string]string, error) {
	if !slices.Contains(bundledPromptGroupNames, group) {
		return nil, fmt.Errorf(
			"runtime.prompt_group %q is unknown (available: %s)",
			group,
			strings.Join(bundledPromptGroupNames, ", "),
		)
	}
	directory := "prompts/" + group
	entries, err := bundledPromptFiles.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("runtime.prompt_group %q: read bundled resources: %w", group, err)
	}
	templates := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("runtime.prompt_group %q: unexpected directory %s", group, entry.Name())
		}
		content, err := bundledPromptFiles.ReadFile(directory + "/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("runtime.prompt_group %q: read bundled template %s: %w", group, entry.Name(), err)
		}
		templates[entry.Name()] = string(content)
	}
	return templates, nil
}

func validatePromptTemplates(group string, templates map[string]string) error {
	var errors []string
	for _, name := range requiredPromptTemplates {
		content, ok := templates[name]
		if !ok {
			errors = append(errors, fmt.Sprintf("missing template %s", name))
			continue
		}
		if strings.TrimSpace(content) == "" {
			errors = append(errors, fmt.Sprintf("%s is empty", name))
			continue
		}
		if len(content) > maxPromptTemplateBytes {
			errors = append(errors, fmt.Sprintf("%s exceeds %d bytes", name, maxPromptTemplateBytes))
			continue
		}
		if strings.Contains(content, "{graph_yaml}") {
			errors = append(errors, fmt.Sprintf("%s: {graph_yaml} is forbidden; Reason/Explore read the per-Run Blackboard file", name))
			continue
		}
		var missing []string
		for _, token := range requiredPromptPlaceholders[name] {
			if !strings.Contains(content, token) {
				missing = append(missing, token)
			}
		}
		if len(missing) > 0 {
			errors = append(errors, fmt.Sprintf("%s: missing placeholders %s", name, strings.Join(missing, ", ")))
		}
	}
	for name := range templates {
		if !slices.Contains(requiredPromptTemplates, name) {
			errors = append(errors, fmt.Sprintf("unexpected template %s", name))
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("runtime.prompt_group %q: %s", group, strings.Join(errors, "; "))
	}
	return nil
}
