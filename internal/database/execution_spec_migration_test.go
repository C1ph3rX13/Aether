package database_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

const (
	legacyPromptDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	legacyInputDigest  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	v2PromptDigest     = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	v2InputDigest      = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestExecutionSpecMigrationPreservesLegacyJSONAndGeneratesEvidence(t *testing.T) {
	raw, migrations, legacyJSON := version23ExecutionSpecDatabase(t, "", "")
	defer raw.Close()

	if err := database.Migrate(t.Context(), raw, migrations, database.CurrentSchemaVersion); err != nil {
		t.Fatal(err)
	}

	var schema, storedJSON, inputHash, inputSchema, promptHash, promptSchema string
	var worker, revision, image, resultSchema string
	var generation int
	var inputRevision int64
	if err := raw.QueryRowContext(t.Context(), `
		SELECT execution_spec_schema, execution_spec_json,
		       input_generation, input_revision, input_hash, input_evidence_schema,
		       prompt_hash, prompt_evidence_schema, worker_name, config_revision,
		       image_reference, result_schema
		FROM execution_runs WHERE id = 'run-legacy'
	`).Scan(
		&schema, &storedJSON, &generation, &inputRevision, &inputHash, &inputSchema,
		&promptHash, &promptSchema, &worker, &revision, &image, &resultSchema,
	); err != nil {
		t.Fatal(err)
	}
	if schema != "aether.execution-spec/legacy-v1" || storedJSON != legacyJSON {
		t.Fatalf("legacy classification changed bytes: schema=%q json=%q", schema, storedJSON)
	}
	if generation != 1 || inputRevision != 3 || inputHash != legacyInputDigest ||
		inputSchema != "aether.input-hash/v1" || promptHash != legacyPromptDigest ||
		promptSchema != "aether.prompt-template-hash/v1" || worker != "reasoner" ||
		revision != "config-v1" || image != "worker:v1" || resultSchema != "aether.run-result/v1" {
		t.Fatalf(
			"generated evidence: generation=%d input_revision=%d input=%q/%q prompt=%q/%q worker=%q revision=%q image=%q result=%q",
			generation, inputRevision, inputSchema, inputHash, promptSchema, promptHash,
			worker, revision, image, resultSchema,
		)
	}

	if _, err := raw.ExecContext(t.Context(),
		"UPDATE execution_runs SET worker_name = 'forged' WHERE id = 'run-legacy'",
	); err == nil {
		t.Fatal("generated worker column accepted an application write")
	}
	if _, err := raw.ExecContext(t.Context(),
		"UPDATE execution_runs SET execution_spec_json = execution_spec_json || ' ' WHERE id = 'run-legacy'",
	); err == nil || !strings.Contains(err.Error(), "execution run spec is immutable") {
		t.Fatalf("immutable authoritative document update error=%v", err)
	}
	if _, err := raw.ExecContext(t.Context(),
		"UPDATE execution_runs SET execution_spec_schema = 'aether.execution-spec/v2' WHERE id = 'run-legacy'",
	); err == nil || !strings.Contains(err.Error(), "execution run spec is immutable") {
		t.Fatalf("immutable authoritative schema update error=%v", err)
	}
	if _, err := raw.ExecContext(t.Context(),
		"UPDATE execution_runs SET dispatcher_id = 'forged' WHERE id = 'run-legacy'",
	); err == nil || !strings.Contains(err.Error(), "execution run spec is immutable") {
		t.Fatalf("immutable Run identity update error=%v", err)
	}
	if _, err := raw.ExecContext(t.Context(),
		"UPDATE execution_runs SET id = 'run-renamed' WHERE id = 'run-legacy'",
	); err == nil || !strings.Contains(err.Error(), "execution run spec is immutable") {
		t.Fatalf("immutable Run ID update error=%v", err)
	}
}

func TestExecutionSpecMigrationRetainsHistoricallyValidUppercaseLegacyDigests(t *testing.T) {
	raw, migrations, legacyJSON := version23ExecutionSpecDatabase(t, "", "uppercase legacy")
	defer raw.Close()
	if err := database.Migrate(t.Context(), raw, migrations, database.CurrentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	var storedJSON, inputHash, promptHash string
	if err := raw.QueryRowContext(t.Context(), `
		SELECT execution_spec_json, input_hash, prompt_hash
		FROM execution_runs WHERE id = 'run-legacy'
	`).Scan(&storedJSON, &inputHash, &promptHash); err != nil {
		t.Fatal(err)
	}
	if storedJSON != legacyJSON || inputHash != strings.ToUpper(legacyInputDigest) ||
		promptHash != strings.ToUpper(legacyPromptDigest) {
		t.Fatalf("uppercase legacy evidence changed: json=%q input=%q prompt=%q", storedJSON, inputHash, promptHash)
	}
}

func TestExecutionSpecMigrationRejectsUntrustworthyLegacyRows(t *testing.T) {
	tests := []struct {
		name      string
		rowConfig string
	}{
		{name: "malformed JSON"},
		{name: "duplicate JSON field"},
		{name: "case variant JSON field"},
		{name: "uppercase canonical evidence"},
		{name: "row and document disagree", rowConfig: "forged-config"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, migrations, _ := version23ExecutionSpecDatabase(t, test.rowConfig, test.name)
			defer raw.Close()

			err := database.Migrate(t.Context(), raw, migrations, database.CurrentSchemaVersion)
			if err == nil {
				t.Fatal("migration accepted an untrustworthy legacy Execution Spec")
			}
			var version int
			if scanErr := raw.QueryRowContext(t.Context(),
				"SELECT MAX(version) FROM schema_migrations",
			).Scan(&version); scanErr != nil {
				t.Fatal(scanErr)
			}
			if version != 23 {
				t.Fatalf("failed migration committed schema version %d", version)
			}
		})
	}
}

func TestExecutionSpecV2DatabaseContractAndQueryPlan(t *testing.T) {
	raw, migrations, _ := version23ExecutionSpecDatabase(t, "", "")
	defer raw.Close()
	if err := database.Migrate(t.Context(), raw, migrations, database.CurrentSchemaVersion); err != nil {
		t.Fatal(err)
	}

	insertExecutionSpecFixture(t, raw, "v2", "run-v2", v2ExecutionSpecJSON())
	var schema, inputSchema, promptSchema string
	if err := raw.QueryRowContext(t.Context(), `
		SELECT execution_spec_schema, input_evidence_schema, prompt_evidence_schema
		FROM execution_runs WHERE id = 'run-v2'
	`).Scan(&schema, &inputSchema, &promptSchema); err != nil {
		t.Fatal(err)
	}
	if schema != "aether.execution-spec/v2" || inputSchema != "aether.input-hash/v1" ||
		promptSchema != "aether.prompt-template-hash/v1" {
		t.Fatalf("v2 generated schemas: spec=%q input=%q prompt=%q", schema, inputSchema, promptSchema)
	}

	for _, test := range []struct {
		name   string
		schema string
		spec   string
	}{
		{name: "row schema disagrees", schema: "aether.execution-spec/legacy-v1", spec: v2ExecutionSpecJSON()},
		{name: "duplicate field", schema: "aether.execution-spec/v2", spec: strings.Replace(v2ExecutionSpecJSON(), `"adapter":"mock"`, `"adapter":"mock","adapter":"forged"`, 1)},
		{name: "case variant field", schema: "aether.execution-spec/v2", spec: strings.Replace(v2ExecutionSpecJSON(), `"name":"reasoner-v2"`, `"name":"reasoner-v2","Name":"reasoner-v2"`, 1)},
		{name: "empty worker", schema: "aether.execution-spec/v2", spec: strings.Replace(v2ExecutionSpecJSON(), `"name":"reasoner-v2"`, `"name":""`, 1)},
		{name: "wrong digest type", schema: "aether.execution-spec/v2", spec: strings.Replace(v2ExecutionSpecJSON(), `"sha256":"`+v2InputDigest+`"`, `"sha256":42`, 1)},
		{name: "out of bounds timeout", schema: "aether.execution-spec/v2", spec: strings.Replace(v2ExecutionSpecJSON(), `"run_seconds":10`, `"run_seconds":999999`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := tryInsertExecutionSpecFixture(t, raw, test.schema, "run-invalid-"+strings.ReplaceAll(test.name, " ", "-"), test.spec); err == nil {
				t.Fatal("database accepted an invalid v2 Execution Spec")
			}
		})
	}

	for _, schema := range []string{"aether.execution-spec/legacy-v1", "aether.execution-spec/v2"} {
		rows, err := raw.QueryContext(t.Context(), `
			EXPLAIN QUERY PLAN
			SELECT id, input_evidence_schema, input_hash,
			       prompt_evidence_schema, prompt_hash
			FROM execution_runs
			WHERE execution_spec_schema = ? AND project_id = 'project-spec'
			ORDER BY started_at DESC, id DESC
		`, schema)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), "execution_runs_schema_project_activity_idx") {
			t.Fatalf("schema %q query plan did not use Execution Spec index: %s", schema, plan.String())
		}
	}
	rows, err := raw.QueryContext(t.Context(), `
		EXPLAIN QUERY PLAN
		SELECT id, worker_name, input_generation, input_revision
		FROM execution_runs
		WHERE project_id = 'project-spec'
		ORDER BY started_at DESC, id DESC
	`)
	if err != nil {
		t.Fatal(err)
	}
	var activityPlan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		activityPlan.WriteString(detail)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(activityPlan.String(), "execution_runs_project_activity_idx") {
		t.Fatalf("Activity query plan did not use generated evidence index: %s", activityPlan.String())
	}
}

