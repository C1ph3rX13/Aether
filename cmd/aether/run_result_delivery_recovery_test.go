package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	deliveryPrimaryWorker   = "delivery-primary"
	deliveryPreferredWorker = "delivery-preferred"
	deliveryFixtureSecret   = "delivery-fixture-secret-never-expose"
)

const deliveryFakeClaude = `#!/bin/sh
set -eu

arguments=$*
mode=primary
output_format=
schema=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -r)
      mode=fallback
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

if [ "$mode" = fallback ]; then
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ignored","session_id":"fixture","structured_output":{"outcome":{"kind":"conclude_intent","fact":{"description":"invalid \xFF"}}}}'
  exit 0
fi

case "$arguments" in
  *MARKER_SUCCESS_FIXTURE*)
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ignored","session_id":"fixture","structured_output":{"outcome":{"kind":"complete_project","description":"marker success","fact":{"description":"committed exactly once"}}}}'
    ;;
  *)
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ignored","session_id":"fixture","structured_output":{"outcome":{"kind":"complete_project","description":"partial result must not commit","fact":{"description":"partial fact must not commit"}}}}'
    sleep 30
    ;;
esac
`

// TestRunResultDeliveryRecoveryDockerIntegration is the permanent acceptance
// baseline for Issue #153. It stays opt-in because it requires a local Docker
// daemon and the explicitly provisioned Aether worker image.
func TestRunResultDeliveryRecoveryDockerIntegration(t *testing.T) {
	image := dockerIntegrationImage(t)

	host, err := controlplane.NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "delivery-test-operator"},
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
		InstanceID: "delivery-recovery-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	failureProject := createDeliveryProject(t, server, deliveryProjectRequest{
		Title: "Delivery failure", Origin: "RECOVERY_FAILURE_FIXTURE", Goal: "must not accept partial output",
		BootstrapEnabled: true,
	})
	successProject := createDeliveryProject(t, server, deliveryProjectRequest{
		Title: "Delivery success", Origin: "MARKER_SUCCESS_FIXTURE", Goal: "commit one canonical result",
		BootstrapEnabled: true,
	})
	failureIntentID := bootstrapIntentID(t, failureProject)

	dockerAPI, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dockerAPI.Close() })
	baseRuntime, err := newDockerIntegrationRuntime(dockerruntime.Options{
		DeploymentID: "delivery-recovery-test", DispatcherID: lease.InstanceID, DisposeGrace: time.Second,
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
	for _, projectID := range []string{failureProject.Project.ID, successProject.Project.ID} {
		containerID := provisionDeliveryFakeClaude(t, baseRuntime, dockerAPI, image, projectID)
		t.Cleanup(func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = dockerAPI.ContainerRemove(cleanupContext, containerID, dockerclient.ContainerRemoveOptions{Force: true})
		})
	}

	observedRuntime := &deliveryObservedRuntime{delegate: baseRuntime}
	catalog := deliveryWorkerCatalog(t, image, failureProject.Project.ConfigRevision)
	secretResolver, err := rundomain.NewSecretResolver(map[string]rundomain.SecretProvider{
		"configuration": lookupSecretProvider{lookup: func(key string) (string, bool) {
			if key == "AETHER_DELIVERY_FIXTURE_TOKEN" {
				return deliveryFixtureSecret, true
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
	policy := deliverySchedulerPolicy()

	runDeliverySchedulerPass(
		t, control, catalog, observedRuntime, executions, lease, failureProject.Project.ConfigRevision, policy, false,
	)
	firstFailureRuns, _ := deliveryActivity(t, server, failureProject.Project.ID)
	firstSuccessRuns, _ := deliveryActivity(t, server, successProject.Project.ID)
	if len(firstFailureRuns.Runs) != 1 || len(firstSuccessRuns.Runs) != 1 ||
		firstFailureRuns.Runs[0].FinishedAt == nil || firstSuccessRuns.Runs[0].FinishedAt == nil ||
		firstFailureRuns.Runs[0].StartedAt.After(*firstSuccessRuns.Runs[0].FinishedAt) ||
		!firstSuccessRuns.Runs[0].StartedAt.Before(*firstFailureRuns.Runs[0].FinishedAt) {
		t.Fatalf("distinct Work Keys did not overlap: failure=%+v success=%+v", firstFailureRuns.Runs, firstSuccessRuns.Runs)
	}

	for attempt := 2; attempt <= 3; attempt++ {
		waitForDeliveryRetry(t, server, failureProject.Project.ID, attempt-1)
		runDeliverySchedulerPass(
			t, control, catalog, observedRuntime, executions, lease, failureProject.Project.ConfigRevision, policy, true,
		)
	}

	failureActivity, failureActivityJSON := deliveryActivity(t, server, failureProject.Project.ID)
	launches, outputs := observedRuntime.Snapshot()
	assertDeliveryFailureChain(t, failureActivity.Runs)
	chainJSON := deliveryGetJSON(t, server, "/api/v1/projects/"+url.PathEscape(failureProject.Project.ID)+
		"/activity/"+url.PathEscape(string(failureActivity.Runs[0].ID))+"/retry-chain", nil)
	var retryChain []rundomain.ActivityRun
	if err := json.Unmarshal(chainJSON, &retryChain); err != nil {
		t.Fatal(err)
	}
	assertDeliveryFailureChain(t, retryChain)
	failureDetailJSON := make([][]byte, 0, len(failureActivity.Runs))
	for _, run := range failureActivity.Runs {
		var detail rundomain.ActivityRunDetail
		raw := deliveryGetJSON(t, server, "/api/v1/projects/"+url.PathEscape(failureProject.Project.ID)+
			"/activity/"+url.PathEscape(string(run.ID)), &detail)
		if detail.ID != run.ID || detail.Worker != deliveryPrimaryWorker ||
			detail.ErrorCode != rundomain.ExecutionCaptureFailed ||
			detail.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate {
			t.Fatalf("failure Activity Detail for attempt %d: %+v", run.Attempt, detail)
		}
		failureDetailJSON = append(failureDetailJSON, raw)
	}

	failureDetail := getDeliveryProject(t, server, failureProject.Project.ID)
	if failureDetail.Project.Status != "active" || failureDetail.Project.Revision != 1 || len(failureDetail.Facts) != 2 {
		t.Fatalf("partial timed-out result changed Blackboard: %+v", failureDetail)
	}
	for _, fact := range failureDetail.Facts {
		if fact.Description == "partial fact must not commit" {
			t.Fatalf("partial timed-out Fact reached Blackboard: %+v", fact)
		}
	}

	evidencePath := "/api/v1/projects/" + url.PathEscape(failureProject.Project.ID) +
		"/execution-evidence?intent_id=" + url.QueryEscape(failureIntentID)
	var evidence deliveryWorkspaceExecutionEvidence
	evidenceJSON := deliveryGetJSON(t, server, evidencePath, &evidence)
	if evidence.SelectedIntentRun == nil || evidence.SelectedIntentRun.RunID != string(failureActivity.Runs[0].ID) ||
		evidence.SelectedIntentRun.Status != rundomain.StatusFailed || evidence.SelectedIntentRun.Attempt != 3 ||
		evidence.SelectedIntentRun.Worker != deliveryPrimaryWorker ||
		evidence.SelectedIntentRun.ErrorCode != rundomain.ExecutionCaptureFailed ||
		evidence.SelectedIntentRun.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate {
		t.Fatalf("Workspace Execution Evidence: %+v", evidence)
	}

	catalogJSON := deliveryGetJSON(t, server, "/api/v1/projects", nil)
	if bytes.Contains(catalogJSON, []byte(rundomain.CaptureFailureInvalidCandidate)) ||
		!catalogHasRetryExhausted(t, catalogJSON, failureProject.Project.ID) {
		t.Fatalf("Catalog attention exposed parser detail or omitted exhaustion: %s", catalogJSON)
	}

	sessions := assertDeliveryRuntimeEvidence(t, failureActivity.Runs, launches, outputs)
	runIDs := make([]rundomain.RunID, 0, len(failureActivity.Runs)+1)
	for _, run := range failureActivity.Runs {
		runIDs = append(runIDs, run.ID)
	}
	runIDs = append(runIDs, firstSuccessRuns.Runs[0].ID)
	activeHandles, err := control.ListExecutionHandles(t.Context(), rundomain.ExecutionHandleListRequest{
		RunIDs: runIDs, DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
	})
	if err != nil || len(activeHandles) != 0 {
		t.Fatalf("terminal phases retained active execution handles: handles=%+v error=%v", activeHandles, err)
	}

	successActivity, successActivityJSON := deliveryActivity(t, server, successProject.Project.ID)
	if len(successActivity.Runs) != 1 || successActivity.Runs[0].Status != rundomain.StatusCommitted ||
		successActivity.Runs[0].OutcomeKind != rundomain.OutcomeCompleteProject ||
		successActivity.Runs[0].Worker != deliveryPrimaryWorker {
		t.Fatalf("marker success activity: %+v", successActivity.Runs)
	}
	successRun := successActivity.Runs[0]
	var successRunDetail rundomain.ActivityRunDetail
	successDetailJSON := deliveryGetJSON(t, server, "/api/v1/projects/"+url.PathEscape(successProject.Project.ID)+
		"/activity/"+url.PathEscape(string(successRun.ID)), &successRunDetail)
	candidate := json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"marker success","fact":{"description":"committed exactly once"}}}`)
	_, err = control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: successRun.ID, FencingToken: successRunDetail.FencingToken + 1, Candidate: candidate,
	})
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorFenced {
		t.Fatalf("wrong fencing token reached idempotent Finish: %v", err)
	}
	replayed, err := control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: successRun.ID, FencingToken: successRunDetail.FencingToken, Candidate: candidate,
	})
	if err != nil || !replayed.Replayed || replayed.Status != rundomain.StatusCommitted {
		t.Fatalf("idempotent Finish replay: receipt=%+v error=%v", replayed, err)
	}
	successDetail := getDeliveryProject(t, server, successProject.Project.ID)
	if successDetail.Project.Status != "completed" || successDetail.Project.Revision != 2 ||
		countDeliveryFact(successDetail, "committed exactly once") != 1 {
		t.Fatalf("marked result did not commit exactly once: %+v", successDetail)
	}

	projected := bytes.Join(append([][]byte{
		failureActivityJSON, chainJSON, evidenceJSON, successActivityJSON, successDetailJSON,
	}, failureDetailJSON...), nil)
	for _, prohibited := range append([]string{
		deliveryFixtureSecret, "partial result must not commit", "partial fact must not commit",
		"invalid \\xFF", "--session-id", `"stdout"`, `"stderr"`, `"session_id"`,
		`"container_id"`, `"environment"`, `"secret_references"`,
	}, sessions...) {
		if bytes.Contains(projected, []byte(prohibited)) {
			t.Fatalf("read-only execution projection exposed %q", prohibited)
		}
	}
}

type deliveryWorkspaceExecutionEvidence struct {
	ProjectID         string `json:"project_id"`
	SelectedIntentRun *struct {
		RunID                string                         `json:"run_id"`
		Status               rundomain.Status               `json:"status"`
		Attempt              int64                          `json:"attempt"`
		Worker               string                         `json:"worker"`
		ErrorCode            rundomain.ErrorCode            `json:"error_code"`
		CaptureFailureReason rundomain.CaptureFailureReason `json:"capture_failure_reason"`
		ResultEvidenceSource rundomain.ResultEvidenceSource `json:"result_evidence_source"`
	} `json:"selected_intent_run"`
}

type deliveryProjectRequest struct {
	Title            string `json:"title"`
	Origin           string `json:"origin"`
	Goal             string `json:"goal"`
	BootstrapEnabled bool   `json:"bootstrap_enabled"`
}

type deliveryProjectDetail struct {
	Project struct {
		ID             string `json:"id"`
		Status         string `json:"status"`
		Revision       int64  `json:"revision"`
		ConfigRevision string `json:"config_revision"`
	} `json:"project"`
	Facts []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"facts"`
	Intents []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	} `json:"intents"`
}

func createDeliveryProject(t *testing.T, server *httptest.Server, request deliveryProjectRequest) deliveryProjectDetail {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Post(server.URL+"/api/v1/projects", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create delivery Project status=%d body=%s", response.StatusCode, body)
	}
	var detail deliveryProjectDetail
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

func getDeliveryProject(t *testing.T, server *httptest.Server, projectID string) deliveryProjectDetail {
	t.Helper()
	var detail deliveryProjectDetail
	deliveryGetJSON(t, server, "/api/v1/projects/"+url.PathEscape(projectID), &detail)
	return detail
}

func bootstrapIntentID(t *testing.T, detail deliveryProjectDetail) string {
	t.Helper()
	for _, intent := range detail.Intents {
		if intent.Kind == "bootstrap" && intent.Status == "open" {
			return intent.ID
		}
	}
	t.Fatalf("Project has no open Bootstrap Intent: %+v", detail.Intents)
	return ""
}

func deliveryWorkerCatalog(t *testing.T, image, configRevision string) *worker.Catalog {
	t.Helper()
	workers := []worker.CatalogWorker{
		{
			Name: deliveryPreferredWorker, Adapter: string(worker.WorkerTypeClaudeCode), Model: "fixture-model",
			TaskTypes: []rundomain.TaskType{rundomain.TaskBootstrap}, MaxRunning: 2, Priority: 0,
			Environment: map[string]string{
				"IS_SANDBOX":         "1",
				"ANTHROPIC_BASE_URL": "http://127.0.0.1:1", "ANTHROPIC_MODEL": "fixture-model",
			},
			Capabilities: []string{"bootstrap"},
			SecretReferences: []rundomain.SecretReference{{
				EnvironmentVariable: "ANTHROPIC_AUTH_TOKEN", Provider: "configuration", Key: "AETHER_DELIVERY_FIXTURE_TOKEN",
			}},
		},
		{
			Name: deliveryPrimaryWorker, Adapter: string(worker.WorkerTypeClaudeCode), Model: "fixture-model",
			TaskTypes: []rundomain.TaskType{rundomain.TaskBootstrap}, MaxRunning: 2, Priority: 10,
			Environment: map[string]string{
				"IS_SANDBOX":         "1",
				"ANTHROPIC_BASE_URL": "http://127.0.0.1:1", "ANTHROPIC_MODEL": "fixture-model",
			},
			Capabilities: []string{"bootstrap"},
			SecretReferences: []rundomain.SecretReference{{
				EnvironmentVariable: "ANTHROPIC_AUTH_TOKEN", Provider: "configuration", Key: "AETHER_DELIVERY_FIXTURE_TOKEN",
			}},
		},
	}
	catalog, err := worker.NewCatalogForRevision(configRevision, worker.CatalogSource{
		ImageReference: image, Workers: workers,
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason delivery fixture", Finalization: "finalize reason delivery fixture"},
			rundomain.TaskExplore:   {Primary: "explore delivery fixture", Fallback: "conclude explore delivery fixture", Finalization: "finalize explore delivery fixture"},
			rundomain.TaskBootstrap: {Primary: "bootstrap delivery fixture", Fallback: "conclude bootstrap delivery fixture", Finalization: "finalize bootstrap delivery fixture"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func deliverySchedulerPolicy() scheduler.RunSchedulerPolicy {
	retry := rundomain.RetryPolicy{
		MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond,
		Multiplier: 1, DeclineCooldown: time.Second,
	}
	return scheduler.RunSchedulerPolicy{
		PollInterval: time.Second, MaxWorkers: 2, MaxProjectWorkers: 2, MaxRunningProjects: 2,
		ExecutionPolicies: executionPolicyTableForWorkers(
			[]string{deliveryPreferredWorker, deliveryPrimaryWorker}, retry, 1, 1, 0, 1, 4, 6, 2,
		),
	}
}

func runDeliverySchedulerPass(
	t *testing.T,
	control *controlplane.HTTPRunControl,
	catalog *worker.Catalog,
	runtime *deliveryObservedRuntime,
	executions *RunExecutionFactory,
	lease rundomain.DispatcherLease,
	configRevision string,
	policy scheduler.RunSchedulerPolicy,
	preferredReady bool,
) {
	t.Helper()
	runner, err := scheduler.NewRunScheduler(scheduler.RunSchedulerControls{
		Planning: control, Inventory: control, Recovery: control, Lifecycle: control, Cleanup: control,
	}, scheduler.RunSchedulerConfig{
		ConfigRevision: configRevision,
		Policy:         policy, Catalog: catalog, Resources: runtime, Executions: executions,
		Lease: lease, DeploymentID: "delivery-recovery-test", Once: true,
		PrepareRevisionWorkers: func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{
				deliveryPrimaryWorker: true, deliveryPreferredWorker: preferredReady,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func waitForDeliveryRetry(t *testing.T, server *httptest.Server, projectID string, completedAttempts int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		activity, _ := deliveryActivity(t, server, projectID)
		if len(activity.Runs) == completedAttempts && activity.Runs[0].NextEligibleAt != nil &&
			!time.Now().Before(*activity.Runs[0].NextEligibleAt) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("attempt %d retry did not become eligible", completedAttempts)
}

func deliveryActivity(t *testing.T, server *httptest.Server, projectID string) (rundomain.ActivityPage, []byte) {
	t.Helper()
	var page rundomain.ActivityPage
	raw := deliveryGetJSON(t, server, "/api/v1/projects/"+url.PathEscape(projectID)+"/activity?view=all", &page)
	sort.Slice(page.Runs, func(i, j int) bool { return page.Runs[i].Attempt > page.Runs[j].Attempt })
	return page, raw
}

func deliveryGetJSON(t *testing.T, server *httptest.Server, path string, target any) []byte {
	t.Helper()
	response, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", path, response.StatusCode, body)
	}
	if target != nil {
		if err := json.Unmarshal(body, target); err != nil {
			t.Fatalf("decode GET %s: %v; body=%s", path, err, body)
		}
	}
	return body
}

func assertDeliveryFailureChain(t *testing.T, runs []rundomain.ActivityRun) {
	t.Helper()
	sort.Slice(runs, func(i, j int) bool { return runs[i].Attempt > runs[j].Attempt })
	if len(runs) != 3 {
		t.Fatalf("failure retry chain length=%d: %+v", len(runs), runs)
	}
	for index, run := range runs {
		wantAttempt := int64(3 - index)
		if run.Attempt != wantAttempt || run.Status != rundomain.StatusFailed ||
			run.ErrorCode != rundomain.ExecutionCaptureFailed ||
			run.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate ||
			run.Worker != deliveryPrimaryWorker || run.OutcomeKind != "" {
			t.Fatalf("failure attempt %d: %+v", wantAttempt, run)
		}
		if index < len(runs)-1 {
			previous := runs[index+1].ID
			if run.PreviousRunID == nil || *run.PreviousRunID != previous {
				t.Fatalf("retry lineage for attempt %d: %+v", run.Attempt, run)
			}
		}
	}
	if runs[0].Attention != rundomain.AttentionRetryExhausted || runs[0].NextEligibleAt != nil {
		t.Fatalf("final retry exhaustion evidence: %+v", runs[0])
	}
}

func catalogHasRetryExhausted(t *testing.T, raw []byte, projectID string) bool {
	t.Helper()
	var entries []struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Execution struct {
			Status string `json:"status"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Project.ID == projectID {
			return entry.Execution.Status == "retry_exhausted"
		}
	}
	return false
}

func countDeliveryFact(detail deliveryProjectDetail, description string) int {
	count := 0
	for _, fact := range detail.Facts {
		if fact.Description == description {
			count++
		}
	}
	return count
}

type deliveryLaunchObservation struct {
	Spec        rundomain.LaunchSpec
	ContainerID string
	Handle      rundomain.ExecutionHandle
}

type deliveryOutputObservation struct {
	RunID  rundomain.RunID
	Phase  rundomain.ExecutionPhase
	Output rundomain.ProcessOutput
	Err    error
}

type deliveryObservedRuntime struct {
	delegate *dockerruntime.Runtime
	mu       sync.Mutex
	launches []deliveryLaunchObservation
	outputs  []deliveryOutputObservation
}

func (runtime *deliveryObservedRuntime) Launch(ctx context.Context, spec rundomain.LaunchSpec) (rundomain.Process, error) {
	process, err := runtime.delegate.Launch(ctx, spec)
	return runtime.observeLaunch(spec, process, err)
}

func (runtime *deliveryObservedRuntime) LaunchTracked(
	ctx context.Context,
	spec rundomain.LaunchSpec,
	registration rundomain.ExecutionHandleRegistration,
) (rundomain.Process, error) {
	process, err := runtime.delegate.LaunchTracked(ctx, spec, registration)
	return runtime.observeLaunch(spec, process, err)
}

func (runtime *deliveryObservedRuntime) observeLaunch(
	spec rundomain.LaunchSpec,
	process rundomain.Process,
	err error,
) (rundomain.Process, error) {
	if err != nil {
		runtime.mu.Lock()
		runtime.outputs = append(runtime.outputs, deliveryOutputObservation{
			RunID: spec.RunID, Phase: spec.Phase, Err: err,
		})
		runtime.mu.Unlock()
		return nil, err
	}
	observation := deliveryLaunchObservation{Spec: cloneDeliveryLaunchSpec(spec)}
	if handled, ok := process.(rundomain.ProcessHandle); ok {
		observation.ContainerID = handled.Handle().ContainerID
		observation.Handle = handled.Handle()
	}
	runtime.mu.Lock()
	runtime.launches = append(runtime.launches, observation)
	runtime.mu.Unlock()
	return &deliveryObservedProcess{delegate: process, runtime: runtime, runID: spec.RunID, phase: spec.Phase}, nil
}

func (runtime *deliveryObservedRuntime) CompleteRun(ctx context.Context, runID rundomain.RunID) error {
	return runtime.delegate.CompleteRun(ctx, runID)
}

func (runtime *deliveryObservedRuntime) StopExecutionHandle(ctx context.Context, handle rundomain.ExecutionHandle) error {
	return runtime.delegate.StopExecutionHandle(ctx, handle)
}

func (runtime *deliveryObservedRuntime) ReconcileProjectInventory(
	ctx context.Context,
	lookup rundomain.ProjectRuntimeStatusLookup,
) error {
	return runtime.delegate.ReconcileProjectInventory(ctx, lookup)
}

func (runtime *deliveryObservedRuntime) CleanupProject(ctx context.Context, job rundomain.ProjectCleanupJob) error {
	return runtime.delegate.CleanupProject(ctx, job)
}

func (runtime *deliveryObservedRuntime) Snapshot() ([]deliveryLaunchObservation, []deliveryOutputObservation) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	launches := append([]deliveryLaunchObservation(nil), runtime.launches...)
	outputs := append([]deliveryOutputObservation(nil), runtime.outputs...)
	return launches, outputs
}

type deliveryObservedProcess struct {
	delegate rundomain.Process
	runtime  *deliveryObservedRuntime
	runID    rundomain.RunID
	phase    rundomain.ExecutionPhase
}

func (process *deliveryObservedProcess) Wait(ctx context.Context) (rundomain.ProcessOutput, error) {
	output, err := process.delegate.Wait(ctx)
	process.runtime.mu.Lock()
	process.runtime.outputs = append(process.runtime.outputs, deliveryOutputObservation{
		RunID: process.runID, Phase: process.phase, Output: output, Err: err,
	})
	process.runtime.mu.Unlock()
	return output, err
}

func (process *deliveryObservedProcess) Stop(ctx context.Context, grace time.Duration) error {
	return process.delegate.Stop(ctx, grace)
}

func (process *deliveryObservedProcess) Handle() rundomain.ExecutionHandle {
	if handled, ok := process.delegate.(rundomain.ProcessHandle); ok {
		return handled.Handle()
	}
	return rundomain.ExecutionHandle{}
}

func cloneDeliveryLaunchSpec(spec rundomain.LaunchSpec) rundomain.LaunchSpec {
	spec.Command = append([]string(nil), spec.Command...)
	spec.Environment = nil
	if spec.RenderedInput != nil {
		spec.RenderedInput = &rundomain.RenderedInput{
			Path: spec.RenderedInput.Path, Bytes: append([]byte(nil), spec.RenderedInput.Bytes...), Hash: spec.RenderedInput.Hash,
		}
	}
	return spec
}

func assertDeliveryRuntimeEvidence(
	t *testing.T,
	runs []rundomain.ActivityRun,
	launches []deliveryLaunchObservation,
	outputs []deliveryOutputObservation,
) []string {
	t.Helper()
	launchByRun := make(map[rundomain.RunID]map[rundomain.ExecutionPhase]deliveryLaunchObservation)
	for _, launch := range launches {
		if launch.Spec.ProjectID == runs[0].ProjectID {
			if launchByRun[launch.Spec.RunID] == nil {
				launchByRun[launch.Spec.RunID] = make(map[rundomain.ExecutionPhase]deliveryLaunchObservation)
			}
			launchByRun[launch.Spec.RunID][launch.Spec.Phase] = launch
		}
	}
	outputByRun := make(map[rundomain.RunID]map[rundomain.ExecutionPhase]deliveryOutputObservation)
	for _, output := range outputs {
		if outputByRun[output.RunID] == nil {
			outputByRun[output.RunID] = make(map[rundomain.ExecutionPhase]deliveryOutputObservation)
		}
		outputByRun[output.RunID][output.Phase] = output
	}

	sessions := make([]string, 0, len(runs))
	containers := make(map[string]struct{})
	seenSessions := make(map[string]struct{})
	for _, run := range runs {
		primary := launchByRun[run.ID][rundomain.PhaseBootstrap]
		fallback := launchByRun[run.ID][rundomain.PhaseBootstrapConclude]
		primarySession := deliveryCommandValue(primary.Spec.Command, "--session-id")
		fallbackSession := deliveryCommandValue(fallback.Spec.Command, "-r")
		if primarySession == "" || fallbackSession != primarySession {
			t.Fatalf("attempt %d did not preserve its fallback Session: primary=%q fallback=%q", run.Attempt, primarySession, fallbackSession)
		}
		if _, reused := seenSessions[primarySession]; reused {
			t.Fatalf("technical retry reused Session %q", primarySession)
		}
		seenSessions[primarySession] = struct{}{}
		sessions = append(sessions, primarySession)
		if primary.ContainerID == "" || fallback.ContainerID != primary.ContainerID {
			t.Fatalf("attempt %d fallback changed Project container: primary=%q fallback=%q", run.Attempt, primary.ContainerID, fallback.ContainerID)
		}
		containers[primary.ContainerID] = struct{}{}

		primaryOutput := outputByRun[run.ID][rundomain.PhaseBootstrap]
		if primaryOutput.Err != nil || !primaryOutput.Output.Exited || !primaryOutput.Output.TimedOut ||
			!strings.Contains(primaryOutput.Output.Stdout, "partial result must not commit") {
			t.Fatalf("attempt %d primary timeout evidence: %+v", run.Attempt, primaryOutput)
		}
		fallbackOutput := outputByRun[run.ID][rundomain.PhaseBootstrapConclude]
		if fallbackOutput.Err != nil || !fallbackOutput.Output.Exited || fallbackOutput.Output.ExitCode != 0 ||
			fallbackOutput.Output.TimedOut || !strings.Contains(fallbackOutput.Output.Stdout, `\xFF`) {
			t.Fatalf("attempt %d fallback malformed output evidence: %+v", run.Attempt, fallbackOutput)
		}
	}
	if len(containers) != 1 {
		t.Fatalf("retry chain did not preserve its Project container: %+v", containers)
	}
	return sessions
}

func deliveryCommandValue(command []string, flag string) string {
	for index := 0; index+1 < len(command); index++ {
		if command[index] == flag {
			return command[index+1]
		}
	}
	return ""
}

func provisionDeliveryFakeClaude(
	t *testing.T,
	runtime *dockerruntime.Runtime,
	dockerAPI *dockerclient.Client,
	image string,
	projectID string,
) string {
	t.Helper()
	input := []byte("schema: aether.bootstrap-input/v1\ngeneration: 1\nrevision: 1\n")
	digest := sha256.Sum256(input)
	runID := rundomain.RunID("delivery-setup-" + projectID)
	process, err := runtime.Launch(t.Context(), rundomain.LaunchSpec{
		RunID: runID, ProjectID: projectID, Task: rundomain.TaskBootstrap, Phase: rundomain.PhaseBootstrap,
		FencingToken: 1, ConfigRevision: "delivery-fixture-setup", Image: image,
		Command: []string{
			"/bin/sh", "-c",
			`umask 077; mkdir -p /home/kali/.local/bin; printf '%s' "$1" > /home/kali/.local/bin/claude; chmod 700 /home/kali/.local/bin/claude`,
			"delivery-fixture", deliveryFakeClaude,
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
		t.Fatalf("provision fake Claude script: output=%+v error=%v", output, err)
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
		"/bin/sh", "-c", "rm -f /usr/local/bin/claude && ln -s /home/kali/.local/bin/claude /usr/local/bin/claude",
	})
	return containerID
}

func runDeliveryRootExec(t *testing.T, dockerAPI *dockerclient.Client, containerID string, command []string) {
	t.Helper()
	created, err := dockerAPI.ExecCreate(t.Context(), containerID, dockerclient.ExecCreateOptions{
		User: "root", Cmd: command,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dockerAPI.ExecStart(t.Context(), created.ID, dockerclient.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		inspection, inspectErr := dockerAPI.ExecInspect(t.Context(), created.ID, dockerclient.ExecInspectOptions{})
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if !inspection.Running {
			if inspection.ExitCode != 0 {
				t.Fatalf("root fixture exec exited %d: %s", inspection.ExitCode, strings.Join(command, " "))
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("root fixture exec did not finish: %s", strings.Join(command, " "))
}

var _ rundomain.TrackedRuntime = (*deliveryObservedRuntime)(nil)
var _ scheduler.RunResources = (*deliveryObservedRuntime)(nil)
var _ scheduler.ProjectInventoryReconciler = (*deliveryObservedRuntime)(nil)
var _ scheduler.ProjectCleanupRuntime = (*deliveryObservedRuntime)(nil)
var _ rundomain.Process = (*deliveryObservedProcess)(nil)
