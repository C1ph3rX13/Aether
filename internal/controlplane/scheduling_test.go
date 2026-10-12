package controlplane

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

func TestSchedulingSnapshotIsRevisionConsistentAndNonMutating(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	detail, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "bootstrap snapshot", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 1 || len(detail.Intents) != 1 ||
		detail.Intents[0].Kind != project.IntentKindBootstrap || detail.Intents[0].ID != "i001" {
		t.Fatalf("materialized Bootstrap state: %+v", detail)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-a", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	before := schedulingPersistenceCounts(t, application.DB, detail.Project.ID)
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskBootstrap, rundomain.TaskReason},
		CandidateLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	after := schedulingPersistenceCounts(t, application.DB, detail.Project.ID)
	if before != after {
		t.Fatalf("Scheduling Snapshot mutated persistence: before=%+v after=%+v", before, after)
	}
	if len(snapshot.RevokedRunIDs) != 0 || len(snapshot.Work) != 1 || snapshot.Work[0].ProjectID != detail.Project.ID ||
		snapshot.Work[0].Task != rundomain.TaskBootstrap ||
		snapshot.Work[0].Target == nil || snapshot.Work[0].Target.ID != "i001" ||
		snapshot.Work[0].State.WorkKey != snapshot.Work[0].WorkKey {
		t.Fatalf("Bootstrap Scheduling Snapshot: %+v", snapshot)
	}
	if snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.Location() != time.UTC || snapshot.NextProjectCursor != detail.Project.ID {
		t.Fatalf("Scheduling Snapshot observation: %+v", snapshot)
	}

	fallback, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore},
		CandidateLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback.Work) != 1 || fallback.Work[0].Task != rundomain.TaskReason ||
		fallback.Work[0].State.RetryState != rundomain.RetryEligible {
		t.Fatalf("Reason fallback without Bootstrap worker: %+v", fallback.Work)
	}
}

func TestSchedulingSnapshotCarriesTechnicalRetryWorkerAffinityWithoutDeclineCooldown(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "retry affinity snapshot", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-affinity", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
	}
	initial, err := application.ControlPlane.SchedulerSnapshot(t.Context(), request)
	if err != nil || len(initial.Work) != 1 || initial.Work[0].ProjectID != created.Project.ID {
		t.Fatalf("initial scheduling snapshot: snapshot=%+v error=%v", initial, err)
	}
	candidate := initial.Work[0]
	start := rundomain.StartRequest{
		ProjectID: candidate.ProjectID, Task: candidate.Task,
		CandidateGeneration: candidate.Generation, CandidateRevision: candidate.Revision,
		WorkKey: candidate.WorkKey, DispatcherID: lease.InstanceID,
		DispatcherFencingToken: lease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "affinity-v1", PromptTemplateHash: strings.Repeat("a", 64),
			Worker: rundomain.WorkerSnapshot{Name: "previous-worker", Adapter: "mock"},
			Image: rundomain.ImageSnapshot{
				RequestedRef: "worker@sha256:" + strings.Repeat("b", 64),
				ImageID:      "sha256:" + strings.Repeat("b", 64),
				RepoDigest:   "worker@sha256:" + strings.Repeat("b", 64),
			},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 4, PhaseSeconds: 1, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 1,
		},
		RetryPolicy: rundomain.RetryPolicy{
			MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
			Multiplier: 1, DeclineCooldown: time.Hour,
		},
	}
	first, err := application.ControlPlane.StartRun(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ControlPlane.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
		Termination: &rundomain.RunTermination{
			Status: rundomain.StatusFailed, Code: rundomain.ExecutionCaptureFailed,
			CaptureFailureReason: rundomain.CaptureFailureInvalidCandidate,
		},
	}); err != nil {
		t.Fatal(err)
	}

	var retry scheduler.ControlSchedulingSnapshot
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		retry, err = application.ControlPlane.SchedulerSnapshot(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(retry.Work) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(retry.Work) != 1 {
		t.Fatalf("technical retry did not become schedulable: %+v", retry)
	}
	state := retry.Work[0].State
	if state.PreviousWorker != "previous-worker" || len(state.WorkerRetryAfter) != 0 ||
		state.Attempt != 1 || state.RetryState != rundomain.RetryBackoff {
		t.Fatalf("technical retry affinity state: %+v", state)
	}
}

