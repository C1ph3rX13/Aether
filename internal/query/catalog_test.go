package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
)

func TestListProjectCatalogProjectsCanonicalCountsAndExecutionEvidence(t *testing.T) {
	db := configuredTestDB(t)
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), `
INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
VALUES
  ('proj-a', 'Mixed project', 'active', 1, '2026-07-01T00:00:00Z', 2, 17),
  ('proj-b', 'Backoff project', 'paused', 0, '2026-07-02T00:00:00Z', 1, 4);

INSERT INTO facts (id, project_id, description, retracted_at) VALUES
  ('origin', 'proj-a', 'Origin', NULL),
  ('goal', 'proj-a', 'Goal', NULL),
  ('old', 'proj-a', 'Old evidence', '2026-07-03T00:00:00Z');

INSERT INTO intents (id, project_id, to_fact_id, description, creator, created_at, cancelled_at) VALUES
  ('i-open-1', 'proj-a', NULL, 'Explore one', 'reasoner', '2026-07-03T00:00:00Z', NULL),
  ('i-open-2', 'proj-a', NULL, 'Explore two', 'reasoner', '2026-07-03T00:00:01Z', NULL),
  ('i-open-3', 'proj-a', NULL, 'Bootstrap', 'reasoner', '2026-07-03T00:00:02Z', NULL),
  ('i-done', 'proj-a', 'old', 'Done', 'reasoner', '2026-07-03T00:00:03Z', NULL),
  ('i-cancelled', 'proj-a', NULL, 'Cancelled', 'reasoner', '2026-07-03T00:00:04Z', '2026-07-04T00:00:00Z');

INSERT INTO hints (id, project_id, content, creator, created_at, generation, withdrawn_at) VALUES
  ('h-active', 'proj-a', 'Use evidence', 'operator', '2026-07-03T00:00:00Z', 2, NULL),
  ('h-old', 'proj-a', 'Old hint', 'operator', '2026-07-02T00:00:00Z', 1, '2026-07-04T00:00:00Z');

INSERT INTO execution_work_keys
  (work_key, project_id, task_type, target_intent_id, last_attempt, created_at, retry_state, next_eligible_at)
VALUES
  ('reason:proj-a:g2:r17', 'proj-a', 'reason', NULL, 1, '2026-07-05T00:00:00Z', 'eligible', NULL),
  ('intent:proj-a:i-open-1:explore', 'proj-a', 'explore', 'i-open-1', 1, '2026-07-05T00:00:00Z', 'eligible', NULL),
  ('intent:proj-a:i-open-2:explore', 'proj-a', 'explore', 'i-open-2', 1, '2026-07-05T00:00:00Z', 'eligible', NULL),
  ('intent:proj-a:i-open-3:bootstrap', 'proj-a', 'bootstrap', 'i-open-3', 1, '2026-07-05T00:00:00Z', 'eligible', NULL),
  ('failed:proj-a', 'proj-a', 'reason', NULL, 1, '2026-07-05T00:00:00Z', 'eligible', NULL),
  ('exhausted:proj-a', 'proj-a', 'reason', NULL, 3, '2026-07-05T00:00:00Z', 'exhausted', NULL),
  ('backoff:proj-b', 'proj-b', 'reason', NULL, 1, '2026-07-05T00:00:00Z', 'backoff', '2026-07-06T00:00:00Z');

INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, target_intent_id, created_at) VALUES
  ('reason:proj-a', 'proj-a', 'reason', NULL, '2026-07-05T00:00:00Z'),
  ('intent:proj-a:i-open-1', 'proj-a', 'intent', 'i-open-1', '2026-07-05T00:00:00Z'),
  ('intent:proj-a:i-open-2', 'proj-a', 'intent', 'i-open-2', '2026-07-05T00:00:00Z'),
  ('intent:proj-a:i-open-3', 'proj-a', 'intent', 'i-open-3', '2026-07-05T00:00:00Z'),
  ('reason:proj-b', 'proj-b', 'reason', NULL, '2026-07-05T00:00:00Z');

WITH run_seed(
  id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
  fencing_token, task_type, input_generation, input_revision, worker_name,
  result_schema, started_at
) AS (VALUES
  ('run-reason', 'proj-a', NULL, 'reason:proj-a:g2:r17', 'reason:proj-a', 1, 'active', 1,
   'reason', 2, 17, 'reason.worker', 'result/v1', '2026-07-05T00:00:00Z'),
  ('run-explore-1', 'proj-a', 'i-open-1', 'intent:proj-a:i-open-1:explore', 'intent:proj-a:i-open-1', 1, 'active', 1,
   'explore', 2, 17, 'explore.worker', 'result/v1', '2026-07-05T00:00:01Z'),
  ('run-explore-2', 'proj-a', 'i-open-2', 'intent:proj-a:i-open-2:explore', 'intent:proj-a:i-open-2', 1, 'active', 1,
   'explore', 2, 17, 'explore.worker', 'result/v1', '2026-07-05T00:00:02Z'),
  ('run-bootstrap', 'proj-a', 'i-open-3', 'intent:proj-a:i-open-3:bootstrap', 'intent:proj-a:i-open-3', 1, 'active', 1,
   'bootstrap', 2, 17, 'bootstrap.worker', 'result/v1', '2026-07-05T00:00:03Z'),
  ('run-failed', 'proj-a', NULL, 'failed:proj-a', 'reason:proj-a', 1, 'failed', 2,
   'reason', 2, 16, 'reason.worker', 'result/v1', '2026-07-04T00:00:00Z'),
  ('run-exhausted', 'proj-a', NULL, 'exhausted:proj-a', 'reason:proj-a', 3, 'failed', 3,
   'reason', 2, 15, 'reason.worker', 'result/v1', '2026-07-03T00:00:00Z'),
  ('run-backoff', 'proj-b', NULL, 'backoff:proj-b', 'reason:proj-b', 1, 'failed', 1,
   'reason', 1, 4, 'reason.worker', 'result/v1', '2026-07-05T00:00:00Z')
)
INSERT INTO execution_runs (
  id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
  fencing_token, dispatcher_id, task_type, execution_spec_schema,
  execution_spec_json, started_at
)
SELECT id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
       fencing_token, 'dispatcher', task_type, 'aether.execution-spec/v2',
       json_object(
         'schema', 'aether.execution-spec/v2',
         'config_revision', 'cfg',
         'prompt', json_object(
           'schema', 'aether.prompt-template-hash/v1',
           'sha256', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
         ),
         'input', CASE WHEN target_intent_id IS NULL THEN json_object(
           'generation', input_generation, 'revision', input_revision,
           'identity', json_object(
             'schema', 'aether.input-hash/v1',
             'sha256', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
           )
         ) ELSE json_object(
           'generation', input_generation, 'revision', input_revision,
           'identity', json_object(
             'schema', 'aether.input-hash/v1',
             'sha256', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
           ),
           'target_intent_id', target_intent_id
         ) END,
         'worker', json_object('name', worker_name, 'adapter', 'test'),
         'image', json_object('requested_ref', 'image:test'),
         'timeouts', CASE WHEN task_type = 'reason' THEN json_object(
           'run_seconds', 10, 'phase_seconds', 5, 'grace_seconds', 1
         ) ELSE json_object(
           'run_seconds', 12, 'phase_seconds', 5,
           'fallback_phase_seconds', 5, 'grace_seconds', 1
         ) END,
         'result_schema', result_schema,
         'max_proposed_intents', CASE WHEN task_type = 'reason' THEN 8 ELSE 0 END
       ),
       started_at
FROM run_seed;
`)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	entries, err := newQueryService(db).ListProjectCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(entries))
	}
	mixed := entries[0]
	if mixed.Blackboard.Facts != (ActiveCounts{Total: 3, Active: 2}) ||
		mixed.Blackboard.Intents != (IntentCounts{Total: 5, Open: 3}) ||
		mixed.Blackboard.Hints != (ActiveCounts{Total: 2, Active: 1}) {
		t.Fatalf("mixed counts: %+v", mixed.Blackboard)
	}
	if mixed.Execution.Status != "retry_exhausted" {
		t.Fatalf("mixed execution: %+v", mixed.Execution)
	}
	if entries[1].Execution.Status != "retrying" {
		t.Fatalf("backoff execution: %+v", entries[1].Execution)
	}
}

