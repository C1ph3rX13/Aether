package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceConnectionUsesDurableRuntimeAndRecordsOptimize(t *testing.T) {
	db := newMaintenanceTestDB(t)
	maintenance := db.maintenance
	if maintenance == nil {
		t.Fatal("maintenance connection was not configured")
	}
	if err := assertSQLiteRuntime(t.Context(), maintenance.db, false); err != nil {
		t.Fatalf("maintenance runtime: %v", err)
	}
	startup := db.MaintenanceSnapshot().StartupOptimize
	if startup.ObservedAt.IsZero() || !startup.Startup || startup.Error != "" || startup.Duration <= 0 {
		t.Fatalf("startup optimize observation: %+v", startup)
	}
	if err := maintenance.runOptimize(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	daily := db.MaintenanceSnapshot().Optimize
	if daily.ObservedAt.IsZero() || daily.Startup || daily.Error != "" || daily.Duration <= 0 {
		t.Fatalf("daily optimize observation: %+v", daily)
	}
}

func TestMaintenanceLoopRunsCheckpointAndDailyOptimizeTimers(t *testing.T) {
	db := newMaintenanceTestDB(t)
	maintenance := db.maintenance
	maintenance.stop()
	maintenance.checkpointInterval = 5 * time.Millisecond
	maintenance.optimizeInterval = 5 * time.Millisecond
	maintenance.start()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		snapshot := maintenance.snapshot()
		if !snapshot.Checkpoint.ObservedAt.IsZero() && !snapshot.Optimize.ObservedAt.IsZero() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("maintenance timers did not run: %+v", snapshot)
		case <-poll.C:
		}
	}
}

