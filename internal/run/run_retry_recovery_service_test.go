package run_test

import (
	"fmt"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestTechnicalRetryChainPersistsPolicyBackoffAndExhaustion(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "retry chain", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(project.Project)
	request.RetryPolicy = rundomain.RetryPolicy{
		MaxAttempts: 2, InitialBackoff: 10 * time.Second, MaxBackoff: 20 * time.Second,
		Multiplier: 2, DeclineCooldown: time.Second,
	}
	first, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed},
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := runService.GetRun(t.Context(), first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantFirstRetry := now.Add(request.RetryPolicy.BackoffFor(first.Run.WorkKey, 1))
	if stored.NextEligibleAt == nil || !stored.NextEligibleAt.Equal(wantFirstRetry) {
		t.Fatalf("first retry eligibility: %+v", stored.NextEligibleAt)
	}
	assertRunErrorCode(t, rundomain.ErrorRetryDeferred, func() error {
		_, err := runService.StartRun(t.Context(), request)
		return err
	})

	now = wantFirstRetry
	changedPolicy := request
	changedPolicy.RetryPolicy.MaxAttempts = 10
	second, err := runService.StartRun(t.Context(), changedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.Attempt != 2 || second.Run.PreviousRunID == nil || *second.Run.PreviousRunID != first.Run.ID {
		t.Fatalf("retry chain identity: %+v", second.Run)
	}
	if _, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: second.Run.ID, FencingToken: second.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusInterrupted},
	}); err != nil {
		t.Fatal(err)
	}
	assertRunErrorCode(t, rundomain.ErrorRetryExhausted, func() error {
		_, err := runService.StartRun(t.Context(), changedPolicy)
		return err
	})

	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var state string
	var failures, maxAttempts int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT retry_state, failure_count, max_attempts
		FROM execution_work_keys WHERE work_key = ?`, first.Run.WorkKey,
	).Scan(&state, &failures, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if state != string(rundomain.RetryExhausted) || failures != 2 || maxAttempts != 2 {
		t.Fatalf("persisted retry budget: state=%s failures=%d max=%d", state, failures, maxAttempts)
	}
}

func TestTechnicalFailuresKeepPreviousWorkerWithoutDeclineCooldown(t *testing.T) {
	tests := []struct {
		name   string
		code   rundomain.ErrorCode
		reason rundomain.CaptureFailureReason
	}{
		{name: "capture failure", code: rundomain.ExecutionCaptureFailed, reason: rundomain.CaptureFailureInvalidCandidate},
		{name: "deadline", code: rundomain.ExecutionDeadline},
		{name: "process failure", code: rundomain.ExecutionProcessFailed},
		{name: "invalid result", code: rundomain.ExecutionInvalidResult},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectService, runService, cleanup := newRunTestServices(t)
			defer cleanup()
			created, err := projectService.Create(t.Context(), CreateProjectRequest{
				Title: test.name, Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
			runService.clock = func() time.Time { return now }
			request := reasonStartRequest(created.Project)
			request.RetryPolicy = rundomain.RetryPolicy{
				MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: time.Second,
				Multiplier: 1, DeclineCooldown: time.Hour,
			}
			first, err := runService.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
				RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
				Termination: &rundomain.RunTermination{
					Status: rundomain.StatusFailed, Code: test.code, CaptureFailureReason: test.reason,
				},
			}); err != nil {
				t.Fatal(err)
			}

			state := inspectReasonSchedulingState(t, runService, created)
			if state.PreviousWorker != request.Spec.Worker.Name || len(state.WorkerRetryAfter) != 0 ||
				state.RetryState != rundomain.RetryBackoff || state.NextEligibleAt == nil {
				t.Fatalf("technical retry scheduling state: %+v", state)
			}
			now = *state.NextEligibleAt
			second, err := runService.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if second.Run.ID == first.Run.ID || second.Run.Attempt != 2 || second.Run.PreviousRunID == nil ||
				*second.Run.PreviousRunID != first.Run.ID || second.Run.Spec.Worker.Name != request.Spec.Worker.Name {
				t.Fatalf("Worker-affine retry chain: first=%+v second=%+v", first.Run, second.Run)
			}
			chain, err := runService.GetProjectRetryChain(t.Context(), created.Project.ID, first.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(chain) != 2 || chain[0].Worker != request.Spec.Worker.Name || chain[1].Worker != request.Spec.Worker.Name {
				t.Fatalf("Retry Chain Worker evidence: %+v", chain)
			}
		})
	}
}

func TestProjectCascadeDeletesExecutionRetryChain(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "delete retry chain", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 30, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(created.Project)
	request.RetryPolicy = rundomain.RetryPolicy{
		MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: time.Second,
		Multiplier: 1, DeclineCooldown: time.Second,
	}
	first, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed},
	}); err != nil {
		t.Fatal(err)
	}
	firstStored, err := runService.GetRun(t.Context(), first.Run.ID)
	if err != nil || firstStored.NextEligibleAt == nil {
		t.Fatalf("first retry eligibility: run=%+v error=%v", firstStored, err)
	}
	now = *firstStored.NextEligibleAt
	second, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.PreviousRunID == nil || *second.Run.PreviousRunID != first.Run.ID {
		t.Fatalf("retry chain identity: %+v", second.Run)
	}
	if _, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: second.Run.ID, FencingToken: second.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed},
	}); err != nil {
		t.Fatal(err)
	}

	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE execution_runs SET previous_run_id = NULL WHERE id = ?", second.Run.ID,
	); err == nil {
		t.Fatal("direct retry lineage mutation was accepted")
	}
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE projects SET deleting = 1 WHERE id = ?;
		DELETE FROM projects WHERE id = ?`, created.Project.ID, created.Project.ID,
	); err != nil {
		t.Fatalf("delete Project with retry chain: %v", err)
	}
	for _, table := range []string{"projects", "execution_runs"} {
		column := "project_id"
		if table == "projects" {
			column = "id"
		}
		var count int
		if err := conn.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+table+" WHERE "+column+" = ?", created.Project.ID,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows remain after Project cascade: %d", table, count)
		}
	}
}

