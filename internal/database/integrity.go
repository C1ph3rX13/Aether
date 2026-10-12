package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	legacyProjectOrphanRepairVersion = 1
	legacyProjectOrphanRepairKind    = "legacy_missing_project_children"
)

type foreignKeyViolation struct {
	table        string
	rowID        sql.NullInt64
	parent       string
	foreignKeyID int64
}

type orphanRepairPlan struct {
	projectIDs []string
}

type orphanRepairResult struct {
	schemaVersion int
	deletedCounts map[string]int64
	repairedAt    string
}

type repairDeletion struct {
	table     string
	predicate string
}

var projectOrphanDeletionOrder = []repairDeletion{
	{table: "execution_worker_declines", predicate: "work_key IN (SELECT work_key FROM execution_work_keys WHERE project_id = ?)"},
	{table: "execution_recovery_events", predicate: "run_id IN (SELECT id FROM execution_runs WHERE project_id = ?)"},
	{table: "execution_runtime_handles", predicate: "project_id = ?"},
	{table: "reason_checkpoints", predicate: "project_id = ?"},
	{table: "execution_claims", predicate: "project_id = ?"},
	{table: "execution_runs", predicate: "project_id = ?"},
	{table: "execution_claim_scopes", predicate: "project_id = ?"},
	{table: "execution_work_keys", predicate: "project_id = ?"},
	{table: "project_completions", predicate: "project_id = ?"},
	{table: "intent_sources", predicate: "project_id = ?"},
	{table: "reason_scheduling_baselines", predicate: "project_id = ?"},
	{table: "blackboard_changes", predicate: "project_id = ?"},
	{table: "project_history_snapshots", predicate: "project_id = ?"},
	{table: "project_runtime_containers", predicate: "project_id = ?"},
	{table: "hints", predicate: "project_id = ?"},
	{table: "intents", predicate: "project_id = ?"},
	{table: "facts", predicate: "project_id = ?"},
	{table: "scoped_counters", predicate: "project_id = ?"},
}

var projectOwnedCascadeTables = []string{
	"facts",
	"intents",
	"hints",
	"scoped_counters",
	"project_completions",
	"blackboard_changes",
	"project_history_snapshots",
	"execution_work_keys",
	"execution_claim_scopes",
	"execution_runs",
	"execution_claims",
	"reason_checkpoints",
	"reason_scheduling_baselines",
	"execution_runtime_handles",
	"project_runtime_containers",
}

var repairableProjectTables = func() map[string]struct{} {
	tables := make(map[string]struct{}, len(projectOwnedCascadeTables))
	for _, table := range projectOwnedCascadeTables {
		tables[table] = struct{}{}
	}
	return tables
}()

func migrateWithStartupIntegrity(
	ctx context.Context,
	db *sql.DB,
	migrations []Migration,
	supportedVersion int,
	recoveryPoint *verifiedOfflineDataRecoveryPoint,
) (err error) {
	if err := ValidateMigrationCatalog(migrations, supportedVersion); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin startup transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = verifyKnownMigrationMetadata(ctx, tx, migrations, supportedVersion); err != nil {
		return err
	}
	pendingMigrations, err := existingDatabaseNeedsMigration(ctx, tx, supportedVersion)
	if err != nil {
		return err
	}
	if pendingMigrations && recoveryPoint == nil {
		return fmt.Errorf("offline data recovery point is required before migrating an existing database")
	}
	if err = checkDatabaseIntegrity(ctx, tx); err != nil {
		return fmt.Errorf("pre-migration integrity check: %w", err)
	}
	violations, err := readForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	var repairResult *orphanRepairResult
	if len(violations) > 0 {
		plan, classifyErr := classifyProjectOrphans(ctx, tx, violations)
		if classifyErr != nil {
			return classifyErr
		}
		if recoveryPoint == nil {
			return fmt.Errorf("offline data recovery point is required to repair legacy Project orphans")
		}
		result, repairErr := repairProjectOrphans(ctx, tx, plan)
		if repairErr != nil {
			err = repairErr
			return err
		}
		repairResult = &result
		if err = checkDatabaseIntegrity(ctx, tx); err != nil {
			return fmt.Errorf("post-repair integrity check: %w", err)
		}
		if err = requireNoForeignKeyViolations(ctx, tx); err != nil {
			return fmt.Errorf("post-repair foreign key check: %w", err)
		}
	}

	if err = applyMigrations(ctx, tx, migrations, supportedVersion); err != nil {
		return err
	}
	if repairResult != nil {
		if err = finalizeProjectCleanupEvidence(ctx, tx); err != nil {
			return err
		}
		if err = recordDatabaseRepair(ctx, tx, *repairResult); err != nil {
			return err
		}
	}
	if err = verifyDatabaseConstraints(ctx, tx); err != nil {
		return fmt.Errorf("verify database constraints: %w", err)
	}
	if err = checkDatabaseIntegrity(ctx, tx); err != nil {
		return fmt.Errorf("post-migration integrity check: %w", err)
	}
	if err = requireNoForeignKeyViolations(ctx, tx); err != nil {
		return fmt.Errorf("post-migration foreign key check: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit startup transaction: %w", err)
	}
	return nil
}

