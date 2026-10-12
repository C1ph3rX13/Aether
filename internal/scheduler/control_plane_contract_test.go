package scheduler_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/controlplane"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

type schedulerControlContract interface {
	scheduler.SchedulingControl
	scheduler.ProjectInventoryControl
	scheduler.RunRecoveryControl
	scheduler.RunLifecycleControl
	scheduler.ProjectCleanupControl
}

type schedulerControlFactory func(*testing.T, *controlplane.ControlPlaneHost) (schedulerControlContract, func())

type projectMetaWire struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Generation int    `json:"generation"`
	Revision   int64  `json:"revision"`
}

type projectDetailWire struct {
	Project projectMetaWire `json:"project"`
}

type deleteReceiptWire struct {
	ProjectID    string `json:"project_id"`
	CleanupJobID string `json:"cleanup_job_id"`
}

func schedulerControlFactories() map[string]schedulerControlFactory {
	return map[string]schedulerControlFactory{
		"in-process": func(t *testing.T, host *controlplane.ControlPlaneHost) (schedulerControlContract, func()) {
			t.Helper()
			control, err := controlplane.NewInProcessRunControl(host.ControlPlane())
			if err != nil {
				t.Fatal(err)
			}
			return control, func() {}
		},
		"http-v1": func(t *testing.T, host *controlplane.ControlPlaneHost) (schedulerControlContract, func()) {
			t.Helper()
			server := httptest.NewServer(host.Handler())
			control, err := controlplane.NewHTTPRunControl(server.URL + "/api/v1")
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			return control, func() {
				control.Close()
				server.Close()
			}
		},
	}
}

func TestRunSchedulerControlAdaptersShareContract(t *testing.T) {
	for name, factory := range schedulerControlFactories() {
		t.Run(name, func(t *testing.T) {
			host := newControlPlaneContractHost(t)
			control, cleanup := factory(t, host)
			defer cleanup()
			runControlPlaneContract(t, host.Handler(), host.ControlPlane(), control)
		})
	}
}

func TestExecutionContractMismatchRetrySemanticsMatchAcrossControlAdapters(t *testing.T) {
	for name, factory := range schedulerControlFactories() {
		t.Run(name, func(t *testing.T) {
			host := newControlPlaneContractHost(t)
			control, cleanup := factory(t, host)
			defer cleanup()
			created := createProject(t, host.Handler(), "contract mismatch "+name, false)
			lease, err := host.ControlPlane().AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
				InstanceID: "dispatcher-mismatch-" + name, TTL: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := control.SchedulingSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
				DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
				SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
			})
			if err != nil || len(snapshot.Work) != 1 || snapshot.Work[0].ProjectID != created.Project.ID {
				t.Fatalf("mismatch candidate: snapshot=%+v error=%v", snapshot, err)
			}
			request := controlPlaneContractCandidateStartRequest(snapshot.Work[0], lease)
			request.RetryPolicy = rundomain.RetryPolicy{
				MaxAttempts: 1, InitialBackoff: time.Hour, MaxBackoff: time.Hour,
				Multiplier: 1, DeclineCooldown: time.Hour,
			}
			first, err := control.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := control.FinishRun(t.Context(), rundomain.FinishRequest{
				RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
				Termination: &rundomain.RunTermination{
					Status: rundomain.StatusFailed, Code: rundomain.ErrorExecutionContractMismatch,
				},
			})
			if err != nil || receipt.Status != rundomain.StatusFailed ||
				receipt.Code != rundomain.ErrorExecutionContractMismatch {
				t.Fatalf("mismatch terminal receipt=%+v error=%v", receipt, err)
			}
			second, err := control.StartRun(t.Context(), request)
			if err != nil {
				t.Fatalf("mismatch consumed %s retry budget: %v", name, err)
			}
			if second.Run.Attempt != 2 || second.Run.PreviousRunID == nil || *second.Run.PreviousRunID != first.Run.ID {
				t.Fatalf("mismatch retry lineage: %+v", second.Run)
			}
		})
	}
}

