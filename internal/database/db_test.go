package database_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestConfigureCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "aether.db")

	db := database.NewDB()
	if err := db.Configure(dbPath); err != nil {
		t.Fatalf("configure: %v", err)
	}

	conn, err := db.GetConn(context.Background())
	if err != nil {
		t.Fatalf("get conn: %v", err)
	}
	assertRuntimePragmas(t, conn, false)
	readConnections := make([]*sql.Conn, 0, 4)
	for range 4 {
		readConn, err := db.GetReadConn(context.Background())
		if err != nil {
			t.Fatalf("get read conn: %v", err)
		}
		readConnections = append(readConnections, readConn)
		assertRuntimePragmas(t, readConn, true)
	}
	for _, readConn := range readConnections {
		readConn.Close()
	}

	for _, legacyTable := range []string{"settings"} {
		var count int
		if err := conn.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", legacyTable,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy table %q still exists", legacyTable)
		}
	}
	for _, legacyColumn := range []struct{ table, column string }{
		{"projects", "reason_worker"}, {"projects", "reason_trigger"},
		{"projects", "reason_started_at"}, {"projects", "reason_last_heartbeat_at"},
		{"intents", "worker"}, {"intents", "last_heartbeat_at"},
	} {
		var count int
		if err := conn.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", legacyColumn.table, legacyColumn.column,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy column %s.%s still exists", legacyColumn.table, legacyColumn.column)
		}
	}
	var version int
	var checksum, appliedAt string
	if err := conn.QueryRowContext(context.Background(), "SELECT version, checksum, applied_at FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version, &checksum, &appliedAt); err != nil {
		t.Fatalf("scan migration metadata: %v", err)
	}
	if version != database.CurrentSchemaVersion || len(checksum) != 64 || appliedAt == "" {
		t.Fatalf("migration metadata: version=%d checksum=%q applied_at=%q", version, checksum, appliedAt)
	}

	conn.Close()
	db.Close()

	// Verify file was created
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("db file not created: %v", err)
	}
}

