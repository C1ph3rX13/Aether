package worker

import (
	"fmt"
	"regexp"
	"slices"
)

// CodexDriver constructs commands for the Codex CLI.
type CodexDriver struct{}

var codexSessionPattern = regexp.MustCompile(`(?i)session id:\s*([0-9a-f-]+)`)

func NewCodexDriver() *CodexDriver { return &CodexDriver{} }

func (d *CodexDriver) BuildExecute(w WorkerConfig, prompt string, session *string) DriverResult {
	argv := []string{
		"codex", "exec",
	}
	if session != nil && *session != "" {
		argv = append(argv, "resume", *session)
	}
	argv = append(argv, codexProviderArgs(w)...)
	argv = append(argv, "--", prompt)
	return DriverResult{
		Argv:    argv,
		Session: session,
	}
}

func (d *CodexDriver) BuildConclude(w WorkerConfig, prompt string, session string) []string {
	argv := []string{
		"codex", "exec", "resume", session,
	}
	argv = append(argv, codexProviderArgs(w)...)
	argv = append(argv, "--", prompt)
	return argv
}

func (d *CodexDriver) BuildRecovery(w WorkerConfig, prompt, session string, phase Phase) []string {
	return d.BuildConclude(w, prompt, session)
}

func codexProviderArgs(w WorkerConfig) []string {
	env := w.Env
	return []string{
		"--dangerously-bypass-approvals-and-sandbox",
		"--model", env["CODEX_MODEL"],
		"-c", `model_provider="aether"`,
		"-c", `model_providers.aether.name="aether"`,
		"-c", `model_providers.aether.wire_api="responses"`,
		"-c", fmt.Sprintf(`model_providers.aether.base_url=%q`, env["CODEX_BASE_URL"]),
		"-c", `model_providers.aether.env_key="OPENAI_API_KEY"`,
	}
}

func (d *CodexDriver) ExtractSession(session *string, stdout, stderr string) *string {
	if session != nil && *session != "" {
		return session
	}
	match := codexSessionPattern.FindStringSubmatch(stderr)
	if len(match) != 2 {
		return nil
	}
	return &match[1]
}

func (d *CodexDriver) addSchemaFile(argv []string, path string) []string {
	index := slices.Index(argv, "--")
	if index < 0 {
		return nil
	}
	result := append([]string(nil), argv[:index]...)
	result = append(result, "--output-schema", path)
	return append(result, argv[index:]...)
}

func (d *CodexDriver) ResultEvidence() resultEvidenceModule { return codexResultEvidence{} }

var _ resultEvidenceProvider = (*CodexDriver)(nil)