func existingDatabaseNeedsMigration(ctx context.Context, tx *sql.Tx, supportedVersion int) (bool, error) {
	projectsExist, err := tableExists(ctx, tx, "projects")
	if err != nil || !projectsExist {
		return false, err
	}
	version, err := appliedSchemaVersion(ctx, tx)
	if err != nil {
		return false, err
	}
	return version < supportedVersion, nil
}

func verifyKnownMigrationMetadata(ctx context.Context, tx *sql.Tx, migrations []Migration, supportedVersion int) error {
	exists, err := tableExists(ctx, tx, "schema_migrations")
	if err != nil || !exists {
		return err
	}
	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	return verifyAppliedMigrations(applied, migrations, supportedVersion)
}

func checkDatabaseIntegrity(ctx context.Context, db DBTX) error {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	var failures []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			failures = append(failures, result)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("SQLite integrity check failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func readForeignKeyViolations(ctx context.Context, db DBTX) ([]foreignKeyViolation, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, fmt.Errorf("read foreign key violations: %w", err)
	}
	defer rows.Close()
	var violations []foreignKeyViolation
	for rows.Next() {
		var violation foreignKeyViolation
		if err := rows.Scan(&violation.table, &violation.rowID, &violation.parent, &violation.foreignKeyID); err != nil {
			return nil, fmt.Errorf("scan foreign key violation: %w", err)
		}
		violations = append(violations, violation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return violations, nil
}

func requireNoForeignKeyViolations(ctx context.Context, db DBTX) error {
	violations, err := readForeignKeyViolations(ctx, db)
	if err != nil {
		return err
	}
	if len(violations) == 0 {
		return nil
	}
	first := violations[0]
	return fmt.Errorf(
		"%d violation(s) remain; first table=%s parent=%s foreign_key=%d",
		len(violations), first.table, first.parent, first.foreignKeyID,
	)
}

func verifyDatabaseIntegrity(ctx context.Context, db DBTX) error {
	if err := checkDatabaseIntegrity(ctx, db); err != nil {
		return err
	}
	return requireNoForeignKeyViolations(ctx, db)
}

func classifyProjectOrphans(ctx context.Context, tx *sql.Tx, violations []foreignKeyViolation) (orphanRepairPlan, error) {
	projectIDs := make(map[string]struct{})
	for _, violation := range violations {
		if _, ok := repairableProjectTables[violation.table]; !ok || !violation.rowID.Valid || violation.parent != "projects" {
			return orphanRepairPlan{}, unknownForeignKeyViolation(violation)
		}
		var projectID string
		query := fmt.Sprintf("SELECT project_id FROM %s WHERE rowid = ?", violation.table)
		if err := tx.QueryRowContext(ctx, query, violation.rowID.Int64).Scan(&projectID); err != nil {
			return orphanRepairPlan{}, fmt.Errorf("classify foreign key violation in %s: %w", violation.table, err)
		}
		var exists int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", projectID).Scan(&exists)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return orphanRepairPlan{}, err
			}
			return orphanRepairPlan{}, unknownForeignKeyViolation(violation)
		}
		projectIDs[projectID] = struct{}{}
	}
	plan := orphanRepairPlan{projectIDs: make([]string, 0, len(projectIDs))}
	for projectID := range projectIDs {
		plan.projectIDs = append(plan.projectIDs, projectID)
	}
	sort.Strings(plan.projectIDs)
	return plan, nil
}

