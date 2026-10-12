package database_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestConfigureRebuildsBlackboardStorageProjectFirstWithoutDataLoss(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "project-first.db")
	createVersionTwentyOneProjectFirstFixture(t, dbPath)

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatalf("configure offline data recovery point: %v", err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	wantPrimaryKeys := map[string][]string{
		"facts":          {"project_id", "id"},
		"intents":        {"project_id", "id"},
		"hints":          {"project_id", "id"},
		"intent_sources": {"project_id", "intent_id", "fact_id"},
	}
	for table, want := range wantPrimaryKeys {
		rows, err := conn.QueryContext(t.Context(),
			"SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk", table,
		)
		if err != nil {
			t.Fatalf("read %s primary key: %v", table, err)
		}
		var got []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			got = append(got, column)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s primary key=%v, want %v", table, got, want)
		}
	}
	for _, constraint := range []struct {
		table   string
		parent  string
		columns []foreignKeyColumn
	}{
		{table: "intent_sources", parent: "intents", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"intent_id", "id"}}},
		{table: "intent_sources", parent: "facts", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"fact_id", "id"}}},
		{table: "project_completions", parent: "intents", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"intent_id", "id"}}},
		{table: "execution_work_keys", parent: "intents", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_claim_scopes", parent: "intents", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_runs", parent: "intents", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_claims", parent: "intents", columns: []foreignKeyColumn{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
	} {
		assertCompositeForeignKey(t, conn, constraint.table, constraint.parent, constraint.columns)
	}

	wantCounts := map[string]int{
		"projects": 2, "facts": 4, "intents": 2, "intent_sources": 2, "hints": 2,
		"project_completions": 1, "execution_work_keys": 1, "execution_claim_scopes": 1,
		"execution_runs": 1, "execution_claims": 1, "execution_worker_declines": 1,
		"execution_recovery_events": 1, "execution_runtime_handles": 1, "reason_checkpoints": 1,
	}
	for table, want := range wantCounts {
		var got int
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s row count=%d, want %d", table, got, want)
		}
	}

	var kind, description string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT kind, description FROM intents
		WHERE project_id = 'project-a' AND id = 'shared-intent'
	`).Scan(&kind, &description); err != nil {
		t.Fatal(err)
	}
	if kind != "completion" || description != "completed in project A" {
		t.Fatalf("persisted intent changed: kind=%q description=%q", kind, description)
	}

	var legacyTableCount int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name LIKE '%_legacy_project_first'
	`).Scan(&legacyTableCount); err != nil {
		t.Fatal(err)
	}
	if legacyTableCount != 0 {
		t.Fatalf("migration left %d legacy tables", legacyTableCount)
	}
	var redundantIndexCount int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name IN ('facts_project_order_idx', 'intent_sources_project_order_idx')
	`).Scan(&redundantIndexCount); err != nil {
		t.Fatal(err)
	}
	if redundantIndexCount != 0 {
		t.Fatalf("migration retained %d redundant Project-order indexes", redundantIndexCount)
	}

	result, err := conn.ExecContext(t.Context(), `
		INSERT INTO execution_recovery_events (run_id, dispatcher_id, status, reason, observed_at)
		VALUES ('run-project-b', 'dispatcher', 'observed', 'after migration', '2026-01-02T00:04:00Z')
	`)
	if err != nil {
		t.Fatal(err)
	}
	recoveryEventID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if recoveryEventID != 3 {
		t.Fatalf("recovery event id=%d, want preserved AUTOINCREMENT high-water mark 3", recoveryEventID)
	}
	assertDatabaseIntegrity(t, conn)
}

func TestIntentSourcesCannotReferenceFactsFromAnotherProject(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "source-isolation.db")); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO projects (id, title, created_at) VALUES
			('project-a', 'Project A', '2026-01-01T00:00:00Z'),
			('project-b', 'Project B', '2026-01-01T00:00:00Z');
		INSERT INTO facts (project_id, id, description)
		VALUES ('project-b', 'source', 'belongs to project B');
		INSERT INTO intents (
			project_id, id, kind, description, creator, created_at, sources_locked
		) VALUES (
			'project-a', 'intent', 'explore', 'project A intent', 'test',
			'2026-01-01T00:00:00Z', 0
		);
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO intent_sources (project_id, intent_id, fact_id)
		VALUES ('project-a', 'intent', 'source')
	`); err == nil {
		t.Fatal("cross-Project Fact reference unexpectedly succeeded")
	}

	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO facts (project_id, id, description)
		VALUES ('project-a', 'source', 'belongs to project A');
		INSERT INTO intent_sources (project_id, intent_id, fact_id)
		VALUES ('project-a', 'intent', 'source');
	`); err != nil {
		t.Fatalf("same-Project Fact reference: %v", err)
	}
}

func TestProjectFirstMigrationDigestMismatchRollsBack(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "digest-rollback.db")
	createVersionTwentyOneProjectFirstFixture(t, dbPath)
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]database.Migration(nil), migrations...)
	tampered[21] = database.NewMigration(
		22,
		migrations[21].Name,
		"UPDATE facts SET retracted_by = 'migration-tamper' WHERE project_id = 'project-a' AND id = 'origin';",
	)

	raw := openRawSQLite(t, dbPath)
	defer raw.Close()
	err = database.Migrate(t.Context(), raw, tampered, 22)
	if err == nil || !strings.Contains(err.Error(), "content digest changed") {
		t.Fatalf("migration error=%v, want content digest mismatch", err)
	}

	var version, tamperedRows int
	if err := raw.QueryRowContext(t.Context(), `
		SELECT
			(SELECT MAX(version) FROM schema_migrations),
			(SELECT COUNT(*) FROM facts WHERE retracted_by = 'migration-tamper')
	`).Scan(&version, &tamperedRows); err != nil {
		t.Fatal(err)
	}
	if version != 21 || tamperedRows != 0 {
		t.Fatalf("failed verification committed changes: version=%d tampered_rows=%d", version, tamperedRows)
	}
}

func TestConfigureRejectsMissingBlackboardImmutabilityTrigger(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing-immutability-trigger.db")
	configured := database.NewDB()
	if err := configured.Configure(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := configured.Close(); err != nil {
		t.Fatal(err)
	}

	raw := openRawSQLite(t, dbPath)
	if _, err := raw.ExecContext(t.Context(), "DROP TRIGGER intents_kind_immutable"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err := db.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "intents_kind_immutable") {
		t.Fatalf("configure error=%v, want missing immutability trigger", err)
	}
	if _, err := db.GetConn(t.Context()); err == nil {
		t.Fatal("failed startup left database configured")
	}
}

func TestConfigureRejectsIneffectiveBlackboardImmutabilityTrigger(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ineffective-immutability-trigger.db")
	configured := database.NewDB()
	if err := configured.Configure(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := configured.Close(); err != nil {
		t.Fatal(err)
	}

	raw := openRawSQLite(t, dbPath)
	if _, err := raw.ExecContext(t.Context(), `
		DROP TRIGGER intents_kind_immutable;
		CREATE TRIGGER intents_kind_immutable
		BEFORE UPDATE OF kind ON intents
		BEGIN
			SELECT 1;
		END;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err := db.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "intent kind immutability") {
		t.Fatalf("configure error=%v, want ineffective immutability trigger failure", err)
	}
	if _, err := db.GetConn(t.Context()); err == nil {
		t.Fatal("failed startup left database configured")
	}
}

