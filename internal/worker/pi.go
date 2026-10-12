package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PiDriver constructs commands for the Pi CLI.
type PiDriver struct{}

func NewPiDriver() *PiDriver { return &PiDriver{} }

func (d *PiDriver) BuildExecute(w WorkerConfig, prompt string, session *string) DriverResult {
	return DriverResult{Argv: d.build(w, prompt, session), Session: session}
}

func (d *PiDriver) BuildConclude(w WorkerConfig, prompt, session string) []string {
	return d.build(w, prompt, &session)
}

func (d *PiDriver) BuildRecovery(w WorkerConfig, prompt, session string, phase Phase) []string {
	return d.build(w, prompt, &session)
}

func (d *PiDriver) ResultEvidence() resultEvidenceModule {
	return newPiResultEvidence()
}

func (d *PiDriver) build(w WorkerConfig, prompt string, session *string) []string {
	agentDir := piAgentDir(w.Name)
	const setup = `set -eu
agent_dir="$1"
shift
mkdir -p "$agent_dir/sessions"
node - "$agent_dir/models.json" <<'JS'
const fs = require("fs");
const model = {id: process.env.PI_MODEL, name: process.env.PI_MODEL};
if (process.env.PI_MODEL_CONTEXT_WINDOW) {
  model.contextWindow = Number.parseInt(process.env.PI_MODEL_CONTEXT_WINDOW, 10);
}
const provider = {
  baseUrl: process.env.PI_BASE_URL,
  api: process.env.PI_PROVIDER_API,
  apiKey: process.env.PI_API_KEY,
  models: [model],
};
fs.writeFileSync(process.argv[2], JSON.stringify({providers: {aether: provider}}));
JS
node - <<'JS'
process.stdout.write(JSON.stringify({schema: "aether.pi-cwd/v1", cwd: process.cwd()}) + "\n");
JS
exec env PI_CODING_AGENT_DIR="$agent_dir" pi "$@"`
	argv := []string{
		"/bin/sh", "-lc", setup, "--", agentDir,
		"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
		"--tools", "read,write,edit,bash,grep,find,ls",
		"--provider", "aether", "--model", w.Env["PI_MODEL"], "--thinking", "off", "--mode", "json",
		"--session-dir", agentDir + "/sessions",
	}
	if session != nil && *session != "" {
		argv = append(argv, "--session", *session)
	}
	return append(argv, "-p", prompt)
}

func piAgentDir(workerName string) string {
	safe := []byte(strings.TrimSpace(workerName))
	for index, character := range safe {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '.' || character == '-' {
			continue
		}
		safe[index] = '-'
	}
	if len(safe) == 0 {
		safe = []byte("worker")
	}
	if len(safe) > 64 {
		safe = safe[:64]
	}
	digest := sha256.Sum256([]byte(workerName))
	return "/tmp/aether-pi/" + string(safe) + "-" + hex.EncodeToString(digest[:6])
}

type piEvent struct {
	Type     string      `json:"type"`
	ID       string      `json:"id"`
	Message  *piMessage  `json:"message"`
	Messages []piMessage `json:"messages"`
}

type piMessage struct {
	Role       string      `json:"role"`
	Content    []piContent `json:"content"`
	StopReason string      `json:"stopReason"`
	overflow   bool
}

type piContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	overflow bool
}