func unknownForeignKeyViolation(violation foreignKeyViolation) error {
	return fmt.Errorf(
		"unknown foreign key violation: table=%s parent=%s foreign_key=%d",
		violation.table, violation.parent, violation.foreignKeyID,
	)
}

func repairProjectOrphans(ctx context.Context, tx *sql.Tx, plan orphanRepairPlan) (orphanRepairResult, error) {
	schemaVersion, err := appliedSchemaVersion(ctx, tx)
	if err != nil {
		return orphanRepairResult{}, err
	}
	runtimeTables, err := prepareProjectCleanupEvidence(ctx, tx)
	if err != nil {
		return orphanRepairResult{}, err
	}

	counts := make(map[string]int64, len(projectOrphanDeletionOrder))
	for _, deletion := range projectOrphanDeletionOrder {
		counts[deletion.table] = 0
	}
	repairedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, projectID := range plan.projectIDs {
		if err := captureProjectCleanupEvidence(ctx, tx, runtimeTables, projectID, repairedAt); err != nil {
			return orphanRepairResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, title, status, bootstrap_enabled, created_at)
			VALUES (?, 'legacy repair placeholder', 'active', 0, ?)
		`, projectID, repairedAt); err != nil {
			return orphanRepairResult{}, fmt.Errorf("create repair placeholder Project %s: %w", projectID, err)
		}
		hasDeleting, err := columnExists(ctx, tx, "projects", "deleting")
		if err != nil {
			return orphanRepairResult{}, err
		}
		if hasDeleting {
			if _, err := tx.ExecContext(ctx, "UPDATE projects SET deleting = 1 WHERE id = ?", projectID); err != nil {
				return orphanRepairResult{}, fmt.Errorf("authorize Project orphan cleanup %s: %w", projectID, err)
			}
		}
		for _, deletion := range projectOrphanDeletionOrder {
			exists, err := tableExists(ctx, tx, deletion.table)
			if err != nil {
				return orphanRepairResult{}, err
			}
			if !exists {
				continue
			}
			var expected int64
			countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", deletion.table, deletion.predicate)
			if err := tx.QueryRowContext(ctx, countQuery, projectID).Scan(&expected); err != nil {
				return orphanRepairResult{}, fmt.Errorf("count known Project orphans in %s: %w", deletion.table, err)
			}
			deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE %s", deletion.table, deletion.predicate)
			result, err := tx.ExecContext(ctx, deleteQuery, projectID)
			if err != nil {
				return orphanRepairResult{}, fmt.Errorf("delete known Project orphans from %s: %w", deletion.table, err)
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return orphanRepairResult{}, err
			}
			if deleted != expected {
				return orphanRepairResult{}, fmt.Errorf(
					"repair count mismatch for %s: expected %d deleted %d",
					deletion.table, expected, deleted,
				)
			}
			counts[deletion.table] += deleted
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", projectID); err != nil {
			return orphanRepairResult{}, fmt.Errorf("remove repair placeholder Project %s: %w", projectID, err)
		}
	}
	return orphanRepairResult{
		schemaVersion: schemaVersion,
		deletedCounts: counts,
		repairedAt:    repairedAt,
	}, nil
}

type runtimeEvidenceTables struct {
	handles    bool
	containers bool
}

func prepareProjectCleanupEvidence(ctx context.Context, tx *sql.Tx) (runtimeEvidenceTables, error) {
	handles, err := tableExists(ctx, tx, "execution_runtime_handles")
	if err != nil {
		return runtimeEvidenceTables{}, err
	}
	containers, err := tableExists(ctx, tx, "project_runtime_containers")
	if err != nil {
		return runtimeEvidenceTables{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TEMP TABLE startup_orphan_cleanup_projects (
			project_id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL UNIQUE,
			container_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TEMP TABLE startup_orphan_cleanup_handles (
			project_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			phase TEXT NOT NULL,
			container_id TEXT NOT NULL,
			exec_id TEXT NOT NULL,
			PRIMARY KEY (project_id, run_id, phase, exec_id)
		);
	`); err != nil {
		return runtimeEvidenceTables{}, fmt.Errorf("prepare runtime cleanup evidence: %w", err)
	}
	return runtimeEvidenceTables{handles: handles, containers: containers}, nil
}

