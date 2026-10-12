package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MockDriver is a probabilistic driver for testing without real Agent CLIs.
type MockDriver struct{}

func NewMockDriver() *MockDriver { return &MockDriver{} }

func (d *MockDriver) ResultEvidence() resultEvidenceModule { return mockResultEvidence{} }

func (d *MockDriver) PrepareSession() *string {
	s := "mock-session"
	return &s
}

func (d *MockDriver) BuildExecute(w WorkerConfig, prompt string, session *string) DriverResult {
	behavior, err := parseMockBehavior(w.Env)
	if err != nil {
		return DriverResult{
			Argv:    mockConfigurationFailureCommand(w.Name),
			Session: session,
		}
	}

	phase := w.Env["MOCK_PHASE"]
	if phase == "" {
		phase = "bootstrap"
	}

	cfgJSON, _ := json.Marshal(behavior)
	promptData := buildPromptData(prompt, phase, w.Env)
	promptJSON, _ := json.Marshal(promptData)

	return DriverResult{
		Argv:    []string{"python3", "-c", mockScript, string(cfgJSON), string(promptJSON)},
		Session: session,
	}
}

func (d *MockDriver) BuildRecovery(w WorkerConfig, prompt, session string, phase Phase) []string {
	if phase == PhaseExploreConcludeRecovery || phase == PhaseBootstrapConcludeRecovery {
		return d.BuildConclude(w, prompt, session)
	}
	return d.BuildExecute(w, prompt, &session).Argv
}

func (d *MockDriver) BuildConclude(w WorkerConfig, prompt string, session string) []string {
	behavior, err := parseMockBehavior(w.Env)
	if err != nil {
		return mockConfigurationFailureCommand(w.Name)
	}

	phase := w.Env["MOCK_PHASE"]
	concludePhase := phase
	switch phase {
	case "", "bootstrap":
		concludePhase = "bootstrap_conclude"
	case "explore_execute":
		concludePhase = "explore_conclude"
	case "reason_finalization":
		concludePhase = "reason"
	}

	cfgJSON, _ := json.Marshal(behavior)
	promptData := buildPromptData(prompt, concludePhase, w.Env)
	promptJSON, _ := json.Marshal(promptData)

	return []string{"python3", "-c", mockScript, string(cfgJSON), string(promptJSON)}
}

func mockConfigurationFailureCommand(workerName string) []string {
	// Catalog validation normally catches this before execution. Keep the
	// direct Driver seam deterministic without reintroducing the legacy
	// accepted/data submission package.
	payload, _ := json.Marshal(map[string]any{
		"outcome": map[string]any{"kind": "decline", "reason": fmt.Sprintf("mock configuration is invalid for %s", workerName)},
	})
	return []string{"python3", "-c", "import sys; print(sys.argv[1])", string(payload)}
}

// buildPromptData extracts context from the rendered prompt and MOCK_CONTEXT env var
// for rule evaluation. The MOCK_CONTEXT is injected by task runners with project state.
func buildPromptData(prompt, phase string, env map[string]string) map[string]interface{} {
	data := map[string]interface{}{
		"phase": phase,
	}
	if strings.Contains(prompt, OutcomeSubmissionSchemaV1) {
		data["submission_schema"] = OutcomeSubmissionSchemaV1
	}
	if protocol := env["AETHER_RESULT_PROTOCOL"]; protocol != "" {
		data["submission_protocol"] = protocol
	}

	// Parse MOCK_CONTEXT from env (injected by task runners)
	if ctx, ok := env["MOCK_CONTEXT"]; ok && ctx != "" {
		var mockCtx map[string]interface{}
		if json.Unmarshal([]byte(ctx), &mockCtx) == nil {
			// Convert fact_ids_count to a list for rule matching
			if count, ok := mockCtx["fact_ids_count"].(float64); ok {
				facts := make([]string, int(count))
				for i := range facts {
					facts[i] = fmt.Sprintf("f%03d", i+1)
				}
				data["fact_ids"] = facts
			}
			if empty, ok := mockCtx["open_intents_empty"].(bool); ok {
				if !empty {
					data["open_intents"] = []string{"i001"}
				} else {
					data["open_intents"] = []string{}
				}
			}
			if mi, ok := mockCtx["max_intents"].(float64); ok {
				data["max_intents"] = int(mi)
			}
			if iid, ok := mockCtx["intent_id"].(string); ok {
				data["intent_id"] = iid
			}
		}
	}

	// Ensure defaults for fields not in MOCK_CONTEXT
	if _, ok := data["fact_ids"]; !ok {
		data["fact_ids"] = []string{}
	}
	if _, ok := data["open_intents"]; !ok {
		data["open_intents"] = []string{}
	}
	if _, ok := data["max_intents"]; !ok {
		data["max_intents"] = 3
	}
	if _, ok := data["intent_id"]; !ok {
		data["intent_id"] = ""
	}

	return data
}

