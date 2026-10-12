package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/controlplane"
	dockerruntime "github.com/C1ph3rX13/Aether/internal/docker"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/C1ph3rX13/Aether/internal/worker"
	dockerclient "github.com/moby/moby/client"
)

const (
	piRecoverySecret            = "pi-recovery-fixture-secret-never-expose"
	piRecoveryToken             = "AETHER_PI_RECOVERY_TOKEN"
	piFinalizationFixtureMarker = "AETHER_RESULT_FINALIZATION_FIXTURE"
)

const piRecoveryFakeCLI = `#!/bin/sh
set -eu

mode=${PI_BASE_URL##*/}
resumed=0
session_dir=
session=
prompt=
tools=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session)
      resumed=1
      session=$2
      shift 2
      ;;
    --session-dir)
      session_dir=$2
      shift 2
      ;;
    --tools)
      tools=$2
      shift 2
      ;;
    --no-tools)
      tools=
      shift
      ;;
    -p)
      prompt=$2
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

case "$mode" in
  verbose) session_offset=10 ;;
  bootstrap-recovery) session_offset=20 ;;
  bootstrap-conclude-recovery) session_offset=30 ;;
  recovery) session_offset=40 ;;
  explore-recovery-malformed) session_offset=42 ;;
  explore-recovery-oversized) session_offset=44 ;;
  oversized) session_offset=50 ;;
  retry) session_offset=60 ;;
  cancel) session_offset=70 ;;
  capture-conclude) session_offset=80 ;;
  conclude-finalization) session_offset=82 ;;
  reason-finalization) session_offset=83 ;;
  timeout-session-missing) session_offset=86 ;;
  timeout-session-malformed) session_offset=88 ;;
  timeout-session-oversized) session_offset=90 ;;
  timeout-session-inconsistent) session_offset=92 ;;
  *) session_offset=94 ;;
esac
if [ "$resumed" -eq 0 ]; then
  counter="$session_dir/.fixture-${mode}.counter"
  attempt=0
  if [ -f "$counter" ]; then attempt=$(cat "$counter"); fi
  attempt=$((attempt + 1))
  printf '%s\n' "$attempt" > "$counter"
  session=$(printf '0192f7e5-7b2a-7c01-8a11-2233445566%02d' "$((session_offset + attempt))")
fi

timestamp=2026-08-27T13:15:30.123Z
cwd=$(node -p 'process.cwd()')
header=$(node - "$session" "$timestamp" "$cwd" <<'JS'
process.stdout.write(JSON.stringify({type: "session", version: 3, id: process.argv[2], timestamp: process.argv[3], cwd: process.argv[4]}));
JS
)
printf '%s\n' "$header"
file_timestamp=$(printf '%s' "$timestamp" | sed 's/[:.]/-/g')
session_file="$session_dir/${file_timestamp}_${session}.jsonl"

write_session() {
  node - "$session_file" "$header" "$prompt" "$1" "$resumed" <<'JS'
const fs = require("fs");
const [file, headerJSON, prompt, finalValue, resumed] = process.argv.slice(2);
const final = finalValue === "OVERSIZED" ? JSON.stringify({schema: "aether.run-result/v1", outcome: {kind: "complete_project", description: "x".repeat(300000)}}) : finalValue;
let entries = [];
if (resumed === "1" && fs.existsSync(file)) {
  entries = fs.readFileSync(file, "utf8").split("\n").filter(Boolean).map(JSON.parse);
}
if (entries.length === 0) entries = [JSON.parse(headerJSON)];
const prior = entries[entries.length - 1];
const parentId = prior.type === "session" ? null : prior.id;
const next = entries.filter(entry => entry.type !== "session").length + 1;
const timestamp = JSON.parse(headerJSON).timestamp;
const entryTime = offset => new Date(Date.parse(timestamp) + offset).toISOString();
entries.push({type: "message", id: next.toString(16).padStart(8, "0"), parentId, timestamp: entryTime(next), message: {role: "user", content: [{type: "text", text: prompt}]}});
entries.push({type: "message", id: (next + 1).toString(16).padStart(8, "0"), parentId: next.toString(16).padStart(8, "0"), timestamp: entryTime(next + 1), message: {role: "assistant", stopReason: "stop", content: [{type: "text", text: final}]}});
fs.writeFileSync(file, entries.map(entry => JSON.stringify(entry)).join("\n") + "\n");
JS
}

write_malformed_session() {
  printf '%s\n%s\n' "$header" '{"type":"message"' > "$session_file"
}

write_oversized_session() {
  node - "$session_file" "$header" <<'JS'
const fs = require("fs");
fs.writeFileSync(process.argv[2], process.argv[3] + "\n" + "x".repeat(9 * 1024 * 1024) + "\n");
JS
}

write_inconsistent_session() {
  write_session "$1"
  node - "$session_file" <<'JS'
const fs = require("fs");
const file = process.argv[2];
const entries = fs.readFileSync(file, "utf8").split("\n").filter(Boolean).map(JSON.parse);
entries[0].cwd = "/different-workspace";
fs.writeFileSync(file, entries.map(entry => JSON.stringify(entry)).join("\n") + "\n");
JS
}

result_file=$(node - "$prompt" <<'JS'
const prompt = process.argv[2] || "";
const match = prompt.match(/\/tmp\/aether-invocations\/[^\s]+\/result\.toml/);
process.stdout.write(match ? match[0] : "");
JS
)

write_result_file() {
  node - "$result_file" "$1" "$2" <<'JS'
const fs = require("fs");
const [file, kind, description] = process.argv.slice(2);
if (!file) throw new Error("Pi result file path was not included in the prompt");
const escaped = description.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
if (kind === "propose_intents") {
  fs.writeFileSync(file, "[outcome]\nkind = \"propose_intents\"\n\n[[outcome.intents]]\nfrom = [\"origin\"]\ndescription = \"" + escaped + "\"\n");
  process.exit(0);
}
if (kind === "conclude_intent") {
  fs.writeFileSync(file, "[outcome]\nkind = \"conclude_intent\"\n\n[outcome.fact]\ndescription = \"" + escaped + "\"\n");
  process.exit(0);
}
if (kind === "complete_project") {
  fs.writeFileSync(file, "[outcome]\nkind = \"complete_project\"\ndescription = \"" + escaped + "\"\n\n[outcome.fact]\ndescription = \"" + escaped + "\"\n");
  process.exit(0);
}
if (kind === "oversized") {
  fs.writeFileSync(file, "[outcome]\nkind = \"decline\"\nreason = \"" + "x".repeat(70000) + "\"\n");
  process.exit(0);
}
if (kind === "invalid") {
  fs.writeFileSync(file, "[outcome]\nkind = \"propose_intents\"\nunknown = true\n");
  process.exit(0);
}
throw new Error("unsupported fixture result kind " + kind);
JS
}

emit_events() {
  node - "$1" "$2" "$3" <<'JS'
const [finalValue, complete, includeUser] = process.argv.slice(2);
const final = finalValue === "OVERSIZED" ? JSON.stringify({schema: "aether.run-result/v1", outcome: {kind: "complete_project", description: "x".repeat(300000)}}) : finalValue;
const user = {role: "user", content: [{type: "text", text: "fixture user message"}]};
const assistant = {role: "assistant", stopReason: "stop", content: [{type: "text", text: final}]};
const events = [{type: "agent_start"}];
if (includeUser === "1") events.push({type: "message_end", message: user});
events.push({type: "message_end", message: assistant});
if (complete === "1") {
  events.push({type: "turn_end", message: assistant});
  events.push({type: "agent_end", messages: [assistant]});
}
for (const event of events) process.stdout.write(JSON.stringify(event) + "\n");
JS
}

case "$mode" in
  reason-finalization)
    if [ "$resumed" -eq 0 ]; then
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"Reason Finalization proposed intent"}]},"unexpected":"private invalid Reason response"}'
      write_result_file invalid "Reason Finalization proposed intent"
    else
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"Reason Finalization proposed intent"}]}}'
      write_result_file propose_intents "Reason Finalization proposed intent"
    fi
    write_session "Pi Reason result is stored in result.toml"
    emit_events "Pi Reason result is stored in result.toml" 1 0
    ;;
  reason)
    final='{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"Explore the deterministic recovery fixture"}]}}'
    write_result_file propose_intents "Explore the deterministic recovery fixture"
    write_session "Pi Reason result is stored in result.toml"
    emit_events "Pi Reason result is stored in result.toml" 1 0
    ;;
  explore|explore-recovery-fail|explore-recovery-malformed|explore-recovery-oversized)
    final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"Explore recovery committed fact"}}}'
    if [ "$resumed" -eq 0 ]; then
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"abnormal Explore candidate must not commit"}}}'
      case "$mode" in
        explore-recovery-malformed) write_malformed_session ;;
        explore-recovery-oversized) write_oversized_session ;;
        *) write_session "$final" ;;
      esac
      emit_events "$final" 0 0
      exit 9
    fi
    if [ "$mode" = "explore-recovery-malformed" ] || [ "$mode" = "explore-recovery-oversized" ]; then
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"unverified abnormal continuation must not run"}}}'
    fi
    if [ "$resumed" -eq 1 ]; then write_result_file conclude_intent "Explore recovery committed fact"; fi
    write_session "$final"
    if [ "$mode" = "explore-recovery-fail" ]; then
      exit 11
    fi
    emit_events "$final" 1 0
    ;;
  verbose)
    final='{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"Pi verbose success","fact":{"description":"Pi verbose fact"}}}'
    write_result_file complete_project "Pi verbose fact"
    write_session "$final"
    for _ in 1 2 3; do
      printf '{"type":"tool_execution_end","result":"%*s"}\n' 400000 ''
    done
    emit_events "$final" 1 0
    ;;
  capture-conclude)
    if [ "$resumed" -eq 0 ]; then
      final='not a result'
    else
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"Pi capture Conclude fact"}}}'
    fi
    if [ "$resumed" -eq 1 ]; then write_result_file conclude_intent "Pi capture Conclude fact"; fi
    write_session "$final"
    emit_events "$final" 1 0
    ;;
  conclude-finalization)
    if [ "$resumed" -eq 0 ]; then
      final='not a result'
    else
      case "$prompt" in
        *"AETHER_RESULT_FINALIZATION_FIXTURE"*)
          final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"Pi Result Finalization fact"}}}'
          ;;
        *)
          final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"confirmed but malformed"}},"description":"unexpected"}'
          ;;
      esac
    fi
    if [ "$resumed" -eq 1 ] && printf '%s' "$final" | grep -q 'confirmed but malformed'; then
      write_result_file invalid "confirmed but malformed"
    elif [ "$resumed" -eq 1 ]; then
      write_result_file conclude_intent "Pi Result Finalization fact"
    fi
    write_session "$final"
    emit_events "$final" 1 0
    ;;
  timeout-session-missing|timeout-session-malformed|timeout-session-oversized|timeout-session-inconsistent)
    if [ "$resumed" -eq 1 ]; then
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"ambiguous timeout continuation must not run"}}}'
      write_session "$final"
      emit_events "$final" 1 0
      exit 0
    fi
    final='timeout evidence must not commit'
    case "$mode" in
      timeout-session-missing) ;;
      timeout-session-malformed) write_malformed_session ;;
      timeout-session-oversized) write_oversized_session ;;
      timeout-session-inconsistent) write_inconsistent_session "$final" ;;
    esac
    emit_events "$final" 0 0
    sleep 30
    ;;
  bootstrap-recovery)
    final='{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"Pi Bootstrap recovery success","fact":{"description":"Pi Bootstrap recovery fact"}}}'
    write_result_file complete_project "Pi Bootstrap recovery fact"
    write_session "$final"
    if [ "$resumed" -eq 0 ]; then
      emit_events "$final" 0 0
      exit 9
    fi
    emit_events "$final" 1 0
    ;;
  bootstrap-conclude-recovery)
    if [ "$resumed" -eq 0 ]; then
      final='{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"must not commit abnormal conclude","fact":{"description":"abnormal conclude candidate"}}}'
      write_session "$final"
      emit_events "$final" 0 0
      sleep 30
    fi
    case "$prompt" in
      *"Recover the interrupted Bootstrap conclude"*)
        final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"Pi Bootstrap conclude recovery fact"}}}'
        write_result_file conclude_intent "Pi Bootstrap conclude recovery fact"
        write_session "$final"
        emit_events "$final" 1 0
        ;;
      *)
        final='{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"must not commit abnormal conclude","fact":{"description":"abnormal conclude candidate"}}}'
        write_session "$final"
        emit_events "$final" 0 0
        exit 9
        ;;
    esac
    ;;
  recovery)
    final='{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"Pi conclude fact"}}}'
    write_result_file conclude_intent "Pi conclude fact"
    write_session "$final"
    if [ "$resumed" -eq 0 ]; then
      emit_events "$final" 0 0
      sleep 30
    fi
    emit_events "$final" 1 0
    ;;
  oversized)
    final='Pi result file is oversized'
    write_result_file oversized "oversized"
    write_session "$final"
    emit_events "$final" 1 0
    ;;
  retry)
    final='not a result'
    write_session "$final"
    emit_events "$final" 1 0
    ;;
  cancel)
    final='not a result'
    write_session "$final"
    emit_events "$final" 0 0
    sleep 30
    ;;
  *)
    exit 64
    ;;
esac
`