func TestSchedulingSnapshotBoundsAndRotatesProjectCandidates(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	projectIDs := make([]string, 3)
	for index := range projectIDs {
		detail, createErr := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: "bounded scheduling", Origin: "origin", Goal: "goal",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		projectIDs[index] = detail.Project.ID
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-window", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 2,
	}
	first, err := application.ControlPlane.SchedulerSnapshot(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Work) != 2 || first.Work[0].ProjectID != projectIDs[0] || first.Work[1].ProjectID != projectIDs[1] ||
		first.NextProjectCursor != projectIDs[1] {
		t.Fatalf("first scheduling window: %+v", first)
	}
	for _, candidate := range first.Work {
		if candidate.ProjectID == "" || candidate.WorkKey == "" {
			t.Fatalf("candidate identity: %+v", candidate)
		}
	}

	request.ProjectCursor = first.NextProjectCursor
	second, err := application.ControlPlane.SchedulerSnapshot(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Work) != 2 || second.Work[0].ProjectID != projectIDs[2] || second.Work[1].ProjectID != projectIDs[0] ||
		second.NextProjectCursor != projectIDs[0] {
		t.Fatalf("rotated scheduling window: %+v", second)
	}
}

func TestSchedulingSnapshotAppliesCandidateLimitAfterEligibility(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	covered, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "covered Reason", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	schedulingProposeIntents(t, application, covered, []string{"already covered"})
	runnable, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "runnable Reason", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-eligibility-window", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Work) != 1 || snapshot.Work[0].ProjectID != runnable.Project.ID ||
		snapshot.NextProjectCursor != runnable.Project.ID {
		t.Fatalf("eligibility-filtered window: %+v", snapshot)
	}
}

func TestSchedulingSnapshotInterleavesOldestExploreIntentsByProject(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	first, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "first fair project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "second fair project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	schedulingProposeIntents(t, application, first, []string{"first", "second", "third"})
	schedulingProposeIntents(t, application, second, []string{"first", "second"})

	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-fair-intents", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore}, CandidateLimit: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProjects := []string{first.Project.ID, second.Project.ID, first.Project.ID, second.Project.ID}
	wantIntents := []string{"i001", "i001", "i002", "i002"}
	if len(snapshot.Work) != len(wantProjects) {
		t.Fatalf("fair Explore window: %+v", snapshot.Work)
	}
	for index, candidate := range snapshot.Work {
		if candidate.ProjectID != wantProjects[index] || candidate.Task != rundomain.TaskExplore ||
			candidate.Target == nil || candidate.Target.ID != wantIntents[index] {
			t.Fatalf("candidate %d: got %+v, want Project=%s Intent=%s", index, candidate, wantProjects[index], wantIntents[index])
		}
	}
}

func TestSchedulingSnapshotKeepsUnevenExploreWindowsFair(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	large, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "large fair project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	small, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "small fair project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	schedulingProposeIntents(t, application, large, []string{"first", "second", "third", "fourth"})
	schedulingProposeIntents(t, application, small, []string{"only"})

	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-uneven-fair-intents", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskExplore}, CandidateLimit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProjects := []string{large.Project.ID, small.Project.ID, large.Project.ID}
	wantIntents := []string{"i001", "i001", "i002"}
	if len(snapshot.Work) != len(wantProjects) {
		t.Fatalf("uneven Explore window: %+v", snapshot.Work)
	}
	for index, candidate := range snapshot.Work {
		if candidate.ProjectID != wantProjects[index] || candidate.Target == nil || candidate.Target.ID != wantIntents[index] {
			t.Fatalf("candidate %d: got %+v, want Project=%s Intent=%s", index, candidate, wantProjects[index], wantIntents[index])
		}
	}
}

