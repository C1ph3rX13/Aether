package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
	dockerruntime "github.com/C1ph3rX13/Aether/internal/docker"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/containerd/errdefs"
	dockerclient "github.com/moby/moby/client"
)

const multiRevisionWorker = "shared-worker"

func TestMultipleProjectRevisionsRunThroughOneDispatcherDockerIntegration(t *testing.T) {
	image := dockerIntegrationImage(t)
	provider := newMultiRevisionProvider(t)
	defer provider.Close()

	databasePath := filepath.Join(t.TempDir(), "aether.db")
	host, err := controlplane.NewControlPlaneHost(
		databasePath, nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "multi-revision-operator", DeploymentID: "multi-revision"},
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

	firstDraft := multiRevisionDraft(image, provider.URL+"/revision-a", "default", "none", "stop", 2)
	firstDraft.Tasks.Bootstrap.PrimarySeconds = 5
	firstDraft.Workers[0].TimeoutOverrides["bootstrap.primary"] = 1
	firstRead := saveMultiRevisionDraft(t, server, 1, firstDraft, "revision-a-secret")
	firstRevision := readMultiRevisionConfig(t, server)
	firstProject := createMultiRevisionProject(t, server, "Revision A")
	if firstProject.Project.ConfigRevision != firstRevision.ID {
		t.Fatalf("first Project Config Revision=%q, want %q", firstProject.Project.ConfigRevision, firstRevision.ID)
	}

	secondDraft := multiRevisionDraft(image, provider.URL+"/revision-b", "mock", "bridge", "remove", 1)
	secondDraft.Tasks.Bootstrap.PrimarySeconds = 4
	saveMultiRevisionDraft(t, server, firstRead.DraftVersion, secondDraft, "revision-b-secret")
	secondRevision := readMultiRevisionConfig(t, server)
	secondProject := createMultiRevisionProject(t, server, "Revision B")
	if secondProject.Project.ConfigRevision != secondRevision.ID || firstRevision.ID == secondRevision.ID {
		t.Fatalf("second Project Config Revision=%q, first=%q second=%q", secondProject.Project.ConfigRevision, firstRevision.ID, secondRevision.ID)
	}

	control, err := controlplane.NewInProcessRunControl(host.ControlPlane())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	lease, err := control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "multi-revision-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := control.SchedulingSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskBootstrap, rundomain.TaskReason, rundomain.TaskExplore},
		CandidateLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DispatcherPolicy.ConfigRevision != secondRevision.ID || snapshot.DispatcherPolicy.MaxWorkers != 2 || len(snapshot.Work) != 2 {
		t.Fatalf("multi-Revision Scheduling Snapshot: %+v", snapshot)
	}
	boundRevisions := map[string]string{}
	for _, work := range snapshot.Work {
		boundRevisions[work.ProjectID] = work.ConfigRevision
	}
	if boundRevisions[firstProject.Project.ID] != firstRevision.ID || boundRevisions[secondProject.Project.ID] != secondRevision.ID {
		t.Fatalf("candidate Config Revisions: %+v", boundRevisions)
	}

	firstConfig, err := host.ExecutionConfig(t.Context(), firstRevision.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondConfig, err := host.ExecutionConfig(t.Context(), secondRevision.ID)
	if err != nil {
		t.Fatal(err)
	}
	dockerAPI, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dockerAPI.Close() })
	containers := provisionMultiRevisionProjects(
		t, dockerAPI, lease, firstConfig, secondConfig,
		map[string]multiRevisionFixture{
			firstProject.Project.ID:  {revision: firstRevision.ID, token: "revision-a-secret", fact: "revision A complete", delay: "sleep 30"},
			secondProject.Project.ID: {revision: secondRevision.ID, token: "revision-b-secret", fact: "revision B complete", delay: ":"},
		},
	)
	for _, containerID := range containers {
		containerID := containerID
		t.Cleanup(func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = dockerAPI.ContainerRemove(cleanupContext, containerID, dockerclient.ContainerRemoveOptions{Force: true})
		})
	}

	runMultiRevisionSchedulerPass(t, host, control, lease, secondConfig, dockerAPI.DaemonHost())
	firstActivity, _ := deliveryActivity(t, server, firstProject.Project.ID)
	secondActivity, _ := deliveryActivity(t, server, secondProject.Project.ID)
	if len(firstActivity.Runs) != 1 || firstActivity.Runs[0].Status != rundomain.StatusFailed ||
		firstActivity.Runs[0].FinishedAt == nil || firstActivity.Runs[0].FinishedAt.Sub(firstActivity.Runs[0].StartedAt) < 900*time.Millisecond ||
		len(secondActivity.Runs) != 1 || secondActivity.Runs[0].Status != rundomain.StatusCommitted {
		t.Fatalf("first scheduling pass: revision-a=%+v revision-b=%+v", firstActivity.Runs, secondActivity.Runs)
	}
	firstRun, secondRun := firstActivity.Runs[0], secondActivity.Runs[0]
	if firstRun.FinishedAt == nil || secondRun.FinishedAt == nil ||
		!firstRun.StartedAt.Before(*secondRun.FinishedAt) || !secondRun.StartedAt.Before(*firstRun.FinishedAt) {
		t.Fatalf("same-named Revision Workers did not overlap: first=%+v second=%+v", firstRun, secondRun)
	}
	assertMultiRevisionContainerNetwork(t, dockerAPI, containers[firstProject.Project.ID], "none", firstRevision.ID)
	assertMultiRevisionContainerNetwork(t, dockerAPI, containers[secondProject.Project.ID], "bridge", secondRevision.ID)

	waitForDeliveryRetry(t, server, firstProject.Project.ID, 1)
	runMultiRevisionSchedulerPass(t, host, control, lease, secondConfig, dockerAPI.DaemonHost())
	firstActivity, _ = deliveryActivity(t, server, firstProject.Project.ID)
	secondActivity, _ = deliveryActivity(t, server, secondProject.Project.ID)
	if len(firstActivity.Runs) != 2 || firstActivity.Runs[0].Attempt != 2 || firstActivity.Runs[0].Status != rundomain.StatusCommitted ||
		firstActivity.Runs[0].Worker != multiRevisionWorker || firstActivity.Runs[1].Worker != multiRevisionWorker ||
		firstActivity.Runs[0].PreviousRunID == nil || *firstActivity.Runs[0].PreviousRunID != firstActivity.Runs[1].ID ||
		len(secondActivity.Runs) != 1 || secondActivity.Runs[0].Worker != multiRevisionWorker {
		t.Fatalf("Revision-scoped retry budgets: revision-a=%+v revision-b=%+v", firstActivity.Runs, secondActivity.Runs)
	}

	var firstDetail, secondDetail rundomain.ActivityRunDetail
	deliveryGetJSON(t, server, "/api/v1/projects/"+firstProject.Project.ID+"/activity/"+string(firstActivity.Runs[0].ID), &firstDetail)
	deliveryGetJSON(t, server, "/api/v1/projects/"+secondProject.Project.ID+"/activity/"+string(secondActivity.Runs[0].ID), &secondDetail)
	if firstDetail.ConfigRevision != firstRevision.ID || secondDetail.ConfigRevision != secondRevision.ID ||
		firstDetail.ImageReference != image || secondDetail.ImageReference != image ||
		firstDetail.PromptEvidence.SHA256 == secondDetail.PromptEvidence.SHA256 {
		t.Fatalf("Revision-scoped execution evidence: first=%+v second=%+v", firstDetail, secondDetail)
	}

	reconcileMultiRevisionContainers(
		t, host, control, lease, secondConfig, dockerAPI,
		containers[firstProject.Project.ID], containers[secondProject.Project.ID],
	)
	firstInspection, err := dockerAPI.ContainerInspect(t.Context(), containers[firstProject.Project.ID], dockerclient.ContainerInspectOptions{})
	if err != nil || firstInspection.Container.State == nil || firstInspection.Container.State.Running {
		t.Fatalf("Revision A completed_action=stop: inspection=%+v error=%v", firstInspection.Container.State, err)
	}
	if _, err := dockerAPI.ContainerInspect(t.Context(), containers[secondProject.Project.ID], dockerclient.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("Revision B completed_action=remove left container: %v", err)
	}
	provider.assertRevisionCredentials(t)
}

