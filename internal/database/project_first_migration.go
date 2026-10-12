package database

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"strconv"
	"time"
)

type projectFirstStorageTable struct {
	name  string
	query string
}

var projectFirstStorageTables = []projectFirstStorageTable{
	{name: "facts", query: `
		SELECT id, project_id, description, supersedes_fact_id, retracted_at,
		       retracted_by, retraction_reason, replacement_fact_id
		FROM facts ORDER BY project_id, id`},
	{name: "intents", query: `
		SELECT id, project_id, to_fact_id, description, creator, created_at,
		       concluded_at, cancelled_at, cancelled_by, cancellation_reason,
		       sources_locked, kind
		FROM intents ORDER BY project_id, id`},
	{name: "intent_sources", query: `
		SELECT intent_id, project_id, fact_id
		FROM intent_sources ORDER BY project_id, intent_id, fact_id`},
	{name: "hints", query: `
		SELECT id, project_id, content, creator, created_at, generation,
		       withdrawn_at, withdrawn_by, withdrawal_reason
		FROM hints ORDER BY project_id, id`},
	{name: "project_completions", query: `
		SELECT project_id, generation, intent_id, description, actor, completed_at
		FROM project_completions ORDER BY project_id, generation`},
	{name: "execution_work_keys", query: `
		SELECT work_key, project_id, task_type, target_intent_id, last_attempt,
		       created_at, retry_state, failure_count, max_attempts,
		       initial_backoff_ms, max_backoff_ms, backoff_multiplier,
		       decline_cooldown_ms, next_eligible_at, updated_at
		FROM execution_work_keys ORDER BY work_key`},
	{name: "execution_claim_scopes", query: `
		SELECT claim_scope, project_id, scope_type, target_intent_id,
		       last_fencing_token, created_at
		FROM execution_claim_scopes ORDER BY claim_scope`},
	{name: "execution_runs", query: `
		SELECT id, project_id, target_intent_id, work_key, claim_scope, attempt,
		       status, fencing_token, dispatcher_id, task_type, input_generation,
		       input_revision, input_hash, worker_name, config_revision,
		       image_reference, result_schema, execution_spec_json, started_at,
		       finished_at, result_hash, outcome_kind, error_code, result_json,
		       finish_receipt_json, previous_run_id, next_eligible_at,
		       dispatcher_fencing_token
		FROM execution_runs ORDER BY id`},
	{name: "execution_claims", query: `
		SELECT claim_scope, work_key, run_id, project_id, target_intent_id,
		       task_type, fencing_token, dispatcher_id, claimed_at,
		       lease_expires_at, lease_expires_unix_nano
		FROM execution_claims ORDER BY claim_scope`},
	{name: "execution_worker_declines", query: `
		SELECT work_key, worker_name, retry_after, updated_at
		FROM execution_worker_declines ORDER BY work_key, worker_name`},
	{name: "execution_recovery_events", query: `
		SELECT id, run_id, dispatcher_id, status, reason, observed_at
		FROM execution_recovery_events ORDER BY id`},
	{name: "execution_runtime_handles", query: `
		SELECT run_id, project_id, phase, container_id, exec_id, started_at,
		       status, stopped_at
		FROM execution_runtime_handles ORDER BY run_id, phase, exec_id`},
	{name: "reason_checkpoints", query: `
		SELECT project_id, generation, input_revision, run_id, checked_at
		FROM reason_checkpoints ORDER BY project_id, generation`},
}

type projectFirstStorageDigest struct {
	rowCount int64
	digest   string
}

type projectFirstStorageSnapshot map[string]projectFirstStorageDigest

func snapshotProjectFirstStorage(ctx context.Context, db DBTX) (projectFirstStorageSnapshot, error) {
	snapshot := make(projectFirstStorageSnapshot, len(projectFirstStorageTables))
	for _, table := range projectFirstStorageTables {
		value, err := digestProjectFirstStorageTable(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("digest table %s: %w", table.name, err)
		}
		snapshot[table.name] = value
	}
	return snapshot, nil
}

func digestProjectFirstStorageTable(
	ctx context.Context,
	db DBTX,
	table projectFirstStorageTable,
) (projectFirstStorageDigest, error) {
	rows, err := db.QueryContext(ctx, table.query)
	if err != nil {
		return projectFirstStorageDigest{}, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return projectFirstStorageDigest{}, err
	}
	digest := sha256.New()
	for _, column := range columns {
		writeProjectFirstDigestString(digest, column)
	}

	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	var count int64
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			return projectFirstStorageDigest{}, err
		}
		digest.Write([]byte{'r'})
		for _, value := range values {
			if err := writeProjectFirstDigestValue(digest, value); err != nil {
				return projectFirstStorageDigest{}, err
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return projectFirstStorageDigest{}, err
	}
	return projectFirstStorageDigest{
		rowCount: count,
		digest:   hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

func writeProjectFirstDigestValue(digest hash.Hash, value any) error {
	switch typed := value.(type) {
	case nil:
		digest.Write([]byte{'n'})
	case int64:
		digest.Write([]byte{'i'})
		writeProjectFirstDigestString(digest, strconv.FormatInt(typed, 10))
	case float64:
		digest.Write([]byte{'f'})
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], math.Float64bits(typed))
		digest.Write(encoded[:])
	case string:
		digest.Write([]byte{'s'})
		writeProjectFirstDigestString(digest, typed)
	case []byte:
		digest.Write([]byte{'b'})
		writeProjectFirstDigestBytes(digest, typed)
	case bool:
		digest.Write([]byte{'o'})
		if typed {
			digest.Write([]byte{1})
		} else {
			digest.Write([]byte{0})
		}
	case time.Time:
		digest.Write([]byte{'t'})
		writeProjectFirstDigestString(digest, typed.UTC().Format(time.RFC3339Nano))
	default:
		return fmt.Errorf("unsupported SQLite value type %T", value)
	}
	return nil
}

func writeProjectFirstDigestString(digest hash.Hash, value string) {
	writeProjectFirstDigestBytes(digest, []byte(value))
}

func writeProjectFirstDigestBytes(digest hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	digest.Write(length[:])
	digest.Write(value)
}

func compareProjectFirstStorageSnapshots(before, after projectFirstStorageSnapshot) error {
	for _, table := range projectFirstStorageTables {
		beforeValue, beforeExists := before[table.name]
		afterValue, afterExists := after[table.name]
		if !beforeExists || !afterExists {
			return fmt.Errorf("table %s snapshot is missing", table.name)
		}
		if beforeValue.rowCount != afterValue.rowCount {
			return fmt.Errorf(
				"table %s row count changed from %d to %d",
				table.name, beforeValue.rowCount, afterValue.rowCount,
			)
		}
		if beforeValue.digest != afterValue.digest {
			return fmt.Errorf(
				"table %s content digest changed from %s to %s",
				table.name, beforeValue.digest, afterValue.digest,
			)
		}
	}
	return nil
}