func TestSchedulingSnapshotAdaptersShareSupportedBoundedContract(t *testing.T) {
	for name, factory := range schedulerControlFactories() {
		t.Run(name, func(t *testing.T) {
			host := newControlPlaneContractHost(t)
			control, cleanup := factory(t, host)
			defer cleanup()
			first := createProject(t, host.Handler(), "first adapter contract", false)
			second := createProject(t, host.Handler(), "second adapter contract", false)
			third := createProject(t, host.Handler(), "third adapter contract", false)
			lease, err := host.ControlPlane().AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
				InstanceID: "dispatcher-bounded-" + name, TTL: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := rundomain.SchedulingSnapshotRequest{
				DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
				SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 2,
			}
			snapshot, err := control.SchedulingSnapshot(t.Context(), request)
			if err != nil || len(snapshot.Work) != 2 || snapshot.Work[0].ProjectID != first.Project.ID ||
				snapshot.Work[1].ProjectID != second.Project.ID {
				t.Fatalf("bounded adapter snapshot: %+v error=%v", snapshot, err)
			}
			request.CandidateLimit = 1
			request.ProjectCursor = first.Project.ID
			rotated, err := control.SchedulingSnapshot(t.Context(), request)
			if err != nil || len(rotated.Work) != 1 || rotated.Work[0].ProjectID != second.Project.ID ||
				rotated.NextProjectCursor != second.Project.ID {
				t.Fatalf("adapter cursor rotation: snapshot=%+v error=%v", rotated, err)
			}
			request.ObservedRunIDs = make([]rundomain.RunID, rundomain.MaximumObservedRunIDs)
			for index := range request.ObservedRunIDs {
				request.ObservedRunIDs[index] = rundomain.RunID(fmt.Sprintf("missing-%04d-%s", index, stringOf('r', 243)))
			}
			maximum, err := control.SchedulingSnapshot(t.Context(), request)
			if err != nil || len(maximum.RevokedRunIDs) != rundomain.MaximumObservedRunIDs {
				t.Fatalf("maximum observed Run adapter request: revocations=%d error=%v", len(maximum.RevokedRunIDs), err)
			}
			pauseProject(t, host.Handler(), first)
			pauseProject(t, host.Handler(), second)
			request = rundomain.SchedulingSnapshotRequest{
				DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
				SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
			}
			thirdSnapshot, err := control.SchedulingSnapshot(t.Context(), request)
			if err != nil || len(thirdSnapshot.Work) != 1 || thirdSnapshot.Work[0].ProjectID != third.Project.ID {
				t.Fatalf("paused projects remained schedulable: snapshot=%+v error=%v", thirdSnapshot, err)
			}
		})
	}
}