type multiRevisionProject struct {
	Project struct {
		ID             string `json:"id"`
		ConfigRevision string `json:"config_revision"`
	} `json:"project"`
}

type multiRevisionFixture struct {
	revision string
	token    string
	fact     string
	delay    string
}

type multiRevisionProvider struct {
	*httptest.Server
	mu   sync.Mutex
	seen map[string]int
}

func newMultiRevisionProvider(t *testing.T) *multiRevisionProvider {
	t.Helper()
	provider := &multiRevisionProvider{seen: map[string]int{}}
	provider.Server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		key := strings.TrimSuffix(request.URL.Path, "/v1/messages") + "|" + request.Header.Get("Authorization")
		provider.mu.Lock()
		provider.seen[key]++
		provider.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	}))
	return provider
}

func (provider *multiRevisionProvider) assertRevisionCredentials(t *testing.T) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	for _, key := range []string{
		"/revision-a|Bearer revision-a-secret",
		"/revision-b|Bearer revision-b-secret",
	} {
		if provider.seen[key] == 0 {
			t.Fatalf("Provider readiness did not use Revision credential %q: %+v", key, provider.seen)
		}
	}
}

func multiRevisionDraft(
	image, providerURL, promptGroup, networkMode, completedAction string,
	maxAttempts int,
) config.DynamicDraft {
	draft := config.RecommendedDynamicDraft()
	draft.Runtime.SchedulerIntervalSeconds = 1
	draft.Runtime.MaxWorkers = 2
	draft.Runtime.MaxRunningProjects = 2
	draft.Runtime.MaxProjectWorkers = 1
	draft.Runtime.HealthcheckTimeoutSeconds = 2
	draft.Runtime.WorkerHealthcheck = "startup_and_task"
	draft.Runtime.PromptGroup = promptGroup
	draft.Container.Image = image
	draft.Container.NetworkMode = networkMode
	draft.Container.CompletedAction = completedAction
	draft.Tasks.Bootstrap.ConcludeSeconds = 1
	draft.Tasks.Bootstrap.RecoverySeconds = 1
	draft.Retry.Bootstrap.MaxAttempts = maxAttempts
	draft.Retry.Bootstrap.InitialIntervalSeconds = 1
	draft.Retry.Bootstrap.MaxIntervalSeconds = 1
	draft.Retry.Bootstrap.Multiplier = 1
	draft.Workers = []config.DynamicWorkerDraft{
		{
			Name: multiRevisionWorker, AgentType: "claude_code", ProviderName: "fixture",
			TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 1, Priority: 1,
			AgentEnv: map[string]string{
				"IS_SANDBOX": "1", "ANTHROPIC_BASE_URL": providerURL, "ANTHROPIC_MODEL": "fixture-model",
			},
			TimeoutOverrides: map[string]int{},
		},
		{
			Name: "z-backup-worker", AgentType: "claude_code", ProviderName: "fixture",
			TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 1, Priority: 1,
			AgentEnv: map[string]string{
				"IS_SANDBOX": "1", "ANTHROPIC_BASE_URL": providerURL, "ANTHROPIC_MODEL": "fixture-model",
			},
			TimeoutOverrides: map[string]int{},
		},
	}
	return draft
}

