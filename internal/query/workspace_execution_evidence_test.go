package query

import "testing"

func TestGetWorkspaceExecutionEvidenceProjectsActiveReasonAndLatestSelectedIntentRun(t *testing.T) {
	db := configuredTestDB(t)
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
VALUES ('proj-evidence', 'Workspace evidence', 'active', 0, '2026-07-01T00:00:00Z', 2, 17);

INSERT INTO intents (id, project_id, to_fact_id, description, creator, created_at)
VALUES ('i-selected', 'proj-evidence', NULL, 'Selected direction', 'reason.worker', '2026-07-02T00:00:00Z');

INSERT INTO execution_work_keys
  (work_key, project_id, task_type, target_intent_id, last_attempt, created_at, retry_state)
VALUES
  ('reason:proj-evidence:g2:r17', 'proj-evidence', 'reason', NULL, 1, '2026-07-03T00:00:00Z', 'eligible'),
  ('intent:proj-evidence:i-selected:explore', 'proj-evidence', 'explore', 'i-selected', 2, '2026-07-03T00:00:00Z', 'eligible');

INSERT INTO execution_claim_scopes (claim_scope, project_id, scope_type, target_intent_id, created_at)
VALUES
  ('reason:proj-evidence', 'proj-evidence', 'reason', NULL, '2026-07-03T00:00:00Z'),
  ('intent:proj-evidence:i-selected', 'proj-evidence', 'intent', 'i-selected', '2026-07-03T00:00:00Z');

WITH run_seed(
  id, target_intent_id, work_key, claim_scope, attempt, status, fencing_token,
  task_type, worker_name, started_at, finished_at, result_hash, outcome_kind, error_code,
  capture_failure_reason, capture_diagnostic, result_evidence_source, result_json, finish_receipt_json
) AS (VALUES
  ('run-reason', NULL, 'reason:proj-evidence:g2:r17', 'reason:proj-evidence', 1, 'active', 1,
   'reason', 'reason.worker', '2026-07-03T10:00:00Z', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
  ('run-explore-1', 'i-selected', 'intent:proj-evidence:i-selected:explore', 'intent:proj-evidence:i-selected', 1, 'failed', 1,
   'explore', 'explore.worker', '2026-07-03T10:01:00Z', '2026-07-03T10:02:00Z', NULL, NULL, 'process_failed', NULL, NULL, NULL, NULL, NULL),
  ('run-explore-2', 'i-selected', 'intent:proj-evidence:i-selected:explore', 'intent:proj-evidence:i-selected', 2, 'committed', 2,
   'explore', 'explore.worker', '2026-07-03T10:03:00Z', '2026-07-03T10:04:00Z', 'result-hash', 'no_change', NULL, NULL, NULL, 'file', '{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}', '{}')
)
INSERT INTO execution_runs (
  id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
  fencing_token, dispatcher_id, task_type, execution_spec_schema,
  execution_spec_json, started_at, finished_at, result_hash, outcome_kind, error_code,
  capture_failure_reason, capture_diagnostic, result_evidence_source, result_json, finish_receipt_json
)
SELECT id, 'proj-evidence', target_intent_id, work_key, claim_scope, attempt,
       status, fencing_token, 'dispatcher', task_type, 'aether.execution-spec/v2',
       json_object(
         'schema', 'aether.execution-spec/v2', 'config_revision', 'cfg',
         'prompt', json_object(
           'schema', 'aether.prompt-template-hash/v1',
           'sha256', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
         ),
         'input', CASE WHEN target_intent_id IS NULL THEN json_object(
           'generation', 2, 'revision', 17,
           'identity', json_object(
             'schema', 'aether.input-hash/v1',
             'sha256', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
           )
         ) ELSE json_object(
           'generation', 2, 'revision', 17,
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
         'result_schema', 'aether.run-result/v1',
         'max_proposed_intents', CASE WHEN task_type = 'reason' THEN 8 ELSE 0 END
       ),
       started_at, finished_at, result_hash, outcome_kind, error_code, capture_failure_reason, capture_diagnostic,
       result_evidence_source, result_json, finish_receipt_json
FROM run_seed;
`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	selectedIntentID := "i-selected"
	evidence, err := newQueryService(db).GetWorkspaceExecutionEvidence(t.Context(), "proj-evidence", &selectedIntentID)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ProjectID != "proj-evidence" || evidence.ActiveReason == nil || evidence.SelectedIntentRun == nil {
		t.Fatalf("Workspace Execution Evidence: %+v", evidence)
	}
	if evidence.ActiveReason.RunID != "run-reason" || evidence.ActiveReason.Status != "active" ||
		evidence.ActiveReason.Worker != "reason.worker" ||
		evidence.ActiveReason.StartedAt != "2026-07-03T10:00:00Z" {
		t.Fatalf("Active Reason evidence: %+v", evidence.ActiveReason)
	}
	if evidence.SelectedIntentRun.RunID != "run-explore-2" || evidence.SelectedIntentRun.Task != "explore" ||
		evidence.SelectedIntentRun.Status != "committed" || evidence.SelectedIntentRun.Attempt != 2 ||
		evidence.SelectedIntentRun.Worker != "explore.worker" || evidence.SelectedIntentRun.OutcomeKind != "no_change" ||
		evidence.SelectedIntentRun.ErrorCode != "" || evidence.SelectedIntentRun.CaptureFailureReason != "" {
		t.Fatalf("selected Intent evidence: %+v", evidence.SelectedIntentRun)
	}
	if evidence.SelectedIntentRun.CaptureDiagnostic != "" {
		t.Fatalf("selected Intent capture diagnostic=%q, want empty", evidence.SelectedIntentRun.CaptureDiagnostic)
	}
	if evidence.SelectedIntentRun.ResultEvidenceSource != "file" {
		t.Fatalf("selected Intent result evidence source=%q, want file", evidence.SelectedIntentRun.ResultEvidenceSource)
	}
}