func TestCatalogQueryUsesProjectRangeIndexes(t *testing.T) {
	db := configuredTestDB(t)
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 250
		)
		INSERT INTO projects (id, title, status, created_at)
		SELECT printf('catalog-plan-%04d', value), 'Catalog Plan Project',
		       CASE WHEN value % 2 = 0 THEN 'active' ELSE 'paused' END,
		       printf('2026-01-01T00:%02d:00Z', value % 60)
		FROM sequence;
		INSERT INTO facts (project_id, id, description)
		SELECT id, 'origin', 'origin' FROM projects;
		INSERT INTO intents (project_id, id, kind, description, creator, created_at)
		SELECT id, 'i001', 'explore', 'inspect', 'test', created_at FROM projects;
		INSERT INTO hints (project_id, id, content, creator, created_at)
		SELECT id, 'h001', 'hint', 'test', created_at FROM projects;
		INSERT INTO execution_work_keys (
			work_key, project_id, task_type, last_attempt, created_at, retry_state, updated_at
		)
		SELECT 'reason:' || id, id, 'reason', 0, created_at, 'backoff', created_at FROM projects;
		ANALYZE;
	`); err != nil {
		t.Fatalf("create Catalog query-plan scale fixture: %v", err)
	}

	rows, err := conn.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+catalogQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	for _, table := range []string{"facts", "intents", "hints"} {
		if strings.Contains(plan, "SCAN "+table) || strings.Contains(plan, "SCAN "+table+" ") {
			t.Errorf("catalog query scans %s:\n%s", table, plan)
		}
	}
	for _, index := range []string{
		"projects_created_id_idx",
	} {
		if !strings.Contains(plan, index) {
			t.Errorf("catalog query does not use %s:\n%s", index, plan)
		}
	}
	if strings.Contains(plan, "USE TEMP B-TREE") {
		t.Fatalf("catalog query uses a temporary sort:\n%s", plan)
	}
}

func TestCatalogReadTransactionKeepsOneSnapshotAcrossConcurrentWrite(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "aether.db")
	db := database.NewDB()
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
VALUES ('proj-snapshot', 'Snapshot project', 'active', 0, '2026-07-01T00:00:00Z', 1, 1);
INSERT INTO facts (id, project_id, description) VALUES ('origin', 'proj-snapshot', 'Origin');
`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var initialCount int
	if err := tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM facts WHERE project_id = 'proj-snapshot'").Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	if initialCount != 1 {
		t.Fatalf("initial fact count=%d, want 1", initialCount)
	}

	writer, err := sql.Open("sqlite", "file:"+databasePath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writeContext, cancelWrite := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWrite()
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := writer.ExecContext(writeContext,
			"INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj-snapshot', 'Goal')")
		writeDone <- writeErr
	}()

	var writeErr error
	writePending := true
	select {
	case err := <-writeDone:
		writeErr = err
		writePending = false
	case <-time.After(100 * time.Millisecond):
		// An immediate read transaction may hold the writer until commit.
	}
	var snapshotCount int
	if err := tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM facts WHERE project_id = 'proj-snapshot'").Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 1 {
		t.Fatalf("read transaction mixed snapshots: fact count=%d, want 1", snapshotCount)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if writePending {
		writeErr = <-writeDone
	}
	if writeErr != nil {
		if !strings.Contains(writeErr.Error(), "locked") {
			t.Fatal(writeErr)
		}
		if _, err := writer.ExecContext(t.Context(),
			"INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj-snapshot', 'Goal')"); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := newQueryService(db).ListProjectCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Blackboard.Facts.Total != 2 {
		t.Fatalf("post-commit Catalog=%+v, want both committed Facts", entries)
	}
}