// TestPiDeliveryRecoveryDockerIntegration is the permanent real-Docker Pi
// acceptance baseline for the Dispatcher-to-Control-Plane delivery boundary.
// The test uses the fixed Worker image and never asks Aether to pull it.
func TestPiDeliveryRecoveryDockerIntegration(t *testing.T) {
	image := dockerIntegrationImage(t)

	host, err := controlplane.NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "pi-recovery-test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := host.Shutdown(); shutdownErr != nil {
			t.Errorf("Control Plane shutdown: %v", shutdownErr)
		}
	})
	server := httptest.NewServer(host.Handler())
	t.Cleanup(server.Close)

	control, err := controlplane.NewHTTPRunControl(server.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	lease, err := control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "pi-delivery-recovery-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	dockerAPI, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dockerAPI.Close() })
	baseRuntime, err := newDockerIntegrationRuntime(dockerruntime.Options{
		DeploymentID: "pi-delivery-recovery-test", DispatcherID: lease.InstanceID, DisposeGrace: time.Second,
		OutputLimits: dockerruntime.DefaultOutputLimits(),
		Project: dockerruntime.ProjectConfig{
			Image: image, NetworkMode: "none", CompletedAction: "remove",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := baseRuntime.Close(); closeErr != nil {
			t.Errorf("Docker Runtime close: %v", closeErr)
		}
	})

	observedRuntime := &deliveryObservedRuntime{delegate: baseRuntime}
	secretResolver, err := rundomain.NewSecretResolver(map[string]rundomain.SecretProvider{
		"configuration": lookupSecretProvider{lookup: func(key string) (string, bool) {
			if key == piRecoveryToken {
				return piRecoverySecret, true
			}
			return "", false
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	scenarios := make(map[string]deliveryProjectDetail)
	intentIDs := make(map[string]string)
	for _, scenario := range []struct {
		name   string
		mode   string
		title  string
		goal   string
		reason bool
	}{
		{name: "reason-finalization", mode: "reason-finalization", title: "Pi Reason Finalization", goal: "finalize an invalid Reason result once", reason: true},
		{name: "verbose", mode: "verbose", title: "Pi verbose delivery", goal: "commit the bounded final result"},
		{name: "capture-conclude", mode: "capture-conclude", title: "Pi capture Conclude", goal: "continue a verified Session after normal capture failure"},
		{name: "conclude-finalization", mode: "conclude-finalization", title: "Pi Result Finalization", goal: "finalize an invalid Conclude result once"},
		{name: "timeout-session-missing", mode: "timeout-session-missing", title: "Pi timeout missing Session", goal: "preserve timeout when Session evidence is missing"},
		{name: "timeout-session-malformed", mode: "timeout-session-malformed", title: "Pi timeout malformed Session", goal: "preserve timeout when Session evidence is malformed"},
		{name: "timeout-session-oversized", mode: "timeout-session-oversized", title: "Pi timeout oversized Session", goal: "preserve timeout when Session evidence is oversized"},
		{name: "timeout-session-inconsistent", mode: "timeout-session-inconsistent", title: "Pi timeout inconsistent Session", goal: "preserve timeout when Session evidence is inconsistent"},
		{name: "bootstrap-recovery", mode: "bootstrap-recovery", title: "Pi Bootstrap recovery", goal: "recover the failed Bootstrap process"},
		{name: "bootstrap-conclude-recovery", mode: "bootstrap-conclude-recovery", title: "Pi Bootstrap conclude recovery", goal: "recover the failed Bootstrap conclude process"},
		{name: "recovery", mode: "recovery", title: "Pi same-run recovery", goal: "recover the timed out bootstrap"},
		{name: "oversized", mode: "oversized", title: "Pi oversized result file", goal: "reject the oversized result file"},
		{name: "retry", mode: "retry", title: "Pi retry chain", goal: "preserve retry lineage"},
		{name: "cancel", mode: "cancel", title: "Pi cancelled delivery", goal: "terminate the cancelled exec"},
	} {
		project := createDeliveryProject(t, server, deliveryProjectRequest{
			Title: scenario.title, Origin: "PI_" + strings.ToUpper(scenario.mode) + "_FIXTURE", Goal: scenario.goal,
			BootstrapEnabled: !scenario.reason,
		})
		scenarios[scenario.name] = project
		if !scenario.reason {
			intentIDs[scenario.name] = bootstrapIntentID(t, project)
		}
		containerID := provisionPiRecoveryFakeCLI(t, baseRuntime, dockerAPI, image, project.Project.ID)
		t.Cleanup(func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = dockerAPI.ContainerRemove(cleanupContext, containerID, dockerclient.ContainerRemoveOptions{Force: true})
		})
	}

	reasonFinalizationRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["reason-finalization"].Project.ID, "pi-reason-finalization", "reason-finalization", piDeliveryPolicy(1, false), true)
	assertPiReasonFinalization(t, server, scenarios["reason-finalization"].Project.ID, reasonFinalizationRunIDs, observedRuntime)

	runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["verbose"].Project.ID, "pi-verbose", "verbose", piDeliveryPolicy(1, false), true)
	assertPiVerboseDelivery(t, server, scenarios["verbose"].Project.ID, control)

	captureConcludeRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["capture-conclude"].Project.ID, "pi-capture-conclude", "capture-conclude", piDeliveryPolicy(1, false), true)
	assertPiCompletionContinuation(t, server, scenarios["capture-conclude"].Project.ID, captureConcludeRunIDs,
		observedRuntime, "Pi capture Conclude fact", 2)

	finalizationRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["conclude-finalization"].Project.ID, "pi-conclude-finalization", "conclude-finalization", piDeliveryPolicy(1, false), true)
	assertPiCompletionContinuation(t, server, scenarios["conclude-finalization"].Project.ID, finalizationRunIDs,
		observedRuntime, "Pi Result Finalization fact", 3)

	var unverifiedTimeoutRunIDs []rundomain.RunID
	for _, name := range []string{
		"timeout-session-missing", "timeout-session-malformed", "timeout-session-oversized", "timeout-session-inconsistent",
	} {
		runIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
			scenarios[name].Project.ID, "pi-"+name, name, piDeliveryPolicy(1, false), true)
		assertPiUnverifiedTimeout(t, server, scenarios[name].Project.ID, runIDs, observedRuntime)
		unverifiedTimeoutRunIDs = append(unverifiedTimeoutRunIDs, runIDs...)
	}

	bootstrapRecoveryRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["bootstrap-recovery"].Project.ID, "pi-bootstrap-recovery", "bootstrap-recovery", piDeliveryPolicy(1, true), true)
	assertPiBootstrapPrimaryRecovery(t, server, scenarios["bootstrap-recovery"].Project.ID, bootstrapRecoveryRunIDs, observedRuntime)

	bootstrapConcludeRecoveryRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["bootstrap-conclude-recovery"].Project.ID, "pi-bootstrap-conclude-recovery", "bootstrap-conclude-recovery", piDeliveryPolicy(1, true), true)
	assertPiBootstrapConcludeRecovery(t, server, scenarios["bootstrap-conclude-recovery"].Project.ID, bootstrapConcludeRecoveryRunIDs, observedRuntime)

	recoveryRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["recovery"].Project.ID, "pi-recovery", "recovery", piDeliveryPolicy(1, false), true)
	assertPiSameRunRecovery(t, server, scenarios["recovery"].Project.ID, recoveryRunIDs, observedRuntime)

	runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["oversized"].Project.ID, "pi-oversized", "oversized", piDeliveryPolicy(1, false), true)
	assertPiOversizedResultFile(t, server, scenarios["oversized"].Project.ID, intentIDs["oversized"])

	retryRunIDs := runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["retry"].Project.ID, "pi-retry", "retry", piDeliveryPolicy(2, false), true)
	waitForDeliveryRetry(t, server, scenarios["retry"].Project.ID, 1)
	retryRunIDs = append(retryRunIDs, runPiDeliveryScenario(t, server, control, observedRuntime, secretResolver, lease, image,
		scenarios["retry"].Project.ID, "pi-retry", "retry", piDeliveryPolicy(2, false), true)...)
	assertPiRetryChain(t, server, scenarios["retry"].Project.ID, intentIDs["retry"], retryRunIDs, observedRuntime)

	cancelRunID := runPiDeliveryScenarioCancellable(t, control, observedRuntime, secretResolver, lease, image,
		scenarios["cancel"].Project.ID, scenarios["cancel"].Project.ConfigRevision, "pi-cancel", "cancel")
	assertPiCancellation(t, server, scenarios["cancel"].Project.ID, cancelRunID, observedRuntime)

	allRunIDs := make([]rundomain.RunID, 0, len(reasonFinalizationRunIDs)+len(captureConcludeRunIDs)+len(finalizationRunIDs)+
		len(unverifiedTimeoutRunIDs)+len(bootstrapRecoveryRunIDs)+len(bootstrapConcludeRecoveryRunIDs)+len(recoveryRunIDs)+len(retryRunIDs)+1)
	allRunIDs = append(allRunIDs, reasonFinalizationRunIDs...)
	allRunIDs = append(allRunIDs, captureConcludeRunIDs...)
	allRunIDs = append(allRunIDs, finalizationRunIDs...)
	allRunIDs = append(allRunIDs, unverifiedTimeoutRunIDs...)
	allRunIDs = append(allRunIDs, bootstrapRecoveryRunIDs...)
	allRunIDs = append(allRunIDs, bootstrapConcludeRecoveryRunIDs...)
	allRunIDs = append(allRunIDs, recoveryRunIDs...)
	allRunIDs = append(allRunIDs, retryRunIDs...)
	allRunIDs = append(allRunIDs, cancelRunID)
	for _, name := range []string{"verbose", "oversized"} {
		activity, _ := deliveryActivity(t, server, scenarios[name].Project.ID)
		for _, run := range activity.Runs {
			allRunIDs = append(allRunIDs, run.ID)
		}
	}
	activeHandles, err := control.ListExecutionHandles(t.Context(), rundomain.ExecutionHandleListRequest{
		RunIDs: allRunIDs, DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
	})
	if err != nil || len(activeHandles) != 0 {
		t.Fatalf("terminal Pi phases retained active execution handles: handles=%+v error=%v", activeHandles, err)
	}

	assertPiProjectionRedaction(t, server, scenarios, intentIDs)
}