func captureProjectCleanupEvidence(
	ctx context.Context,
	tx *sql.Tx,
	tables runtimeEvidenceTables,
	projectID string,
	createdAt string,
) error {
	var containerID sql.NullString
	if tables.containers {
		err := tx.QueryRowContext(ctx,
			"SELECT container_id FROM project_runtime_containers WHERE project_id = ?", projectID,
		).Scan(&containerID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read orphan Project container: %w", err)
		}
	}
	if !containerID.Valid && tables.handles {
		err := tx.QueryRowContext(ctx, `
			SELECT container_id FROM execution_runtime_handles
			WHERE project_id = ? ORDER BY started_at DESC, exec_id DESC LIMIT 1
		`, projectID).Scan(&containerID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read orphan runtime container: %w", err)
		}
	}
	if !containerID.Valid {
		return nil
	}
	jobID := "cleanup-legacy-orphan-v1:" + projectID
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO startup_orphan_cleanup_projects (project_id, job_id, container_id, created_at)
		VALUES (?, ?, ?, ?)
	`, projectID, jobID, containerID.String, createdAt); err != nil {
		return fmt.Errorf("preserve orphan Project cleanup identity: %w", err)
	}
	if tables.handles {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO startup_orphan_cleanup_handles (
				project_id, run_id, phase, container_id, exec_id
			)
			SELECT project_id, run_id, phase, container_id, exec_id
			FROM execution_runtime_handles
			WHERE project_id = ? AND status = 'active'
		`, projectID); err != nil {
			return fmt.Errorf("preserve orphan runtime handles: %w", err)
		}
	}
	return nil
}

func finalizeProjectCleanupEvidence(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE startup_orphan_cleanup_projects
		SET job_id = COALESCE(
			(SELECT id FROM project_cleanup_jobs
			 WHERE project_id = startup_orphan_cleanup_projects.project_id
			 ORDER BY created_at, id LIMIT 1),
			job_id
		);

		INSERT INTO project_cleanup_jobs (
			id, project_id, container_id, created_at, updated_at
		)
		SELECT job_id, project_id, container_id, created_at, created_at
		FROM startup_orphan_cleanup_projects
		WHERE NOT EXISTS (
			SELECT 1 FROM project_cleanup_jobs
			WHERE id = startup_orphan_cleanup_projects.job_id
		);

		UPDATE project_cleanup_jobs
		SET container_id = COALESCE(
			container_id,
			(SELECT container_id FROM startup_orphan_cleanup_projects
			 WHERE job_id = project_cleanup_jobs.id)
		)
		WHERE id IN (SELECT job_id FROM startup_orphan_cleanup_projects);

		INSERT OR IGNORE INTO project_cleanup_handles (
			job_id, run_id, phase, container_id, exec_id
		)
		SELECT projects.job_id, handles.run_id, handles.phase,
		       handles.container_id, handles.exec_id
		FROM startup_orphan_cleanup_handles AS handles
		JOIN startup_orphan_cleanup_projects AS projects
		  ON projects.project_id = handles.project_id;

		DROP TABLE startup_orphan_cleanup_handles;
		DROP TABLE startup_orphan_cleanup_projects;
	`); err != nil {
		return fmt.Errorf("finalize orphan Project cleanup evidence: %w", err)
	}
	return nil
}

func recordDatabaseRepair(ctx context.Context, tx *sql.Tx, result orphanRepairResult) error {
	encodedCounts, err := json.Marshal(result.deletedCounts)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO database_repairs (
			repair_version, repair_kind, schema_version_before, deleted_counts_json, repaired_at
		) VALUES (?, ?, ?, ?, ?)
	`, legacyProjectOrphanRepairVersion, legacyProjectOrphanRepairKind, result.schemaVersion,
		string(encodedCounts), result.repairedAt); err != nil {
		return fmt.Errorf("record database repair: %w", err)
	}
	return nil
}