func TestListProjectCatalogFailsClosedWhenProjectHasMultipleActiveReasonRuns(t *testing.T) {
	db := configuredTestDB(t)
	seedCatalogReasonRuns(t, db, "reason.worker", "2026-07-05T00:00:00Z", 2)

	entries, err := newQueryService(db).ListProjectCatalog(t.Context())
	assertCatalogInternalError(t, err)
	if entries != nil {
		t.Fatalf("entries=%+v, want no partial Catalog projection", entries)
	}
}

func TestListProjectCatalogFailsClosedWhenActiveReasonEvidenceIsEmpty(t *testing.T) {
	tests := []struct {
		name      string
		worker    string
		startedAt string
	}{
		{name: "started at", worker: "reason.worker", startedAt: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := configuredTestDB(t)
			seedCatalogReasonRuns(t, db, test.worker, test.startedAt, 1)
			conn, err := db.GetConn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var storedWorker, storedStartedAt string
			if err := conn.QueryRowContext(t.Context(),
				"SELECT worker_name, started_at FROM execution_runs WHERE project_id = 'proj-reason'",
			).Scan(&storedWorker, &storedStartedAt); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			conn.Close()
			if storedWorker != test.worker || storedStartedAt != test.startedAt {
				t.Fatalf("stored evidence=(%q, %q), want (%q, %q)", storedWorker, storedStartedAt, test.worker, test.startedAt)
			}

			entries, err := newQueryService(db).ListProjectCatalog(t.Context())
			assertCatalogInternalError(t, err)
			if entries != nil {
				t.Fatalf("entries=%+v, want no partial Catalog projection", entries)
			}
		})
	}
}