func runControlPlaneContract(
	t *testing.T,
	handler http.Handler,
	service controlplane.ControlPlaneCapability,
	control schedulerControlContract,
) {
	t.Helper()
	ctx := t.Context()
	createdProject := createProject(t, handler, "contract", false)
	lease, err := service.AcquireDispatcherLease(ctx, rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-contract", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshotRequest := rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 16,
	}
	snapshot, err := control.SchedulingSnapshot(ctx, snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RevokedRunIDs) != 0 || len(snapshot.Work) != 1 ||
		snapshot.Work[0].Task != rundomain.TaskReason || snapshot.Work[0].State.RetryState != rundomain.RetryEligible ||
		snapshot.ObservedAt.IsZero() || snapshot.NextProjectCursor != createdProject.Project.ID {
		t.Fatalf("scheduling snapshot: %+v", snapshot)
	}
	bootstrap := createProject(t, handler, "bootstrap contract", true)
	bootstrapSnapshot, err := control.SchedulingSnapshot(ctx, rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskBootstrap}, CandidateLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	var bootstrapWork *scheduler.ControlSchedulingWork
	for index := range bootstrapSnapshot.Work {
		if bootstrapSnapshot.Work[index].ProjectID == bootstrap.Project.ID {
			bootstrapWork = &bootstrapSnapshot.Work[index]
			break
		}
	}
	if bootstrapWork == nil || bootstrapWork.Task != rundomain.TaskBootstrap || bootstrapWork.Target == nil {
		t.Fatalf("Bootstrap scheduling contract: %+v", bootstrapSnapshot.Work)
	}
	startRequest := controlPlaneContractCandidateStartRequest(snapshot.Work[0], lease)
	started, err := control.StartRun(ctx, startRequest)
	if err != nil {
		t.Fatal(err)
	}
	if started.BlackboardInput == nil || started.BootstrapInput != nil ||
		!started.BlackboardInput.MatchesInputSnapshot(started.Run.Spec.Input) {
		t.Fatalf("Reason admitted input: %+v", started)
	}
	if data, hash, marshalErr := started.BlackboardInput.MarshalYAML(); marshalErr != nil ||
		len(data) > rundomain.MaximumBlackboardInputBytes || len(hash) != 64 {
		t.Fatalf("Blackboard input contract: bytes=%d hash=%q error=%v", len(data), hash, marshalErr)
	}
	bootstrapRun, err := control.StartRun(ctx, controlPlaneContractCandidateStartRequest(*bootstrapWork, lease))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapRun.BootstrapInput == nil || bootstrapRun.BlackboardInput != nil ||
		!bootstrapRun.BootstrapInput.MatchesInputSnapshot(bootstrapRun.Run.Spec.Input) {
		t.Fatalf("Bootstrap admitted input: %+v", bootstrapRun)
	}
	if data, hash, marshalErr := bootstrapRun.BootstrapInput.MarshalYAML(); marshalErr != nil ||
		len(data) > rundomain.MaximumBootstrapInputBytes || len(hash) != 64 {
		t.Fatalf("Bootstrap input contract: bytes=%d hash=%q error=%v", len(data), hash, marshalErr)
	}
	if _, err := control.FinishRun(ctx, rundomain.FinishRequest{
		RunID: bootstrapRun.Run.ID, FencingToken: bootstrapRun.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusCancelled},
	}); err != nil {
		t.Fatal(err)
	}
	snapshotRequest.ObservedRunIDs = []rundomain.RunID{started.Run.ID}
	ownedSnapshot, err := control.SchedulingSnapshot(ctx, snapshotRequest)
	if err != nil || len(ownedSnapshot.RevokedRunIDs) != 0 {
		t.Fatalf("active owned Run was revoked: snapshot=%+v error=%v", ownedSnapshot, err)
	}
	expiresAt, err := control.RenewRun(ctx, rundomain.RenewRunRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: lease.InstanceID, LeaseExtension: 30 * time.Second,
	})
	if err != nil || !expiresAt.After(started.Claim.LeaseExpiresAt) {
		t.Fatalf("renewed Run: expires=%s error=%v", expiresAt, err)
	}
	finish := rundomain.FinishRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusCancelled},
	}
	receipt, err := control.FinishRun(ctx, finish)
	if err != nil || receipt.Status != rundomain.StatusCancelled || receipt.Replayed {
		t.Fatalf("finished Run: receipt=%+v error=%v", receipt, err)
	}
	replayed, err := control.FinishRun(ctx, finish)
	if err != nil || !replayed.Replayed || replayed.Status != receipt.Status {
		t.Fatalf("replayed Finish: receipt=%+v error=%v", replayed, err)
	}
	revokedSnapshot, err := control.SchedulingSnapshot(ctx, snapshotRequest)
	if err != nil || len(revokedSnapshot.RevokedRunIDs) != 1 || revokedSnapshot.RevokedRunIDs[0] != started.Run.ID {
		t.Fatalf("terminal Run revocation: snapshot=%+v error=%v", revokedSnapshot, err)
	}

	pauseProject(t, handler, bootstrap)
	statuses, err := control.ProjectRuntimeStatuses(ctx, rundomain.ProjectRuntimeStatusRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		ProjectIDs: []string{createdProject.Project.ID, bootstrap.Project.ID, "missing-project"},
	})
	if err != nil || len(statuses) != 3 ||
		statuses[0] != (rundomain.ProjectRuntimeStatusResult{ProjectID: createdProject.Project.ID, Status: rundomain.ProjectRuntimeActive}) ||
		statuses[1] != (rundomain.ProjectRuntimeStatusResult{ProjectID: bootstrap.Project.ID, Status: rundomain.ProjectRuntimePaused}) ||
		statuses[2] != (rundomain.ProjectRuntimeStatusResult{ProjectID: "missing-project", Status: rundomain.ProjectRuntimeMissing}) {
		t.Fatalf("Project runtime status batch: results=%+v error=%v", statuses, err)
	}
	lifecycleProject := createProject(t, handler, "lifecycle revocation", false)
	lifecycleRun, err := control.StartRun(ctx, controlPlaneContractStartRequest(lifecycleProject.Project, lease))
	if err != nil {
		t.Fatal(err)
	}
	pauseProject(t, handler, lifecycleProject)
	lifecycleSnapshotRequest := snapshotRequest
	lifecycleSnapshotRequest.ObservedRunIDs = []rundomain.RunID{lifecycleRun.Run.ID}
	lifecycleSnapshot, err := control.SchedulingSnapshot(ctx, lifecycleSnapshotRequest)
	if err != nil || len(lifecycleSnapshot.RevokedRunIDs) != 1 || lifecycleSnapshot.RevokedRunIDs[0] != lifecycleRun.Run.ID {
		t.Fatalf("Project lifecycle Run revocation: snapshot=%+v error=%v", lifecycleSnapshot, err)
	}
	recovered, err := control.RecoverRuns(ctx, rundomain.RecoveryOptions{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		Reason: "contract recovery",
	})
	if err != nil || len(recovered) != 0 {
		t.Fatalf("recovered Runs: ids=%v error=%v", recovered, err)
	}

	snapshotRequest.DispatcherFencingToken++
	_, err = control.SchedulingSnapshot(ctx, snapshotRequest)
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorFenced {
		t.Fatalf("snapshot fencing error: %v", err)
	}

	deleted := deleteProject(t, handler, createdProject)
	jobs, err := control.LeaseProjectCleanupJobs(ctx, rundomain.CleanupLeaseRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken, Limit: 10,
	})
	if err != nil || len(jobs) != 1 || jobs[0].ID != deleted.CleanupJobID || jobs[0].ProjectID != deleted.ProjectID {
		t.Fatalf("leased cleanup job: jobs=%+v error=%v", jobs, err)
	}
	if err := control.CompleteProjectCleanupJob(ctx, rundomain.CleanupCompletionRequest{
		JobID: jobs[0].ID, DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
	}); err != nil {
		t.Fatal(err)
	}
	fencedProject := createProject(t, handler, "dispatcher fencing revocation", false)
	fencedRun, err := control.StartRun(ctx, controlPlaneContractStartRequest(fencedProject.Project, lease))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseDispatcherLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	newLease, err := service.AcquireDispatcherLease(ctx, rundomain.DispatcherLeaseRequest{
		InstanceID: lease.InstanceID, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	fencedSnapshot, err := control.SchedulingSnapshot(ctx, rundomain.SchedulingSnapshotRequest{
		DispatcherID: newLease.InstanceID, DispatcherFencingToken: newLease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
		ObservedRunIDs: []rundomain.RunID{fencedRun.Run.ID},
	})
	if err != nil || len(fencedSnapshot.RevokedRunIDs) != 1 || fencedSnapshot.RevokedRunIDs[0] != fencedRun.Run.ID {
		t.Fatalf("Dispatcher fencing Run revocation: snapshot=%+v error=%v", fencedSnapshot, err)
	}
}

func controlPlaneContractStartRequest(project projectMetaWire, lease rundomain.DispatcherLease) rundomain.StartRequest {
	workKey, err := rundomain.WorkKeyFor(project.ID, rundomain.TaskReason, rundomain.InputSnapshot{
		Generation: project.Generation, Revision: project.Revision,
	})
	if err != nil {
		panic(err)
	}
	return rundomain.StartRequest{
		ProjectID: project.ID, Task: rundomain.TaskReason,
		CandidateGeneration: project.Generation, CandidateRevision: project.Revision, WorkKey: workKey,
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "contract-v1", PromptTemplateHash: stringOf('a', 64),
			Worker:       rundomain.WorkerSnapshot{Name: "contract", Adapter: "mock"},
			Image:        rundomain.ImageSnapshot{RequestedRef: "aether@sha256:contract", ImageID: "sha256:contract", RepoDigest: "aether@sha256:contract"},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 4, PhaseSeconds: 1, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 1,
		},
		RetryPolicy: rundomain.DefaultRetryPolicy(),
	}
}