func version23ExecutionSpecDatabase(t *testing.T, rowConfig, suffix string) (*sql.DB, []database.Migration, string) {
	t.Helper()
	migrations, err := database.LoadEmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw := openRawSQLite(t, filepath.Join(t.TempDir(), "execution-spec-"+strings.ReplaceAll(suffix, " ", "-")+".db"))
	if err := database.Migrate(t.Context(), raw, migrations[:23], 23); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	legacyJSON := legacyExecutionSpecJSON()
	rowInputHash := legacyInputDigest
	if suffix == "uppercase legacy" {
		legacyJSON = renderedLegacyExecutionSpecJSON(
			strings.ToUpper(legacyPromptDigest), strings.ToUpper(legacyInputDigest),
		)
		rowInputHash = strings.ToUpper(legacyInputDigest)
	}
	if suffix == "uppercase canonical evidence" {
		legacyJSON = strings.ReplaceAll(legacyJSON, legacyPromptDigest, strings.ToUpper(legacyPromptDigest))
		legacyJSON = strings.ReplaceAll(legacyJSON, legacyInputDigest, strings.ToUpper(legacyInputDigest))
		rowInputHash = strings.ToUpper(legacyInputDigest)
	}
	if suffix == "malformed JSON" {
		legacyJSON = "{"
	}
	if suffix == "duplicate JSON field" {
		legacyJSON = strings.Replace(legacyJSON, `"name":"reasoner"`, `"name":"reasoner","name":"forged"`, 1)
	}
	if suffix == "case variant JSON field" {
		legacyJSON = strings.Replace(legacyJSON, `"name":"reasoner"`, `"name":"reasoner","Name":"reasoner"`, 1)
	}
	if rowConfig == "" {
		rowConfig = "config-v1"
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at)
		VALUES ('project-spec', 'Execution Spec', 'active', 0, '2026-01-01T00:00:00Z');
		INSERT INTO execution_work_keys (work_key, project_id, task_type, created_at)
		VALUES ('reason:legacy', 'project-spec', 'reason', '2026-01-01T00:00:00Z');
		INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, created_at)
		VALUES ('reason:project-spec', 'project-spec', 'reason', '2026-01-01T00:00:00Z');
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, input_generation, input_revision, input_hash,
			worker_name, config_revision, image_reference, result_schema,
			execution_spec_json, started_at
		) VALUES (
			'run-legacy', 'project-spec', 'reason:legacy', 'reason:project-spec', 1,
			'active', 1, 'dispatcher', 'reason', 1, 3, ?, 'reasoner', ?,
			'worker:v1', 'aether.run-result/v1', ?, '2026-01-01T00:00:00Z'
		)
	`, rowInputHash, rowConfig, legacyJSON); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	return raw, migrations, legacyJSON
}

func insertExecutionSpecFixture(t *testing.T, db *sql.DB, suffix, runID, spec string) {
	t.Helper()
	if err := tryInsertExecutionSpecFixture(t, db, "aether.execution-spec/v2", runID, spec); err != nil {
		t.Fatal(err)
	}
}

func tryInsertExecutionSpecFixture(t *testing.T, db *sql.DB, schema, runID, spec string) error {
	t.Helper()
	suffix := strings.TrimPrefix(runID, "run-")
	_, err := db.ExecContext(t.Context(), fmt.Sprintf(`
		INSERT INTO execution_work_keys (work_key, project_id, task_type, created_at)
		VALUES ('reason:%[1]s', 'project-spec', 'reason', '2026-01-01T00:01:00Z');
		INSERT INTO execution_runs (
			id, project_id, work_key, claim_scope, attempt, status, fencing_token,
			dispatcher_id, task_type, execution_spec_schema, execution_spec_json, started_at
		) VALUES (?, 'project-spec', 'reason:%[1]s', 'reason:project-spec', 1,
			'active', (SELECT COALESCE(MAX(fencing_token), 0) + 1 FROM execution_runs WHERE claim_scope = 'reason:project-spec'),
			'dispatcher', 'reason', ?, ?, '2026-01-01T00:01:00Z')
	`, suffix), runID, schema, spec)
	return err
}

func legacyExecutionSpecJSON() string {
	return `{"config_revision":"config-v1","prompt_template_hash":"` + legacyPromptDigest +
		`","input":{"generation":1,"revision":3,"identity":{"schema":"aether.input-hash/v1","sha256":"` + legacyInputDigest +
		`"}},"worker":{"name":"reasoner","adapter":"mock"},"image":{"requested_ref":"worker:v1"},` +
		`"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}`
}

func renderedLegacyExecutionSpecJSON(promptHash, inputHash string) string {
	return `{"config_revision":"config-v1","prompt_hash":"` + promptHash +
		`","input":{"generation":1,"revision":3,"hash":"` + inputHash +
		`"},"worker":{"name":"reasoner","adapter":"mock"},"image":{"requested_ref":"worker:v1"},` +
		`"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1"}`
}

func v2ExecutionSpecJSON() string {
	return `{"schema":"aether.execution-spec/v2","config_revision":"config-v2",` +
		`"prompt":{"schema":"aether.prompt-template-hash/v1","sha256":"` + v2PromptDigest + `"},` +
		`"input":{"generation":1,"revision":4,"identity":{"schema":"aether.input-hash/v1","sha256":"` + v2InputDigest + `"}},` +
		`"worker":{"name":"reasoner-v2","adapter":"mock"},"image":{"requested_ref":"worker:v2"},` +
		`"timeouts":{"run_seconds":10,"phase_seconds":5,"grace_seconds":1},"result_schema":"aether.run-result/v1","max_proposed_intents":8}`
}