func seedCatalogReasonRuns(t *testing.T, db *database.DB, worker, startedAt string, count int) {
	t.Helper()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `
INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
VALUES ('proj-reason', 'Reason evidence', 'active', 0, '2026-07-01T00:00:00Z', 2, 17);
INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, target_intent_id, created_at)
VALUES ('reason:proj-reason', 'proj-reason', 'reason', NULL, '2026-07-05T00:00:00Z');
`); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= count; index++ {
		workKey := fmt.Sprintf("reason:proj-reason:%d", index)
		runID := fmt.Sprintf("run-reason-%d", index)
		if _, err := conn.ExecContext(t.Context(), `
INSERT INTO execution_work_keys
  (work_key, project_id, task_type, target_intent_id, last_attempt, created_at, retry_state, next_eligible_at)
VALUES (?, 'proj-reason', 'reason', NULL, 1, '2026-07-05T00:00:00Z', 'eligible', NULL);
`, workKey); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(), `
INSERT INTO execution_runs
  (id, project_id, target_intent_id, work_key, claim_scope, attempt, status, fencing_token,
   dispatcher_id, task_type, execution_spec_schema, execution_spec_json, started_at)
VALUES
  (?, 'proj-reason', NULL, ?, 'reason:proj-reason', 1, 'active', ?,
	'dispatcher', 'reason', 'aether.execution-spec/v2',
	json_object(
	  'schema', 'aether.execution-spec/v2', 'config_revision', 'cfg',
	  'prompt', json_object('schema', 'aether.prompt-template-hash/v1', 'sha256', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'),
	  'input', json_object('generation', 2, 'revision', 17, 'identity', json_object('schema', 'aether.input-hash/v1', 'sha256', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')),
	  'worker', json_object('name', ?, 'adapter', 'test'),
	  'image', json_object('requested_ref', 'image:test'),
	  'timeouts', json_object('run_seconds', 10, 'phase_seconds', 5, 'grace_seconds', 1),
	  'result_schema', 'result/v1', 'max_proposed_intents', 8
	), ?);`,
			runID, workKey, index, worker, startedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCatalogInternalError(t *testing.T, err error) {
	t.Helper()
	var domainErr *project.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != project.ErrorInternal {
		t.Fatalf("error=%v, want project error code %s", err, project.ErrorInternal)
	}
}

func configuredTestDB(t *testing.T) *database.DB {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