func controlPlaneContractCandidateStartRequest(candidate scheduler.ControlSchedulingWork, lease rundomain.DispatcherLease) rundomain.StartRequest {
	request := rundomain.StartRequest{
		ProjectID: candidate.ProjectID, Task: candidate.Task,
		CandidateGeneration: candidate.Generation, CandidateRevision: candidate.Revision,
		WorkKey:      candidate.WorkKey,
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "contract-v1", PromptTemplateHash: stringOf('a', 64),
			Worker:       rundomain.WorkerSnapshot{Name: "contract", Adapter: "mock"},
			Image:        rundomain.ImageSnapshot{RequestedRef: "aether@sha256:contract", ImageID: "sha256:contract", RepoDigest: "aether@sha256:contract"},
			ResultSchema: rundomain.ResultSchemaV1,
		},
		RetryPolicy: rundomain.DefaultRetryPolicy(),
	}
	if candidate.Target != nil {
		targetID := candidate.Target.ID
		request.TargetIntentID = &targetID
	}
	switch candidate.Task {
	case rundomain.TaskReason:
		request.Spec.Timeouts = rundomain.TimeoutSnapshot{RunSeconds: 4, PhaseSeconds: 1, GraceSeconds: 1}
		request.Spec.MaxProposedIntents = 1
	case rundomain.TaskExplore, rundomain.TaskBootstrap:
		request.Spec.Timeouts = rundomain.TimeoutSnapshot{
			RunSeconds: 6, PhaseSeconds: 1, FallbackPhaseSeconds: 1, GraceSeconds: 1,
		}
	}
	return request
}