func TestSchedulingSnapshotBoundsLargeIntentCollection(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	detail, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "large scheduling set", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := application.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	for index := 0; index < 2500; index++ {
		if _, err := tx.ExecContext(t.Context(), `
			INSERT INTO intents (project_id, id, kind, description, creator, created_at)
			VALUES (?, ?, 'explore', 'bounded fixture', 'fixture', ?)`,
			detail.Project.ID, fmt.Sprintf("bulk-%04d", index), createdAt,
		); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-large-intents", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskExplore}, CandidateLimit: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Work) != 7 {
		t.Fatalf("large Intent window has %d candidates", len(snapshot.Work))
	}
	for index, candidate := range snapshot.Work {
		want := fmt.Sprintf("bulk-%04d", index)
		if candidate.Target == nil || candidate.Target.ID != want {
			t.Fatalf("candidate %d = %+v, want target %s", index, candidate, want)
		}
	}
	tx, err = application.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < rundomain.MaximumSchedulingInspectedWork; index++ {
		intentID := fmt.Sprintf("bulk-%04d", index)
		if _, err := tx.ExecContext(t.Context(), `
			INSERT INTO execution_work_keys
				(work_key, project_id, task_type, target_intent_id, created_at, retry_state)
			VALUES (?, ?, 'explore', ?, ?, 'exhausted')`,
			fmt.Sprintf("intent:%s:%s:explore", detail.Project.ID, intentID),
			detail.Project.ID, intentID, createdAt,
		); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snapshot, err = application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskExplore}, CandidateLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTarget := fmt.Sprintf("bulk-%04d", rundomain.MaximumSchedulingInspectedWork)
	if len(snapshot.Work) != 1 || snapshot.Work[0].Target == nil || snapshot.Work[0].Target.ID != wantTarget {
		t.Fatalf("eligibility-filtered large Intent window: %+v", snapshot.Work)
	}
}