func TestCurrentSchemaAllowsReasonFinalizationExecutionHandlePhase(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "reason-finalization.db")); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var schema string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 'execution_runtime_handles'
	`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(schema, "'reason_finalization'") {
		t.Fatalf("execution_runtime_handles schema omitted Reason Finalization: %s", schema)
	}
}

func TestConfigureRepairsKnownProjectOrphansOnlyWithRecoveryPoint(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "known-project-orphans.db")
	createLegacyDatabaseWithProjectOrphans(t, dbPath)

	regular := database.NewDB()
	err := regular.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "offline data recovery point") {
		t.Fatalf("regular startup error=%v, want offline data recovery point requirement", err)
	}
	if _, err := regular.GetConn(t.Context()); err == nil {
		t.Fatal("failed regular startup left database configured")
	}

	upgrading := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, upgrading, dbPath); err != nil {
		t.Fatalf("offline data recovery point configure: %v", err)
	}
	defer upgrading.Close()
	conn, err := upgrading.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var retainedProject, retainedFacts, orphanFacts, orphanIntents, orphanHints, orphanSources, orphanCounters int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM projects WHERE id = 'retained-project'),
			(SELECT COUNT(*) FROM facts WHERE project_id = 'retained-project'),
			(SELECT COUNT(*) FROM facts WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM intents WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM hints WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM intent_sources WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM scoped_counters WHERE project_id = 'deleted-project')
	`).Scan(
		&retainedProject, &retainedFacts, &orphanFacts, &orphanIntents,
		&orphanHints, &orphanSources, &orphanCounters,
	); err != nil {
		t.Fatal(err)
	}
	if retainedProject != 1 || retainedFacts != 2 {
		t.Fatalf("retained business data changed: projects=%d facts=%d", retainedProject, retainedFacts)
	}
	if orphanFacts+orphanIntents+orphanHints+orphanSources+orphanCounters != 0 {
		t.Fatalf(
			"known orphans remain: facts=%d intents=%d hints=%d sources=%d counters=%d",
			orphanFacts, orphanIntents, orphanHints, orphanSources, orphanCounters,
		)
	}

	var repairVersion int
	var factCount, intentCount, hintCount, sourceCount, counterCount int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT repair_version,
		       json_extract(deleted_counts_json, '$.facts'),
		       json_extract(deleted_counts_json, '$.intents'),
		       json_extract(deleted_counts_json, '$.hints'),
		       json_extract(deleted_counts_json, '$.intent_sources'),
		       json_extract(deleted_counts_json, '$.scoped_counters')
		FROM database_repairs ORDER BY repair_version DESC LIMIT 1
	`).Scan(&repairVersion, &factCount, &intentCount, &hintCount, &sourceCount, &counterCount); err != nil {
		t.Fatal(err)
	}
	if repairVersion != 1 || factCount != 1 || intentCount != 1 || hintCount != 1 || sourceCount != 1 || counterCount != 1 {
		t.Fatalf(
			"repair record: version=%d facts=%d intents=%d hints=%d sources=%d counters=%d",
			repairVersion, factCount, intentCount, hintCount, sourceCount, counterCount,
		)
	}
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE database_repairs SET repaired_at = '2030-01-01T00:00:00Z' WHERE repair_version = 1",
	); err == nil {
		t.Fatal("database repair record accepted an update")
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM database_repairs WHERE repair_version = 1",
	); err == nil {
		t.Fatal("database repair record accepted deletion")
	}
	assertDatabaseIntegrity(t, conn)
}

func TestConfigureRequiresRecoveryPointBeforeMigratingExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pending-migration.db")
	createVersionOneDatabase(t, dbPath)

	regular := database.NewDB()
	err := regular.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "offline data recovery point") {
		t.Fatalf("regular startup error=%v, want offline data recovery point requirement", err)
	}

	upgrading := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, upgrading, dbPath); err != nil {
		t.Fatalf("offline data recovery point configure: %v", err)
	}
	defer upgrading.Close()
	conn, err := upgrading.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var version int
	if err := conn.QueryRowContext(t.Context(), "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != database.CurrentSchemaVersion {
		t.Fatalf("schema version=%d, want %d", version, database.CurrentSchemaVersion)
	}
	assertDatabaseIntegrity(t, conn)
}

func TestConfigureConsumesOfflineDataRecoveryPointMarkerAfterSuccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "marked-upgrade.db")
	createVersionOneDatabase(t, dbPath)
	markerPath := offlineDataRecoveryPointPath(dbPath)
	writeOfflineDataRecoveryPoint(t, dbPath)

	db := database.NewDB()
	if err := db.Configure(dbPath); err != nil {
		t.Fatalf("configure with recovery marker: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("successful startup did not consume recovery marker: %v", err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var version int
	if err := conn.QueryRowContext(t.Context(), "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != database.CurrentSchemaVersion {
		t.Fatalf("schema version=%d, want %d", version, database.CurrentSchemaVersion)
	}
}

func TestConfigureRejectsUnverifiableOfflineDataRecoveryPoint(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "forged-recovery-point.db")
	createVersionOneDatabase(t, dbPath)
	markerPath := offlineDataRecoveryPointPath(dbPath)
	if err := os.WriteFile(markerPath, []byte("arbitrary non-empty authorization\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err := db.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "recovery point") {
		t.Fatalf("configure error=%v, want invalid recovery point", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("failed startup consumed recovery point: %v", err)
	}
	assertSchemaVersion(t, dbPath, 1)
}

func TestConfigureRejectsTamperedOfflineDataRecoveryPointBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tampered-recovery-point.db")
	createVersionOneDatabase(t, dbPath)
	backupDatabasePath := writeOfflineDataRecoveryPoint(t, dbPath)
	if err := os.WriteFile(backupDatabasePath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err := db.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("configure error=%v, want backup checksum failure", err)
	}
	assertSchemaVersion(t, dbPath, 1)
}

func TestConfigureRejectsOfflineDataRecoveryPointSymlinks(t *testing.T) {
	for _, testCase := range []struct {
		name string
		link func(t *testing.T, recoveryPointPath, databasePath string)
	}{
		{
			name: "data directory",
			link: func(t *testing.T, recoveryPointPath, databasePath string) {
				externalData := filepath.Join(t.TempDir(), "data")
				if err := os.MkdirAll(externalData, 0o700); err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(filepath.Join(recoveryPointPath, "data", filepath.Base(databasePath)))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(externalData, filepath.Base(databasePath)), contents, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(recoveryPointPath, "data")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalData, filepath.Join(recoveryPointPath, "data")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest",
			link: func(t *testing.T, recoveryPointPath, _ string) {
				externalManifest := filepath.Join(t.TempDir(), "SHA256SUMS")
				contents, err := os.ReadFile(filepath.Join(recoveryPointPath, "SHA256SUMS"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(externalManifest, contents, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(recoveryPointPath, "SHA256SUMS")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalManifest, filepath.Join(recoveryPointPath, "SHA256SUMS")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "symlinked-recovery-point.db")
			createVersionOneDatabase(t, dbPath)
			backupDatabasePath := writeOfflineDataRecoveryPoint(t, dbPath)
			recoveryPointPath := filepath.Dir(filepath.Dir(backupDatabasePath))
			testCase.link(t, recoveryPointPath, dbPath)

			db := database.NewDB()
			err := db.Configure(dbPath)
			if err == nil || !strings.Contains(err.Error(), "symbolic link") {
				t.Fatalf("configure error=%v, want symbolic-link rejection", err)
			}
			assertSchemaVersion(t, dbPath, 1)
		})
	}
}

func TestConfigureRepairsVersionTwelveProjectOrphansInDependencyOrder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "version-twelve-orphans.db")
	createVersionTwelveDatabaseWithProjectOrphans(t, dbPath)

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatalf("offline data recovery point configure: %v", err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, table := range []string{
		"facts", "intents", "intent_sources", "hints", "scoped_counters",
		"project_completions", "blackboard_changes", "execution_work_keys",
		"execution_claim_scopes", "execution_runs", "execution_claims",
		"reason_checkpoints", "execution_worker_declines", "execution_recovery_events",
		"reason_scheduling_baselines", "execution_runtime_handles",
	} {
		var count int
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s rows remain after repair: %d", table, count)
		}
	}

	var schemaVersion int
	var deletedCounts string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT schema_version_before, deleted_counts_json
		FROM database_repairs WHERE repair_version = 1
	`).Scan(&schemaVersion, &deletedCounts); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 12 {
		t.Fatalf("repair schema version=%d, want 12", schemaVersion)
	}
	wantDeleted := map[string]int{
		"facts": 2, "intents": 1, "intent_sources": 1, "hints": 1,
		"scoped_counters": 1, "project_completions": 1, "blackboard_changes": 1,
		"execution_work_keys": 1, "execution_claim_scopes": 1, "execution_runs": 1,
		"execution_claims": 1, "reason_checkpoints": 1, "execution_worker_declines": 1,
		"execution_recovery_events": 1, "reason_scheduling_baselines": 1,
		"execution_runtime_handles": 1,
	}
	for table, want := range wantDeleted {
		var deleted int
		if err := conn.QueryRowContext(t.Context(),
			"SELECT json_extract(?, '$.' || ?)", deletedCounts, table,
		).Scan(&deleted); err != nil {
			t.Fatalf("repair count %s: %v", table, err)
		}
		if deleted != want {
			t.Errorf("repair count %s=%d, want %d", table, deleted, want)
		}
	}
	var cleanupProjectID, cleanupContainerID, cleanupRunID, cleanupExecID string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT jobs.project_id, jobs.container_id, handles.run_id, handles.exec_id
		FROM project_cleanup_jobs AS jobs
		JOIN project_cleanup_handles AS handles ON handles.job_id = jobs.id
		WHERE jobs.project_id = 'deleted-project'
	`).Scan(&cleanupProjectID, &cleanupContainerID, &cleanupRunID, &cleanupExecID); err != nil {
		t.Fatal(err)
	}
	if cleanupProjectID != "deleted-project" || cleanupContainerID != "container" ||
		cleanupRunID != "run-orphan" || cleanupExecID != "exec" {
		t.Fatalf(
			"runtime cleanup evidence not preserved: project=%q container=%q run=%q exec=%q",
			cleanupProjectID, cleanupContainerID, cleanupRunID, cleanupExecID,
		)
	}
	assertDatabaseIntegrity(t, conn)
}

func TestConfigureRejectsUnknownForeignKeyViolationWithoutMutation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unknown-violation.db")
	createVersionOneDatabase(t, dbPath)
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO intent_sources (intent_id, project_id, fact_id)
		VALUES ('missing-intent', 'deleted-project', 'origin')
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err = configureOfflineDataRecoveryPoint(t, db, dbPath)
	if err == nil || !strings.Contains(err.Error(), "unknown foreign key violation") {
		t.Fatalf("configure error=%v, want unknown foreign key violation", err)
	}
	if _, err := db.GetConn(t.Context()); err == nil {
		t.Fatal("failed startup left database configured")
	}

	inspection, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var schemaVersion, sourceCount, repairTableCount int
	if err := inspection.QueryRowContext(t.Context(), `
		SELECT
			(SELECT MAX(version) FROM schema_migrations),
			(SELECT COUNT(*) FROM intent_sources WHERE intent_id = 'missing-intent'),
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'database_repairs')
	`).Scan(&schemaVersion, &sourceCount, &repairTableCount); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 1 || sourceCount != 1 || repairTableCount != 0 {
		t.Fatalf(
			"failed startup mutated database: version=%d sources=%d repair_table=%d",
			schemaVersion, sourceCount, repairTableCount,
		)
	}
}

func TestConfigureRejectsRepairCountMismatchWithoutMutation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "repair-count-mismatch.db")
	createVersionTwelveDatabaseWithProjectOrphans(t, dbPath)
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		CREATE TRIGGER ignore_orphan_fact_delete
		BEFORE DELETE ON facts
		WHEN OLD.project_id = 'deleted-project' AND OLD.id = 'goal'
		BEGIN
			SELECT RAISE(IGNORE);
		END;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err = configureOfflineDataRecoveryPoint(t, db, dbPath)
	if err == nil || !strings.Contains(err.Error(), "repair count mismatch") {
		t.Fatalf("configure error=%v, want repair count mismatch", err)
	}

	inspection, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var schemaVersion, orphanFactCount, repairTableCount int
	if err := inspection.QueryRowContext(t.Context(), `
		SELECT
			(SELECT MAX(version) FROM schema_migrations),
			(SELECT COUNT(*) FROM facts WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'database_repairs')
	`).Scan(&schemaVersion, &orphanFactCount, &repairTableCount); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 12 || orphanFactCount != 2 || repairTableCount != 0 {
		t.Fatalf(
			"count mismatch committed partial repair: version=%d orphan_facts=%d repair_table=%d",
			schemaVersion, orphanFactCount, repairTableCount,
		)
	}
}

func TestConfigureRollsBackOrphanRepairWhenMigrationFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "repair-rollback.db")
	createVersionTwelveDatabaseWithProjectOrphans(t, dbPath)
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), "ALTER TABLE blackboard_changes RENAME COLUMN data TO broken_data"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err = configureOfflineDataRecoveryPoint(t, db, dbPath)
	if err == nil {
		t.Fatal("configure unexpectedly succeeded after legacy schema damage")
	}
	if _, err := db.GetConn(t.Context()); err == nil {
		t.Fatal("failed startup left database configured")
	}

	inspection, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var schemaVersion, orphanFactCount, repairTableCount int
	if err := inspection.QueryRowContext(t.Context(), `
		SELECT
			(SELECT MAX(version) FROM schema_migrations),
			(SELECT COUNT(*) FROM facts WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'database_repairs')
	`).Scan(&schemaVersion, &orphanFactCount, &repairTableCount); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 12 || orphanFactCount != 2 || repairTableCount != 0 {
		t.Fatalf(
			"failed migration committed partial repair: version=%d orphan_facts=%d repair_table=%d",
			schemaVersion, orphanFactCount, repairTableCount,
		)
	}
}

func TestConfigureRejectsMissingDeleteConstraintAfterRepair(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing-delete-constraint.db")
	current := database.NewDB()
	if err := current.Configure(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		DROP TRIGGER facts_delete_only_with_project;
		INSERT INTO facts (id, project_id, description)
		VALUES ('orphan-fact', 'deleted-project', 'must roll back');
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err = configureOfflineDataRecoveryPoint(t, db, dbPath)
	if err == nil || !strings.Contains(err.Error(), "delete constraint") {
		t.Fatalf("configure error=%v, want delete constraint failure", err)
	}

	inspection, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var orphanCount, repairCount int
	if err := inspection.QueryRowContext(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM facts WHERE project_id = 'deleted-project'),
			(SELECT COUNT(*) FROM database_repairs)
	`).Scan(&orphanCount, &repairCount); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 1 || repairCount != 0 {
		t.Fatalf("constraint failure committed repair: orphans=%d repairs=%d", orphanCount, repairCount)
	}
}

