package run_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestBeginProcessRecoveryReservesOneExploreRunOpportunity(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	created, err := projectService.Create(ctx, CreateProjectRequest{Title: "recovery", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	intent := proposeIntent(t, projectService, runService, created.Project.ID, "investigate", []string{"origin"})
	current, err := projectService.Get(ctx, created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, rundomain.TaskExplore, intent.ID)
	request.Spec.Worker = rundomain.WorkerSnapshot{
		Name: "explorer", Adapter: "mock",
		Capabilities: []string{"explore", rundomain.ProcessRecoveryCapability},
	}
	request.Spec.Timeouts = rundomain.TimeoutSnapshot{
		RunSeconds: 15, PhaseSeconds: 5, FallbackPhaseSeconds: 2, RecoveryPhaseSeconds: 2, GraceSeconds: 1,
	}
	request.Spec.MaxProposedIntents = 0
	request.Spec.Worker.SecretReferences = nil
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := runService.BeginProcessRecovery(ctx, rundomain.BeginProcessRecoveryRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
		FailedPhase: rundomain.PhaseExploreConclude, Mode: rundomain.ProcessRecoveryExploreConclude,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.RunID != started.Run.ID || reservation.Phase != rundomain.PhaseExploreConcludeRecovery ||
		reservation.PhaseTimeout != 2*time.Second {
		t.Fatalf("reservation: %+v", reservation)
	}
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.RecoveryUsed || stored.RecoveryPhase != rundomain.PhaseExploreConcludeRecovery || stored.RecoveryStartedAt == nil {
		t.Fatalf("persisted recovery reservation: %+v", stored)
	}
	assertRunErrorCode(t, rundomain.ErrorConflict, func() error {
		_, err := runService.BeginProcessRecovery(ctx, rundomain.BeginProcessRecoveryRequest{
			RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
			DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
			FailedPhase: rundomain.PhaseExplore, Mode: rundomain.ProcessRecoveryExplorePrimary,
		})
		return err
	})
}

func TestBeginProcessRecoveryRejectsInsufficientRemainingRunDeadline(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return base }
	created, err := projectService.Create(ctx, CreateProjectRequest{Title: "deadline", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	intent := proposeIntent(t, projectService, runService, created.Project.ID, "investigate", []string{"origin"})
	current, err := projectService.Get(ctx, created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, rundomain.TaskExplore, intent.ID)
	request.Spec.Worker = rundomain.WorkerSnapshot{
		Name: "explorer", Adapter: "mock", Capabilities: []string{"explore", rundomain.ProcessRecoveryCapability},
	}
	request.Spec.Timeouts = rundomain.TimeoutSnapshot{
		RunSeconds: 9, PhaseSeconds: 1, FallbackPhaseSeconds: 1, RecoveryPhaseSeconds: 2, GraceSeconds: 1,
	}
	request.Spec.MaxProposedIntents = 0
	request.Spec.Worker.SecretReferences = nil
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	runService.clock = func() time.Time { return base.Add(7 * time.Second) }
	_, err = runService.BeginProcessRecovery(ctx, rundomain.BeginProcessRecoveryRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
		FailedPhase: rundomain.PhaseExplore, Mode: rundomain.ProcessRecoveryExplorePrimary,
	})
	assertRunErrorCode(t, rundomain.ExecutionDeadline, func() error { return err })
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RecoveryUsed {
		t.Fatalf("deadline rejection consumed recovery opportunity: %+v", stored)
	}
}

func TestBeginProcessRecoveryConcurrentReservationsAreSerialized(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	created, err := projectService.Create(ctx, CreateProjectRequest{Title: "concurrent recovery", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	intent := proposeIntent(t, projectService, runService, created.Project.ID, "investigate", []string{"origin"})
	current, err := projectService.Get(ctx, created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, rundomain.TaskExplore, intent.ID)
	request.Spec.Worker = rundomain.WorkerSnapshot{
		Name: "explorer", Adapter: "mock", Capabilities: []string{"explore", rundomain.ProcessRecoveryCapability},
	}
	request.Spec.Timeouts = rundomain.TimeoutSnapshot{
		RunSeconds: 15, PhaseSeconds: 5, FallbackPhaseSeconds: 2, RecoveryPhaseSeconds: 2, GraceSeconds: 1,
	}
	request.Spec.MaxProposedIntents = 0
	request.Spec.Worker.SecretReferences = nil
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	recoveryRequest := rundomain.BeginProcessRecoveryRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
		FailedPhase: rundomain.PhaseExplore, Mode: rundomain.ProcessRecoveryExplorePrimary,
	}
	const callers = 8
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, callErr := runService.BeginProcessRecovery(ctx, recoveryRequest)
			results <- callErr
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for callErr := range results {
		if callErr == nil {
			successes++
			continue
		}
		var domainErr *rundomain.DomainError
		if errors.As(callErr, &domainErr) && domainErr.Code == rundomain.ErrorConflict {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent reservation error: %v", callErr)
	}
	if successes != 1 || conflicts != callers-1 {
		t.Fatalf("concurrent recovery reservations: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestBeginProcessRecoveryDoesNotTrustMismatchedClaimIdentity(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	created, err := projectService.Create(ctx, CreateProjectRequest{Title: "claim recovery", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	intent := proposeIntent(t, projectService, runService, created.Project.ID, "investigate", []string{"origin"})
	current, err := projectService.Get(ctx, created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, rundomain.TaskExplore, intent.ID)
	request.Spec.Worker = rundomain.WorkerSnapshot{
		Name: "explorer", Adapter: "mock", Capabilities: []string{"explore", rundomain.ProcessRecoveryCapability},
	}
	request.Spec.Timeouts = rundomain.TimeoutSnapshot{
		RunSeconds: 15, PhaseSeconds: 5, FallbackPhaseSeconds: 2, RecoveryPhaseSeconds: 2, GraceSeconds: 1,
	}
	request.Spec.MaxProposedIntents = 0
	request.Spec.Worker.SecretReferences = nil
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, "DELETE FROM execution_claims WHERE run_id = ?", started.Run.ID)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = runService.BeginProcessRecovery(ctx, rundomain.BeginProcessRecoveryRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
		FailedPhase: rundomain.PhaseExplore, Mode: rundomain.ProcessRecoveryExplorePrimary,
	})
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error { return err })
}

func TestBeginProcessRecoveryReservesOneBootstrapRunOpportunity(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	created, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "bootstrap recovery", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Intents) != 1 || !schedulingIsBootstrapIntent(created.Intents[0]) {
		t.Fatalf("Bootstrap intent: %+v", created.Intents)
	}
	current, err := projectService.Get(ctx, created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, rundomain.TaskBootstrap, created.Intents[0].ID)
	request.Spec.Worker = rundomain.WorkerSnapshot{
		Name: "bootstrapper", Adapter: "mock",
		Capabilities: []string{rundomain.ProcessRecoveryCapability},
	}
	request.Spec.Timeouts = rundomain.TimeoutSnapshot{
		RunSeconds: 15, PhaseSeconds: 5, FallbackPhaseSeconds: 2, RecoveryPhaseSeconds: 2, GraceSeconds: 1,
	}
	request.Spec.MaxProposedIntents = 0
	request.Spec.Worker.SecretReferences = nil
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := runService.BeginProcessRecovery(ctx, rundomain.BeginProcessRecoveryRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
		FailedPhase: rundomain.PhaseBootstrapConclude, Mode: rundomain.ProcessRecoveryBootstrapConclude,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.RunID != started.Run.ID || reservation.Phase != rundomain.PhaseBootstrapConcludeRecovery ||
		reservation.PhaseTimeout != 2*time.Second {
		t.Fatalf("reservation: %+v", reservation)
	}
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.RecoveryUsed || stored.RecoveryPhase != rundomain.PhaseBootstrapConcludeRecovery || stored.RecoveryStartedAt == nil {
		t.Fatalf("persisted Bootstrap recovery reservation: %+v", stored)
	}
	assertRunErrorCode(t, rundomain.ErrorConflict, func() error {
		_, err := runService.BeginProcessRecovery(ctx, rundomain.BeginProcessRecoveryRequest{
			RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
			DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
			FailedPhase: rundomain.PhaseBootstrap, Mode: rundomain.ProcessRecoveryBootstrapPrimary,
		})
		return err
	})
}

func TestStartRunAtomicallyPersistsRunClaimAndSafeSnapshot(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t, project.ServiceOptions{
		RuntimePolicy: project.RuntimePolicy{IntentLeaseSeconds: 31, ReasonLeaseSeconds: 42},
	})
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "atomic start", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(project.Project)
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(requestJSON), `"input"`) {
		t.Fatalf("Run Admission request carried Project input: %s", requestJSON)
	}
	resolvedSecret := "sk-resolved-value-must-never-be-persisted"
	_ = map[string]string{"OPENAI_API_KEY": resolvedSecret}
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.Status != rundomain.StatusActive || started.Run.FencingToken <= 0 || started.Run.Attempt != 1 {
		t.Fatalf("started run: %+v", started.Run)
	}
	if started.BlackboardInput == nil || started.BootstrapInput != nil {
		t.Fatalf("Reason Admitted Run input union: %+v", started)
	}
	inputIdentity, err := started.BlackboardInput.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.Spec.Input.Identity != inputIdentity ||
		started.Run.Spec.Input.Revision != started.BlackboardInput.Revision {
		t.Fatalf("admitted input identity: run=%+v input=%+v", started.Run.Spec.Input, inputIdentity)
	}
	if started.Claim.RunID != started.Run.ID || started.Claim.FencingToken != started.Run.FencingToken || started.Claim.WorkKey != started.Run.WorkKey {
		t.Fatalf("claim does not own started run: run=%+v claim=%+v", started.Run, started.Claim)
	}
	if started.Run.WorkKey != rundomain.WorkKey("reason:"+project.Project.ID+":g1:r1") {
		t.Fatalf("work key: %q", started.Run.WorkKey)
	}
	if !started.Claim.LeaseExpiresAt.Equal(started.Claim.ClaimedAt.Add(42 * time.Second)) {
		t.Fatalf("claim lease: %+v", started.Claim)
	}

	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Worker.Name != "reasoner" || stored.Spec.Image.RequestedRef == "" ||
		stored.Spec.ResultSchema != "aether.run-result/v1" {
		t.Fatalf("stored execution spec: %+v", stored.Spec)
	}
	if got := stored.Spec.Worker.Capabilities; len(got) != 2 || got[0] != "blackboard-read" || got[1] != "reason" {
		t.Fatalf("canonical capabilities: %v", got)
	}

	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var runCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_runs WHERE id = ?", started.Run.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	var claimRunID string
	var claimToken int64
	if err := conn.QueryRowContext(ctx,
		"SELECT run_id, fencing_token FROM execution_claims WHERE work_key = ?", started.Run.WorkKey,
	).Scan(&claimRunID, &claimToken); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || claimRunID != string(started.Run.ID) || claimToken != int64(started.Run.FencingToken) {
		t.Fatalf("persisted ownership: runs=%d claim_run=%q claim_token=%d", runCount, claimRunID, claimToken)
	}
	var snapshot, inputHash string
	if err := conn.QueryRowContext(ctx,
		"SELECT execution_spec_json, input_hash FROM execution_runs WHERE id = ?", started.Run.ID,
	).Scan(&snapshot, &inputHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot, resolvedSecret) {
		t.Fatalf("resolved secret entered execution snapshot: %s", snapshot)
	}
	if !strings.Contains(snapshot, `"environment_variable":"OPENAI_API_KEY"`) || !strings.Contains(snapshot, `"provider":"environment"`) {
		t.Fatalf("secret reference missing from execution snapshot: %s", snapshot)
	}
	if !strings.Contains(snapshot, `"identity":{"schema":"aether.input-hash/v1","sha256":"`) ||
		inputHash != started.Run.Spec.Input.Identity.SHA256 {
		t.Fatalf("versioned Input Identity was not persisted: hash=%q snapshot=%s", inputHash, snapshot)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE execution_runs SET execution_spec_json = '{}' WHERE id = ?", started.Run.ID,
	); err == nil || !strings.Contains(err.Error(), "spec is immutable") {
		t.Fatalf("expected immutable execution snapshot, got %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE execution_claims SET fencing_token = fencing_token + 1 WHERE run_id = ?", started.Run.ID,
	); err == nil || !strings.Contains(err.Error(), "claim identity is immutable") {
		t.Fatalf("expected immutable execution claim, got %v", err)
	}
}

func TestReasonSchedulingIgnoresItsOwnIntentProposalUntilIntentResolves(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "reason trigger", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	proposal := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"investigate"}]}}`)
	if _, err := runService.FinishRun(t.Context(), finishRequest(started, proposal)); err != nil {
		t.Fatal(err)
	}
	current, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Intents) != 1 || current.Project.Revision <= project.Project.Revision {
		t.Fatalf("Reason proposal: %+v", current)
	}
	state := inspectReasonSchedulingState(t, runService, current)
	if state.RetryState != rundomain.RetryClosed {
		t.Fatalf("Reason self-trigger was not suppressed: %+v", state)
	}
	if _, err := projectService.CancelIntent(t.Context(), current.Project.ID, current.Intents[0].ID, CancelIntentRequest{
		Reason: "resolved elsewhere", ExpectedRevision: current.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := projectService.Get(t.Context(), current.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	state = inspectReasonSchedulingState(t, runService, resolved)
	if state.RetryState != rundomain.RetryEligible {
		t.Fatalf("resolved Intent did not trigger Reason: %+v", state)
	}
}

func TestReasonSchedulingReopensAfterBlackboardCorrections(t *testing.T) {
	t.Run("withdrawn hint", func(t *testing.T) {
		projectService, runService, cleanup := newRunTestServices(t)
		defer cleanup()
		project, err := projectService.Create(t.Context(), CreateProjectRequest{
			Title: "hint correction", Origin: "origin", Goal: "goal",
			Hints: []CreateHintInline{{Content: "tentative"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		recordReasonCheckpoint(t, runService, project)
		if _, err := projectService.WithdrawHint(t.Context(), project.Project.ID, "h001", WithdrawHintRequest{
			Reason: "invalidated", ExpectedRevision: project.Project.Revision,
		}); err != nil {
			t.Fatal(err)
		}
		current, err := projectService.Get(t.Context(), project.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state := inspectReasonSchedulingState(t, runService, current); state.RetryState != rundomain.RetryEligible {
			t.Fatalf("withdrawn Hint did not trigger Reason: %+v", state)
		}
	})

	t.Run("retracted fact", func(t *testing.T) {
		projectService, runService, cleanup := newRunTestServices(t)
		defer cleanup()
		project, err := projectService.Create(t.Context(), CreateProjectRequest{
			Title: "fact correction", Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		intent := proposeIntent(t, projectService, runService, project.Project.ID, "verify", []string{"origin"})
		_, concludedFact := concludeIntent(t, projectService, runService, project.Project.ID, intent.ID, "evidence")
		current, err := projectService.Get(t.Context(), project.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		recordReasonCheckpoint(t, runService, current)
		if _, err := projectService.RetractFact(t.Context(), project.Project.ID, concludedFact.ID, RetractFactRequest{
			Reason: "invalidated", ExpectedRevision: current.Project.Revision,
		}); err != nil {
			t.Fatal(err)
		}
		current, err = projectService.Get(t.Context(), project.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state := inspectReasonSchedulingState(t, runService, current); state.RetryState != rundomain.RetryEligible {
			t.Fatalf("retracted Fact did not trigger Reason: %+v", state)
		}
	})
}

func TestReasonSchedulingPersistsBaselineForExistingOpenIntents(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "existing work", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposeIntent(t, projectService, runService, project.Project.ID, "already in progress", []string{"origin"})
	current, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var baseline int64
	if err := conn.QueryRowContext(t.Context(), `
		SELECT input_revision FROM reason_scheduling_baselines
		WHERE project_id = ? AND generation = ?`, current.Project.ID, current.Project.Generation,
	).Scan(&baseline); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	if baseline != current.Project.Revision {
		t.Fatalf("baseline revision=%d, want %d", baseline, current.Project.Revision)
	}
	if _, err := projectService.CreateHint(t.Context(), current.Project.ID, CreateHintRequest{
		Content: "new evidence", ExpectedRevision: current.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := projectService.Get(t.Context(), current.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state := inspectReasonSchedulingState(t, runService, changed); state.RetryState != rundomain.RetryEligible {
		t.Fatalf("change before first inspection was swallowed by the baseline: %+v", state)
	}
}

func TestReasonSchedulingDefaultsRunnableWhenReplayIntervalIsIncomplete(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "missing replay", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	recordReasonCheckpoint(t, runService, created)
	changed, err := projectService.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "evidence whose history is unavailable", ExpectedRevision: created.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO blackboard_history_retention_operations
			(operation_id, project_id, active, created_at)
		VALUES ('missing-replay-fixture', ?, 1, ?)`,
		created.Project.ID, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM blackboard_changes WHERE project_id = ? AND revision = ?",
		created.Project.ID, changed.ProjectRevision,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	current, err := projectService.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state := inspectReasonSchedulingState(t, runService, current); state.RetryState != rundomain.RetryEligible {
		t.Fatalf("incomplete replay suppressed Reason: %+v", state)
	}
}

