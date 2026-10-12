package main

import (
	"bytes"
	"context"
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

// TestPiExploreProcessRecoveryDockerIntegration exercises the reported
// abnormal-process boundary through the production Scheduler, Control Plane,
// Worker Adapter, Project Runtime Container, and Docker Exec lifecycle.
func TestPiExploreProcessRecoveryDockerIntegration(t *testing.T) {
	image := dockerIntegrationImage(t)

	host, err := controlplane.NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "pi-explore-recovery-test-operator"},
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
		InstanceID: "pi-explore-recovery-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	dockerAPI, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dockerAPI.Close() })
	runtime, err := newDockerIntegrationRuntime(dockerruntime.Options{
		DeploymentID: "pi-explore-recovery-test", DispatcherID: lease.InstanceID, DisposeGrace: time.Second,
		OutputLimits: dockerruntime.DefaultOutputLimits(),
		Project:      dockerruntime.ProjectConfig{Image: image, NetworkMode: "none", CompletedAction: "remove"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(); closeErr != nil {
			t.Errorf("Docker Runtime close: %v", closeErr)
		}
	})

	secrets, err := rundomain.NewSecretResolver(map[string]rundomain.SecretProvider{
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
	observedRuntime := &deliveryObservedRuntime{delegate: runtime}

	failureProject := runPiExploreFixture(t, server, control, observedRuntime, dockerAPI, secrets, lease, image,
		"explore-recovery-fail", "pi-explore-recovery-fail", "Explore recovery failure")
	assertPiExploreRecoveryFailure(t, server, observedRuntime, failureProject.Project.ID, control, lease, secrets, image)

	for _, scenario := range []struct {
		mode  string
		name  string
		title string
	}{
		{mode: "explore-recovery-malformed", name: "pi-explore-recovery-malformed", title: "Explore malformed recovery evidence"},
		{mode: "explore-recovery-oversized", name: "pi-explore-recovery-oversized", title: "Explore oversized recovery evidence"},
	} {
		project := runPiExploreFixture(t, server, control, observedRuntime, dockerAPI, secrets, lease, image,
			scenario.mode, scenario.name, scenario.title)
		assertPiExploreUnverifiedAbnormal(t, server, observedRuntime, project.Project.ID)
	}

	successProject := runPiExploreFixture(t, server, control, observedRuntime, dockerAPI, secrets, lease, image,
		"explore", "pi-explore-recovery", "Explore recovery")
	assertPiExploreRecoverySuccess(t, server, observedRuntime, successProject.Project.ID)
}

func runPiExploreFixture(
	t *testing.T,
	server *httptest.Server,
	control *controlplane.HTTPRunControl,
	runtime *deliveryObservedRuntime,
	dockerAPI *dockerclient.Client,
	secrets rundomain.SecretEnvironmentProvider,
	lease rundomain.DispatcherLease,
	image, mode, workerName, title string,
) deliveryProjectDetail {
	t.Helper()
	project := createDeliveryProject(t, server, deliveryProjectRequest{
		Title: title, Origin: "PI_EXPLORE_ORIGIN", Goal: "recover the Explore process", BootstrapEnabled: false,
	})
	containerID := provisionPiRecoveryFakeCLI(t, runtime.delegate, dockerAPI, image, project.Project.ID)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = dockerAPI.ContainerRemove(cleanupContext, containerID, dockerclient.ContainerRemoveOptions{Force: true})
	})

	reasonCatalog := piExploreWorkerCatalog(t, image, workerName+"-reason", "reason", false, project.Project.ConfigRevision)
	reasonExecutions, err := NewRunExecutionFactory(reasonCatalog, runtime, secrets, control)
	if err != nil {
		t.Fatal(err)
	}
	// Reason only seeds the Explore Intent; its fixture starts several Node
	// processes and must not share the recovery scenarios' one-second deadline.
	reasonPolicy := piExplorePolicy(2, false)
	reasonExecution := reasonPolicy.ExecutionPolicies["__test_worker__"][rundomain.TaskReason]
	reasonExecution.Timeouts.PhaseSeconds = 5
	reasonExecution.Timeouts.RunSeconds = 12
	reasonPolicy.ExecutionPolicies["__test_worker__"][rundomain.TaskReason] = reasonExecution
	runPiExploreSchedulerPass(
		t, control, reasonCatalog, runtime, reasonExecutions, lease, project.Project.ConfigRevision, reasonPolicy,
	)

	waitForPiProjectRevision(t, server, project.Project.ID, 2)
	updated := getDeliveryProject(t, server, project.Project.ID)
	openIntent := false
	for _, intent := range updated.Intents {
		if intent.Kind == "explore" && intent.Status == "open" {
			openIntent = true
			break
		}
	}
	if !openIntent {
		t.Fatalf("Reason did not create an open Explore Intent: %+v", updated)
	}

	exploreCatalog := piExploreWorkerCatalog(t, image, workerName, mode, true, project.Project.ConfigRevision)
	exploreExecutions, err := NewRunExecutionFactory(exploreCatalog, runtime, secrets, control)
	if err != nil {
		t.Fatal(err)
	}
	maxAttempts := 2
	if mode == "explore-recovery-malformed" || mode == "explore-recovery-oversized" {
		maxAttempts = 1
	}
	runPiExploreSchedulerPass(
		t, control, exploreCatalog, runtime, exploreExecutions, lease, project.Project.ConfigRevision, piExplorePolicy(maxAttempts, true),
	)
	return project
}

func runPiExploreSchedulerPass(
	t *testing.T,
	control *controlplane.HTTPRunControl,
	catalog *worker.Catalog,
	runtime *deliveryObservedRuntime,
	executions *RunExecutionFactory,
	lease rundomain.DispatcherLease,
	configRevision string,
	policy scheduler.RunSchedulerPolicy,
) {
	t.Helper()
	policy = bindExecutionPoliciesToCatalog(policy, catalog)
	runner, err := scheduler.NewRunScheduler(scheduler.RunSchedulerControls{
		Planning: control, Inventory: control, Recovery: control, Lifecycle: control, Cleanup: control,
	}, scheduler.RunSchedulerConfig{
		ConfigRevision: configRevision,
		Policy:         policy, Catalog: catalog, Resources: runtime, Executions: executions,
		Lease: lease, DeploymentID: "pi-explore-recovery-test", Once: true,
		PrepareRevisionWorkers: func(context.Context, string) (map[string]bool, error) {
			ready := make(map[string]bool)
			for _, configured := range catalog.Workers() {
				ready[configured.Name] = true
			}
			return ready, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func piExploreWorkerCatalog(t *testing.T, image, workerName, mode string, recovery bool, configRevision string) *worker.Catalog {
	t.Helper()
	taskTypes := []rundomain.TaskType{rundomain.TaskReason}
	capabilities := []string{"reason"}
	if mode != "reason" {
		taskTypes = []rundomain.TaskType{rundomain.TaskExplore}
		capabilities = []string{"explore"}
		if recovery {
			capabilities = append(capabilities, rundomain.ProcessRecoveryCapability)
		}
	}
	prompts := worker.PromptSet{
		Primary:          "Run the deterministic Pi Explore recovery fixture.",
		Fallback:         "Serialize the deterministic Pi Explore recovery fixture result.",
		RecoveryPrimary:  "Continue the interrupted deterministic Pi Explore recovery fixture.",
		RecoveryConclude: "Use available tools only to inspect existing artifacts, then serialize the confirmed Explore result as conclude_intent or decline.",
		Finalization:     "Re-serialize the confirmed deterministic result as conclude_intent or decline.",
	}
	catalog, err := worker.NewCatalogForRevision(configRevision, worker.CatalogSource{
		ImageReference: image,
		Workers: []worker.CatalogWorker{{
			Name: workerName, Adapter: string(worker.WorkerTypePi), Model: "fixture-model",
			TaskTypes: taskTypes, MaxRunning: 1, Priority: 0, Capabilities: capabilities,
			Environment: map[string]string{
				"PI_BASE_URL": "http://127.0.0.1:1/" + mode, "PI_MODEL": "fixture-model",
				"PI_PROVIDER_API": "openai-completions",
			},
			SecretReferences: []rundomain.SecretReference{{
				EnvironmentVariable: "PI_API_KEY", Provider: "configuration", Key: piRecoveryToken,
			}},
		}},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "Reason over the deterministic recovery fixture.", Finalization: "Finalize the deterministic Reason fixture."},
			rundomain.TaskExplore:   prompts,
			rundomain.TaskBootstrap: prompts,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func waitForPiProjectRevision(t *testing.T, server *httptest.Server, projectID string, revision int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if detail := getDeliveryProject(t, server, projectID); detail.Project.Revision >= revision {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	detail := getDeliveryProject(t, server, projectID)
	activity, _ := deliveryActivity(t, server, projectID)
	t.Fatalf("Project %s did not reach Revision %d: detail=%+v runs=%+v", projectID, revision, detail, activity.Runs)
}

func piExplorePolicy(maxAttempts int, withRecovery bool) scheduler.RunSchedulerPolicy {
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

func assertPiExploreRecoverySuccess(t *testing.T, server *httptest.Server, runtime *deliveryObservedRuntime, projectID string) {
	t.Helper()
	activity, _ := deliveryActivity(t, server, projectID)
	var reason, explore *rundomain.ActivityRun
	for index := range activity.Runs {
		run := activity.Runs[index]
		switch run.Task {
		case rundomain.TaskReason:
			reason = &run
		case rundomain.TaskExplore:
			explore = &run
		}
	}
	if reason == nil || reason.Status != rundomain.StatusCommitted || reason.OutcomeKind != rundomain.OutcomeProposeIntents {
		t.Fatalf("Reason did not commit its Intent proposal: %+v", activity.Runs)
	}
	if explore == nil || explore.Status != rundomain.StatusCommitted || explore.OutcomeKind != rundomain.OutcomeConcludeIntent {
		t.Fatalf("Explore recovery did not commit exactly once: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 3 ||
		countDeliveryFact(detail, "Explore recovery committed fact") != 1 ||
		countDeliveryFact(detail, "abnormal Explore candidate must not commit") != 0 {
		t.Fatalf("Explore recovery changed Blackboard incorrectly: %+v", detail)
	}

	launches, outputs := runtime.Snapshot()
	var primary, recovery *deliveryLaunchObservation
	for index := range launches {
		launch := launches[index]
		if launch.Spec.RunID != explore.ID {
			continue
		}
		switch launch.Spec.Phase {
		case rundomain.PhaseExplore:
			primary = &launch
		case rundomain.PhaseExploreRecovery:
			recovery = &launch
		case rundomain.PhaseExploreConclude, rundomain.PhaseExploreConcludeRecovery:
			t.Fatalf("successful Explore Recovery recursed through Conclude: %+v", launches)
		}
	}
	if primary == nil || recovery == nil {
		t.Fatalf("Explore recovery phases: primary=%v recovery=%v", primary, recovery)
	}
	primaryOutput := piOutputForPhase(t, outputs, explore.ID, rundomain.PhaseExplore)
	if primaryOutput.Err != nil || !primaryOutput.Output.Exited || primaryOutput.Output.ExitCode != 9 ||
		primaryOutput.Output.TimedOut || primaryOutput.Output.Cancelled ||
		!strings.Contains(primaryOutput.Output.Stdout, "abnormal Explore candidate must not commit") {
		t.Fatalf("Explore primary abnormal output: %+v", primaryOutput)
	}
	recoveryOutput := piOutputForPhase(t, outputs, explore.ID, rundomain.PhaseExploreRecovery)
	if recoveryOutput.Err != nil || !recoveryOutput.Output.Exited || recoveryOutput.Output.ExitCode != 0 ||
		recoveryOutput.Output.TimedOut || recoveryOutput.Output.Cancelled ||
		!strings.Contains(recoveryOutput.Output.Stdout, "Explore recovery committed fact") {
		t.Fatalf("Explore recovery output: %+v", recoveryOutput)
	}
	if primary.ContainerID == "" || recovery.ContainerID != primary.ContainerID ||
		primary.Handle.ExecID == "" || recovery.Handle.ExecID == "" || primary.Handle.ExecID == recovery.Handle.ExecID {
		t.Fatalf("Explore recovery did not preserve container while isolating Exec handles: primary=%+v recovery=%+v", primary, recovery)
	}
	if deliveryCommandValue(primary.Spec.Command, "--session") != "" || deliveryCommandValue(recovery.Spec.Command, "--session") == "" {
		t.Fatalf("Explore recovery Session lineage: primary=%v recovery=%v", primary.Spec.Command, recovery.Spec.Command)
	}
	if primary.Spec.RenderedInput == nil || recovery.Spec.RenderedInput == nil ||
		primary.Spec.RenderedInput.Path == recovery.Spec.RenderedInput.Path || recovery.Spec.RenderedInput.Path == "" ||
		primary.Spec.RenderedInput.Hash != recovery.Spec.RenderedInput.Hash ||
		!bytes.Equal(primary.Spec.RenderedInput.Bytes, recovery.Spec.RenderedInput.Bytes) {
		t.Fatalf("Explore recovery changed rendered input identity: primary=%+v recovery=%+v", primary.Spec.RenderedInput, recovery.Spec.RenderedInput)
	}
}

func assertPiExploreUnverifiedAbnormal(
	t *testing.T,
	server *httptest.Server,
	runtime *deliveryObservedRuntime,
	projectID string,
) {
	t.Helper()
	activity, _ := deliveryActivity(t, server, projectID)
	var explore *rundomain.ActivityRun
	for index := range activity.Runs {
		if activity.Runs[index].Task == rundomain.TaskExplore {
			explore = &activity.Runs[index]
			break
		}
	}
	if explore == nil || explore.Status != rundomain.StatusFailed || explore.ErrorCode != rundomain.ExecutionProcessFailed ||
		explore.OutcomeKind != "" || explore.CaptureFailureReason != "" || explore.CaptureDiagnostic != "" {
		t.Fatalf("unverified abnormal Pi Activity: %+v", activity.Runs)
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Status != "active" || detail.Project.Revision != 2 ||
		countDeliveryFact(detail, "abnormal Explore candidate must not commit") != 0 ||
		countDeliveryFact(detail, "unverified abnormal continuation must not run") != 0 {
		t.Fatalf("unverified abnormal Pi changed Blackboard: %+v", detail)
	}
	launches, outputs := runtime.Snapshot()
	phaseCounts := make(map[rundomain.ExecutionPhase]int)
	for _, launch := range launches {
		if launch.Spec.RunID == explore.ID {
			phaseCounts[launch.Spec.Phase]++
		}
	}
	if phaseCounts[rundomain.PhaseExplore] != 1 || phaseCounts[rundomain.PhaseExploreRecovery] != 0 ||
		phaseCounts[rundomain.PhaseExploreConclude] != 0 || phaseCounts[rundomain.PhaseExploreConcludeRecovery] != 0 {
		t.Fatalf("unverified abnormal Pi launched a successor: %+v", phaseCounts)
	}
	primaryOutput := piOutputForPhase(t, outputs, explore.ID, rundomain.PhaseExplore)
	if primaryOutput.Err != nil || !primaryOutput.Output.Exited || primaryOutput.Output.ExitCode != 9 ||
		primaryOutput.Output.TimedOut || primaryOutput.Output.Cancelled || primaryOutput.Output.ErrorCode != "" {
		t.Fatalf("unverified abnormal Pi output: %+v", primaryOutput)
	}
}

func waitForPiExploreRetry(t *testing.T, server *httptest.Server, projectID string, completedAttempts int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		activity, _ := deliveryActivity(t, server, projectID)
		var exploreRuns []rundomain.ActivityRun
		for _, run := range activity.Runs {
			if run.Task == rundomain.TaskExplore {
				exploreRuns = append(exploreRuns, run)
			}
		}
		if len(exploreRuns) == completedAttempts && exploreRuns[0].NextEligibleAt != nil &&
			!time.Now().Before(*exploreRuns[0].NextEligibleAt) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Explore attempt %d retry did not become eligible", completedAttempts)
}

func assertPiExploreRecoveryFailure(
	t *testing.T,
	server *httptest.Server,
	runtime *deliveryObservedRuntime,
	projectID string,
	control *controlplane.HTTPRunControl,
	lease rundomain.DispatcherLease,
	secrets rundomain.SecretEnvironmentProvider,
	image string,
) {
	t.Helper()
	first, _ := deliveryActivity(t, server, projectID)
	if len(first.Runs) != 2 {
		t.Fatalf("initial Explore recovery failure run count: %+v", first.Runs)
	}
	var firstExplore rundomain.ActivityRun
	for _, run := range first.Runs {
		if run.Task == rundomain.TaskExplore {
			firstExplore = run
		}
	}
	if firstExplore.Status != rundomain.StatusFailed || firstExplore.ErrorCode != rundomain.ExecutionProcessFailed {
		t.Fatalf("initial Explore recovery failure was not a process failure: %+v", first.Runs)
	}
	waitForPiExploreRetry(t, server, projectID, 1)

	// A failed Recovery releases the original Claim through FinishRun and may
	// create only the configured next Attempt. The new Attempt gets a new
	// Session; it cannot re-enter the old Run's Recovery path.
	// The fixture's worker name is encoded in the Project title and the mode is
	// stable, so the same catalog/adapter is rebuilt for the retry.
	projectBeforeRetry := getDeliveryProject(t, server, projectID)
	catalog := piExploreWorkerCatalog(t, image, "pi-explore-recovery-fail", "explore-recovery-fail", true, projectBeforeRetry.Project.ConfigRevision)
	executions, err := NewRunExecutionFactory(catalog, runtime, secrets, control)
	if err != nil {
		t.Fatal(err)
	}
	runPiExploreSchedulerPass(
		t, control, catalog, runtime, executions, lease, projectBeforeRetry.Project.ConfigRevision, piExplorePolicy(2, true),
	)

	activity, _ := deliveryActivity(t, server, projectID)
	var exploreRuns []rundomain.ActivityRun
	for _, run := range activity.Runs {
		if run.Task == rundomain.TaskExplore {
			exploreRuns = append(exploreRuns, run)
		}
	}
	if len(exploreRuns) != 2 {
		t.Fatalf("Recovery failure created more or fewer than one retry Attempt: %+v", exploreRuns)
	}
	for _, run := range exploreRuns {
		if run.Status != rundomain.StatusFailed || run.ErrorCode != rundomain.ExecutionProcessFailed || run.OutcomeKind != "" {
			t.Fatalf("Recovery failure run terminal semantics: %+v", exploreRuns)
		}
	}
	detail := getDeliveryProject(t, server, projectID)
	if detail.Project.Revision != 2 || countDeliveryFact(detail, "abnormal Explore candidate must not commit") != 0 {
		t.Fatalf("Recovery failure left partial Blackboard state: %+v", detail)
	}
	launchesAfter, _ := runtime.Snapshot()
	recoverySessionByRun := make(map[rundomain.RunID]string, len(exploreRuns))
	for _, run := range exploreRuns {
		phases := make(map[rundomain.ExecutionPhase]int)
		sessions := make(map[string]struct{})
		for _, launch := range launchesAfter {
			if launch.Spec.RunID != run.ID {
				continue
			}
			phases[launch.Spec.Phase]++
			if session := deliveryCommandValue(launch.Spec.Command, "--session"); session != "" {
				sessions[session] = struct{}{}
			}
		}
		if phases[rundomain.PhaseExplore] != 1 || phases[rundomain.PhaseExploreRecovery] != 1 ||
			phases[rundomain.PhaseExploreConclude] != 0 || phases[rundomain.PhaseExploreConcludeRecovery] != 0 || len(sessions) != 1 {
			t.Fatalf("Recovery failure recursed or changed Session semantics for %s: phases=%+v sessions=%+v", run.ID, phases, sessions)
		}
		recoverySessionByRun[run.ID] = ""
		for session := range sessions {
			recoverySessionByRun[run.ID] = session
		}
	}
	if len(recoverySessionByRun) != 2 {
		t.Fatalf("Recovery failure did not preserve two retry Sessions: %+v", recoverySessionByRun)
	}
	sessions := make(map[string]rundomain.RunID, len(recoverySessionByRun))
	for runID, session := range recoverySessionByRun {
		if previous, exists := sessions[session]; exists {
			t.Fatalf("technical retry reused Session %q for Runs %s and %s", session, previous, runID)
		}
		sessions[session] = runID
	}
	activeHandles, err := control.ListExecutionHandles(t.Context(), rundomain.ExecutionHandleListRequest{
		RunIDs:       []rundomain.RunID{exploreRuns[0].ID, exploreRuns[1].ID},
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
	})
	if err != nil || len(activeHandles) != 0 {
		t.Fatalf("Recovery failure retained active execution handles: handles=%+v error=%v", activeHandles, err)
	}
}