func TestReadConnectionRejectsWrites(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "read-only.db")); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "CREATE TABLE should_not_exist (id TEXT)"); err == nil {
		t.Fatal("read connection accepted a write")
	}
}

func TestForeignKeyCascadeCanDeleteProjectOwnedBlackboard(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn, err := db.GetConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `
		INSERT INTO projects (id, title, created_at) VALUES ('project-cascade', 'cascade', '2026-01-01T00:00:00Z');
		INSERT INTO facts (id, project_id, description) VALUES ('origin', 'project-cascade', 'origin');
		INSERT INTO facts (id, project_id, description) VALUES ('goal', 'project-cascade', 'goal');
		INSERT INTO intents (id, project_id, description, creator, created_at, sources_locked)
		VALUES ('i001', 'project-cascade', 'intent', 'test', '2026-01-01T00:00:00Z', 0);
		INSERT INTO intent_sources (intent_id, project_id, fact_id)
		VALUES ('i001', 'project-cascade', 'origin');
		UPDATE intents SET sources_locked = 1 WHERE id = 'i001' AND project_id = 'project-cascade';
		INSERT INTO hints (id, project_id, content, creator, created_at)
		VALUES ('h001', 'project-cascade', 'hint', 'test', '2026-01-01T00:00:00Z');
		UPDATE projects SET deleting = 1 WHERE id = 'project-cascade';
		DELETE FROM projects WHERE id = 'project-cascade';
	`); err != nil {
		t.Fatalf("project cascade delete: %v", err)
	}
	for _, table := range []string{"projects", "facts", "intents", "hints"} {
		var count int
		column := "project_id"
		if table == "projects" {
			column = "id"
		}
		if err := conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table+" WHERE "+column+" = ?", "project-cascade").Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows remain after cascade: %d", table, count)
		}
	}
}

func TestConfigureBackfillsImmutableIntentKinds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "intent-kind.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:17], 17); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES
			('kind-bootstrap', 'bootstrap', 'active', 1, '2026-01-01T00:00:00Z', 1, 0),
			('kind-completion', 'completion', 'completed', 0, '2026-01-01T00:00:00Z', 1, 0),
			('kind-explore', 'explore', 'active', 0, '2026-01-01T00:00:00Z', 1, 0),
			('kind-ambiguous', 'ambiguous', 'active', 1, '2026-01-01T00:00:00Z', 1, 0);
		INSERT INTO facts (id, project_id, description) VALUES
			('origin', 'kind-bootstrap', 'origin'),
			('goal', 'kind-bootstrap', 'goal'),
			('origin', 'kind-completion', 'origin'),
			('goal', 'kind-completion', 'goal'),
			('origin', 'kind-explore', 'origin'),
			('goal', 'kind-explore', 'goal'),
			('origin', 'kind-ambiguous', 'origin'),
			('goal', 'kind-ambiguous', 'goal'),
			('extra', 'kind-ambiguous', 'extra');
		INSERT INTO intents (id, project_id, to_fact_id, description, creator, created_at, sources_locked)
		VALUES
			('i001', 'kind-bootstrap', NULL, 'bootstrap', 'dispatcher.bootstrap', '2026-01-01T00:00:00Z', 0),
			('i001', 'kind-completion', 'goal', 'finished', 'reasoner', '2026-01-01T00:00:00Z', 0),
			('i001', 'kind-explore', NULL, 'inspect', 'reasoner', '2026-01-01T00:00:00Z', 0),
			('i001', 'kind-ambiguous', NULL, 'bootstrap', 'dispatcher.bootstrap', '2026-01-01T00:00:00Z', 0);
		INSERT INTO intent_sources (intent_id, project_id, fact_id) VALUES ('i001', 'kind-bootstrap', 'origin');
		INSERT INTO intent_sources (intent_id, project_id, fact_id) VALUES ('i001', 'kind-ambiguous', 'origin');
		UPDATE intents SET sources_locked = 1;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	want := map[string]string{
		"kind-bootstrap":  "bootstrap",
		"kind-completion": "completion",
		"kind-explore":    "explore",
		"kind-ambiguous":  "explore",
	}
	for projectID, wantKind := range want {
		var got string
		if err := conn.QueryRowContext(t.Context(),
			"SELECT kind FROM intents WHERE project_id = ? AND id = 'i001'", projectID,
		).Scan(&got); err != nil {
			t.Fatalf("read kind for %s: %v", projectID, err)
		}
		if got != wantKind {
			t.Errorf("kind for %s = %q, want %q", projectID, got, wantKind)
		}
	}
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE intents SET kind = 'completion' WHERE project_id = 'kind-explore' AND id = 'i001'"); err == nil {
		t.Fatal("expected intent kind immutability trigger")
	}
}