func recordReasonCheckpoint(t *testing.T, runService *RunService, project ProjectDetail) {
	t.Helper()
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.FinishRun(t.Context(), finishRequest(started,
		[]byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`))); err != nil {
		t.Fatal(err)
	}
}

func inspectReasonSchedulingState(t *testing.T, runService *RunService, project ProjectDetail) rundomain.SchedulingWorkState {
	t.Helper()
	input := rundomain.InputSnapshot{Generation: project.Project.Generation, Revision: project.Project.Revision}
	workKey, err := rundomain.WorkKeyFor(project.Project.ID, rundomain.TaskReason, input)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, intent := range project.Intents {
		if intent.IsOpen() {
			open++
		}
	}
	states := inspectSchedulingStates(t, runService, []rundomain.SchedulingWorkQuery{{
		WorkKey: workKey, ProjectID: project.Project.ID, Task: rundomain.TaskReason,
		Generation: project.Project.Generation, Revision: project.Project.Revision, HasOpenIntents: open > 0,
	}})
	if len(states) != 1 {
		t.Fatalf("Reason scheduling states: %+v", states)
	}
	return states[0]
}

func TestInspectSchedulingWorkHandlesMaximumBatchWithBulkQueries(t *testing.T) {
	_, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	queries := make([]rundomain.SchedulingWorkQuery, rundomain.MaximumSchedulingWorkKeys)
	for index := range queries {
		queries[index].WorkKey = rundomain.WorkKey(fmt.Sprintf("bulk-work-%04d", index))
	}
	states := inspectSchedulingStates(t, runService, queries)
	if len(states) != len(queries) || states[0].WorkKey != queries[0].WorkKey || states[len(states)-1].WorkKey != queries[len(queries)-1].WorkKey {
		t.Fatalf("bulk scheduling states: got %d, first/last=%v/%v", len(states), states[0].WorkKey, states[len(states)-1].WorkKey)
	}
}

func TestInspectSchedulingWorkMarksActiveClaim(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "claimed scheduling", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	state := inspectReasonSchedulingState(t, runService, created)
	if !state.Claimed {
		t.Fatalf("active Claim was absent from scheduling state: %+v", state)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE execution_claims SET lease_expires_at = ? WHERE run_id = ?",
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), started.Run.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	state = inspectReasonSchedulingState(t, runService, created)
	if state.Claimed {
		t.Fatalf("expired Claim still blocked scheduling state: %+v", state)
	}
}

func inspectSchedulingStates(
	t *testing.T,
	runService *RunService,
	queries []rundomain.SchedulingWorkQuery,
) []rundomain.SchedulingWorkState {
	t.Helper()
	tx, err := runService.DB.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := runService.Service.AuthorizeDispatcher(t.Context(), tx, "dispatcher-a", 1); err != nil {
		t.Fatal(err)
	}
	states, err := runService.Service.InspectSchedulingWorkInTransaction(t.Context(), tx, queries, runService.clock())
	if err != nil {
		t.Fatal(err)
	}
	return states
}

func TestStartRunReadinessRejectsBeforeRunOrClaimCreation(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "readiness gate", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	runService.readiness = rundomain.StartReadinessFunc(func(context.Context, rundomain.StartRequest) error {
		return &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "image unavailable"}
	})
	if _, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project)); err == nil {
		t.Fatal("unready request created a Run")
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var runs, claims int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || claims != 0 {
		t.Fatalf("unready admission persisted runs=%d claims=%d", runs, claims)
	}
}

func TestPauseFencesActiveRunWithoutConsumingRetryBudget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "pause active Run", Origin: "origin", Goal: "goal", BootstrapEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(project.Project)
	started, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectService.PauseProject(t.Context(), project.Project.ID, ProjectLifecycleRequest{ExpectedRevision: project.Project.Revision}); err != nil {
		t.Fatal(err)
	}
	paused, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != rundomain.StatusCancelled || paused.ErrorCode != rundomain.ErrorProjectPaused {
		t.Fatalf("paused Run: %+v", paused)
	}
	if _, err := runService.RenewRun(t.Context(), rundomain.RenewRunRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, LeaseExtension: time.Minute,
	}); err == nil {
		t.Fatal("paused Run retained its execution claim")
	}
	if _, err := projectService.ResumeProject(t.Context(), project.Project.ID, ProjectLifecycleRequest{ExpectedRevision: project.Project.Revision + 1}); err != nil {
		t.Fatal(err)
	}
	current, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := runService.StartRun(t.Context(), reasonStartRequest(current.Project))
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Run.WorkKey == started.Run.WorkKey || restarted.Run.Attempt != 1 {
		t.Fatalf("resumed revision must start fresh work: first=%+v restarted=%+v", started.Run, restarted.Run)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var failureCount int
	if err := conn.QueryRowContext(t.Context(),
		"SELECT failure_count FROM execution_work_keys WHERE work_key = ?", started.Run.WorkKey,
	).Scan(&failureCount); err != nil {
		t.Fatal(err)
	}
	if failureCount != 0 {
		t.Fatalf("Pause consumed technical retry budget: failure_count=%d", failureCount)
	}
}

func TestStartRunCompetitionCreatesOneExecutionRight(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "competing start", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(project.Project)

	const contenders = 8
	start := make(chan struct{})
	errorsByContender := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := runService.StartRun(context.Background(), request)
			errorsByContender <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByContender)

	succeeded := 0
	claimed := 0
	for err := range errorsByContender {
		if err == nil {
			succeeded++
			continue
		}
		var domainErr *rundomain.DomainError
		if errors.As(err, &domainErr) && domainErr.Code == rundomain.ErrorWorkClaimed {
			claimed++
			continue
		}
		t.Fatalf("unexpected competing start error: %v", err)
	}
	if succeeded != 1 || claimed != contenders-1 {
		t.Fatalf("competing starts: succeeded=%d claimed=%d", succeeded, claimed)
	}

	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var runs, claims int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || claims != 1 {
		t.Fatalf("persisted competing starts: runs=%d claims=%d", runs, claims)
	}
}

func TestStartRunRejectsStaleInputAndUnavailableTarget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "validated start", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	stale := reasonStartRequest(project.Project)
	stale.CandidateRevision--
	stale.WorkKey = rundomain.WorkKey(fmt.Sprintf(
		"reason:%s:g%d:r%d", stale.ProjectID, stale.CandidateGeneration, stale.CandidateRevision,
	))
	assertRunErrorCode(t, rundomain.ErrorStaleInput, func() error {
		_, err := runService.StartRun(ctx, stale)
		return err
	})
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rejectedRuns, rejectedClaims int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_runs").Scan(&rejectedRuns); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims").Scan(&rejectedClaims); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	if rejectedRuns != 0 || rejectedClaims != 0 {
		t.Fatalf("rejected Admission persisted rows: runs=%d claims=%d", rejectedRuns, rejectedClaims)
	}

	if len(project.Intents) != 1 || !schedulingIsBootstrapIntent(project.Intents[0]) {
		t.Fatalf("materialized Bootstrap intent: %+v", project.Intents)
	}
	current, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	target := project.Intents[0].ID
	bootstrap := reasonStartRequest(current.Project)
	setStartRequestTarget(&bootstrap, rundomain.TaskBootstrap, target)
	bootstrap.Spec.MaxProposedIntents = 0
	bootstrap.Spec.Timeouts.FallbackPhaseSeconds = bootstrap.Spec.Timeouts.PhaseSeconds
	bootstrap.Spec.Timeouts.RunSeconds = 3 * (bootstrap.Spec.Timeouts.PhaseSeconds + bootstrap.Spec.Timeouts.GraceSeconds)
	started, err := runService.StartRun(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.TargetIntentID == nil || *started.Run.TargetIntentID != target ||
		started.Run.WorkKey != rundomain.WorkKey("intent:"+project.Project.ID+":"+target+":bootstrap") {
		t.Fatalf("bootstrap run identity: %+v", started.Run)
	}
	assertRunErrorCode(t, rundomain.ErrorWorkClaimed, func() error {
		_, err := runService.StartRun(ctx, bootstrap)
		return err
	})
	conn, err = runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE execution_claims SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE run_id = ?", started.Run.ID,
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	assertRunErrorCode(t, rundomain.ErrorRetryDeferred, func() error {
		_, err := runService.StartRun(ctx, bootstrap)
		return err
	})
	interrupted, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil || interrupted.NextEligibleAt == nil {
		t.Fatalf("interrupted Bootstrap retry: run=%+v error=%v", interrupted, err)
	}
	now = *interrupted.NextEligibleAt
	bootstrapped, err := runService.StartRun(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapped.Run.ClaimScope != started.Run.ClaimScope || bootstrapped.Run.FencingToken <= started.Run.FencingToken {
		t.Fatalf("Intent fencing did not advance: first=%+v second=%+v", started.Run, bootstrapped.Run)
	}

	missing := "i999"
	missingTarget := bootstrap
	setStartRequestTarget(&missingTarget, rundomain.TaskExplore, missing)
	assertRunErrorCode(t, rundomain.ErrorTargetUnavailable, func() error {
		_, err := runService.StartRun(ctx, missingTarget)
		return err
	})
}

func TestStartRunExploreAdmitsCurrentProjectInputWhenCandidateTargetRemainsValid(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	created, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "Explore admission", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := proposeIntent(t, projectService, runService, created.Project.ID, "investigate", []string{"origin"})
	candidate, err := projectService.Get(ctx, created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(candidate.Project)
	setStartRequestTarget(&request, rundomain.TaskExplore, target.ID)
	request.Spec.MaxProposedIntents = 0
	request.Spec.Timeouts.FallbackPhaseSeconds = request.Spec.Timeouts.PhaseSeconds
	request.Spec.Timeouts.RunSeconds = 3 * (request.Spec.Timeouts.PhaseSeconds + request.Spec.Timeouts.GraceSeconds)

	changed, err := projectService.CreateHint(ctx, created.Project.ID, CreateHintRequest{
		Content: "admit this newer context", ExpectedRevision: candidate.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.BlackboardInput == nil || admitted.BootstrapInput != nil ||
		admitted.Run.Spec.Input.Revision != changed.ProjectRevision ||
		admitted.BlackboardInput.Revision != changed.ProjectRevision ||
		len(admitted.BlackboardInput.Hints) != 1 ||
		!admitted.BlackboardInput.MatchesInputSnapshot(admitted.Run.Spec.Input) {
		t.Fatalf("Explore did not bind the authoritative current input: %+v", admitted)
	}
}

func TestStartRunRejectsOrdinaryIntentAsBootstrapTarget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "ordinary bootstrap", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := proposeIntent(t, projectService, runService, project.Project.ID, "ordinary", []string{"origin"})
	current, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, rundomain.TaskBootstrap, created.ID)
	request.Spec.Timeouts.FallbackPhaseSeconds = request.Spec.Timeouts.PhaseSeconds
	request.Spec.Timeouts.RunSeconds = 3 * (request.Spec.Timeouts.PhaseSeconds + request.Spec.Timeouts.GraceSeconds)
	request.Spec.MaxProposedIntents = 0
	assertRunErrorCode(t, rundomain.ErrorTargetUnavailable, func() error {
		_, err := runService.StartRun(ctx, request)
		return err
	})
}

func TestStartRunReasonClaimSpansRevisionsAndExpiredClaimReleases(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "reason lease", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectService.UpdateTitle(ctx, project.Project.ID, UpdateProjectTitleRequest{
		Title: "new title", ExpectedRevision: project.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	current, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := reasonStartRequest(current.Project)
	assertRunErrorCode(t, rundomain.ErrorWorkClaimed, func() error {
		_, err := runService.StartRun(ctx, secondRequest)
		return err
	})

	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE execution_claims SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE run_id = ?", first.Run.ID,
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	second, err := runService.StartRun(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.WorkKey == first.Run.WorkKey {
		t.Fatalf("new Reason revision reused old Work Key: first=%q second=%q", first.Run.WorkKey, second.Run.WorkKey)
	}
	if second.Run.ClaimScope != first.Run.ClaimScope || second.Run.FencingToken <= first.Run.FencingToken {
		t.Fatalf("Reason fencing did not advance across revisions: first=%+v second=%+v", first.Run, second.Run)
	}
	conn, err = runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var runs, claims int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || claims != 1 {
		t.Fatalf("lazy lease release: runs=%d claims=%d", runs, claims)
	}
}

func TestStartRunRollsBackRunAndFenceWhenClaimInsertFails(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{
		Title: "rollback start", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER reject_execution_claim
		BEFORE INSERT ON execution_claims
		BEGIN
			SELECT RAISE(ABORT, 'reject claim');
		END`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	if _, err := runService.StartRun(ctx, reasonStartRequest(project.Project)); err == nil || !strings.Contains(err.Error(), "reject claim") {
		t.Fatalf("expected claim insertion failure, got %v", err)
	}
	conn, err = runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, table := range []string{"execution_work_keys", "execution_claim_scopes", "execution_runs", "execution_claims"} {
		var count int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s leaked %d rows after rollback", table, count)
		}
	}
}

func reasonStartRequest(project ProjectMeta) rundomain.StartRequest {
	workKey := rundomain.WorkKey(fmt.Sprintf("reason:%s:g%d:r%d", project.ID, project.Generation, project.Revision))
	return rundomain.StartRequest{
		ProjectID:              project.ID,
		Task:                   rundomain.TaskReason,
		CandidateGeneration:    project.Generation,
		CandidateRevision:      project.Revision,
		WorkKey:                workKey,
		DispatcherID:           "dispatcher-a",
		DispatcherFencingToken: 1,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision:     "config-v1",
			PromptTemplateHash: strings.Repeat("a", 64),
			Worker: rundomain.WorkerSnapshot{
				Name:         "reasoner",
				Adapter:      "codex",
				Model:        "gpt-5",
				Capabilities: []string{"reason", "blackboard-read"},
				SecretReferences: []rundomain.SecretReference{{
					EnvironmentVariable: "OPENAI_API_KEY",
					Provider:            "environment",
					Key:                 "OPENAI_API_KEY",
				}},
			},
			Image: rundomain.ImageSnapshot{
				RequestedRef: "aether-agent@sha256:" + strings.Repeat("c", 64),
				ImageID:      "sha256:" + strings.Repeat("d", 64),
				RepoDigest:   "aether-agent@sha256:" + strings.Repeat("c", 64),
			},
			Timeouts:           rundomain.TimeoutSnapshot{RunSeconds: 300, PhaseSeconds: 120, GraceSeconds: 5},
			ResultSchema:       "aether.run-result/v1",
			MaxProposedIntents: 3,
		},
	}
}

func setStartRequestTarget(request *rundomain.StartRequest, task rundomain.TaskType, target string) {
	request.Task = task
	request.TargetIntentID = &target
	request.WorkKey = rundomain.WorkKey(fmt.Sprintf("intent:%s:%s:%s", request.ProjectID, target, task))
}

func assertRunErrorCode(t *testing.T, want rundomain.ErrorCode, command func() error) {
	t.Helper()
	err := command()
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != want {
		t.Fatalf("error=%v, want code %s", err, want)
	}
}