func TestExecutionContractMismatchRetainsTerminalEvidenceWithoutConsumingRetryBudget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "contract mismatch", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(created.Project)
	request.RetryPolicy = rundomain.RetryPolicy{
		MaxAttempts: 1, InitialBackoff: time.Hour, MaxBackoff: time.Hour,
		Multiplier: 1, DeclineCooldown: time.Hour,
	}
	first, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
		Termination: &rundomain.RunTermination{
			Status: rundomain.StatusFailed, Code: rundomain.ErrorExecutionContractMismatch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := runService.GetRun(t.Context(), first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusFailed || receipt.Code != rundomain.ErrorExecutionContractMismatch ||
		stored.Status != rundomain.StatusFailed || stored.ErrorCode != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("contract mismatch evidence: receipt=%+v run=%+v", receipt, stored)
	}
	lease := acquireRecoveryLease(t, runService, first.Run.DispatcherID)
	recovered, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
		DispatcherID: first.Run.DispatcherID, DispatcherFencingToken: lease.FencingToken,
		Reason: "restart after execution contract mismatch",
	})
	if err != nil || len(recovered) != 0 {
		t.Fatalf("terminal mismatch entered active Run recovery: runs=%v error=%v", recovered, err)
	}
	second, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatalf("contract mismatch consumed retry budget: %v", err)
	}
	if second.Run.Attempt != 2 || second.Run.PreviousRunID == nil || *second.Run.PreviousRunID != first.Run.ID {
		t.Fatalf("contract mismatch attempt lineage: %+v", second.Run)
	}
}