func TestPassiveCheckpointDoesNotWaitForBoundedReader(t *testing.T) {
	db := newMaintenanceTestDB(t)
	writer, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(t.Context(), `
		CREATE TABLE maintenance_checkpoint_values (id INTEGER PRIMARY KEY, value BLOB);
		INSERT INTO maintenance_checkpoint_values (value) VALUES (randomblob(2048));
	`); err != nil {
		t.Fatal(err)
	}
	reader, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var initialCount int
	if err := reader.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM maintenance_checkpoint_values").Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(t.Context(), `
		WITH RECURSIVE values_to_write(id) AS (
			SELECT 1 UNION ALL SELECT id + 1 FROM values_to_write WHERE id < 300
		)
		INSERT INTO maintenance_checkpoint_values (value)
		SELECT randomblob(2048) FROM values_to_write;
	`); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	checkpoint, _ := db.maintenance.runPassiveMaintenance(t.Context())
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("PASSIVE checkpoint waited for reader: %s", elapsed)
	}
	if checkpoint.Mode != "PASSIVE" || checkpoint.Error != "" || checkpoint.WALFrames <= 0 {
		t.Fatalf("passive checkpoint observation: %+v", checkpoint)
	}
	if checkpoint.CheckpointedFrames >= checkpoint.WALFrames {
		t.Fatalf("reader did not retain a WAL boundary: %+v", checkpoint)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ = db.maintenance.runPassiveMaintenance(t.Context())
	if checkpoint.Error != "" || checkpoint.CheckpointedFrames != checkpoint.WALFrames {
		t.Fatalf("checkpoint did not catch up after reader drained: %+v", checkpoint)
	}
}

func TestIncrementalVacuumUsesWatermarkAndPageLimit(t *testing.T) {
	db := newMaintenanceTestDB(t)
	createVacuumFreelist(t, db)
	observation, err := db.maintenance.incrementalVacuum(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if observation.FreelistBefore < observation.Threshold || observation.RequestedPages != vacuumPageLimit {
		t.Fatalf("vacuum was not triggered at the configured watermark: %+v", observation)
	}
	if observation.ReclaimedPages <= 0 || observation.ReclaimedPages > vacuumPageLimit ||
		observation.FreelistAfter >= observation.FreelistBefore || observation.Duration <= 0 {
		t.Fatalf("incremental vacuum was not bounded: %+v", observation)
	}
}

func TestIncrementalVacuumSkipsBelowWatermark(t *testing.T) {
	db := newMaintenanceTestDB(t)
	observation, err := db.maintenance.incrementalVacuum(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if observation.FreelistBefore >= observation.Threshold || observation.RequestedPages != 0 || observation.ReclaimedPages != 0 {
		t.Fatalf("vacuum ran below its watermark: %+v", observation)
	}
}

func TestIncrementalVacuumDefersWhenWriterIsBusy(t *testing.T) {
	db := newMaintenanceTestDB(t)
	createVacuumFreelist(t, db)
	if _, err := db.maintenance.db.ExecContext(t.Context(), "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	writerTx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writerTx.Rollback()
	if _, err := writerTx.ExecContext(t.Context(), `
		INSERT INTO maintenance_vacuum_values (value) VALUES (randomblob(64))
	`); err != nil {
		t.Fatal(err)
	}
	observation, err := db.maintenance.incrementalVacuum(t.Context())
	if err == nil || !observation.Deferred || observation.RequestedPages != vacuumPageLimit {
		t.Fatalf("busy incremental vacuum was not deferred: observation=%+v error=%v", observation, err)
	}
	if !isSQLiteBusy(err) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy incremental vacuum error = %v", err)
	}
}

func TestCloseTruncatesWALAndPreservesCommittedData(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "durable.db")
	db := NewDB()
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TABLE maintenance_durability_evidence (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO maintenance_durability_evidence (id, value) VALUES (1, 'committed');
	`); err != nil {
		conn.Close()
		db.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	shutdown := db.MaintenanceSnapshot().ShutdownCheckpoint
	if shutdown.Mode != "TRUNCATE" || shutdown.Busy != 0 || shutdown.Error != "" {
		t.Fatalf("shutdown checkpoint: %+v", shutdown)
	}
	if info, err := os.Stat(databasePath + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("shutdown WAL size = %d, want 0", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	reopened := NewDB()
	if err := reopened.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	readConn, err := reopened.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer readConn.Close()
	var value string
	if err := readConn.QueryRowContext(t.Context(),
		"SELECT value FROM maintenance_durability_evidence WHERE id = 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("recovered value = %q", value)
	}
}

func TestCommittedAuthoritativeStateSurvivesAbruptProcessExit(t *testing.T) {
	if os.Getenv("AETHER_DATABASE_CRASH_HELPER") == "1" {
		runAbruptExitCommitHelper(os.Getenv("AETHER_DATABASE_CRASH_PATH"))
		return
	}
	databasePath := filepath.Join(t.TempDir(), "abrupt-exit.db")
	command := exec.Command(os.Args[0], "-test.run=^TestCommittedAuthoritativeStateSurvivesAbruptProcessExit$")
	command.Env = append(os.Environ(),
		"AETHER_DATABASE_CRASH_HELPER=1",
		"AETHER_DATABASE_CRASH_PATH="+databasePath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("abrupt-exit helper: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "authoritative commit completed") {
		t.Fatalf("helper exited before commit evidence: %s", output)
	}

	reopened := NewDB()
	if err := reopened.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	conn, err := reopened.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var projectCount, changeCount, claimCount, failedRunCount, receiptCount int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM projects WHERE id = 'durable-project'),
			(SELECT COUNT(*) FROM blackboard_changes WHERE project_id = 'durable-project' AND revision = 1),
			(SELECT COUNT(*) FROM execution_claims WHERE run_id = 'run-active'),
			(SELECT COUNT(*) FROM execution_runs WHERE id = 'run-terminal' AND status = 'failed'),
			(SELECT COUNT(*) FROM execution_runs WHERE id = 'run-terminal' AND finish_receipt_json IS NOT NULL)
	`).Scan(&projectCount, &changeCount, &claimCount, &failedRunCount, &receiptCount); err != nil {
		t.Fatal(err)
	}
	if projectCount != 1 || changeCount != 1 || claimCount != 1 || failedRunCount != 1 || receiptCount != 1 {
		t.Fatalf("abrupt-exit recovery: project=%d change=%d claim=%d failed_run=%d receipt=%d",
			projectCount, changeCount, claimCount, failedRunCount, receiptCount)
	}
}

func TestExistingDatabaseNeedsOfflineDataRecoveryPointToEnableIncrementalAutoVacuum(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "auto-vacuum.db")
	db := NewDB()
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), "PRAGMA auto_vacuum=NONE; VACUUM"); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	regular := NewDB()
	err = regular.Configure(databasePath)
	if err == nil || !strings.Contains(err.Error(), "offline data recovery point") {
		regular.Close()
		t.Fatalf("regular startup error = %v", err)
	}
}

func newMaintenanceTestDB(t *testing.T) *DB {
	t.Helper()
	db := NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return db
}

func createVacuumFreelist(t *testing.T, db *DB) {
	t.Helper()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TABLE maintenance_vacuum_values (id INTEGER PRIMARY KEY, value BLOB);
		WITH RECURSIVE values_to_write(id) AS (
			SELECT 1 UNION ALL SELECT id + 1 FROM values_to_write WHERE id < 900
		)
		INSERT INTO maintenance_vacuum_values (value)
		SELECT randomblob(4096) FROM values_to_write;
		DELETE FROM maintenance_vacuum_values;
	`); err != nil {
		t.Fatal(err)
	}
}

func runAbruptExitCommitHelper(databasePath string) {
	db := NewDB()
	if err := db.Configure(databasePath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, err = tx.ExecContext(context.Background(), `
		INSERT INTO projects (
			id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision
		) VALUES ('durable-project', 'Durable', 'active', 0, '2026-08-03T00:00:00Z', 1, 1);
		INSERT INTO facts (project_id, id, description) VALUES
			('durable-project', 'origin', 'origin'),
			('durable-project', 'goal', 'goal');
		INSERT INTO blackboard_changes (
			project_id, revision, generation, kind, entity_type, entity_id, data, created_at
		) VALUES (
			'durable-project', 1, 1, 'project_created', 'project', 'durable-project',
			'{"schema_version":0,"before":null,"after":null}', '2026-08-03T00:00:00Z'
		);
		INSERT INTO execution_work_keys (
			work_key, project_id, task_type, last_attempt, created_at
		) VALUES
			('reason:durable-project:active', 'durable-project', 'reason', 1, '2026-08-03T00:00:00Z'),
			('reason:durable-project:terminal', 'durable-project', 'reason', 1, '2026-08-03T00:00:00Z');
		INSERT INTO execution_claim_scopes (
			claim_scope, project_id, scope_type, last_fencing_token, created_at
		) VALUES (
			'reason:durable-project', 'durable-project', 'reason', 2, '2026-08-03T00:00:00Z'
		);
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, execution_spec_schema, execution_spec_json, started_at
		) VALUES
			('run-active', 'durable-project', 'reason:durable-project:active',
			 'reason:durable-project', 1, 'active', 1, 'dispatcher', 'reason',
			 'aether.execution-spec/v2',
			 '{"schema":"aether.execution-spec/v2","config_revision":"config","prompt":{"schema":"aether.prompt-template-hash/v1","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"input":{"generation":1,"revision":1,"identity":{"schema":"aether.input-hash/v1","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},"worker":{"name":"worker","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"result/v1","max_proposed_intents":8}',
			 '2026-08-03T00:00:00Z'),
			('run-terminal', 'durable-project', 'reason:durable-project:terminal',
			 'reason:durable-project', 1, 'active', 2, 'dispatcher', 'reason',
			 'aether.execution-spec/v2',
			 '{"schema":"aether.execution-spec/v2","config_revision":"config","prompt":{"schema":"aether.prompt-template-hash/v1","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"input":{"generation":1,"revision":1,"identity":{"schema":"aether.input-hash/v1","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},"worker":{"name":"worker","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"result/v1","max_proposed_intents":8}',
			 '2026-08-03T00:00:00Z');
		INSERT INTO execution_claims (
			claim_scope, work_key, run_id, project_id, task_type, fencing_token,
			dispatcher_id, claimed_at, lease_expires_at
		) VALUES (
			'reason:durable-project', 'reason:durable-project:active', 'run-active',
			'durable-project', 'reason', 1, 'dispatcher', '2026-08-03T00:00:00Z',
			'2026-08-03T00:01:00Z'
		);
		UPDATE execution_runs
		SET status = 'failed', finished_at = '2026-08-03T00:00:01Z',
			error_code = 'runtime_failed',
			finish_receipt_json = '{"status":"failed","code":"runtime_failed"}'
		WHERE id = 'run-terminal';
	`)
	if err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "authoritative commit completed")
	os.Exit(0)
}