func runPiDeliveryScenario(
	t *testing.T,
	server *httptest.Server,
	control *controlplane.HTTPRunControl,
	runtime *deliveryObservedRuntime,
	secrets rundomain.SecretEnvironmentProvider,
	lease rundomain.DispatcherLease,
	image, projectID, workerName, mode string,
	policy scheduler.RunSchedulerPolicy,
	once bool,
) []rundomain.RunID {
	t.Helper()
	before, _ := deliveryActivity(t, server, projectID)
	known := make(map[rundomain.RunID]struct{}, len(before.Runs))
	for _, run := range before.Runs {
		known[run.ID] = struct{}{}
	}
	var project deliveryProjectDetail
	deliveryGetJSON(t, server, "/api/v1/projects/"+projectID, &project)
	catalog := piRecoveryWorkerCatalog(t, image, workerName, mode, project.Project.ConfigRevision)
	executions, err := NewRunExecutionFactory(catalog, runtime, secrets, control)
	runner, err := newPiDeliveryScheduler(
		control, catalog, runtime, executions, lease, project.Project.ConfigRevision, policy, once,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	runIDs := make([]rundomain.RunID, 0, len(activity.Runs))
	for _, run := range activity.Runs {
		if _, exists := known[run.ID]; !exists {
			runIDs = append(runIDs, run.ID)
		}
	}
	return runIDs
}

func newPiDeliveryScheduler(
	control *controlplane.HTTPRunControl,
	catalog *worker.Catalog,
	runtime *deliveryObservedRuntime,
	executions *RunExecutionFactory,
	lease rundomain.DispatcherLease,
	configRevision string,
	policy scheduler.RunSchedulerPolicy,
	once bool,
) (*scheduler.RunScheduler, error) {
	policy = bindExecutionPoliciesToCatalog(policy, catalog)
	return scheduler.NewRunScheduler(scheduler.RunSchedulerControls{
		Planning: control, Inventory: control, Recovery: control, Lifecycle: control, Cleanup: control,
	}, scheduler.RunSchedulerConfig{
		ConfigRevision: configRevision,
		Policy:         policy, Catalog: catalog, Resources: runtime, Executions: executions,
		Lease: lease, DeploymentID: "pi-delivery-recovery-test", Once: once,
		PrepareRevisionWorkers: func(context.Context, string) (map[string]bool, error) {
			ready := make(map[string]bool)
			for _, configured := range catalog.Workers() {
				ready[configured.Name] = true
			}
			return ready, nil
		},
	})
}

func runPiDeliveryScenarioCancellable(
	t *testing.T,
	control *controlplane.HTTPRunControl,
	runtime *deliveryObservedRuntime,
	secrets rundomain.SecretEnvironmentProvider,
	lease rundomain.DispatcherLease,
	image, projectID, configRevision, workerName, mode string,
) rundomain.RunID {
	t.Helper()
	catalog := piRecoveryWorkerCatalog(t, image, workerName, mode, configRevision)
	executions, err := NewRunExecutionFactory(catalog, runtime, secrets, control)
	if err != nil {
		t.Fatal(err)
	}
	policy := piDeliveryPolicy(1, false)
	runner, err := newPiDeliveryScheduler(
		control, catalog, runtime, executions, lease, configRevision, policy, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := runtime.Snapshot()
	cancelContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(cancelContext) }()
	launch := waitForPiLaunch(t, runtime, projectID, len(before))
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Pi scheduler: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Pi scheduler did not terminate")
	}
	return launch.Spec.RunID
}