func TestDeclineCoolsOnlySelectedWorkerWithoutConsumingBudget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "decline cooldown", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 13, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(project.Project)
	request.RetryPolicy = rundomain.RetryPolicy{
		MaxAttempts: 1, InitialBackoff: time.Second, MaxBackoff: time.Second,
		Multiplier: 1, DeclineCooldown: 30 * time.Second,
	}
	first, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	decline := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"not my domain"}}`)
	if _, err := runService.FinishRun(t.Context(), finishRequest(first, decline)); err != nil {
		t.Fatal(err)
	}
	assertRunErrorCode(t, rundomain.ErrorRetryDeferred, func() error {
		_, err := runService.StartRun(t.Context(), request)
		return err
	})
	alternate := request
	alternate.Spec.Worker.Name = "reasoner-alternate"
	second, err := runService.StartRun(t.Context(), alternate)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.PreviousRunID == nil || *second.Run.PreviousRunID != first.Run.ID {
		t.Fatalf("decline chain: %+v", second.Run)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var failures int
	if err := conn.QueryRowContext(t.Context(),
		"SELECT failure_count FROM execution_work_keys WHERE work_key = ?", first.Run.WorkKey,
	).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("Declined Run consumed technical retry budget: %d", failures)
	}
}

func TestRenewRunRequiresCurrentUnexpiredExecutionRight(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "renew lease", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 14, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	expiresAt, err := runService.RenewRun(t.Context(), rundomain.RenewRunRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, LeaseExtension: 40 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(now.Add(40 * time.Second)) {
		t.Fatalf("renewed expiration: %s", expiresAt)
	}
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, err := runService.RenewRun(t.Context(), rundomain.RenewRunRequest{
			RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
			DispatcherID: "wrong-dispatcher", LeaseExtension: time.Second,
		})
		return err
	})
}

func TestExpiredLeaseInterruptsRunAndConsumesRetryBudget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "expired budget", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 14, 30, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(project.Project)
	request.RetryPolicy = rundomain.RetryPolicy{
		MaxAttempts: 1, InitialBackoff: time.Second, MaxBackoff: time.Second,
		Multiplier: 1, DeclineCooldown: time.Second,
	}
	started, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE execution_claims SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE run_id = ?", started.Run.ID,
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	assertRunErrorCode(t, rundomain.ErrorRetryExhausted, func() error {
		_, err := runService.StartRun(t.Context(), request)
		return err
	})
	stored, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusInterrupted || stored.ErrorCode != rundomain.ExecutionLeaseExpired {
		t.Fatalf("expired Run: %+v", stored)
	}
	conn, err = runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var failures, claims int
	var state string
	if err := conn.QueryRowContext(t.Context(),
		"SELECT retry_state, failure_count FROM execution_work_keys WHERE work_key = ?", started.Run.WorkKey,
	).Scan(&state, &failures); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if state != string(rundomain.RetryExhausted) || failures != 1 || claims != 0 {
		t.Fatalf("expired budget state=%s failures=%d claims=%d", state, failures, claims)
	}
}

func TestClaimLeaseUsesNanosecondBoundary(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "lease precision", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 14, 45, 0, 123, time.UTC)
	runService.clock = func() time.Time { return now }
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := runService.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	valid, err := runService.claimIsCurrent(
		t.Context(), tx, started.Run, started.Claim.LeaseExpiresAt.Add(-time.Nanosecond).UnixNano(),
	)
	if err != nil || !valid {
		t.Fatalf("claim one nanosecond before expiry: valid=%t error=%v", valid, err)
	}
	valid, err = runService.claimIsCurrent(
		t.Context(), tx, started.Run, started.Claim.LeaseExpiresAt.UnixNano(),
	)
	if err != nil || valid {
		t.Fatalf("claim at expiry: valid=%t error=%v", valid, err)
	}
}

func TestRecoverRunsInterruptsOldDispatcherAndPreservesRetryBudget(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "restart recovery", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	dispatcherLease := acquireRecoveryLease(t, runService, started.Run.DispatcherID)
	recovered, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: dispatcherLease.FencingToken,
		Reason: "dispatcher startup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0] != started.Run.ID {
		t.Fatalf("recovered Runs: %v", recovered)
	}
	stored, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusInterrupted || stored.ErrorCode != "dispatcher_restarted" ||
		stored.NextEligibleAt == nil || !stored.NextEligibleAt.Equal(now.Add(rundomain.DefaultRetryPolicy().BackoffFor(started.Run.WorkKey, 1))) {
		t.Fatalf("recovered Run: %+v", stored)
	}
	again, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: dispatcherLease.FencingToken,
		Reason: "dispatcher startup replay",
	})
	if err != nil || len(again) != 1 || again[0] != started.Run.ID {
		t.Fatalf("recovery replay: runs=%v error=%v", again, err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims, events int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM execution_recovery_events WHERE run_id = ? AND status = 'interrupted'", started.Run.ID,
	).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if claims != 0 || events != 1 {
		t.Fatalf("recovery persistence: claims=%d events=%d", claims, events)
	}
}

func TestOfflineDataRecoveryPointInterruptsLegacyRunAndRetryCreatesV2(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "legacy recovery", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(created.Project)
	legacyRunID := rundomain.RunID("run-legacy-active")
	legacySpec := fmt.Sprintf(
		`{"config_revision":"legacy-config","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":%d,"revision":%d,"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"worker":{"name":"legacy-worker","adapter":"legacy"},"image":{"requested_ref":"legacy:image"},"timeouts":{"run_seconds":300,"phase_seconds":120,"grace_seconds":5},"result_schema":"aether.run-result/v1"}`,
		created.Project.Generation, created.Project.Revision,
	)
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	leaseExpires := now.Add(time.Minute)
	if _, err = conn.ExecContext(t.Context(), `
		INSERT INTO execution_work_keys (
			work_key, project_id, task_type, last_attempt, created_at
		) VALUES (?, ?, 'reason', 1, ?)
	`, request.WorkKey, created.Project.ID, now.Format(time.RFC3339Nano)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `
		INSERT INTO execution_claim_scopes (
			claim_scope, project_id, scope_type, last_fencing_token, created_at
		) VALUES (?, ?, 'reason', 1, ?)
	`, "reason:"+created.Project.ID, created.Project.ID, now.Format(time.RFC3339Nano)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, dispatcher_fencing_token, task_type,
			execution_spec_schema, execution_spec_json, started_at
		) VALUES (?, ?, ?, ?, 1, 'active', 1, ?, 1, 'reason',
			'aether.execution-spec/legacy-v1', ?, ?)
	`, legacyRunID, created.Project.ID, request.WorkKey, "reason:"+created.Project.ID,
		request.DispatcherID, legacySpec, now.Format(time.RFC3339Nano)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `
		INSERT INTO execution_claims (
			claim_scope, work_key, run_id, project_id, task_type, fencing_token,
			dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
		) VALUES (?, ?, ?, ?, 'reason', 1, ?, ?, ?, ?)
	`, "reason:"+created.Project.ID, request.WorkKey, legacyRunID, created.Project.ID,
		request.DispatcherID, now.Format(time.RFC3339Nano), leaseExpires.Format(time.RFC3339Nano), leaseExpires.UnixNano(),
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	detail, err := runService.GetProjectActivityRunDetail(t.Context(), created.Project.ID, legacyRunID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ExecutionSpecSchema != rundomain.ExecutionSpecSchemaLegacyV1 ||
		detail.InputEvidence.Schema != rundomain.RenderedInputIdentitySchemaLegacyV1 ||
		detail.PromptEvidence.Schema != rundomain.RenderedPromptIdentitySchemaLegacyV1 {
		t.Fatalf("legacy Activity evidence: %+v", detail)
	}

	recovered, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
		DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken,
		Reason: "offline data recovery point",
	})
	if err != nil || len(recovered) != 1 || recovered[0] != legacyRunID {
		t.Fatalf("legacy recovery: runs=%v error=%v", recovered, err)
	}
	legacy, err := runService.GetRun(t.Context(), legacyRunID)
	if err != nil || legacy.Status != rundomain.StatusInterrupted {
		t.Fatalf("recovered legacy Run: run=%+v error=%v", legacy, err)
	}

	now = now.Add(rundomain.DefaultRetryPolicy().BackoffFor(request.WorkKey, 1) + time.Second)
	retry, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Run.Spec.Schema != rundomain.ExecutionSpecSchemaV2 || retry.Run.PreviousRunID == nil ||
		*retry.Run.PreviousRunID != legacyRunID || retry.Run.Attempt != 2 {
		t.Fatalf("legacy retry did not create a linked v2 Run: %+v", retry.Run)
	}
}

func TestRecoverSupersededRunDoesNotRewriteLatestRetryState(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "superseded recovery", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(project.Project)
	first, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM execution_claims WHERE run_id = ?", first.Run.ID,
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	second, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.FinishRun(t.Context(), finishRequest(second, reasonNoChangeResult())); err != nil {
		t.Fatal(err)
	}
	dispatcherLease := acquireRecoveryLease(t, runService, request.DispatcherID)
	recovered, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
		DispatcherID: request.DispatcherID, DispatcherFencingToken: dispatcherLease.FencingToken,
		Reason: "recover superseded Run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0] != first.Run.ID {
		t.Fatalf("superseded recovery: %v", recovered)
	}
	stored, err := runService.GetRun(t.Context(), first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusInterrupted {
		t.Fatalf("superseded Run status: %+v", stored)
	}
	conn, err = runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var state string
	var failures, claims int
	if err := conn.QueryRowContext(t.Context(),
		"SELECT retry_state, failure_count FROM execution_work_keys WHERE work_key = ?", first.Run.WorkKey,
	).Scan(&state, &failures); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if state != string(rundomain.RetryClosed) || failures != 0 || claims != 0 {
		t.Fatalf("latest retry state changed: state=%s failures=%d claims=%d", state, failures, claims)
	}
}

func TestRecoverRunsRequiresCurrentDispatcherLeaseToken(t *testing.T) {
	_, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	now := time.Date(2026, 7, 21, 16, 30, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	clearFixtureDispatcherLease(t, runService)
	first := acquireRecoveryLease(t, runService, "dispatcher-old")
	now = first.ExpiresAt
	current := acquireRecoveryLease(t, runService, "dispatcher-current")
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
			DispatcherID: first.InstanceID, DispatcherFencingToken: first.FencingToken,
			Reason: "stale takeover",
		})
		return err
	})
	if _, err := runService.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
		DispatcherID: current.InstanceID, DispatcherFencingToken: current.FencingToken,
		Reason: "current takeover",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunCommandsRejectReusedDispatcherInstanceWithOldToken(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "dispatcher token fencing", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(project.Project)
	started, err := runService.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := runService.clock()
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE dispatcher_lease
		SET fencing_token = fencing_token + 1,
			lease_expires_at = ?, lease_expires_unix_nano = ?
		WHERE lease_name = 'global'`,
		now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).UnixNano(),
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, err := runService.RenewRun(t.Context(), rundomain.RenewRunRequest{
			RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
			DispatcherID: started.Run.DispatcherID, LeaseExtension: time.Minute,
		})
		return err
	})
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, err := runService.FinishRun(t.Context(), finishRequest(started, reasonNoChangeResult()))
		return err
	})
}

