package worker

import (
	"slices"

	"github.com/google/uuid"
)

// ClaudeCodeDriver constructs commands for the Claude Code CLI.
type ClaudeCodeDriver struct{}

func NewClaudeCodeDriver() *ClaudeCodeDriver { return &ClaudeCodeDriver{} }

func (d *ClaudeCodeDriver) PrepareSession() *string {
	s := uuid.New().String()
	return &s
}

func (d *ClaudeCodeDriver) BuildExecute(w WorkerConfig, prompt string, session *string) DriverResult {
	sid := ""
	if session != nil {
		sid = *session
	}
	return DriverResult{
		Argv:    []string{"claude", "--session-id", sid, "--dangerously-skip-permissions", "-p", "--", prompt},
		Session: session,
	}
}

func (d *ClaudeCodeDriver) BuildConclude(w WorkerConfig, prompt string, session string) []string {
	return []string{
		"claude", "-r", session, "--dangerously-skip-permissions", "--tools=", "-p", "--", prompt,
	}
}

func (d *ClaudeCodeDriver) BuildRecovery(w WorkerConfig, prompt, session string, phase Phase) []string {
	if phase == PhaseExploreConcludeRecovery || phase == PhaseBootstrapConcludeRecovery {
		return d.BuildConclude(w, prompt, session)
	}
	return []string{"claude", "-r", session, "--dangerously-skip-permissions", "-p", "--", prompt}
}

func (d *ClaudeCodeDriver) addStructuredOutput(argv []string, schema string) []string {
	result := append([]string(nil), argv...)
	printIndex := slices.Index(result, "-p")
	if printIndex < 0 {
		return nil
	}
	options := []string{"--output-format", "json", "--json-schema", schema}
	result = append(result, make([]string, len(options))...)
	copy(result[printIndex+len(options):], result[printIndex:len(result)-len(options)])
	copy(result[printIndex:], options)
	return result
}

func (d *ClaudeCodeDriver) ResultEvidence() resultEvidenceModule { return claudeResultEvidence{} }

var _ resultEvidenceProvider = (*ClaudeCodeDriver)(nil)