func TestProjectBlackboardQueriesUseProjectIndexes(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "query-plan.db")); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
		SELECT printf('plan-project-%04d', value), 'Query Plan Project',
		       CASE WHEN value % 2 = 0 THEN 'active' ELSE 'paused' END,
		       printf('2026-01-01T00:%02d:00Z', value % 60)
		FROM sequence;
		INSERT INTO facts (project_id, id, description)
		SELECT id, 'origin', 'origin' FROM projects;
		INSERT INTO intents (
			project_id, id, kind, description, creator, created_at, sources_locked
		)
		SELECT id, 'i001', 'explore', 'inspect', 'test', created_at, 0 FROM projects;
		INSERT INTO intent_sources (project_id, intent_id, fact_id)
		SELECT id, 'i001', 'origin' FROM projects;
		UPDATE intents SET sources_locked = 1;
		INSERT INTO hints (project_id, id, content, creator, created_at)
		SELECT id, 'h001', 'hint', 'test', created_at FROM projects;
		INSERT INTO blackboard_changes (
			project_id, revision, generation, kind, entity_type, entity_id, data, created_at
		)
		SELECT id, 1, 1, 'project_created', 'project', id, '{}', created_at FROM projects;
		INSERT INTO execution_work_keys (
			work_key, project_id, task_type, last_attempt, created_at, retry_state, updated_at
		)
		SELECT 'reason:' || id, id, 'reason', 0, created_at, 'backoff', created_at FROM projects;
		ANALYZE;
	`); err != nil {
		t.Fatalf("create query-plan scale fixture: %v", err)
	}

	checks := []struct {
		name  string
		query string
		args  []any
		index string
		table string
	}{
		{
			name:  "facts by project",
			query: "EXPLAIN QUERY PLAN SELECT id FROM facts WHERE project_id = ? ORDER BY id",
			args:  []any{"plan-project-0100"}, index: "sqlite_autoindex_facts_1", table: "facts",
		},
		{
			name:  "open intents by project and kind",
			query: "EXPLAIN QUERY PLAN SELECT id FROM intents WHERE project_id = ? AND kind = ? AND to_fact_id IS NULL AND cancelled_at IS NULL ORDER BY created_at, id",
			args:  []any{"plan-project-0100", "explore"}, index: "intents_project_open_kind_idx", table: "intents",
		},
		{
			name: "scheduling intents by project",
			query: `EXPLAIN QUERY PLAN
				SELECT id, kind, description, creator, to_fact_id, created_at,
				       concluded_at, cancelled_at, cancelled_by, cancellation_reason
				FROM intents WHERE project_id = ? ORDER BY created_at, id`,
			args:  []any{"plan-project-0100"},
			index: "intents_project_created_idx", table: "intents",
		},
		{
			name:  "hints by project",
			query: "EXPLAIN QUERY PLAN SELECT id FROM hints WHERE project_id = ? ORDER BY created_at, id",
			args:  []any{"plan-project-0100"}, index: "hints_project_created_idx", table: "hints",
		},
		{
			name:  "sources by project",
			query: "EXPLAIN QUERY PLAN SELECT intent_id, fact_id FROM intent_sources WHERE project_id = ? ORDER BY intent_id, fact_id",
			args:  []any{"plan-project-0100"}, index: "sqlite_autoindex_intent_sources_1", table: "intent_sources",
		},
		{
			name: "admission intent point lookup",
			query: `EXPLAIN QUERY PLAN
				SELECT id, kind, description, creator, to_fact_id, created_at,
				       concluded_at, cancelled_at, cancelled_by, cancellation_reason
				FROM intents WHERE id = ? AND project_id = ?`,
			args: []any{"i001", "plan-project-0100"}, index: "sqlite_autoindex_intents_1", table: "intents",
		},
		{
			name: "history by project and revision",
			query: `EXPLAIN QUERY PLAN
				SELECT project_id, revision, generation, kind, entity_type, entity_id, data, created_at
				FROM blackboard_changes
				WHERE project_id = ? AND revision < ?
				ORDER BY revision DESC LIMIT ?`,
			args: []any{"plan-project-0100", 10, 50}, index: "sqlite_autoindex_blackboard_changes_1", table: "blackboard_changes",
		},
		{
			name: "projects by lifecycle for scheduling",
			query: `EXPLAIN QUERY PLAN
				SELECT id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision
				FROM projects WHERE status = ? ORDER BY id`,
			args:  []any{"active"},
			index: "projects_status_id_idx", table: "projects",
		},
		{
			name: "work keys by project task and target",
			query: `EXPLAIN QUERY PLAN
				SELECT work_key FROM execution_work_keys
				WHERE project_id = ? AND task_type = ? AND target_intent_id IS NULL`,
			args:  []any{"plan-project-0100", "reason"},
			index: "execution_work_keys_project_task_target_idx", table: "execution_work_keys",
		},
		{
			name: "scheduling state by work key",
			query: `EXPLAIN QUERY PLAN
				SELECT work_key, retry_state, last_attempt, next_eligible_at
				FROM execution_work_keys WHERE work_key IN (?, ?)`,
			args:  []any{"reason:plan-project-0100", "reason:plan-project-0101"},
			index: "sqlite_autoindex_execution_work_keys_1", table: "execution_work_keys",
		},
		{
			name: "retry state by project",
			query: `EXPLAIN QUERY PLAN
				SELECT COUNT(*), MIN(next_eligible_at) FROM execution_work_keys
				WHERE project_id = ? AND retry_state = ?`,
			args:  []any{"plan-project-0100", "backoff"},
			index: "execution_work_keys_project_retry_idx", table: "execution_work_keys",
		},
		{
			name: "active runtime handles by project",
			query: `EXPLAIN QUERY PLAN
				SELECT run_id, phase, exec_id FROM execution_runtime_handles
				WHERE project_id = ? AND status = 'active'
				ORDER BY started_at, run_id, phase, exec_id`,
			args:  []any{"plan-project-0100"},
			index: "execution_runtime_handles_project_active_idx", table: "execution_runtime_handles",
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			rows, err := conn.QueryContext(t.Context(), check.query, check.args...)
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
			joined := strings.Join(details, "\n")
			if !strings.Contains(joined, check.index) {
				t.Fatalf("query plan does not use %s: %s", check.index, joined)
			}
			if strings.Contains(joined, "SCAN "+check.table) {
				t.Fatalf("query plan scans %s: %s", check.table, joined)
			}
			if strings.Contains(joined, "USE TEMP B-TREE") {
				t.Fatalf("query plan uses a temporary sort: %s", joined)
			}
		})
	}
}

func TestConfigureBackfillsIntentKindsInHistorySnapshots(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history-intent-kind.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:19], 19); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES ('history-kinds', 'history', 'active', 1, '2026-01-01T00:00:00Z', 1, 1);
		INSERT INTO intents (id, project_id, kind, description, creator, created_at)
		VALUES
			('i001', 'history-kinds', 'explore', 'current explore', 'reasoner', '2026-01-01T00:00:00Z'),
			('i002', 'history-kinds', 'completion', 'current completion', 'reasoner', '2026-01-01T00:00:00Z'),
			('i003', 'history-kinds', 'bootstrap', 'current bootstrap', 'dispatcher.bootstrap', '2026-01-01T00:00:00Z');
		INSERT INTO blackboard_changes (project_id, revision, generation, kind, entity_type, entity_id, data, created_at)
		VALUES (
			'history-kinds', 1, 1, 'project_created', 'project', 'history-kinds',
			'{"schema_version":1,"source":{"kind":"operator","name":"test"},"before":null,"after":{"project":{"id":"history-kinds","revision":1,"generation":1},"intents":[{"id":"i001","description":"bootstrap","creator":"dispatcher.bootstrap","from":["origin"]},{"id":"i002","description":"done","creator":"reasoner","to":"goal"},{"id":"i003","description":"inspect","creator":"reasoner"}]}}',
			'2026-01-01T00:00:00Z'
		);
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rows, err := conn.QueryContext(t.Context(), `
		SELECT json_extract(data, '$.after.intents[0].kind'),
		       json_extract(data, '$.after.intents[1].kind'),
		       json_extract(data, '$.after.intents[2].kind')
		FROM blackboard_changes WHERE project_id = 'history-kinds' AND revision = 1
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("history snapshot row missing")
	}
	var bootstrap, completion, explore string
	if err := rows.Scan(&bootstrap, &completion, &explore); err != nil {
		t.Fatal(err)
	}
	if bootstrap != "explore" || completion != "completion" || explore != "bootstrap" {
		t.Fatalf("history intent kinds: bootstrap=%q completion=%q explore=%q", bootstrap, completion, explore)
	}
}

func assertRuntimePragmas(t *testing.T, conn *sql.Conn, readOnly bool) {
	t.Helper()
	var journalMode string
	if err := conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode=%q, want wal", journalMode)
	}
	checks := map[string]int{}
	for _, name := range []string{"foreign_keys", "synchronous", "busy_timeout", "auto_vacuum", "wal_autocheckpoint", "query_only"} {
		var value int
		if err := conn.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&value); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checks[name] = value
	}
	if checks["foreign_keys"] != 1 || checks["synchronous"] != 2 || checks["busy_timeout"] != 5000 ||
		checks["auto_vacuum"] != 2 || checks["wal_autocheckpoint"] != 1000 {
		t.Fatalf("runtime pragmas: %+v", checks)
	}
	wantQueryOnly := 0
	if readOnly {
		wantQueryOnly = 1
	}
	if checks["query_only"] != wantQueryOnly {
		t.Fatalf("query_only=%d, want %d", checks["query_only"], wantQueryOnly)
	}
}