func verifyDatabaseConstraints(ctx context.Context, tx *sql.Tx) error {
	for _, table := range projectOwnedCascadeTables {
		if err := requireCascadeConstraint(ctx, tx, table, "projects", "project_id"); err != nil {
			return err
		}
	}
	for _, constraint := range []struct {
		table  string
		parent string
		column string
	}{
		{table: "intent_sources", parent: "intents", column: "project_id"},
		{table: "intent_sources", parent: "facts", column: "project_id"},
		{table: "execution_worker_declines", parent: "execution_work_keys", column: "work_key"},
		{table: "execution_recovery_events", parent: "execution_runs", column: "run_id"},
		{table: "project_cleanup_handles", parent: "project_cleanup_jobs", column: "job_id"},
	} {
		if err := requireCascadeConstraint(ctx, tx, constraint.table, constraint.parent, constraint.column); err != nil {
			return err
		}
	}
	for _, constraint := range []struct {
		table    string
		parent   string
		onDelete string
		columns  []foreignKeyColumnMapping
	}{
		{table: "intent_sources", parent: "intents", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"intent_id", "id"}}},
		{table: "intent_sources", parent: "facts", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"fact_id", "id"}}},
		{table: "project_completions", parent: "intents", onDelete: "RESTRICT", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"intent_id", "id"}}},
		{table: "execution_work_keys", parent: "intents", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_claim_scopes", parent: "intents", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_runs", parent: "intents", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_claims", parent: "intents", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"project_id", "project_id"}, {"target_intent_id", "id"}}},
		{table: "execution_claims", parent: "execution_runs", onDelete: "CASCADE", columns: []foreignKeyColumnMapping{{"run_id", "id"}, {"work_key", "work_key"}, {"claim_scope", "claim_scope"}, {"fencing_token", "fencing_token"}}},
	} {
		if err := requireForeignKeyConstraint(
			ctx, tx, constraint.table, constraint.parent, constraint.onDelete, constraint.columns,
		); err != nil {
			return err
		}
	}
	for _, constraint := range []struct {
		table   string
		columns []string
	}{
		{table: "facts", columns: []string{"project_id", "id"}},
		{table: "intents", columns: []string{"project_id", "id"}},
		{table: "hints", columns: []string{"project_id", "id"}},
		{table: "intent_sources", columns: []string{"project_id", "intent_id", "fact_id"}},
		{table: "project_completions", columns: []string{"project_id", "generation"}},
		{table: "project_completions", columns: []string{"project_id", "intent_id"}},
		{table: "execution_runs", columns: []string{"work_key", "attempt"}},
		{table: "execution_runs", columns: []string{"claim_scope", "fencing_token"}},
		{table: "execution_runs", columns: []string{"id", "work_key", "claim_scope", "fencing_token"}},
		{table: "execution_claims", columns: []string{"work_key"}},
		{table: "execution_claims", columns: []string{"run_id"}},
		{table: "reason_checkpoints", columns: []string{"run_id"}},
	} {
		if err := requireUniqueConstraint(ctx, tx, constraint.table, constraint.columns); err != nil {
			return err
		}
	}
	for _, trigger := range []string{
		"facts_delete_only_with_project",
		"intents_delete_only_with_project",
		"hints_delete_only_with_project",
		"intent_sources_delete_immutable",
		"database_repairs_update_immutable",
		"database_repairs_delete_immutable",
	} {
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?", trigger,
		).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return fmt.Errorf("database delete constraint trigger %s is missing", trigger)
		}
	}
	for _, trigger := range []string{
		"facts_description_immutable",
		"intents_content_immutable",
		"intents_kind_immutable",
		"intent_sources_immutable",
		"intent_sources_insert_immutable",
		"intent_sources_lock_one_way",
		"hints_content_immutable",
		"execution_work_identity_immutable",
		"execution_claim_scope_identity_immutable",
		"execution_run_spec_immutable",
		"execution_run_terminal_once",
		"execution_run_finish_fields_atomic",
		"execution_run_finish_receipt_required",
		"execution_run_result_required",
		"execution_claim_identity_immutable",
		"execution_claim_lease_text_sync",
		"blackboard_changes_delta_immutable",
		"blackboard_changes_delta_delete_immutable",
		"project_history_snapshots_immutable",
		"project_history_snapshots_delete_immutable",
	} {
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?", trigger,
		).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return fmt.Errorf("required database trigger %s is missing", trigger)
		}
	}
	return verifyProjectDeleteConstraintBehavior(ctx, tx)
}

type foreignKeyColumnMapping struct {
	from string
	to   string
}