func assertPiReasonFinalization(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	if len(runIDs) != 1 {
		t.Fatalf("Reason Finalization created %d Runs: %v", len(runIDs), runIDs)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].ID != runIDs[0] ||
		activity.Runs[0].Task != rundomain.TaskReason || activity.Runs[0].Attempt != 1 ||
		activity.Runs[0].PreviousRunID != nil || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeProposeIntents ||
		activity.Runs[0].ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
		launches, outputs := runtime.Snapshot()
		t.Fatalf("Reason Finalization Activity: %+v launches=%+v outputs=%+v", activity.Runs, launches, outputs)
	}
	project := getDeliveryProject(t, server, projectID)
	if project.Project.Status != "active" || project.Project.Revision != 2 || len(project.Intents) != 1 {
		t.Fatalf("Reason Finalization changed Blackboard incorrectly: %+v", project)
	}
	chainJSON := deliveryGetJSON(t, server, "/api/v1/projects/"+projectID+
		"/activity/"+string(runIDs[0])+"/retry-chain", nil)
	var chain []rundomain.ActivityRun
	if err := json.Unmarshal(chainJSON, &chain); err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].ID != runIDs[0] || chain[0].Attempt != 1 {
		t.Fatalf("Reason Finalization consumed retry budget: %+v", chain)
	}

	launches, outputs := runtime.Snapshot()
	var primary, finalization *deliveryLaunchObservation
	for index := range launches {
		launch := launches[index]
		if launch.Spec.RunID != runIDs[0] {
			continue
		}
		switch launch.Spec.Phase {
		case rundomain.PhaseReason:
			primary = &launch
		case rundomain.PhaseReasonFinalization:
			finalization = &launch
		default:
			t.Fatalf("Reason Finalization launched unexpected phase %s", launch.Spec.Phase)
		}
	}
	if primary == nil || finalization == nil {
		t.Fatalf("Reason Finalization phases: primary=%v finalization=%v", primary, finalization)
	}
	if primary.ContainerID == "" || finalization.ContainerID != primary.ContainerID ||
		primary.Handle.ExecID == "" || finalization.Handle.ExecID == "" ||
		primary.Handle.ExecID == finalization.Handle.ExecID ||
		primary.Spec.FencingToken != finalization.Spec.FencingToken ||
		primary.Spec.Task != rundomain.TaskReason || finalization.Spec.Task != rundomain.TaskReason {
		t.Fatalf("Reason Finalization runtime identity: primary=%+v finalization=%+v", primary, finalization)
	}
	if deliveryCommandValue(primary.Spec.Command, "--session") != "" ||
		deliveryCommandValue(finalization.Spec.Command, "--session") == "" {
		t.Fatalf("Reason Finalization Session lineage: primary=%v finalization=%v",
			primary.Spec.Command, finalization.Spec.Command)
	}
	finalizationCommand := strings.Join(finalization.Spec.Command, "\n")
	if !strings.Contains(finalizationCommand, piFinalizationFixtureMarker) ||
		!strings.Contains(finalizationCommand, "Pi Result File Contract: aether.pi-result-file/v1") ||
		!strings.Contains(finalizationCommand, "result.toml") ||
		strings.Contains(finalizationCommand, "private invalid Reason response") {
		t.Fatalf("Reason Finalization Prompt contract: %v", finalization.Spec.Command)
	}
	if primary.Spec.RenderedInput == nil || finalization.Spec.RenderedInput == nil ||
		primary.Spec.RenderedInput.Path != rundomain.BlackboardPath(runIDs[0], rundomain.PhaseReason) ||
		finalization.Spec.RenderedInput.Path != rundomain.BlackboardPath(runIDs[0], rundomain.PhaseReasonFinalization) ||
		primary.Spec.RenderedInput.Hash != finalization.Spec.RenderedInput.Hash ||
		!bytes.Equal(primary.Spec.RenderedInput.Bytes, finalization.Spec.RenderedInput.Bytes) {
		t.Fatalf("Reason Finalization changed input identity: primary=%+v finalization=%+v",
			primary.Spec.RenderedInput, finalization.Spec.RenderedInput)
	}
	primaryOutput := piOutputForPhase(t, outputs, runIDs[0], rundomain.PhaseReason)
	finalizationOutput := piOutputForPhase(t, outputs, runIDs[0], rundomain.PhaseReasonFinalization)
	if primaryOutput.Err != nil || !primaryOutput.Output.Exited || primaryOutput.Output.ExitCode != 0 ||
		!strings.Contains(primaryOutput.Output.Stdout, "result is stored in result.toml") ||
		finalizationOutput.Err != nil || !finalizationOutput.Output.Exited || finalizationOutput.Output.ExitCode != 0 ||
		!strings.Contains(finalizationOutput.Output.Stdout, "result is stored in result.toml") {
		t.Fatalf("Reason Finalization outputs: primary=%+v finalization=%+v", primaryOutput, finalizationOutput)
	}
}