func TestConfigureUpgradesVersionTwoBlackboardWithCurrentBaselineOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v2.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:2], 2); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES ('proj_v2', 'version two', 'active', 0, '2026-01-01T00:00:00Z', 1, 4);
		INSERT INTO facts (id, project_id, description) VALUES ('origin', 'proj_v2', 'origin');
		INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj_v2', 'goal');
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var revision int64
	var changeCount int
	if err := conn.QueryRowContext(context.Background(), `
		SELECT
			(SELECT blackboard_revision FROM projects WHERE id = 'proj_v2'),
			(SELECT COUNT(*) FROM blackboard_changes WHERE project_id = 'proj_v2')
	`).Scan(&revision, &changeCount); err != nil {
		t.Fatal(err)
	}
	if revision != 4 || changeCount != 1 {
		t.Fatalf("v2 replay boundary: revision=%d changes=%d", revision, changeCount)
	}
	var kind string
	var baselineRevision int64
	var data string
	if err := conn.QueryRowContext(context.Background(), `
		SELECT revision, kind, data FROM blackboard_changes WHERE project_id = 'proj_v2'
	`).Scan(&baselineRevision, &kind, &data); err != nil {
		t.Fatal(err)
	}
	if baselineRevision != revision || kind != "project_baseline" || !strings.Contains(data, `"title":"version two"`) {
		t.Fatalf("v2 baseline: revision=%d kind=%q data=%s", baselineRevision, kind, data)
	}
	var factCount, activeFactCount int
	if err := conn.QueryRowContext(context.Background(), `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE retracted_at IS NULL)
		FROM facts WHERE project_id = 'proj_v2'
	`).Scan(&factCount, &activeFactCount); err != nil {
		t.Fatal(err)
	}
	if factCount != 2 || activeFactCount != 2 {
		t.Fatalf("v2 facts after migration: total=%d active=%d", factCount, activeFactCount)
	}
}

func TestConfigureUpgradesEveryVersionedSchemaWithData(t *testing.T) {
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for oldVersion := 1; oldVersion < database.CurrentSchemaVersion; oldVersion++ {
		t.Run(fmt.Sprintf("v%d", oldVersion), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "old.db")
			raw := openRawSQLite(t, dbPath)
			if err := database.Migrate(context.Background(), raw, migrations[:oldVersion], oldVersion); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`
				INSERT INTO projects (id, title, status, bootstrap_enabled, created_at)
				VALUES ('proj_upgrade', 'preserved', 'active', 0, '2026-01-01T00:00:00Z');
				INSERT INTO facts (id, project_id, description) VALUES ('origin', 'proj_upgrade', 'origin');
				INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj_upgrade', 'goal');
			`); err != nil {
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			db := database.NewDB()
			if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			conn, err := db.GetConn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var title string
			if err := conn.QueryRowContext(context.Background(),
				"SELECT title FROM projects WHERE id = 'proj_upgrade'",
			).Scan(&title); err != nil {
				t.Fatal(err)
			}
			var version, runTableCount int
			if err := conn.QueryRowContext(context.Background(),
				"SELECT MAX(version) FROM schema_migrations",
			).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(context.Background(),
				"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'execution_runs'",
			).Scan(&runTableCount); err != nil {
				t.Fatal(err)
			}
			if title != "preserved" || version != database.CurrentSchemaVersion || runTableCount != 1 {
				t.Fatalf("upgrade result: title=%q version=%d run_table=%d", title, version, runTableCount)
			}
		})
	}
}