func requireForeignKeyConstraint(
	ctx context.Context,
	tx *sql.Tx,
	table, parent, onDelete string,
	want []foreignKeyColumnMapping,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, seq, "table", "from", "to", on_update, on_delete, match
		FROM pragma_foreign_key_list(?)
		ORDER BY id, seq
	`, table)
	if err != nil {
		return err
	}
	type foreignKey struct {
		parent   string
		onDelete string
		columns  []foreignKeyColumnMapping
	}
	constraints := make(map[int]*foreignKey)
	for rows.Next() {
		var id, sequence int
		var gotParent, from, to, onUpdate, gotOnDelete, match string
		if err := rows.Scan(&id, &sequence, &gotParent, &from, &to, &onUpdate, &gotOnDelete, &match); err != nil {
			rows.Close()
			return err
		}
		constraint := constraints[id]
		if constraint == nil {
			constraint = &foreignKey{parent: gotParent, onDelete: gotOnDelete}
			constraints[id] = constraint
		}
		constraint.columns = append(constraint.columns, foreignKeyColumnMapping{from: from, to: to})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, constraint := range constraints {
		if constraint.parent == parent && constraint.onDelete == onDelete &&
			reflect.DeepEqual(constraint.columns, want) {
			return nil
		}
	}
	return fmt.Errorf(
		"required foreign key is missing: %s%v -> %s ON DELETE %s",
		table, want, parent, onDelete,
	)
}

func requireUniqueConstraint(ctx context.Context, tx *sql.Tx, table string, want []string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT name FROM pragma_index_list(?)
		WHERE "unique" = 1 AND partial = 0
		ORDER BY seq
	`, table)
	if err != nil {
		return err
	}
	var indexNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		indexNames = append(indexNames, name)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, indexName := range indexNames {
		indexRows, err := tx.QueryContext(ctx, `
			SELECT name FROM pragma_index_info(?) ORDER BY seqno
		`, indexName)
		if err != nil {
			return err
		}
		var columns []string
		for indexRows.Next() {
			var column string
			if err := indexRows.Scan(&column); err != nil {
				indexRows.Close()
				return err
			}
			columns = append(columns, column)
		}
		if err := indexRows.Close(); err != nil {
			return err
		}
		if reflect.DeepEqual(columns, want) {
			return nil
		}
	}
	return fmt.Errorf("required unique constraint is missing: %s%v", table, want)
}

func requireCascadeConstraint(ctx context.Context, tx *sql.Tx, table, parent, column string) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_list("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, sequence int
		var gotParent, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &gotParent, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return err
		}
		if gotParent == parent && from == column && onDelete == "CASCADE" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("required ON DELETE CASCADE is missing: %s.%s -> %s", table, column, parent)
	}
	return nil
}

