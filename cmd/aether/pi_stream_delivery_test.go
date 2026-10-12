package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
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

const piVerboseFixture = `#!/bin/sh
set -eu

prompt=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -p) prompt=$2; shift 2 ;;
    *) shift ;;
  esac
done
result_file=$(node - "$prompt" <<'JS'
const prompt = process.argv[2] || "";
const match = prompt.match(/\/tmp\/aether-invocations\/[^\s]+\/result\.toml/);
process.stdout.write(match ? match[0] : "");
JS
)
if [ -z "$result_file" ]; then exit 64; fi
cat > "$result_file" <<'TOML'
[outcome]
kind = "complete_project"
description = "verbose Pi commit"

[outcome.fact]
description = "Pi final result"
TOML

node - <<'JS'
process.stdout.write(JSON.stringify({type: "session", version: 3, id: "0192f7e5-7b2a-7c01-8a11-223344556699", timestamp: "2026-08-27T13:15:30.123Z", cwd: process.cwd()}) + "\n");
JS
for _ in 1 2; do
  printf '{"type":"tool_execution_end","result":"%*s"}\n' 600000 ''
done
node - <<'JS'
const final = "verbose Pi commit";
const assistant = {role: "assistant", stopReason: "stop", content: [{type: "text", text: final}]};
for (const event of [{type: "agent_start"}, {type: "message_end", message: assistant}, {type: "turn_end", message: assistant}, {type: "agent_end", messages: [assistant]}]) process.stdout.write(JSON.stringify(event) + "\n");
JS
`