func TestFinishedRunReceiptReplaysAfterDispatcherLeaseEnds(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "finish replay after lease", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	request := finishRequest(started, reasonNoChangeResult())
	if _, err := runService.FinishRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE dispatcher_lease
		SET lease_expires_unix_nano = 0, lease_expires_at = '1970-01-01T00:00:00Z'
		WHERE lease_name = 'global'`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	replayed, err := runService.FinishRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Status != rundomain.StatusCommitted {
		t.Fatalf("replayed receipt: %+v", replayed)
	}
}

func acquireRecoveryLease(t *testing.T, runService *RunService, dispatcherID string) rundomain.DispatcherLease {
	t.Helper()
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var currentID string
	var currentToken rundomain.FencingToken
	var expiresAt string
	err = conn.QueryRowContext(t.Context(), `
		SELECT instance_id, fencing_token, lease_expires_at
		FROM dispatcher_lease WHERE lease_name = 'global'`,
	).Scan(&currentID, &currentToken, &expiresAt)
	conn.Close()
	if err == nil && currentID == dispatcherID {
		expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return rundomain.DispatcherLease{InstanceID: currentID, FencingToken: currentToken, ExpiresAt: expires}
	}
	lease, err := runService.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: dispatcherID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func clearFixtureDispatcherLease(t *testing.T, runService *RunService) {
	t.Helper()
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "DELETE FROM dispatcher_lease"); err != nil {
		t.Fatal(err)
	}
}