func TestConfigureVersionSixteenBackfillsLatestProjectContainer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v15-runtime-handles.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:15], 15); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES ('proj_runtime', 'runtime history', 'active', 0, '2026-01-01T00:00:00Z', 1, 0);
		INSERT INTO facts (id, project_id, description) VALUES ('origin', 'proj_runtime', 'origin');
		INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj_runtime', 'goal');
		INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, last_fencing_token, created_at)
		VALUES ('reason:proj_runtime', 'proj_runtime', 'reason', 2, '2026-01-01T00:00:00Z');
		INSERT INTO execution_work_keys (work_key, project_id, task_type, last_attempt, created_at)
		VALUES ('reason:proj_runtime:g1:r0', 'proj_runtime', 'reason', 2, '2026-01-01T00:00:00Z');
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, input_generation, input_revision, input_hash,
			worker_name, config_revision, image_reference, result_schema, execution_spec_json, started_at
		) VALUES
			('run_old', 'proj_runtime', 'reason:proj_runtime:g1:r0', 'reason:proj_runtime', 1, 'active', 1,
			 'dispatcher', 'reason', 1, 0, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'reasoner', 'config', 'agent:latest', 'aether.reason/v1',
			 '{"config_revision":"config","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":0,"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"agent:latest"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.reason/v1"}', '2026-01-01T00:01:00Z'),
			('run_new', 'proj_runtime', 'reason:proj_runtime:g1:r0', 'reason:proj_runtime', 2, 'active', 2,
			 'dispatcher', 'reason', 1, 0, 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'reasoner', 'config', 'agent:latest', 'aether.reason/v1',
			 '{"config_revision":"config","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":0,"hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"agent:latest"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.reason/v1"}', '2026-01-01T00:02:00Z');
		INSERT INTO execution_runtime_handles (run_id, project_id, phase, container_id, exec_id, started_at)
		VALUES
			('run_old', 'proj_runtime', 'reason', 'container-old', 'exec-old', '2026-01-01T00:01:00Z'),
			('run_new', 'proj_runtime', 'reason', 'container-new', 'exec-new', '2026-01-01T00:02:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var containerID, recordedAt string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT container_id, recorded_at FROM project_runtime_containers WHERE project_id = 'proj_runtime'
	`).Scan(&containerID, &recordedAt); err != nil {
		t.Fatal(err)
	}
	if containerID != "container-new" || recordedAt != "2026-01-01T00:02:00Z" {
		t.Fatalf("runtime container backfill: container=%q recorded_at=%q", containerID, recordedAt)
	}
}

func TestConfigureVersionEightMaterializesOnlyInitialBootstrapIntent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v8-bootstrap.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:8], 8); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES
			('proj_bootstrap', 'eligible', 'active', 1, '2026-01-01T00:00:00Z', 1, 1),
			('proj_paused', 'paused eligible', 'paused', 1, '2026-01-01T00:00:00Z', 1, 1),
			('proj_changed', 'not initial', 'active', 1, '2026-01-01T00:00:00Z', 1, 2);
		INSERT INTO facts (id, project_id, description) VALUES
			('origin', 'proj_bootstrap', 'origin'),
			('goal', 'proj_bootstrap', 'goal'),
			('origin', 'proj_paused', 'origin'),
			('goal', 'proj_paused', 'goal'),
			('origin', 'proj_changed', 'origin'),
			('goal', 'proj_changed', 'goal'),
			('f001', 'proj_changed', 'additional fact');
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var intentCount, sourceCount, sourcesLocked, counter, baselineCount, changeCount int
	var revision int64
	if err := conn.QueryRowContext(context.Background(), `
		SELECT
			(SELECT COUNT(*) FROM intents WHERE project_id = 'proj_bootstrap' AND id = 'i001'),
			(SELECT COUNT(*) FROM intent_sources WHERE project_id = 'proj_bootstrap' AND intent_id = 'i001' AND fact_id = 'origin'),
			(SELECT sources_locked FROM intents WHERE project_id = 'proj_bootstrap' AND id = 'i001'),
			(SELECT value FROM scoped_counters WHERE project_id = 'proj_bootstrap' AND kind = 'intent'),
			(SELECT COUNT(*) FROM reason_scheduling_baselines WHERE project_id = 'proj_bootstrap' AND generation = 1 AND input_revision = 2),
			(SELECT blackboard_revision FROM projects WHERE id = 'proj_bootstrap'),
			(SELECT COUNT(*) FROM blackboard_changes WHERE project_id = 'proj_bootstrap' AND revision = 2 AND kind = 'project_baseline')
	`).Scan(&intentCount, &sourceCount, &sourcesLocked, &counter, &baselineCount, &revision, &changeCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 1 || sourceCount != 1 || sourcesLocked != 1 || counter != 1 ||
		baselineCount != 1 || revision != 2 || changeCount != 1 {
		t.Fatalf(
			"Bootstrap backfill: intents=%d sources=%d locked=%d counter=%d baselines=%d revision=%d changes=%d",
			intentCount, sourceCount, sourcesLocked, counter, baselineCount, revision, changeCount,
		)
	}
	if err := conn.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM intents WHERE project_id = 'proj_paused' AND id = 'i001'",
	).Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 1 {
		t.Fatalf("paused initial Project received %d Bootstrap intents", intentCount)
	}
	if err := conn.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM intents WHERE project_id = 'proj_changed'",
	).Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 0 {
		t.Fatalf("non-initial Project received %d Bootstrap intents", intentCount)
	}
}

func TestConfigureUpgradesActiveVersionFiveRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v5-run.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:5], 5); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES ('proj_v5_run', 'active run', 'active', 0, '2026-01-01T00:00:00Z', 1, 0);
		INSERT INTO facts (id, project_id, description) VALUES ('origin', 'proj_v5_run', 'origin');
		INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj_v5_run', 'goal');
		INSERT INTO execution_work_keys (work_key, project_id, task_type, last_attempt, created_at)
		VALUES ('reason:proj_v5_run:g1:r0', 'proj_v5_run', 'reason', 1, '2026-01-01T00:00:00Z');
		INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, last_fencing_token, created_at)
		VALUES ('reason:proj_v5_run', 'proj_v5_run', 'reason', 1, '2026-01-01T00:00:00Z');
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, input_generation, input_revision, input_hash,
			worker_name, config_revision, image_id, image_repo_digest, run_profile,
			result_schema, execution_spec_json, started_at
		) VALUES (
			'run_v5', 'proj_v5_run', 'reason:proj_v5_run:g1:r0', 'reason:proj_v5_run', 1, 'active', 1,
			'dispatcher-v5', 'reason', 1, 0, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'reasoner', 'config-v5', 'sha256:image', 'repo@sha256:digest', 'restricted',
			'aether.run-result/v1',
			'{"config_revision":"config-v5","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":0,"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"worker":{"name":"reasoner","adapter":"codex"},"image":{"requested_ref":"repo:v5","image_id":"sha256:image","repo_digest":"repo@sha256:digest"},"run_profile":{"name":"restricted","revision":"v1"},"timeouts":{"run_seconds":300,"phase_seconds":120,"grace_seconds":5},"result_schema":"aether.run-result/v1"}',
			'2026-01-01T00:00:00Z'
		);
		INSERT INTO execution_claims (
			claim_scope, work_key, run_id, project_id, task_type, fencing_token,
			dispatcher_id, claimed_at, lease_expires_at
		) VALUES (
			'reason:proj_v5_run', 'reason:proj_v5_run:g1:r0', 'run_v5', 'proj_v5_run', 'reason', 1,
			'dispatcher-v5', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z'
		);
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var imageReference string
	if err := conn.QueryRowContext(context.Background(),
		"SELECT image_reference FROM execution_runs WHERE id = 'run_v5'",
	).Scan(&imageReference); err != nil {
		t.Fatal(err)
	}
	var legacyImageColumns int
	if err := conn.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM pragma_table_info('execution_runs')
		WHERE name IN ('image_id', 'image_repo_digest')
	`).Scan(&legacyImageColumns); err != nil {
		t.Fatal(err)
	}
	if imageReference != "repo:v5" || legacyImageColumns != 0 {
		t.Fatalf("runtime image migration: reference=%q legacy_columns=%d", imageReference, legacyImageColumns)
	}
	var retryState string
	var failureCount, maxAttempts int
	var dispatcherFencingToken int64
	var leaseExpiresUnixNano sql.NullInt64
	if err := conn.QueryRowContext(context.Background(), `
		SELECT work.retry_state, work.failure_count, work.max_attempts,
			run.dispatcher_fencing_token, claim.lease_expires_unix_nano
		FROM execution_runs run
		JOIN execution_work_keys work ON work.work_key = run.work_key
		JOIN execution_claims claim ON claim.run_id = run.id
		WHERE run.id = 'run_v5'
	`).Scan(
		&retryState, &failureCount, &maxAttempts,
		&dispatcherFencingToken, &leaseExpiresUnixNano,
	); err != nil {
		t.Fatal(err)
	}
	if retryState != "eligible" || failureCount != 0 || maxAttempts != 3 ||
		dispatcherFencingToken != 0 || !leaseExpiresUnixNano.Valid || leaseExpiresUnixNano.Int64 <= 0 {
		t.Fatalf(
			"v5 run after migration: retry=%s failures=%d max=%d dispatcher_fencing=%d lease_unix=%v",
			retryState, failureCount, maxAttempts, dispatcherFencingToken, leaseExpiresUnixNano,
		)
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), `
		UPDATE execution_runs
		SET status = 'committed', finished_at = '2026-01-01T00:01:00Z',
			result_hash = 'result-hash', outcome_kind = 'no_change',
			result_json = '{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}',
			finish_receipt_json = '{"run_id":"run_v5","status":"committed","project_revision":0}'
		WHERE id = 'run_v5' AND status = 'active'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(),
		"DELETE FROM execution_claims WHERE run_id = 'run_v5'",
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var status string
	var projectRevision int64
	var claimCount int
	if err := conn.QueryRowContext(context.Background(), `
		SELECT
			(SELECT status FROM execution_runs WHERE id = 'run_v5'),
			(SELECT blackboard_revision FROM projects WHERE id = 'proj_v5_run'),
			(SELECT COUNT(*) FROM execution_claims WHERE run_id = 'run_v5')
	`).Scan(&status, &projectRevision, &claimCount); err != nil {
		t.Fatal(err)
	}
	if status != "committed" || projectRevision != 0 || claimCount != 0 {
		t.Fatalf(
			"v5 run finish after migration: status=%s project_revision=%d claims=%d",
			status, projectRevision, claimCount,
		)
	}
}

func TestConfigureBackfillsVersionSixTerminalRunRetryState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v6-terminal-runs.db")
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, dbPath)
	if err := database.Migrate(context.Background(), raw, migrations[:6], 6); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES ('proj_v6_runs', 'terminal histories', 'active', 0, '2026-01-01T00:00:00Z', 1, 2);
		INSERT INTO facts (id, project_id, description) VALUES ('origin', 'proj_v6_runs', 'origin');
		INSERT INTO facts (id, project_id, description) VALUES ('goal', 'proj_v6_runs', 'goal');
		INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, last_fencing_token, created_at)
		VALUES ('reason:proj_v6_runs', 'proj_v6_runs', 'reason', 5, '2026-01-01T00:00:00Z');
		INSERT INTO execution_work_keys (work_key, project_id, task_type, last_attempt, created_at) VALUES
			('reason:proj_v6_runs:g1:r0', 'proj_v6_runs', 'reason', 1, '2026-01-01T00:00:00Z'),
			('reason:proj_v6_runs:g1:r1', 'proj_v6_runs', 'reason', 3, '2026-01-01T00:00:00Z'),
			('reason:proj_v6_runs:g1:r2', 'proj_v6_runs', 'reason', 1, '2026-01-01T00:00:00Z');
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, input_generation, input_revision, input_hash,
			worker_name, config_revision, image_id, image_repo_digest, run_profile,
			result_schema, execution_spec_json, started_at, finished_at,
			result_hash, outcome_kind, error_code, result_json, finish_receipt_json
		) VALUES
			('run_v6_committed', 'proj_v6_runs', 'reason:proj_v6_runs:g1:r0', 'reason:proj_v6_runs', 1, 'committed', 1,
			 'dispatcher-v6', 'reason', 1, 0, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'reasoner', 'config-v6', 'image', 'digest', 'restricted',
			 'aether.run-result/v1', '{"config_revision":"config-v6","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":0,"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}', '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z',
			 'result-hash', 'no_change', NULL, '{}', '{}'),
			('run_v6_failed_1', 'proj_v6_runs', 'reason:proj_v6_runs:g1:r1', 'reason:proj_v6_runs', 1, 'failed', 2,
			 'dispatcher-v6', 'reason', 1, 1, 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'reasoner', 'config-v6', 'image', 'digest', 'restricted',
			 'aether.run-result/v1', '{"config_revision":"config-v6","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":1,"hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}', '2026-01-01T00:02:00Z', '2026-01-01T00:03:00Z',
			 NULL, NULL, 'process_failed', NULL, '{}'),
			('run_v6_failed_2', 'proj_v6_runs', 'reason:proj_v6_runs:g1:r1', 'reason:proj_v6_runs', 2, 'interrupted', 3,
			 'dispatcher-v6', 'reason', 1, 1, 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'reasoner', 'config-v6', 'image', 'digest', 'restricted',
			 'aether.run-result/v1', '{"config_revision":"config-v6","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":1,"hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}', '2026-01-01T00:04:00Z', '2026-01-01T00:05:00Z',
			 NULL, NULL, 'dispatcher_restarted', NULL, '{}'),
			('run_v6_failed_3', 'proj_v6_runs', 'reason:proj_v6_runs:g1:r1', 'reason:proj_v6_runs', 3, 'failed', 4,
			 'dispatcher-v6', 'reason', 1, 1, 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'reasoner', 'config-v6', 'image', 'digest', 'restricted',
			 'aether.run-result/v1', '{"config_revision":"config-v6","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":1,"hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}', '2026-01-01T00:06:00Z', '2026-01-01T00:07:00Z',
			 NULL, NULL, 'process_failed', NULL, '{}'),
			('run_v6_backoff', 'proj_v6_runs', 'reason:proj_v6_runs:g1:r2', 'reason:proj_v6_runs', 1, 'failed', 5,
			 'dispatcher-v6', 'reason', 1, 2, 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd', 'reasoner', 'config-v6', 'image', 'digest', 'restricted',
			 'aether.run-result/v1', '{"config_revision":"config-v6","prompt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":{"generation":1,"revision":2,"hash":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"worker":{"name":"reasoner","adapter":"test"},"image":{"requested_ref":"image"},"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}', '2026-01-01T00:08:00Z', '2026-01-01T00:09:00Z',
			 NULL, NULL, 'process_failed', NULL, '{}');
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	if err := configureOfflineDataRecoveryPoint(t, db, dbPath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	tests := []struct {
		workKey      string
		wantState    string
		wantFailures int
		wantNext     bool
	}{
		{"reason:proj_v6_runs:g1:r0", "closed", 0, false},
		{"reason:proj_v6_runs:g1:r1", "exhausted", 3, true},
		{"reason:proj_v6_runs:g1:r2", "backoff", 1, true},
	}
	for _, test := range tests {
		var state string
		var failures int
		var next sql.NullString
		if err := conn.QueryRowContext(t.Context(), `
			SELECT retry_state, failure_count, next_eligible_at
			FROM execution_work_keys WHERE work_key = ?`, test.workKey,
		).Scan(&state, &failures, &next); err != nil {
			t.Fatal(err)
		}
		if state != test.wantState || failures != test.wantFailures || next.Valid != test.wantNext {
			t.Fatalf("backfill %s: state=%s failures=%d next=%v", test.workKey, state, failures, next)
		}
	}
}

func TestMigrateDatabaseRejectsChangedChecksum(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "checksum.db")
	configured := database.NewDB()
	if err := configured.Configure(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := configured.Close(); err != nil {
		t.Fatal(err)
	}

	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	migrations[0] = database.NewMigration(migrations[0].Version, migrations[0].Name, migrations[0].SQL+"\n-- rewritten")
	raw := openRawSQLite(t, dbPath)
	defer raw.Close()
	err = database.Migrate(context.Background(), raw, migrations, database.CurrentSchemaVersion)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestConfigureRejectsDatabaseNewerThanBinary(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "future.db")
	configured := database.NewDB()
	if err := configured.Configure(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := configured.Close(); err != nil {
		t.Fatal(err)
	}

	raw := openRawSQLite(t, dbPath)
	if _, err := raw.Exec(
		"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
		database.CurrentSchemaVersion+1, "future.sql", strings.Repeat("0", 64), "2030-01-01T00:00:00Z",
	); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := database.NewDB()
	err := db.Configure(dbPath)
	if err == nil || !strings.Contains(err.Error(), "newer than binary") {
		t.Fatalf("expected future schema error, got %v", err)
	}
	if _, err := db.GetConn(context.Background()); err == nil {
		t.Fatal("failed Configure must not leave database configured")
	}
}

func TestMigrationCatalogRejectsMissingFile(t *testing.T) {
	err := database.ValidateMigrationCatalog(nil, 1)
	if err == nil || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("expected missing migration file error, got %v", err)
	}
}

func TestFailedMigrationRollsBackAllSchemaChanges(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rollback.db")
	raw := openRawSQLite(t, dbPath)
	defer raw.Close()
	migrations := []database.Migration{
		database.NewMigration(1, "0001_first.sql", "CREATE TABLE migration_one (id INTEGER PRIMARY KEY);"),
		database.NewMigration(2, "0002_broken.sql", "CREATE TABLE migration_partial (id INTEGER PRIMARY KEY); SELECT * FROM table_that_does_not_exist;"),
	}

	err := database.Migrate(context.Background(), raw, migrations, 2)
	if err == nil || !strings.Contains(err.Error(), "apply migration 0002") {
		t.Fatalf("expected failed migration, got %v", err)
	}
	for _, table := range []string{"schema_migrations", "migration_one", "migration_partial"} {
		var count int
		if err := raw.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed migration left table %q", table)
		}
	}
}

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_foreign_keys=1&_txlock=immediate", path))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func createLegacyDatabaseWithProjectOrphans(t *testing.T, path string) {
	t.Helper()
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := database.Migrate(t.Context(), raw, migrations[:1], 1); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO projects (id, title, bootstrap_enabled, created_at)
		VALUES ('retained-project', 'Retained', 0, '2026-01-01T00:00:00Z');
		INSERT INTO facts (id, project_id, description) VALUES
			('origin', 'retained-project', 'retained origin'),
			('goal', 'retained-project', 'retained goal'),
			('orphan-fact', 'deleted-project', 'must be deleted');
		INSERT INTO intents (id, project_id, description, creator, created_at)
		VALUES ('orphan-intent', 'deleted-project', 'must be deleted', 'legacy', '2026-01-01T00:00:00Z');
		INSERT INTO intent_sources (intent_id, project_id, fact_id)
		VALUES ('orphan-intent', 'deleted-project', 'orphan-fact');
		INSERT INTO hints (id, project_id, content, creator, created_at)
		VALUES ('orphan-hint', 'deleted-project', 'must be deleted', 'legacy', '2026-01-01T00:00:00Z');
		INSERT INTO scoped_counters (project_id, kind, value)
		VALUES ('deleted-project', 'intent', 1);
	`); err != nil {
		t.Fatal(err)
	}
}