func saveMultiRevisionDraft(
	t *testing.T,
	server *httptest.Server,
	expectedVersion int64,
	draft config.DynamicDraft,
	secret string,
) config.ConfigurationRead {
	t.Helper()
	var read config.ConfigurationRead
	multiRevisionRequest(t, server, http.MethodPut, "/api/v1/configuration", config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: expectedVersion, Draft: draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			multiRevisionWorker: {"ANTHROPIC_AUTH_TOKEN": {Value: secret}},
			"z-backup-worker":   {"ANTHROPIC_AUTH_TOKEN": {Value: secret}},
		},
	}, http.StatusOK, &read)
	return read
}

func readMultiRevisionConfig(
	t *testing.T,
	server *httptest.Server,
) config.ConfigurationRevision {
	t.Helper()
	var revision config.ConfigurationRevision
	multiRevisionRequest(t, server, http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, &revision)
	return revision
}

func createMultiRevisionProject(t *testing.T, server *httptest.Server, title string) multiRevisionProject {
	t.Helper()
	var project multiRevisionProject
	multiRevisionRequest(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": title, "origin": title, "goal": "complete", "bootstrap_enabled": true,
	}, http.StatusCreated, &project)
	return project
}

func multiRevisionRequest(
	t *testing.T,
	server *httptest.Server,
	method, path string,
	body any,
	wantStatus int,
	target any,
) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status=%d body=%s", method, path, response.StatusCode, responseBody)
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