type foreignKeyColumn struct {
	from string
	to   string
}

func assertCompositeForeignKey(
	t *testing.T,
	conn database.DBTX,
	table, parent string,
	want []foreignKeyColumn,
) {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), `
		SELECT id, seq, "table", "from", "to"
		FROM pragma_foreign_key_list(?)
		ORDER BY id, seq
	`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	constraints := make(map[int][]foreignKeyColumn)
	parents := make(map[int]string)
	for rows.Next() {
		var id, sequence int
		var gotParent, from, to string
		if err := rows.Scan(&id, &sequence, &gotParent, &from, &to); err != nil {
			t.Fatal(err)
		}
		parents[id] = gotParent
		constraints[id] = append(constraints[id], foreignKeyColumn{from: from, to: to})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for id, columns := range constraints {
		if parents[id] == parent && reflect.DeepEqual(columns, want) {
			return
		}
	}
	t.Fatalf("%s has no Project-first foreign key to %s with columns %v", table, parent, want)
}

func createVersionTwentyOneProjectFirstFixture(t *testing.T, path string) {
	t.Helper()
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 22 {
		t.Fatalf("migration catalog has %d files, want Project-first migration 22", len(migrations))
	}
	raw := openRawSQLite(t, path)
	defer raw.Close()
	if err := database.Migrate(context.Background(), raw, migrations[:21], 21); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES
			('project-a', 'Project A', 'completed', 0, '2026-01-01T00:00:00Z', 1, 3),
			('project-b', 'Project B', 'active', 0, '2026-01-02T00:00:00Z', 1, 2);
		INSERT INTO facts (id, project_id, description, retracted_at, retracted_by, retraction_reason)
		VALUES
			('origin', 'project-a', 'origin A', NULL, NULL, NULL),
			('goal', 'project-a', 'goal A', NULL, NULL, NULL),
			('origin', 'project-b', 'origin B', '2026-01-03T00:00:00Z', 'operator', 'superseded'),
			('goal', 'project-b', 'goal B', NULL, NULL, NULL);
		INSERT INTO intents (
			id, project_id, to_fact_id, description, creator, created_at, concluded_at,
			sources_locked, kind
		) VALUES
			('shared-intent', 'project-a', 'goal', 'completed in project A', 'reasoner',
			 '2026-01-01T00:01:00Z', '2026-01-01T00:02:00Z', 0, 'completion'),
			('shared-intent', 'project-b', NULL, 'open in project B', 'reasoner',
			 '2026-01-02T00:01:00Z', NULL, 0, 'explore');
		INSERT INTO intent_sources (intent_id, project_id, fact_id) VALUES
			('shared-intent', 'project-a', 'origin'),
			('shared-intent', 'project-b', 'goal');
		UPDATE intents SET sources_locked = 1;
		INSERT INTO hints (
			id, project_id, content, creator, created_at, generation,
			withdrawn_at, withdrawn_by, withdrawal_reason
		) VALUES
			('shared-hint', 'project-a', 'hint A', 'operator', '2026-01-01T00:01:00Z', 1,
			 NULL, NULL, NULL),
			('shared-hint', 'project-b', 'hint B', 'operator', '2026-01-02T00:01:00Z', 1,
			 '2026-01-03T00:00:00Z', 'operator', 'obsolete');
		INSERT INTO project_completions (
			project_id, generation, intent_id, description, actor, completed_at
		) VALUES ('project-a', 1, 'shared-intent', 'complete', 'operator', '2026-01-01T00:02:00Z');
		INSERT INTO execution_work_keys (
			work_key, project_id, task_type, target_intent_id, last_attempt, created_at,
			retry_state, failure_count, max_attempts, initial_backoff_ms, max_backoff_ms,
			backoff_multiplier, decline_cooldown_ms, next_eligible_at, updated_at
		) VALUES (
			'explore:project-b:shared-intent', 'project-b', 'explore', 'shared-intent', 1,
			'2026-01-02T00:02:00Z', 'eligible', 0, 3, 30000, 300000, 2.0, 5000, NULL,
			'2026-01-02T00:02:00Z'
		);
		INSERT INTO execution_claim_scopes (
			claim_scope, project_id, scope_type, target_intent_id, last_fencing_token, created_at
		) VALUES (
			'intent:project-b:shared-intent', 'project-b', 'intent', 'shared-intent', 1,
			'2026-01-02T00:02:00Z'
		);
		INSERT INTO execution_runs (
			id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
			fencing_token, dispatcher_id, task_type, input_generation, input_revision,
			input_hash, worker_name, config_revision, image_reference, result_schema,
			execution_spec_json, started_at, dispatcher_fencing_token
		) VALUES (
			'run-project-b', 'project-b', 'shared-intent', 'explore:project-b:shared-intent',
			'intent:project-b:shared-intent', 1, 'active', 1, 'dispatcher', 'explore', 1, 2,
			'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'worker', 'config', 'image:latest', 'aether.run-result/v1',
			'{"config_revision":"config","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":2,"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","target_intent_id":"shared-intent"},"worker":{"name":"worker","adapter":"test"},"image":{"requested_ref":"image:latest"},"timeouts":{"run_seconds":12,"phase_seconds":5,"fallback_phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}',
			'2026-01-02T00:03:00Z', 1
		);
		INSERT INTO execution_claims (
			claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
			fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
		) VALUES (
			'intent:project-b:shared-intent', 'explore:project-b:shared-intent', 'run-project-b',
			'project-b', 'shared-intent', 'explore', 1, 'dispatcher', '2026-01-02T00:03:00Z',
			'2026-01-02T00:04:00Z', 1767312240000000000
		);
		INSERT INTO execution_worker_declines (work_key, worker_name, retry_after, updated_at)
		VALUES ('explore:project-b:shared-intent', 'other-worker', '2026-01-02T00:05:00Z', '2026-01-02T00:03:00Z');
		INSERT INTO execution_recovery_events (run_id, dispatcher_id, status, reason, observed_at)
		VALUES ('run-project-b', 'dispatcher', 'observed', 'fixture', '2026-01-02T00:03:00Z');
		INSERT INTO execution_recovery_events (run_id, dispatcher_id, status, reason, observed_at)
		VALUES ('run-project-b', 'dispatcher', 'observed', 'deleted fixture', '2026-01-02T00:03:30Z');
		DELETE FROM execution_recovery_events WHERE id = 2;
		INSERT INTO execution_runtime_handles (
			run_id, project_id, phase, container_id, exec_id, started_at, status
		) VALUES (
			'run-project-b', 'project-b', 'explore_execute', 'container-project-b', 'exec-project-b',
			'2026-01-02T00:03:00Z', 'active'
		);
		INSERT INTO reason_checkpoints (project_id, generation, input_revision, run_id, checked_at)
		VALUES ('project-b', 1, 2, 'run-project-b', '2026-01-02T00:03:00Z');
	`); err != nil {
		t.Fatal(err)
	}
}