func createVersionOneDatabase(t *testing.T, path string) {
	t.Helper()
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := database.Migrate(t.Context(), raw, migrations[:1], 1); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO projects (id, title, bootstrap_enabled, created_at)
		VALUES ('retained-project', 'Retained', 0, '2026-01-01T00:00:00Z');
		INSERT INTO facts (id, project_id, description) VALUES
			('origin', 'retained-project', 'retained origin'),
			('goal', 'retained-project', 'retained goal');
	`); err != nil {
		t.Fatal(err)
	}
}

func createVersionTwelveDatabaseWithProjectOrphans(t *testing.T, path string) {
	t.Helper()
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(0)&_txlock=immediate", path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := database.Migrate(t.Context(), raw, migrations[:12], 12); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO projects (
			id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision
		) VALUES ('deleted-project', 'Deleted', 'completed', 0, '2026-01-01T00:00:00Z', 1, 1);
		INSERT INTO facts (id, project_id, description) VALUES
			('origin', 'deleted-project', 'origin'),
			('goal', 'deleted-project', 'goal');
		INSERT INTO intents (
			id, project_id, to_fact_id, description, creator, created_at, concluded_at, sources_locked
		) VALUES ('i001', 'deleted-project', 'goal', 'completed', 'legacy',
			'2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', 0);
		INSERT INTO intent_sources (intent_id, project_id, fact_id)
		VALUES ('i001', 'deleted-project', 'origin');
		UPDATE intents SET sources_locked = 1 WHERE project_id = 'deleted-project';
		INSERT INTO hints (id, project_id, content, creator, created_at, generation)
		VALUES ('h001', 'deleted-project', 'hint', 'legacy', '2026-01-01T00:00:00Z', 1);
		INSERT INTO scoped_counters (project_id, kind, value)
		VALUES ('deleted-project', 'intent', 1);
		INSERT INTO project_completions (
			project_id, generation, intent_id, description, actor, completed_at
		) VALUES ('deleted-project', 1, 'i001', 'completed', 'legacy', '2026-01-01T00:01:00Z');
		INSERT INTO blackboard_changes (
			project_id, revision, generation, kind, entity_type, entity_id, data, created_at
		) VALUES ('deleted-project', 1, 1, 'project_created', 'project', 'deleted-project', '{}', '2026-01-01T00:00:00Z');
		INSERT INTO execution_work_keys (
			work_key, project_id, task_type, last_attempt, created_at,
			retry_state, failure_count, max_attempts, initial_backoff_ms, max_backoff_ms,
			backoff_multiplier, decline_cooldown_ms, updated_at
		) VALUES ('reason:deleted-project:g1:r1', 'deleted-project', 'reason', 1,
			'2026-01-01T00:00:00Z', 'eligible', 0, 3, 30000, 300000, 2.0, 5000,
			'2026-01-01T00:00:00Z');
		INSERT INTO execution_claim_scopes (
			claim_scope, project_id, scope_type, last_fencing_token, created_at
		) VALUES ('reason:deleted-project', 'deleted-project', 'reason', 1, '2026-01-01T00:00:00Z');
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, input_generation, input_revision, input_hash,
			worker_name, config_revision, image_reference, result_schema,
			execution_spec_json, started_at, dispatcher_fencing_token
		) VALUES ('run-orphan', 'deleted-project', 'reason:deleted-project:g1:r1',
			'reason:deleted-project', 1, 'active', 1, 'dispatcher', 'reason', 1, 1,
			'input-hash', 'worker', 'config', 'image:latest', 'aether.run-result/v1', '{}',
			'2026-01-01T00:00:00Z', 1);
		INSERT INTO execution_claims (
			claim_scope, work_key, run_id, project_id, task_type, fencing_token,
			dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
		) VALUES ('reason:deleted-project', 'reason:deleted-project:g1:r1', 'run-orphan',
			'deleted-project', 'reason', 1, 'dispatcher', '2026-01-01T00:00:00Z',
			'2026-01-01T00:01:00Z', 1767225660000000000);
		INSERT INTO reason_checkpoints (
			project_id, generation, input_revision, run_id, checked_at
		) VALUES ('deleted-project', 1, 1, 'run-orphan', '2026-01-01T00:00:00Z');
		INSERT INTO execution_worker_declines (work_key, worker_name, retry_after, updated_at)
		VALUES ('reason:deleted-project:g1:r1', 'worker', '2026-01-01T00:01:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO execution_recovery_events (run_id, dispatcher_id, status, reason, observed_at)
		VALUES ('run-orphan', 'dispatcher', 'observed', 'legacy', '2026-01-01T00:00:00Z');
		INSERT INTO reason_scheduling_baselines (project_id, generation, input_revision, created_at)
		VALUES ('deleted-project', 1, 1, '2026-01-01T00:00:00Z');
		INSERT INTO execution_runtime_handles (
			run_id, project_id, phase, container_id, exec_id, started_at
		) VALUES ('run-orphan', 'deleted-project', 'reason', 'container', 'exec', '2026-01-01T00:00:00Z');
		DELETE FROM projects WHERE id = 'deleted-project';
	`); err != nil {
		t.Fatal(err)
	}
}