func verifyProjectDeleteConstraintBehavior(ctx context.Context, tx *sql.Tx) error {
	const savepoint = "aether_startup_constraint_probe"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return err
	}
	rollback := func() error {
		_, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO "+savepoint)
		_, releaseErr := tx.ExecContext(ctx, "RELEASE "+savepoint)
		return errors.Join(rollbackErr, releaseErr)
	}
	var projectID string
	if err := tx.QueryRowContext(ctx,
		"SELECT '__aether_constraint_probe__' || lower(hex(randomblob(16)))",
	).Scan(&projectID); err != nil {
		return errors.Join(err, rollback())
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	probeSetup := []struct {
		name      string
		statement string
		args      []any
	}{
		{name: "Project", statement: `
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at)
		VALUES (?, 'constraint probe', 'active', 0, ?)
	`, args: []any{projectID, now}},
		{name: "Facts", statement: `
		INSERT INTO facts (project_id, id, description) VALUES
			(?, 'origin', 'constraint probe origin'),
			(?, 'goal', 'constraint probe goal')
	`, args: []any{projectID, projectID}},
		{name: "Intent", statement: `
		INSERT INTO intents (
			project_id, id, kind, description, creator, created_at, sources_locked
		) VALUES (?, 'probe', 'explore', 'constraint probe intent', 'startup', ?, 0)
	`, args: []any{projectID, now}},
		{name: "Intent Source", statement: `
		INSERT INTO intent_sources (project_id, intent_id, fact_id)
		VALUES (?, 'probe', 'origin')
	`, args: []any{projectID}},
		{name: "Intent Source Lock", statement: `
		UPDATE intents SET sources_locked = 1 WHERE project_id = ? AND id = 'probe'
	`, args: []any{projectID}},
		{name: "Hint", statement: `
		INSERT INTO hints (project_id, id, content, creator, created_at)
		VALUES (?, 'probe', 'constraint probe hint', 'startup', ?)
	`, args: []any{projectID, now}},
	}
	for _, setup := range probeSetup {
		if _, err := tx.ExecContext(ctx, setup.statement, setup.args...); err != nil {
			return errors.Join(fmt.Errorf("create constraint probe %s: %w", setup.name, err), rollback())
		}
	}
	rejections := []struct {
		name      string
		statement string
		args      []any
	}{
		{name: "fact content immutability", statement: "UPDATE facts SET description = 'changed' WHERE project_id = ? AND id = 'origin'", args: []any{projectID}},
		{name: "intent content immutability", statement: "UPDATE intents SET description = 'changed' WHERE project_id = ? AND id = 'probe'", args: []any{projectID}},
		{name: "intent kind immutability", statement: "UPDATE intents SET kind = 'completion' WHERE project_id = ? AND id = 'probe'", args: []any{projectID}},
		{name: "intent source lock", statement: "UPDATE intents SET sources_locked = 0 WHERE project_id = ? AND id = 'probe'", args: []any{projectID}},
		{name: "intent source update", statement: "UPDATE intent_sources SET fact_id = 'goal' WHERE project_id = ? AND intent_id = 'probe'", args: []any{projectID}},
		{name: "intent source insert after lock", statement: "INSERT INTO intent_sources (project_id, intent_id, fact_id) VALUES (?, 'probe', 'goal')", args: []any{projectID}},
		{name: "intent source deletion", statement: "DELETE FROM intent_sources WHERE project_id = ? AND intent_id = 'probe'", args: []any{projectID}},
		{name: "hint content immutability", statement: "UPDATE hints SET content = 'changed' WHERE project_id = ? AND id = 'probe'", args: []any{projectID}},
		{name: "direct Fact deletion", statement: "DELETE FROM facts WHERE project_id = ? AND id = 'origin'", args: []any{projectID}},
		{name: "direct Intent deletion", statement: "DELETE FROM intents WHERE project_id = ? AND id = 'probe'", args: []any{projectID}},
		{name: "direct Hint deletion", statement: "DELETE FROM hints WHERE project_id = ? AND id = 'probe'", args: []any{projectID}},
	}
	for _, rejection := range rejections {
		if _, err := tx.ExecContext(ctx, rejection.statement, rejection.args...); err == nil {
			return errors.Join(
				fmt.Errorf("database constraint allowed %s", rejection.name), rollback(),
			)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET deleting = 1 WHERE id = ?;
		DELETE FROM projects WHERE id = ?;
	`, projectID, projectID); err != nil {
		return errors.Join(fmt.Errorf("Project delete cascade probe: %w", err), rollback())
	}
	var projectCount, factCount, intentCount, sourceCount, hintCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM projects WHERE id = ?),
			(SELECT COUNT(*) FROM facts WHERE project_id = ?),
			(SELECT COUNT(*) FROM intents WHERE project_id = ?),
			(SELECT COUNT(*) FROM intent_sources WHERE project_id = ?),
			(SELECT COUNT(*) FROM hints WHERE project_id = ?)
	`, projectID, projectID, projectID, projectID, projectID).Scan(
		&projectCount, &factCount, &intentCount, &sourceCount, &hintCount,
	); err != nil {
		return errors.Join(err, rollback())
	}
	if err := rollback(); err != nil {
		return fmt.Errorf("roll back Project delete constraint probe: %w", err)
	}
	if projectCount != 0 || factCount != 0 || intentCount != 0 || sourceCount != 0 || hintCount != 0 {
		return fmt.Errorf(
			"Project delete cascade left projects=%d facts=%d intents=%d sources=%d hints=%d",
			projectCount, factCount, intentCount, sourceCount, hintCount,
		)
	}
	return nil
}

func appliedSchemaVersion(ctx context.Context, db DBTX) (int, error) {
	exists, err := tableExists(ctx, db, "schema_migrations")
	if err != nil || !exists {
		return 0, err
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func tableExists(ctx context.Context, db DBTX, table string) (bool, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table,
	).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func columnExists(ctx context.Context, db DBTX, table, column string) (bool, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
	).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}