func provisionMultiRevisionProjects(
	t *testing.T,
	dockerAPI *dockerclient.Client,
	lease rundomain.DispatcherLease,
	firstConfig, secondConfig *config.DispatchConfig,
	fixtures map[string]multiRevisionFixture,
) map[string]string {
	t.Helper()
	byRevision := map[string]*config.DispatchConfig{
		firstConfig.ConfigRevision:  firstConfig,
		secondConfig.ConfigRevision: secondConfig,
	}
	projectConfig := func(configured *config.DispatchConfig) dockerruntime.ProjectConfig {
		return dockerruntime.ProjectConfig{
			Image: configured.Container.Image, NetworkMode: configured.Container.NetworkMode,
			CapAdd:          append([]string(nil), configured.Container.CapAdd...),
			CompletedAction: string(configured.Container.CompletedAction),
		}
	}
	outputLimits := func(configured *config.DispatchConfig) dockerruntime.OutputLimits {
		return dockerruntime.OutputLimits{
			StdoutBytes: configured.Container.StdoutLimitBytes,
			StderrBytes: configured.Container.StderrLimitBytes,
		}
	}
	runtime, err := newDockerIntegrationRuntime(dockerruntime.Options{
		DeploymentID: "multi-revision", DispatcherID: lease.InstanceID, DisposeGrace: time.Second,
		Project: projectConfig(secondConfig), OutputLimits: outputLimits(secondConfig),
		ProjectConfigForRevision: func(_ context.Context, revision string) (dockerruntime.ProjectConfig, error) {
			configured := byRevision[revision]
			if configured == nil {
				return dockerruntime.ProjectConfig{}, fmt.Errorf("unknown fixture revision %q", revision)
			}
			return projectConfig(configured), nil
		},
		OutputLimitsForRevision: func(_ context.Context, revision string) (dockerruntime.OutputLimits, error) {
			configured := byRevision[revision]
			if configured == nil {
				return dockerruntime.OutputLimits{}, fmt.Errorf("unknown fixture revision %q", revision)
			}
			return outputLimits(configured), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	containers := make(map[string]string, len(fixtures))
	for projectID, fixture := range fixtures {
		script := strings.NewReplacer(
			"__TOKEN__", fixture.token,
			"__FACT__", fixture.fact,
			"__DELAY__", fixture.delay,
		).Replace(multiRevisionFakeClaude)
		input := []byte("schema: aether.bootstrap-input/v1\ngeneration: 1\nrevision: 1\n")
		digest := sha256.Sum256(input)
		runID := rundomain.RunID("multi-revision-setup-" + projectID)
		process, err := runtime.Launch(t.Context(), rundomain.LaunchSpec{
			RunID: runID, ProjectID: projectID, Task: rundomain.TaskBootstrap, Phase: rundomain.PhaseBootstrap,
			FencingToken: 1, ConfigRevision: fixture.revision, Image: byRevision[fixture.revision].Container.Image,
			Command: []string{
				"/bin/sh", "-c",
				`umask 077; mkdir -p /home/kali/.local/bin; printf '%s' "$1" > /home/kali/.local/bin/claude; chmod 700 /home/kali/.local/bin/claude`,
				"multi-revision-fixture", script,
			},
			RenderedInput: &rundomain.RenderedInput{
				Bytes: input, Hash: rundomain.RenderedInputHash(hex.EncodeToString(digest[:])),
			},
			PhaseTimeout: 5 * time.Second, StopGrace: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		output, err := process.Wait(t.Context())
		if err != nil || !output.Exited || output.ExitCode != 0 {
			t.Fatalf("provision Project %s: output=%+v error=%v", projectID, output, err)
		}
		if err := runtime.CompleteRun(t.Context(), runID); err != nil {
			t.Fatal(err)
		}
		handle, ok := process.(rundomain.ProcessHandle)
		if !ok || handle.Handle().ContainerID == "" {
			t.Fatalf("fixture Project %s returned no container", projectID)
		}
		containers[projectID] = handle.Handle().ContainerID
		runDeliveryRootExec(t, dockerAPI, handle.Handle().ContainerID, []string{
			"/bin/sh", "-c", "rm -f /usr/local/bin/claude && ln -s /home/kali/.local/bin/claude /usr/local/bin/claude",
		})
	}
	return containers
}

const multiRevisionFakeClaude = `#!/bin/sh
set -eu

if [ "${ANTHROPIC_AUTH_TOKEN:-}" != "__TOKEN__" ]; then
  exit 91
fi

mode=primary
output_format=
schema=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -r)
      mode=continuation
      shift 2
      ;;
    --session-id)
      shift 2
      ;;
    --output-format)
      [ "${2:-}" = json ] || exit 92
      output_format=$2
      shift 2
      ;;
    --json-schema)
      schema=$2
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[ "$output_format" = json ] && [ -n "$schema" ] || exit 93

if [ "$mode" = continuation ]; then
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ignored","session_id":"fixture","structured_output":{"outcome":{"kind":"invalid_fixture"}}}'
  exit 0
fi

attempt_file=/tmp/aether-multi-revision-attempt
attempt=0
if [ -r "$attempt_file" ]; then
  attempt=$(cat "$attempt_file")
fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > "$attempt_file"
if [ "$attempt" -eq 1 ]; then
  __DELAY__
fi
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ignored","session_id":"fixture","structured_output":{"outcome":{"kind":"complete_project","description":"fixture complete","fact":{"description":"__FACT__"}}}}'
`

func runMultiRevisionSchedulerPass(
	t *testing.T,
	host *controlplane.ControlPlaneHost,
	control *controlplane.InProcessRunControl,
	lease rundomain.DispatcherLease,
	activeConfig *config.DispatchConfig,
	dockerHost string,
) {
	t.Helper()
	secrets, err := host.RunSecretProvider()
	if err != nil {
		t.Fatal(err)
	}
	readinessSecrets, err := host.RevisionSecretProvider(activeConfig.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := runProductionScheduler(
		t.Context(), control, control, control, activeConfig, lease,
		"multi-revision", dockerHost, secrets, readinessSecrets,
		func(_ context.Context, revision string) (rundomain.SecretEnvironmentProvider, error) {
			return host.RevisionSecretProvider(revision)
		},
		host.ExecutionConfig, true, nil,
	); err != nil {
		t.Fatal(err)
	}
}

func reconcileMultiRevisionContainers(
	t *testing.T,
	host *controlplane.ControlPlaneHost,
	control *controlplane.InProcessRunControl,
	lease rundomain.DispatcherLease,
	activeConfig *config.DispatchConfig,
	dockerAPI *dockerclient.Client,
	stoppedContainerID, removedContainerID string,
) {
	t.Helper()
	secrets, err := host.RunSecretProvider()
	if err != nil {
		t.Fatal(err)
	}
	readinessSecrets, err := host.RevisionSecretProvider(activeConfig.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- runProductionScheduler(
			ctx, control, control, control, activeConfig, lease,
			"multi-revision", dockerAPI.DaemonHost(), secrets, readinessSecrets,
			func(_ context.Context, revision string) (rundomain.SecretEnvironmentProvider, error) {
				return host.RevisionSecretProvider(revision)
			},
			host.ExecutionConfig, false, nil,
		)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stopped, stoppedErr := dockerAPI.ContainerInspect(t.Context(), stoppedContainerID, dockerclient.ContainerInspectOptions{})
		_, removedErr := dockerAPI.ContainerInspect(t.Context(), removedContainerID, dockerclient.ContainerInspectOptions{})
		if stoppedErr == nil && stopped.Container.State != nil && !stopped.Container.State.Running && errdefs.IsNotFound(removedErr) {
			cancel()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("Project container completion policies did not converge")
}

func assertMultiRevisionContainerNetwork(
	t *testing.T,
	dockerAPI *dockerclient.Client,
	containerID, networkMode, revision string,
) {
	t.Helper()
	inspection, err := dockerAPI.ContainerInspect(t.Context(), containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Container.HostConfig == nil || string(inspection.Container.HostConfig.NetworkMode) != networkMode ||
		inspection.Container.Config == nil || inspection.Container.Config.Labels["aether.config_revision"] != revision {
		t.Fatalf("Revision container configuration: network=%v labels=%v", inspection.Container.HostConfig, inspection.Container.Config.Labels)
	}
}