func configureOfflineDataRecoveryPoint(t *testing.T, db *database.DB, path string) error {
	t.Helper()
	writeOfflineDataRecoveryPoint(t, path)
	return db.Configure(path)
}

func offlineDataRecoveryPointPath(databasePath string) string {
	return filepath.Join(filepath.Dir(databasePath), ".aether-offline-data-recovery-point")
}

func writeOfflineDataRecoveryPoint(t *testing.T, databasePath string) string {
	t.Helper()
	backupPath := filepath.Join(filepath.Dir(databasePath), "offline-data-recovery-point-backup")
	backupDataPath := filepath.Join(backupPath, "data")
	if err := os.MkdirAll(backupDataPath, 0o700); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	backupDatabasePath := filepath.Join(backupDataPath, filepath.Base(databasePath))
	if err := os.WriteFile(backupDatabasePath, databaseBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	databaseChecksum := sha256.Sum256(databaseBytes)
	manifest := fmt.Sprintf("%s  data/%s\n", hex.EncodeToString(databaseChecksum[:]), filepath.Base(databasePath))
	if err := os.WriteFile(filepath.Join(backupPath, "SHA256SUMS"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestChecksum := sha256.Sum256([]byte(manifest))
	marker := fmt.Sprintf(
		"aether-offline-data-recovery-point/v1\nrecovery_point_path=%s\nmanifest_sha256=%s\n",
		backupPath, hex.EncodeToString(manifestChecksum[:]),
	)
	if err := os.WriteFile(offlineDataRecoveryPointPath(databasePath), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	return backupDatabasePath
}

func assertSchemaVersion(t *testing.T, databasePath string, want int) {
	t.Helper()
	inspection, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var got int
	if err := inspection.QueryRowContext(t.Context(), "SELECT MAX(version) FROM schema_migrations").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("schema version=%d, want %d", got, want)
	}
}

func assertDatabaseIntegrity(t *testing.T, conn database.DBTX) {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), "PRAGMA integrity_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			t.Fatal(err)
		}
		if result != "ok" {
			t.Fatalf("integrity check: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	violations, err := conn.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		var table, parent string
		var rowID, foreignKeyID int64
		if err := violations.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation remains: table=%s rowid=%d parent=%s fk=%d", table, rowID, parent, foreignKeyID)
	}
	if err := violations.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "subdir", "aether.db")

	db := database.NewDB()
	if err := db.Configure(dbPath); err != nil {
		t.Fatalf("configure with subdir: %v", err)
	}
	db.Close()

	if _, err := os.Stat(filepath.Join(dir, "subdir")); err != nil {
		t.Errorf("subdirectory not created: %v", err)
	}
}