func newControlPlaneContractHost(t *testing.T) *controlplane.ControlPlaneHost {
	t.Helper()
	host, err := controlplane.NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(); err != nil {
			t.Errorf("Control Plane shutdown: %v", err)
		}
	})
	return host
}

func createProject(t *testing.T, handler http.Handler, title string, bootstrap bool) projectDetailWire {
	t.Helper()
	var detail projectDetailWire
	requestJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": title, "origin": "origin", "goal": "goal", "bootstrap_enabled": bootstrap,
	}, http.StatusCreated, &detail)
	return detail
}

func pauseProject(t *testing.T, handler http.Handler, project projectDetailWire) {
	t.Helper()
	requestJSON[map[string]any](t, handler, http.MethodPost,
		"/api/v1/projects/"+project.Project.ID+"/actions/pause",
		map[string]any{"expected_revision": project.Project.Revision}, http.StatusOK, nil)
}

func deleteProject(t *testing.T, handler http.Handler, project projectDetailWire) deleteReceiptWire {
	t.Helper()
	var receipt deleteReceiptWire
	requestJSON(t, handler, http.MethodPost,
		"/api/v1/projects/"+project.Project.ID+"/actions/delete",
		map[string]any{"expected_title": project.Project.Title, "expected_revision": project.Project.Revision},
		http.StatusOK, &receipt)
	return receipt
}

func requestJSON[T any](t *testing.T, handler http.Handler, method, path string, body any, wantStatus int, target *T) {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &payload)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s: got %d, want %d; body: %s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
			t.Fatalf("decode %s %s: %v; body: %s", method, path, err, recorder.Body.String())
		}
	}
}

func stringOf(value byte, count int) string {
	buffer := make([]byte, count)
	for index := range buffer {
		buffer[index] = value
	}
	return string(buffer)
}