func TestSchedulingSnapshotReturnsNoPartialResultWhenRunProjectionFails(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	for index := 0; index < 2; index++ {
		if _, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: "atomic scheduling", Origin: "origin", Goal: "goal",
		}); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := application.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO execution_work_keys
			(work_key, project_id, task_type, created_at, retry_state, next_eligible_at)
		VALUES ('reason:proj_002:g1:r1', 'proj_002', 'reason', ?, 'backoff', 'not-a-timestamp')`,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-atomic-snapshot", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 2,
	})
	if err == nil {
		t.Fatalf("invalid Run projection returned snapshot: %+v", snapshot)
	}
	if len(snapshot.Work) != 0 || !snapshot.ObservedAt.IsZero() || snapshot.NextProjectCursor != "" {
		t.Fatalf("partial scheduling result escaped: %+v", snapshot)
	}
}

func schedulingProposeIntents(t *testing.T, application *testAssembly, detail project.ProjectDetail, descriptions []string) {
	t.Helper()
	proposed := make([]project.ProposedRunIntent, len(descriptions))
	for index, description := range descriptions {
		proposed[index] = project.ProposedRunIntent{From: []string{"origin"}, Description: description}
	}
	tx, err := application.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	appliedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := application.ProjectService.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
		RunID: "fixture-" + detail.Project.ID, ProjectID: detail.Project.ID, Task: project.RunTaskReason,
		Generation: detail.Project.Generation, InputRevision: detail.Project.Revision,
		Worker: "fixture.reasoner", AppliedAt: appliedAt,
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: proposed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.RunService.EnsureReasonSchedulingBaseline(
		t.Context(), tx, detail.Project.ID, detail.Project.Generation, result.ProjectRevision, appliedAt,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulingSnapshotFallsBackToReasonAfterBootstrapCorrections(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	cancelled, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "cancelled bootstrap", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ProjectService.CancelIntent(t.Context(), cancelled.Project.ID, cancelled.Intents[0].ID, project.CancelIntentRequest{
		Reason: "bootstrap is not applicable", ExpectedRevision: cancelled.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	corrected, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "corrected bootstrap", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := application.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	targetIntentID := corrected.Intents[0].ID
	applied, err := application.ProjectService.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
		RunID: "bootstrap-conclusion", ProjectID: corrected.Project.ID,
		Task: project.RunTaskBootstrap, TargetIntent: &targetIntentID,
		Generation: corrected.Project.Generation, InputRevision: corrected.Project.Revision,
		Worker: "fixture.bootstrap", AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeConcludeIntent, FactDescription: "bootstrap evidence"},
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(applied.CreatedFactIDs) != 1 {
		t.Fatalf("Bootstrap conclusion result: %+v", applied)
	}
	superseded, err := application.ProjectService.SupersedeFact(t.Context(), corrected.Project.ID, applied.CreatedFactIDs[0], project.SupersedeFactRequest{
		Description: "replacement evidence", Reason: "correct the bootstrap output", ExpectedRevision: applied.ProjectRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ProjectService.RetractFact(t.Context(), corrected.Project.ID, superseded.Replacement.ID, project.RetractFactRequest{
		Reason: "replacement is also invalid", ExpectedRevision: superseded.ProjectRevision,
	}); err != nil {
		t.Fatal(err)
	}

	healthy, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "unrelated project", Origin: "origin", Goal: "goal", BootstrapEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-bootstrap-corrections", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskBootstrap, rundomain.TaskReason},
		CandidateLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantProjects := map[string]bool{
		cancelled.Project.ID: false,
		corrected.Project.ID: false,
		healthy.Project.ID:   false,
	}
	for _, work := range snapshot.Work {
		if _, tracked := wantProjects[work.ProjectID]; tracked && work.Task == rundomain.TaskReason {
			wantProjects[work.ProjectID] = true
		}
	}
	for projectID, found := range wantProjects {
		if !found {
			t.Fatalf("Project %s has no Reason fallback in Scheduling Snapshot: %+v", projectID, snapshot.Work)
		}
	}
}

func TestSchedulerSnapshotExcludesRunInputsAndPausedProjects(t *testing.T) {
	application, err := newTestAssembly(filepath.Join(t.TempDir(), "aether.db"), ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	active, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "active scheduling", Origin: "origin", Goal: "goal", BootstrapEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := application.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	appliedAt := time.Now().UTC().Format(time.RFC3339Nano)
	applied, err := application.ProjectService.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
		RunID: "snapshot-fixture", ProjectID: active.Project.ID, Task: project.RunTaskReason,
		Generation: active.Project.Generation, InputRevision: active.Project.Revision,
		Worker: "fixture.reasoner", AppliedAt: appliedAt,
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{{
			From: []string{"origin"}, Description: "investigate",
		}}},
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := application.RunService.EnsureReasonSchedulingBaseline(t.Context(), tx, active.Project.ID, active.Project.Generation, applied.ProjectRevision, appliedAt); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	paused, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "paused scheduling", Origin: "origin", Goal: "goal", BootstrapEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ProjectService.PauseProject(t.Context(), paused.Project.ID, project.ProjectLifecycleRequest{ExpectedRevision: paused.Project.Revision}); err != nil {
		t.Fatal(err)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-scheduler-projection", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	inProcess, err := NewInProcessRunControl(application.ControlPlane)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := inProcess.SchedulingSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore},
		CandidateLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RevokedRunIDs) != 0 {
		t.Fatalf("unexpected scheduling Run revocations: %+v", snapshot.RevokedRunIDs)
	}
	statuses, err := application.ControlPlane.ProjectRuntimeStatuses(t.Context(), rundomain.ProjectRuntimeStatusRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		ProjectIDs: []string{active.Project.ID, paused.Project.ID},
	})
	if err != nil || len(statuses) != 2 || statuses[0].Status != rundomain.ProjectRuntimeActive || statuses[1].Status != rundomain.ProjectRuntimePaused {
		t.Fatalf("Project runtime status batch: statuses=%+v error=%v", statuses, err)
	}
	if len(snapshot.Work) != 1 {
		t.Fatalf("scheduler Work: %+v", snapshot.Work)
	}
	for _, work := range snapshot.Work {
		if work.ProjectID != active.Project.ID || work.WorkKey == "" || work.State.WorkKey != work.WorkKey {
			t.Fatalf("scheduler candidate: %+v", work)
		}
		if work.Task == rundomain.TaskExplore && (work.Target == nil || work.Target.ID == "") {
			t.Fatalf("Explore target: %+v", work)
		}
	}
}

type schedulingCounts struct {
	Intents   int
	Changes   int
	Revisions int64
}

func schedulingPersistenceCounts(t *testing.T, db *database.DB, projectID string) schedulingCounts {
	t.Helper()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var counts schedulingCounts
	if err := conn.QueryRowContext(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM intents WHERE project_id = p.id),
			(SELECT COUNT(*) FROM blackboard_changes WHERE project_id = p.id),
			p.blackboard_revision
		FROM projects p WHERE p.id = ?`, projectID,
	).Scan(&counts.Intents, &counts.Changes, &counts.Revisions); err != nil {
		t.Fatal(err)
	}
	return counts
}