// extractListField and extractStringField removed — context comes from MOCK_CONTEXT

// mockScript is the python3 script that implements mock behavior.
// It matches Cairn's mock.py script structure: delay → rule evaluation →
// weighted outcome selection → phase-appropriate JSON output.
const mockScript = `
import json, random, sys, time

try:
    cfg = json.loads(sys.argv[1])
    prompt = json.loads(sys.argv[2])
    phase = prompt["phase"]
    phase_cfg = cfg[phase]
except Exception as exc:
    print(f"mock setup failed: {exc}", file=sys.stderr)
    raise SystemExit(1)

# Delay
delay = phase_cfg["delay"]
if delay[0] > 0 or delay[1] > 0:
    time.sleep(random.uniform(delay["min"] if isinstance(delay, dict) else delay[0],
                              delay["max"] if isinstance(delay, dict) else delay[1]))

# Build weights with context-aware adjustments for reason phase
weights = dict(phase_cfg["outcomes"])
if phase == "reason":
    open_intents = prompt.get("open_intents") or []
    fact_ids = prompt.get("fact_ids") or []
    if not fact_ids:
        weights.pop("complete", None)
        weights.pop("intent", None)

# Filter to positive weights
choices = [(name, weight) for name, weight in weights.items() if weight > 0]
if not choices:
    print(f"mock {phase} has no legal outcomes for prompt context", file=sys.stderr)
    raise SystemExit(2)

# Rule evaluation
def _rule_matches(rule, prompt):
    fact_ids = prompt.get("fact_ids") or []
    open_intents = prompt.get("open_intents") or []
    if "fact_ids_gte" in rule and len(fact_ids) < rule["fact_ids_gte"]:
        return False
    if "fact_ids_lte" in rule and len(fact_ids) > rule["fact_ids_lte"]:
        return False
    if "open_intents_empty" in rule and (len(open_intents) == 0) != rule["open_intents_empty"]:
        return False
    return True

rules = phase_cfg.get("rules") or []
forced = None
for rule in rules:
    if _rule_matches(rule, prompt):
        forced = rule["force"]
        break

# Select outcome
if forced is not None:
    outcome = forced
else:
    pick = random.uniform(0, sum(weight for _, weight in choices))
    total = 0
    outcome = choices[-1][0]
    for name, weight in choices:
        total += weight
        if pick <= total:
            outcome = name
            break

# Handle special outcomes
if phase == "healthcheck":
    raise SystemExit(0 if outcome == "ok" else 1)

if outcome == "command_fail":
    print(f"mock {phase} command failed", file=sys.stderr)
    raise SystemExit(1)

def emit_outcome(payload):
    print(json.dumps({"outcome": payload}, ensure_ascii=False))

if outcome == "invalid_json":
    print("{invalid json")
    raise SystemExit(0)

if outcome == "rejected":
    emit_outcome({"kind": "decline", "reason": "mock_rejected"})
    raise SystemExit(0)
if outcome == "invalid_payload":
    emit_outcome({"kind": "no_change", "unknown": True})
    raise SystemExit(0)
if phase == "reason":
    fact_ids = prompt.get("fact_ids") or []
    max_i = prompt.get("max_intents", 3)
    from_ids = [random.choice(fact_ids)] if fact_ids else []
    if outcome == "complete":
        result = {"kind": "complete_project", "description": "mock complete", "from": from_ids}
    elif outcome == "intent":
        count = random.randint(1, max(1, max_i))
        intents = []
        for idx in range(count):
            source = [random.choice(fact_ids)] if fact_ids else []
            intents.append({"from": source, "description": f"mock intent {idx+1}"})
        result = {"kind": "propose_intents", "intents": intents}
    else:
        result = {"kind": "no_change"}
    emit_outcome(result)
    raise SystemExit(0)
if phase == "bootstrap":
    if outcome == "complete":
        result = {"kind": "complete_project", "description": "mock bootstrap complete", "fact": {"description": "mock bootstrap fact"}}
    else:
        result = {"kind": "conclude_intent", "fact": {"description": "mock bootstrap fact"}}
    emit_outcome(result)
    raise SystemExit(0)
if phase == "bootstrap_conclude":
    emit_outcome({"kind": "conclude_intent", "fact": {"description": "mock bootstrap fallback fact"}})
    raise SystemExit(0)
label = prompt.get("intent_id") or phase
emit_outcome({"kind": "conclude_intent", "fact": {"description": f"mock fact for {label}"}})
raise SystemExit(0)

`