func assertPiCaptureScenario(
	t *testing.T,
	server *httptest.Server,
	projectID, expectedFact string,
	status rundomain.Status,
	expectedFailure string,
	expectedSource rundomain.ResultEvidenceSource,
) {
	t.Helper()
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != status {
		t.Fatalf("Pi capture scenario Activity: %+v", activity.Runs)
	}
	if expectedFailure != "" && activity.Runs[0].CaptureFailureReason != rundomain.CaptureFailureReason(expectedFailure) {
		t.Fatalf("Pi capture failure reason=%q, want %q", activity.Runs[0].CaptureFailureReason, expectedFailure)
	}
	if activity.Runs[0].ResultEvidenceSource != expectedSource {
		t.Fatalf("Pi capture evidence source=%q, want %q", activity.Runs[0].ResultEvidenceSource, expectedSource)
	}
	if status == rundomain.StatusCommitted {
		detail := getDeliveryProject(t, server, projectID)
		if detail.Project.Status != "completed" || countDeliveryFact(detail, expectedFact) != 1 {
			t.Fatalf("Pi capture scenario did not commit expected Fact: %+v", detail)
		}
	}
}

func assertPiVerboseDelivery(t *testing.T, server *httptest.Server, projectID string, control *controlplane.HTTPRunControl) {
	t.Helper()
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeCompleteProject ||
		activity.Runs[0].CaptureFailureReason != "" ||
		activity.Runs[0].ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
		t.Fatalf("verbose Pi Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "completed" || detail.Project.Revision != 2 ||
		countDeliveryFact(detail, "Pi verbose fact") != 1 {
		t.Fatalf("verbose Pi result did not commit exactly once: %+v", detail)
	}
	activityRun := activity.Runs[0]
	var runDetail rundomain.ActivityRunDetail
	deliveryGetJSON(t, server, "/api/v1/projects/"+projectID+"/activity/"+string(activityRun.ID), &runDetail)
	candidate := json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"Pi verbose fact","fact":{"description":"Pi verbose fact"}}}`)
	if _, err := control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: activityRun.ID, FencingToken: runDetail.FencingToken + 1, Candidate: candidate,
		ResultEvidenceSource: rundomain.ResultEvidenceSourceFile,
	}); !isPiDomainError(err, rundomain.ErrorFenced) {
		t.Fatalf("wrong fencing token was accepted: %v", err)
	}
	replayed, err := control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: activityRun.ID, FencingToken: runDetail.FencingToken, Candidate: candidate,
		ResultEvidenceSource: rundomain.ResultEvidenceSourceFile,
	})
	if err != nil || !replayed.Replayed || replayed.Status != rundomain.StatusCommitted {
		t.Fatalf("Pi Finish replay: receipt=%+v error=%v", replayed, err)
	}
	if detail := getDeliveryProject(t, server, projectID); countDeliveryFact(detail, "Pi verbose fact") != 1 {
		t.Fatalf("Finish replay wrote a second Fact: %+v", detail)
	}
}

func assertPiBootstrapPrimaryRecovery(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	if len(runIDs) != 1 {
		t.Fatalf("Bootstrap recovery created %d Runs: %v", len(runIDs), runIDs)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeCompleteProject ||
		activity.Runs[0].ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
		t.Fatalf("Bootstrap recovery Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "completed" || detail.Project.Revision != 2 ||
		countDeliveryFact(detail, "Pi Bootstrap recovery fact") != 1 {
		t.Fatalf("Bootstrap recovery did not commit exactly once: %+v", detail)
	}
	launches, outputs := runtime.Snapshot()
	var primary, recovery *deliveryLaunchObservation
	for index := range launches {
		launch := launches[index]
		if launch.Spec.RunID != activity.Runs[0].ID {
			continue
		}
		switch launch.Spec.Phase {
		case rundomain.PhaseBootstrap:
			primary = &launch
		case rundomain.PhaseBootstrapRecovery:
			recovery = &launch
		case rundomain.PhaseBootstrapConclude:
			t.Fatalf("Bootstrap recovery entered Conclude after valid Recovery")
		}
	}
	if primary == nil || recovery == nil {
		t.Fatalf("Bootstrap recovery phases: primary=%v recovery=%v", primary, recovery)
	}
	primaryOutput := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrap)
	if primaryOutput.Err != nil || !primaryOutput.Output.Exited || primaryOutput.Output.ExitCode != 9 ||
		primaryOutput.Output.TimedOut || primaryOutput.Output.Cancelled {
		t.Fatalf("Bootstrap primary abnormal output: %+v", primaryOutput)
	}
	recoveryOutput := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrapRecovery)
	if recoveryOutput.Err != nil || !recoveryOutput.Output.Exited || recoveryOutput.Output.ExitCode != 0 ||
		recoveryOutput.Output.TimedOut || recoveryOutput.Output.Cancelled ||
		!strings.Contains(recoveryOutput.Output.Stdout, "Pi Bootstrap recovery fact") {
		t.Fatalf("Bootstrap recovery output: %+v", recoveryOutput)
	}
	if primary.ContainerID == "" || recovery.ContainerID != primary.ContainerID {
		t.Fatalf("Bootstrap recovery changed Project container: primary=%q recovery=%q", primary.ContainerID, recovery.ContainerID)
	}
	if deliveryCommandValue(recovery.Spec.Command, "--session") == "" {
		t.Fatalf("Bootstrap recovery did not reuse the opaque Session: %v", recovery.Spec.Command)
	}
	if primary.Spec.RenderedInput == nil || recovery.Spec.RenderedInput == nil || primary.Spec.RenderedInput.Path != "" ||
		recovery.Spec.RenderedInput.Path == "" || primary.Spec.RenderedInput.Hash != recovery.Spec.RenderedInput.Hash ||
		!bytes.Equal(primary.Spec.RenderedInput.Bytes, recovery.Spec.RenderedInput.Bytes) {
		t.Fatalf("Bootstrap recovery changed input identity: primary=%+v recovery=%+v", primary.Spec.RenderedInput, recovery.Spec.RenderedInput)
	}
}

func assertPiBootstrapConcludeRecovery(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	if len(runIDs) != 1 {
		t.Fatalf("Bootstrap conclude recovery created %d Runs: %v", len(runIDs), runIDs)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	launches, outputs := runtime.Snapshot()
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeConcludeIntent ||
		activity.Runs[0].ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
		t.Fatalf("Bootstrap conclude recovery Activity: %+v launches=%+v outputs=%+v", activity.Runs, launches, outputs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 2 ||
		countDeliveryFact(detail, "Pi Bootstrap conclude recovery fact") != 1 ||
		countDeliveryFact(detail, "abnormal conclude candidate") != 0 {
		t.Fatalf("Bootstrap conclude recovery changed Blackboard incorrectly: %+v", detail)
	}
	var primary, conclude, recovery *deliveryLaunchObservation
	for index := range launches {
		launch := launches[index]
		if launch.Spec.RunID != activity.Runs[0].ID {
			continue
		}
		switch launch.Spec.Phase {
		case rundomain.PhaseBootstrap:
			primary = &launch
		case rundomain.PhaseBootstrapConclude:
			conclude = &launch
		case rundomain.PhaseBootstrapConcludeRecovery:
			recovery = &launch
		}
	}
	if primary == nil || conclude == nil || recovery == nil {
		t.Fatalf("Bootstrap conclude recovery phases: primary=%v conclude=%v recovery=%v", primary, conclude, recovery)
	}
	concludeOutput := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrapConclude)
	if concludeOutput.Err != nil || !concludeOutput.Output.Exited || concludeOutput.Output.ExitCode != 9 ||
		concludeOutput.Output.TimedOut || concludeOutput.Output.Cancelled ||
		!strings.Contains(concludeOutput.Output.Stdout, "abnormal conclude candidate") {
		t.Fatalf("Bootstrap abnormal conclude output: %+v", concludeOutput)
	}
	recoveryOutput := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrapConcludeRecovery)
	if recoveryOutput.Err != nil || !recoveryOutput.Output.Exited || recoveryOutput.Output.ExitCode != 0 ||
		recoveryOutput.Output.TimedOut || recoveryOutput.Output.Cancelled ||
		!strings.Contains(recoveryOutput.Output.Stdout, "Pi Bootstrap conclude recovery fact") {
		t.Fatalf("Bootstrap conclude recovery output: %+v", recoveryOutput)
	}
	if primary.ContainerID == "" || conclude.ContainerID != primary.ContainerID || recovery.ContainerID != primary.ContainerID {
		t.Fatalf("Bootstrap conclude recovery changed Project container: primary=%q conclude=%q recovery=%q",
			primary.ContainerID, conclude.ContainerID, recovery.ContainerID)
	}
	concludeSession := deliveryCommandValue(conclude.Spec.Command, "--session")
	if concludeSession == "" || deliveryCommandValue(recovery.Spec.Command, "--session") != concludeSession {
		t.Fatalf("Bootstrap conclude recovery changed Session: conclude=%v recovery=%v", conclude.Spec.Command, recovery.Spec.Command)
	}
	if !strings.Contains(strings.Join(conclude.Spec.Command, "\n"), "--tools\nread,write,edit,bash,grep,find,ls") ||
		!strings.Contains(strings.Join(recovery.Spec.Command, "\n"), "--tools\nread,write,edit,bash,grep,find,ls") {
		t.Fatalf("Bootstrap conclude recovery tool mode: conclude=%v recovery=%v", conclude.Spec.Command, recovery.Spec.Command)
	}
	if conclude.Spec.RenderedInput == nil || recovery.Spec.RenderedInput == nil ||
		conclude.Spec.RenderedInput.Path == recovery.Spec.RenderedInput.Path || recovery.Spec.RenderedInput.Path == "" ||
		conclude.Spec.RenderedInput.Hash != recovery.Spec.RenderedInput.Hash ||
		!bytes.Equal(conclude.Spec.RenderedInput.Bytes, recovery.Spec.RenderedInput.Bytes) {
		t.Fatalf("Bootstrap conclude recovery changed input identity: conclude=%+v recovery=%+v",
			conclude.Spec.RenderedInput, recovery.Spec.RenderedInput)
	}
}

func assertPiSameRunRecovery(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	if len(runIDs) != 1 {
		t.Fatalf("same-run recovery created %d Runs: %v", len(runIDs), runIDs)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeConcludeIntent ||
		activity.Runs[0].ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
		t.Fatalf("same-run recovery Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 2 ||
		countDeliveryFact(detail, "Pi conclude fact") != 1 {
		t.Fatalf("same-run conclude changed Blackboard incorrectly: %+v", detail)
	}
	launches, outputs := runtime.Snapshot()
	primary, fallback := piRunPhases(t, launches, activity.Runs[0].ID)
	if deliveryCommandValue(primary.Spec.Command, "--session") != "" {
		t.Fatalf("Pi primary unexpectedly resumed a Session: %v", primary.Spec.Command)
	}
	fallbackSession := deliveryCommandValue(fallback.Spec.Command, "--session")
	if fallbackSession == "" {
		t.Fatal("Pi conclude did not receive the primary Session")
	}
	primaryOutput := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrap)
	if primaryOutput.Err != nil || !primaryOutput.Output.Exited || !primaryOutput.Output.TimedOut ||
		primaryOutput.Output.Cancelled {
		t.Fatalf("same-run timeout output: %+v", primaryOutput)
	}
	fallbackOutput := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrapConclude)
	if fallbackOutput.Err != nil || !fallbackOutput.Output.Exited || fallbackOutput.Output.ExitCode != 0 ||
		fallbackOutput.Output.TimedOut || fallbackOutput.Output.Cancelled ||
		!strings.Contains(fallbackOutput.Output.Stdout, "Pi conclude fact") {
		t.Fatalf("same-run conclude output: %+v", fallbackOutput)
	}
	if primary.ContainerID == "" || fallback.ContainerID != primary.ContainerID {
		t.Fatalf("same-run conclude changed Project container: primary=%q fallback=%q", primary.ContainerID, fallback.ContainerID)
	}
}

func assertPiCompletionContinuation(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
	expectedFact string,
	expectedPhases int,
) {
	t.Helper()
	if len(runIDs) != 1 {
		t.Fatalf("Pi completion continuation created %d Runs: %v", len(runIDs), runIDs)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeConcludeIntent ||
		activity.Runs[0].ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
		t.Fatalf("Pi completion continuation Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 2 || countDeliveryFact(detail, expectedFact) != 1 {
		t.Fatalf("Pi completion continuation changed Blackboard incorrectly: %+v", detail)
	}
	launches, _ := runtime.Snapshot()
	var phases []deliveryLaunchObservation
	for _, launch := range launches {
		if launch.Spec.RunID == activity.Runs[0].ID {
			phases = append(phases, launch)
		}
	}
	if len(phases) != expectedPhases || phases[0].Spec.Phase != rundomain.PhaseBootstrap {
		t.Fatalf("Pi completion continuation phases=%+v", phases)
	}
	containerID := phases[0].ContainerID
	var session string
	for index, phase := range phases {
		if containerID == "" || phase.ContainerID != containerID {
			t.Fatalf("Pi completion continuation changed container: %+v", phases)
		}
		joined := strings.Join(phase.Spec.Command, "\n")
		if !strings.Contains(joined, "--tools\nread,write,edit,bash,grep,find,ls") || strings.Contains(joined, "--no-tools") {
			t.Fatalf("Pi completion phase %d changed tool policy: %v", index, phase.Spec.Command)
		}
		currentSession := deliveryCommandValue(phase.Spec.Command, "--session")
		if index == 0 {
			if currentSession != "" {
				t.Fatalf("Pi Primary unexpectedly resumed Session %q", currentSession)
			}
			continue
		}
		if phase.Spec.Phase != rundomain.PhaseBootstrapConclude || currentSession == "" {
			t.Fatalf("Pi successor phase/session: %+v", phase)
		}
		if session == "" {
			session = currentSession
		} else if currentSession != session {
			t.Fatalf("Pi successor changed Session: first=%q current=%q", session, currentSession)
		}
		if phase.Spec.RenderedInput == nil || phases[0].Spec.RenderedInput == nil ||
			phase.Spec.RenderedInput.Hash != phases[0].Spec.RenderedInput.Hash ||
			!bytes.Equal(phase.Spec.RenderedInput.Bytes, phases[0].Spec.RenderedInput.Bytes) {
			t.Fatalf("Pi successor changed rendered input: primary=%+v successor=%+v",
				phases[0].Spec.RenderedInput, phase.Spec.RenderedInput)
		}
	}
}

func assertPiUnverifiedTimeout(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	if len(runIDs) != 1 {
		t.Fatalf("unverified Pi timeout created %d Runs: %v", len(runIDs), runIDs)
	}
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusFailed ||
		activity.Runs[0].ErrorCode != rundomain.ExecutionDeadline || activity.Runs[0].CaptureFailureReason != "" ||
		activity.Runs[0].ResultEvidenceSource != "" {
		t.Fatalf("unverified Pi timeout Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 1 || len(detail.Facts) != 2 ||
		countDeliveryFact(detail, "ambiguous timeout continuation must not run") != 0 {
		t.Fatalf("unverified Pi timeout changed Blackboard: %+v", detail)
	}
	launches, outputs := runtime.Snapshot()
	phaseCount := 0
	for _, launch := range launches {
		if launch.Spec.RunID == activity.Runs[0].ID {
			phaseCount++
			if launch.Spec.Phase != rundomain.PhaseBootstrap {
				t.Fatalf("unverified Pi timeout launched continuation phase %s", launch.Spec.Phase)
			}
		}
	}
	if phaseCount != 1 {
		t.Fatalf("unverified Pi timeout launched %d phases, want only Primary", phaseCount)
	}
	output := piOutputForPhase(t, outputs, activity.Runs[0].ID, rundomain.PhaseBootstrap)
	if output.Err != nil || !output.Output.Exited || !output.Output.TimedOut || output.Output.Cancelled {
		t.Fatalf("unverified Pi timeout output: %+v", output)
	}
}

func assertPiOversizedResultFile(t *testing.T, server *httptest.Server, projectID, intentID string) {
	t.Helper()
	activity, activityJSON := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusFailed ||
		activity.Runs[0].ErrorCode != rundomain.ExecutionCaptureFailed ||
		activity.Runs[0].CaptureFailureReason != rundomain.CaptureFailureCandidateTooLarge ||
		activity.Runs[0].OutcomeKind != "" || activity.Runs[0].ResultEvidenceSource != "" {
		t.Fatalf("oversized Pi result file Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 1 || len(detail.Facts) != 2 {
		t.Fatalf("oversized Pi result file changed Blackboard: %+v", detail)
	}
	evidencePath := "/api/v1/projects/" + projectID + "/execution-evidence?intent_id=" + intentID
	var evidence deliveryWorkspaceExecutionEvidence
	evidenceJSON := deliveryGetJSON(t, server, evidencePath, &evidence)
	if evidence.SelectedIntentRun == nil || evidence.SelectedIntentRun.Status != rundomain.StatusFailed ||
		evidence.SelectedIntentRun.ErrorCode != rundomain.ExecutionCaptureFailed ||
		evidence.SelectedIntentRun.CaptureFailureReason != rundomain.CaptureFailureCandidateTooLarge {
		t.Fatalf("oversized Pi result file Workspace Execution Evidence: %+v", evidence)
	}
	if !bytes.Contains(activityJSON, []byte(`"capture_failure_reason":"candidate_too_large"`)) ||
		bytes.Contains(append(activityJSON, evidenceJSON...), []byte("300000")) {
		t.Fatalf("oversized result file evidence was not stable and bounded: activity=%s evidence=%s", activityJSON, evidenceJSON)
	}
}

func assertPiRetryChain(
	t *testing.T,
	server *httptest.Server,
	projectID, intentID string,
	runIDs []rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	activity, activityJSON := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 2 {
		t.Fatalf("Pi retry chain length=%d: %+v", len(activity.Runs), activity.Runs)
	}
	for index, run := range activity.Runs {
		wantAttempt := int64(2 - index)
		if run.Attempt != wantAttempt || run.Status != rundomain.StatusFailed ||
			run.ErrorCode != rundomain.ExecutionCaptureFailed ||
			run.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate ||
			run.CaptureDiagnostic != rundomain.CaptureDiagnosticResultFileInvalid ||
			run.Worker != "pi-retry" || run.OutcomeKind != "" || run.ResultEvidenceSource != "" {
			t.Fatalf("Pi retry attempt %d: %+v", wantAttempt, run)
		}
		if index == 0 {
			if run.Attention != rundomain.AttentionRetryExhausted || run.NextEligibleAt != nil ||
				run.PreviousRunID == nil || *run.PreviousRunID != activity.Runs[1].ID {
				t.Fatalf("Pi retry exhaustion or lineage evidence: %+v", activity.Runs)
			}
		} else if run.PreviousRunID != nil {
			t.Fatalf("Pi first attempt unexpectedly has a predecessor: %+v", activity.Runs)
		}
	}
	if len(runIDs) != 2 || runIDs[0] == runIDs[1] {
		t.Fatalf("Pi retry Run identities: %v", runIDs)
	}
	launches, _ := runtime.Snapshot()
	firstPrimary, firstFallback := piRunPhases(t, launches, activity.Runs[1].ID)
	secondPrimary, secondFallback := piRunPhases(t, launches, activity.Runs[0].ID)
	if deliveryCommandValue(firstPrimary.Spec.Command, "--session") != "" ||
		deliveryCommandValue(secondPrimary.Spec.Command, "--session") != "" {
		t.Fatal("technical retry primary unexpectedly resumed a Session")
	}
	firstSession := deliveryCommandValue(firstFallback.Spec.Command, "--session")
	secondSession := deliveryCommandValue(secondFallback.Spec.Command, "--session")
	if firstSession == "" || secondSession == "" || firstSession == secondSession {
		t.Fatalf("Pi technical retry Session semantics: first=%q second=%q", firstSession, secondSession)
	}
	retryChainJSON := deliveryGetJSON(t, server, "/api/v1/projects/"+projectID+"/activity/"+string(activity.Runs[0].ID)+"/retry-chain", nil)
	var chain []rundomain.ActivityRun
	if err := json.Unmarshal(retryChainJSON, &chain); err != nil {
		t.Fatal(err)
	}
	sort.Slice(chain, func(i, j int) bool { return chain[i].Attempt > chain[j].Attempt })
	if len(chain) != 2 || chain[0].ID != activity.Runs[0].ID || chain[1].ID != activity.Runs[1].ID {
		t.Fatalf("Pi Retry Chain projection: %+v", chain)
	}
	var evidence deliveryWorkspaceExecutionEvidence
	evidenceJSON := deliveryGetJSON(t, server, "/api/v1/projects/"+projectID+"/execution-evidence?intent_id="+intentID, &evidence)
	if evidence.SelectedIntentRun == nil || evidence.SelectedIntentRun.RunID != string(activity.Runs[0].ID) ||
		evidence.SelectedIntentRun.Attempt != 2 || evidence.SelectedIntentRun.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate {
		t.Fatalf("Pi retry Workspace Execution Evidence: %+v", evidence)
	}
	if bytes.Contains(append(append(activityJSON, retryChainJSON...), evidenceJSON...), []byte("not a result")) {
		t.Fatal("Pi raw final text leaked into retry evidence")
	}
}

func assertPiCancellation(
	t *testing.T,
	server *httptest.Server,
	projectID string,
	runID rundomain.RunID,
	runtime *deliveryObservedRuntime,
) {
	t.Helper()
	activity, _ := deliveryActivity(t, server, projectID)
	if len(activity.Runs) != 1 || activity.Runs[0].ID != runID || activity.Runs[0].Status != rundomain.StatusInterrupted ||
		activity.Runs[0].ErrorCode != rundomain.ErrorDispatcherRestarted || activity.Runs[0].OutcomeKind != "" ||
		activity.Runs[0].ResultEvidenceSource != "" {
		t.Fatalf("Pi cancellation Activity: %+v", activity.Runs)
	}
	output := piOutputForPhase(t, runtimeSnapshotOutputs(runtime), runID, rundomain.PhaseBootstrap)
	if output.Err != nil || !output.Output.Exited || !output.Output.Cancelled || output.Output.TimedOut {
		t.Fatalf("Pi cancellation output: %+v", output)
	}
}

func assertPiProjectionRedaction(
	t *testing.T,
	server *httptest.Server,
	scenarios map[string]deliveryProjectDetail,
	intentIDs map[string]string,
) {
	t.Helper()
	var projected []byte
	for _, name := range []string{
		"reason-finalization", "verbose",
		"capture-conclude", "conclude-finalization",
		"timeout-session-missing", "timeout-session-malformed", "timeout-session-oversized", "timeout-session-inconsistent",
		"bootstrap-recovery", "bootstrap-conclude-recovery", "recovery", "oversized", "retry", "cancel",
	} {
		activity, activityJSON := deliveryActivity(t, server, scenarios[name].Project.ID)
		projected = append(projected, activityJSON...)
		for _, run := range activity.Runs {
			projected = append(projected, deliveryGetJSON(t, server,
				"/api/v1/projects/"+scenarios[name].Project.ID+"/activity/"+string(run.ID), nil)...)
		}
		if len(activity.Runs) > 0 {
			projected = append(projected, deliveryGetJSON(t, server,
				"/api/v1/projects/"+scenarios[name].Project.ID+"/activity/"+string(activity.Runs[0].ID)+"/retry-chain", nil)...)
		}
		if intentID := intentIDs[name]; intentID != "" {
			var evidence deliveryWorkspaceExecutionEvidence
			projected = append(projected, deliveryGetJSON(t, server,
				"/api/v1/projects/"+scenarios[name].Project.ID+"/execution-evidence?intent_id="+intentID, &evidence)...)
		}
	}
	for _, prohibited := range []string{
		piRecoverySecret, "private invalid Reason response", "not a result", "timeout evidence must not commit", "bootstrap Pi recovery fixture", "conclude Pi recovery fixture",
		"session_id", "container_id", "environment", "stderr",
	} {
		if bytes.Contains(projected, []byte(prohibited)) {
			t.Fatalf("Pi read-only execution projection exposed %q", prohibited)
		}
	}
}

func TestPiRecoveryWorkerCatalogBindsDistinctFinalizationTemplate(t *testing.T) {
	catalog := piRecoveryWorkerCatalog(t, "fixture-image", "pi-finalization-contract", "conclude-finalization", "fixture-revision")
	prompts, ok := catalog.Prompt(rundomain.TaskBootstrap)
	if !ok {
		t.Fatal("Bootstrap Prompt Set is missing")
	}
	if !strings.Contains(prompts.Finalization, piFinalizationFixtureMarker) || prompts.Finalization == prompts.Fallback {
		t.Fatalf("Bootstrap Finalization template=%q fallback=%q", prompts.Finalization, prompts.Fallback)
	}
}

func piRecoveryWorkerCatalog(t *testing.T, image, workerName, mode, configRevision string) *worker.Catalog {
	t.Helper()
	taskTypes := []rundomain.TaskType{rundomain.TaskBootstrap}
	capabilities := []string{"bootstrap"}
	if mode == "reason-finalization" {
		taskTypes = []rundomain.TaskType{rundomain.TaskReason}
		capabilities = []string{"reason"}
	}
	recoveryPrompt := ""
	recoveryConcludePrompt := ""
	if mode == "bootstrap-recovery" || mode == "bootstrap-conclude-recovery" {
		capabilities = append(capabilities, rundomain.ProcessRecoveryCapability)
		recoveryPrompt = "Continue the interrupted Bootstrap task."
		recoveryConcludePrompt = "Recover the interrupted Bootstrap conclude with the available tools, but return only conclude_intent or decline."
	}
	catalog, err := worker.NewCatalogForRevision(configRevision, worker.CatalogSource{
		ImageReference: image,
		Workers: []worker.CatalogWorker{{
			Name: workerName, Adapter: string(worker.WorkerTypePi), Model: "fixture-model",
			TaskTypes: taskTypes, MaxRunning: 1, Priority: 0,
			Environment: map[string]string{
				"PI_BASE_URL": "http://127.0.0.1:1/" + mode, "PI_MODEL": "fixture-model",
				"PI_PROVIDER_API": "openai-completions",
			},
			Capabilities: capabilities,
			SecretReferences: []rundomain.SecretReference{{
				EnvironmentVariable: "PI_API_KEY", Provider: "configuration", Key: piRecoveryToken,
			}},
		}},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason: {
				Primary:      "reason Pi recovery fixture",
				Finalization: piFinalizationFixtureMarker + " re-serialize the confirmed Reason result.",
			},
			rundomain.TaskExplore: {
				Primary: "explore Pi recovery fixture", Fallback: "conclude Pi recovery fixture",
				Finalization: piFinalizationFixtureMarker + " re-serialize the confirmed Explore result.",
			},
			rundomain.TaskBootstrap: {
				Primary: "bootstrap Pi recovery fixture", Fallback: "conclude Pi recovery fixture",
				RecoveryPrimary: recoveryPrompt, RecoveryConclude: recoveryConcludePrompt,
				Finalization: piFinalizationFixtureMarker + " re-serialize the confirmed Bootstrap result.",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func piDeliveryPolicy(maxAttempts int, withRecovery bool) scheduler.RunSchedulerPolicy {
	retry := rundomain.RetryPolicy{
		MaxAttempts: maxAttempts, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond,
		Multiplier: 1, DeclineCooldown: time.Second,
	}
	recoverySeconds := 0
	phasedRunSeconds := 6
	if withRecovery {
		recoverySeconds = 1
		phasedRunSeconds = 8
	}
	return scheduler.RunSchedulerPolicy{
		PollInterval: time.Second, MaxWorkers: 1, MaxProjectWorkers: 1, MaxRunningProjects: 1,
		ExecutionPolicies: executionPolicyTableForWorkers(
			[]string{"__test_worker__"}, retry, 1, 1, recoverySeconds, 1, 4, phasedRunSeconds, 2,
		),
	}
}

func provisionPiRecoveryFakeCLI(
	t *testing.T,
	runtime *dockerruntime.Runtime,
	dockerAPI *dockerclient.Client,
	image, projectID string,
) string {
	t.Helper()
	input := []byte("schema: aether.bootstrap-input/v1\ngeneration: 1\nrevision: 1\n")
	digest := sha256.Sum256(input)
	runID := rundomain.RunID("pi-recovery-setup-" + projectID)
	process, err := runtime.Launch(t.Context(), rundomain.LaunchSpec{
		RunID: runID, ProjectID: projectID, Task: rundomain.TaskBootstrap, Phase: rundomain.PhaseBootstrap,
		FencingToken: 1, ConfigRevision: "pi-recovery-fixture-setup", Image: image,
		Command: []string{
			"/bin/sh", "-c",
			`umask 077; mkdir -p /home/kali/.local/bin; printf '%s' "$1" > /home/kali/.local/bin/pi; chmod 700 /home/kali/.local/bin/pi`,
			"pi-recovery-fixture", piRecoveryFakeCLI,
		},
		RenderedInput: &rundomain.RenderedInput{Bytes: input, Hash: rundomain.RenderedInputHash(hex.EncodeToString(digest[:]))},
		PhaseTimeout:  5 * time.Second, StopGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(t.Context())
	if err != nil || !output.Exited || output.ExitCode != 0 {
		t.Fatalf("provision fake Pi script: output=%+v error=%v", output, err)
	}
	if err := runtime.CompleteRun(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	handle, ok := process.(rundomain.ProcessHandle)
	if !ok || handle.Handle().ContainerID == "" {
		t.Fatal("Pi fixture setup returned no Project container handle")
	}
	containerID := handle.Handle().ContainerID
	runDeliveryRootExec(t, dockerAPI, containerID, []string{
		"/bin/sh", "-c", "rm -f /usr/local/bin/pi && ln -s /home/kali/.local/bin/pi /usr/local/bin/pi",
	})
	return containerID
}

func piRunPhases(
	t *testing.T,
	launches []deliveryLaunchObservation,
	runID rundomain.RunID,
) (deliveryLaunchObservation, deliveryLaunchObservation) {
	t.Helper()
	var primary, fallback *deliveryLaunchObservation
	for index := range launches {
		launch := launches[index]
		if launch.Spec.RunID != runID {
			continue
		}
		switch launch.Spec.Phase {
		case rundomain.PhaseBootstrap:
			primary = &launch
		case rundomain.PhaseBootstrapConclude:
			fallback = &launch
		}
	}
	if primary == nil || fallback == nil {
		t.Fatalf("Pi Run phases for %s: primary=%v fallback=%v", runID, primary, fallback)
	}
	return *primary, *fallback
}

func piOutputForPhase(
	t *testing.T,
	outputs []deliveryOutputObservation,
	runID rundomain.RunID,
	phase rundomain.ExecutionPhase,
) deliveryOutputObservation {
	t.Helper()
	for _, output := range outputs {
		if output.RunID == runID && output.Phase == phase {
			return output
		}
	}
	t.Fatalf("Pi Run %s has no %s output", runID, phase)
	return deliveryOutputObservation{}
}

func runtimeSnapshotOutputs(runtime *deliveryObservedRuntime) []deliveryOutputObservation {
	_, outputs := runtime.Snapshot()
	return outputs
}

func waitForPiLaunch(
	t *testing.T,
	runtime *deliveryObservedRuntime,
	projectID string,
	baseline int,
) deliveryLaunchObservation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		launches, _ := runtime.Snapshot()
		for _, launch := range launches[baseline:] {
			if launch.Spec.ProjectID == projectID && launch.Spec.Phase == rundomain.PhaseBootstrap &&
				!strings.Contains(string(launch.Spec.RunID), "-setup-") {
				return launch
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Pi Run launch for Project %s was not observed", projectID)
	return deliveryLaunchObservation{}
}

func isPiDomainError(err error, code rundomain.ErrorCode) bool {
	var domainErr *rundomain.DomainError
	return errors.As(err, &domainErr) && domainErr.Code == code
}