// TestPiVerboseRunResultDeliveryDockerIntegration proves the Pi JSONL capture
// through the Dispatcher and Control Plane with the generic 1 MiB output
// budget. It uses the locally provisioned Aether worker image and a deterministic CLI.
func TestPiVerboseRunResultDeliveryDockerIntegration(t *testing.T) {
	image := dockerIntegrationImage(t)

	host, err := controlplane.NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "pi-delivery-test-operator"},
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
		InstanceID: "pi-delivery-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	project := createDeliveryProject(t, server, deliveryProjectRequest{
		Title: "Verbose Pi delivery", Origin: "PI_VERBOSE_FIXTURE", Goal: "commit a bounded final result",
		BootstrapEnabled: true,
	})

	dockerAPI, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dockerAPI.Close() })
	baseRuntime, err := newDockerIntegrationRuntime(dockerruntime.Options{
		DeploymentID: "pi-delivery-test", DispatcherID: lease.InstanceID, DisposeGrace: time.Second,
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
	containerID := provisionDeliveryFakePi(t, baseRuntime, dockerAPI, image, project.Project.ID)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = dockerAPI.ContainerRemove(cleanupContext, containerID, dockerclient.ContainerRemoveOptions{Force: true})
	})

	observedRuntime := &deliveryObservedRuntime{delegate: baseRuntime}
	catalog := piVerboseWorkerCatalog(t, image, project.Project.ConfigRevision)
	secretResolver, err := rundomain.NewSecretResolver(map[string]rundomain.SecretProvider{
		"configuration": lookupSecretProvider{lookup: func(key string) (string, bool) {
			if key == "AETHER_PI_FIXTURE_TOKEN" {
				return "pi-fixture-secret", true
			}
			return "", false
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	executions, err := NewRunExecutionFactory(catalog, observedRuntime, secretResolver, control)
	if err != nil {
		t.Fatal(err)
	}

	runDeliverySchedulerPass(
		t, control, catalog, observedRuntime, executions, lease,
		project.Project.ConfigRevision, deliverySchedulerPolicy(), false,
	)
	activity, _ := deliveryActivity(t, server, project.Project.ID)
	if len(activity.Runs) != 1 || activity.Runs[0].Status != rundomain.StatusCommitted ||
		activity.Runs[0].OutcomeKind != rundomain.OutcomeCompleteProject ||
		activity.Runs[0].CaptureFailureReason != "" {
		var detail rundomain.ActivityRunDetail
		deliveryGetJSON(t, server, "/api/v1/projects/"+project.Project.ID+"/activity/"+string(activity.Runs[0].ID), &detail)
		t.Fatalf("verbose Pi activity: runs=%+v detail=%+v", activity.Runs, detail)
	}
	if detail := getDeliveryProject(t, server, project.Project.ID); detail.Project.Status != "completed" ||
		countDeliveryFact(detail, "Pi final result") != 1 {
		t.Fatalf("verbose Pi result did not commit exactly once: %+v", detail)
	}
	_, outputs := observedRuntime.Snapshot()
	for _, output := range outputs {
		if output.RunID != activity.Runs[0].ID || output.Phase != rundomain.PhaseBootstrap {
			continue
		}
		if output.Err != nil || !output.Output.Exited || output.Output.ExitCode != 0 || output.Output.ErrorCode != "" ||
			!strings.Contains(output.Output.Stdout, "verbose Pi commit") {
			t.Fatalf("verbose Pi process output: %+v", output)
		}
		return
	}
	t.Fatal("verbose Pi Run produced no observed Bootstrap output")
}

func piVerboseWorkerCatalog(t *testing.T, image, configRevision string) *worker.Catalog {
	t.Helper()
	catalog, err := worker.NewCatalogForRevision(configRevision, worker.CatalogSource{
		ImageReference: image,
		Workers: []worker.CatalogWorker{{
			Name: deliveryPrimaryWorker, Adapter: string(worker.WorkerTypePi), Model: "fixture-model",
			TaskTypes: []rundomain.TaskType{rundomain.TaskBootstrap}, MaxRunning: 1, Priority: 0,
			Environment: map[string]string{
				"PI_BASE_URL": "http://127.0.0.1:1", "PI_MODEL": "fixture-model", "PI_PROVIDER_API": "openai-completions",
			},
			Capabilities: []string{"bootstrap"},
			SecretReferences: []rundomain.SecretReference{{
				EnvironmentVariable: "PI_API_KEY", Provider: "configuration", Key: "AETHER_PI_FIXTURE_TOKEN",
			}},
		}},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason Pi delivery fixture", Finalization: "finalize reason Pi delivery fixture"},
			rundomain.TaskExplore:   {Primary: "explore Pi delivery fixture", Fallback: "conclude Pi delivery fixture", Finalization: "finalize Pi delivery fixture"},
			rundomain.TaskBootstrap: {Primary: "bootstrap Pi delivery fixture", Fallback: "conclude Pi delivery fixture", Finalization: "finalize Pi delivery fixture"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func provisionDeliveryFakePi(
	t *testing.T,
	runtime *dockerruntime.Runtime,
	dockerAPI *dockerclient.Client,
	image string,
	projectID string,
) string {
	t.Helper()
	input := []byte("schema: aether.bootstrap-input/v1\ngeneration: 1\nrevision: 1\n")
	digest := sha256.Sum256(input)
	runID := rundomain.RunID("pi-delivery-setup-" + projectID)
	process, err := runtime.Launch(t.Context(), rundomain.LaunchSpec{
		RunID: runID, ProjectID: projectID, Task: rundomain.TaskBootstrap, Phase: rundomain.PhaseBootstrap,
		FencingToken: 1, ConfigRevision: "pi-delivery-fixture-setup", Image: image,
		Command: []string{
			"/bin/sh", "-c",
			`umask 077; mkdir -p /home/kali/.local/bin; printf '%s' "$1" > /home/kali/.local/bin/pi; chmod 700 /home/kali/.local/bin/pi`,
			"pi-delivery-fixture", piVerboseFixture,
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
		t.Fatal("fixture setup returned no Project container handle")
	}
	containerID := handle.Handle().ContainerID
	runDeliveryRootExec(t, dockerAPI, containerID, []string{
		"/bin/sh", "-c", "rm -f /usr/local/bin/pi && ln -s /home/kali/.local/bin/pi /usr/local/bin/pi",
	})
	return containerID
}

var _ scheduler.RunResources = (*deliveryObservedRuntime)(nil)
