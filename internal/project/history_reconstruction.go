package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const (
	historySnapshotCreation   = "creation"
	historySnapshotGeneration = "generation"
	historySnapshotPeriodic   = "periodic"
)

type historySnapshotRow struct {
	Revision   int64
	Generation int
	Schema     string
	StateJSON  []byte
	StateHash  string
}

type storedHistoryEnvelope struct {
	SchemaVersion int            `json:"schema_version"`
	Source        ChangeSource   `json:"source"`
	Before        any            `json:"before"`
	After         any            `json:"after"`
	Result        map[string]any `json:"result,omitempty"`
}

func compatibilityHistoryData(ctx context.Context, conn DBTX, change BlackboardChange) (json.RawMessage, error) {
	before, after, err := reconstructDeltaBoundary(ctx, conn, change)
	if err != nil {
		return nil, err
	}
	beforeJSON := json.RawMessage("null")
	if before != nil {
		beforeJSON, err = marshalCanonicalProjectDetail(*before)
		if err != nil {
			return nil, err
		}
	}
	delta, err := decodeHistoryDelta(change.Delta)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(storedHistoryEnvelope{
		SchemaVersion: ChangeSchemaVersion,
		Source:        change.Source,
		Before:        beforeJSON,
		After:         normalizeProjectDetail(*after),
		Result:        delta.Result,
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// buildStoredHistoryCompatibilityPayload builds the sparse retained envelope
// used in the legacy Data field. It contains only entities touched by the
// authoritative delta and remains private to Project storage validation.
func buildStoredHistoryCompatibilityPayload(change BlackboardChange) (json.RawMessage, error) {
	delta, err := decodeHistoryDelta(change.Delta)
	if err != nil {
		return nil, err
	}
	if delta.Kind != change.Kind {
		return nil, fmt.Errorf("history delta kind does not match change kind")
	}
	beforeState, err := sparseHistoryState(delta, true)
	if err != nil {
		return nil, err
	}
	afterState, err := sparseHistoryState(delta, false)
	if err != nil {
		return nil, err
	}
	var beforeValue any
	if delta.Project != nil {
		fields, fieldsErr := historyDeltaFields(delta.Project.Before, delta.Project.BeforeFields)
		if fieldsErr != nil {
			return nil, fieldsErr
		}
		if fields != nil {
			beforeValue = beforeState
		}
	}
	return json.Marshal(storedHistoryEnvelope{
		SchemaVersion: ChangeSchemaVersion,
		Source:        change.Source,
		Before:        beforeValue,
		After:         afterState,
		Result:        delta.Result,
	})
}

func sparseHistoryState(delta HistoryDelta, before bool) (map[string]any, error) {
	state := map[string]any{
		"facts":              []any{},
		"intents":            []any{},
		"hints":              []any{},
		"completion_records": []any{},
	}
	if delta.Project != nil {
		fields, err := sparseHistoryFields(delta.Project, before)
		if err != nil {
			return nil, err
		}
		if fields != nil {
			state["project"] = fields
		}
	}
	appendEntities := func(target string, values any) {
		state[target] = values
	}
	facts, err := sparseHistoryEntities(delta.Facts, before, func(value Fact) string { return value.ID })
	if err != nil {
		return nil, err
	}
	appendEntities("facts", facts)
	intents, err := sparseHistoryEntities(delta.Intents, before, func(value Intent) string { return value.ID })
	if err != nil {
		return nil, err
	}
	appendEntities("intents", intents)
	hints, err := sparseHistoryEntities(delta.Hints, before, func(value Hint) string { return value.ID })
	if err != nil {
		return nil, err
	}
	appendEntities("hints", hints)
	completions, err := sparseHistoryEntities(delta.Completions, before, func(value CompletionRecord) string { return value.IntentID })
	if err != nil {
		return nil, err
	}
	appendEntities("completion_records", completions)
	return state, nil
}

func sparseHistoryFields[T any](delta *HistoryValueDelta[T], before bool) (map[string]any, error) {
	if delta == nil {
		return nil, nil
	}
	fields := delta.AfterFields
	value := delta.After
	if before {
		fields = delta.BeforeFields
		value = delta.Before
	}
	fields, err := historyDeltaFields(value, fields)
	if err != nil || fields == nil {
		return fields, err
	}
	copyFields := make(map[string]any, len(fields)+1)
	for key, field := range fields {
		copyFields[key] = field
	}
	if _, exists := copyFields["id"]; !exists {
		copyFields["id"] = delta.ID
	}
	return copyFields, nil
}

func sparseHistoryEntities[T any](values []HistoryValueDelta[T], before bool, key func(T) string) ([]any, error) {
	result := make([]any, 0, len(values))
	for index := range values {
		fields, err := sparseHistoryFields(&values[index], before)
		if err != nil {
			return nil, err
		}
		if fields != nil {
			if _, exists := fields["id"]; !exists {
				if values[index].Before != nil && before {
					fields["id"] = key(*values[index].Before)
				} else if values[index].After != nil {
					fields["id"] = key(*values[index].After)
				} else {
					fields["id"] = values[index].ID
				}
			}
			result = append(result, fields)
		}
	}
	return result, nil
}

func storedHistoryDataMatchesDelta(change BlackboardChange) bool {
	expected, err := buildStoredHistoryCompatibilityPayload(change)
	if err != nil {
		return false
	}
	actualCanonical, err := canonicalJSON(change.Data)
	if err != nil {
		return false
	}
	expectedCanonical, err := canonicalJSON(expected)
	if err != nil {
		return false
	}
	return string(actualCanonical) == string(expectedCanonical)
}

func historySnapshotKind(change BlackboardChange) string {
	switch {
	case change.Revision == 1 || change.Kind == ChangeProjectCreated:
		return historySnapshotCreation
	case change.Kind == ChangeProjectReopened:
		return historySnapshotGeneration
	case change.Revision%100 == 0:
		return historySnapshotPeriodic
	default:
		return ""
	}
}

func (s *serviceCore) storeHistorySnapshot(
	ctx context.Context,
	tx DBTX,
	detail ProjectDetail,
	revision int64,
	generation int,
	kind string,
	createdAt string,
) error {
	return storeHistorySnapshot(ctx, tx, detail, revision, generation, kind, createdAt)
}

func storeHistorySnapshot(
	ctx context.Context,
	tx DBTX,
	detail ProjectDetail,
	revision int64,
	generation int,
	kind string,
	createdAt string,
) error {
	detail = normalizeProjectDetail(detail)
	stateJSON, err := marshalCanonicalProjectDetail(detail)
	if err != nil {
		return fmt.Errorf("marshal Project history snapshot: %w", err)
	}
	stateJSON, err = canonicalJSON(stateJSON)
	if err != nil {
		return fmt.Errorf("canonicalize Project history snapshot: %w", err)
	}
	stateHash := sha256Hex(stateJSON)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO project_history_snapshots
		    (project_id, revision, generation, snapshot_kind, schema, state_json, state_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, revision) DO NOTHING`,
		detail.Project.ID, revision, generation, kind, ProjectSnapshotSchema,
		string(stateJSON), stateHash, createdAt)
	return err
}

func reconstructDeltaBoundary(ctx context.Context, conn DBTX, target BlackboardChange) (*ProjectDetail, *ProjectDetail, error) {
	snapshot, found, err := nearestHistorySnapshotBefore(ctx, conn, target.ProjectID, target.Revision)
	if err != nil {
		return nil, nil, err
	}
	var state ProjectDetail
	var before *ProjectDetail
	expectedRevision := int64(1)
	previousHash := ""
	if found {
		state, err = decodeHistorySnapshot(target.ProjectID, snapshot)
		if err != nil {
			return nil, nil, err
		}
		expectedRevision = snapshot.Revision + 1
		previousHash = snapshot.StateHash
		var snapshotChangeHash sql.NullString
		if err := conn.QueryRowContext(ctx, `
			SELECT change_hash
			FROM blackboard_changes
			WHERE project_id = ? AND revision = ? AND payload_schema = ?`,
			target.ProjectID, snapshot.Revision, DeltaHistorySchema).Scan(&snapshotChangeHash); err == nil && snapshotChangeHash.Valid {
			previousHash = snapshotChangeHash.String
		} else if err != nil && err != sql.ErrNoRows {
			return nil, nil, err
		}
	} else if target.Revision != 1 {
		return nil, nil, fmt.Errorf("no snapshot precedes delta revision %d", target.Revision)
	}
	var deltaCount int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_changes
		WHERE project_id = ? AND payload_schema = ? AND revision >= ? AND revision <= ?`,
		target.ProjectID, DeltaHistorySchema, expectedRevision, target.Revision).Scan(&deltaCount); err != nil {
		return nil, nil, err
	}
	if deltaCount > 100 {
		return nil, nil, fmt.Errorf("history reconstruction exceeds the 100-delta snapshot interval")
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT project_id, revision, generation, kind, entity_type, entity_id, data,
		       payload_schema, source_kind, source_name, source_run_id, delta,
		       previous_hash, change_hash, created_at
		FROM blackboard_changes
		WHERE project_id = ? AND revision >= ? AND revision <= ?
		ORDER BY revision`, target.ProjectID, expectedRevision, target.Revision)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	seenTarget := false
	for rows.Next() {
		change, scanErr := scanBlackboardChange(rows)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		if change.PayloadSchema != DeltaHistorySchema {
			return nil, nil, fmt.Errorf("legacy history interrupts delta chain at revision %d", change.Revision)
		}
		if change.Revision != expectedRevision {
			return nil, nil, newHistoryEvidenceConflict("history chain has a missing or reordered revision: got %d want %d", change.Revision, expectedRevision)
		}
		if change.PreviousHash != previousHash {
			return nil, nil, newHistoryEvidenceConflict("history chain predecessor hash mismatch at revision %d", change.Revision)
		}
		if calculateChangeHash(change) != change.ChangeHash {
			return nil, nil, newHistoryEvidenceConflict("history chain hash mismatch at revision %d", change.Revision)
		}
		delta, decodeErr := decodeHistoryDelta(change.Delta)
		if decodeErr != nil {
			return nil, nil, fmt.Errorf("history delta at revision %d is invalid: %w", change.Revision, decodeErr)
		}
		if delta.Kind != change.Kind {
			return nil, nil, newHistoryEvidenceConflict("history delta kind mismatch at revision %d", change.Revision)
		}
		if change.Revision == target.Revision {
			copyState := normalizeProjectDetail(state)
			before = &copyState
		}
		if err := applyHistoryDelta(&state, delta, target.ProjectID, change.Revision, change.Generation); err != nil {
			return nil, nil, fmt.Errorf("reduce history revision %d: %w", change.Revision, err)
		}
		previousHash = change.ChangeHash
		expectedRevision++
		if change.Revision == target.Revision {
			seenTarget = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if !seenTarget {
		return nil, nil, fmt.Errorf("history chain does not contain target revision %d", target.Revision)
	}
	if target.Revision == 1 && before != nil && before.Project.ID == "" {
		before = nil
	}
	return before, &state, nil
}

func nearestHistorySnapshotBefore(ctx context.Context, conn DBTX, projectID string, revision int64) (historySnapshotRow, bool, error) {
	var snapshot historySnapshotRow
	err := conn.QueryRowContext(ctx, `
		SELECT revision, generation, schema, state_json, state_hash
		FROM project_history_snapshots
		WHERE project_id = ? AND revision < ?
		ORDER BY revision DESC LIMIT 1`, projectID, revision).Scan(
		&snapshot.Revision, &snapshot.Generation, &snapshot.Schema, &snapshot.StateJSON, &snapshot.StateHash,
	)
	if err == sql.ErrNoRows {
		return historySnapshotRow{}, false, nil
	}
	if err != nil {
		return historySnapshotRow{}, false, err
	}
	return snapshot, true, nil
}

func historySnapshotAtRevision(ctx context.Context, conn DBTX, projectID string, revision int64) (historySnapshotRow, bool, error) {
	var snapshot historySnapshotRow
	err := conn.QueryRowContext(ctx, `
		SELECT revision, generation, schema, state_json, state_hash
		FROM project_history_snapshots
		WHERE project_id = ? AND revision = ?`, projectID, revision).Scan(
		&snapshot.Revision, &snapshot.Generation, &snapshot.Schema, &snapshot.StateJSON, &snapshot.StateHash,
	)
	if err == sql.ErrNoRows {
		return historySnapshotRow{}, false, nil
	}
	if err != nil {
		return historySnapshotRow{}, false, err
	}
	return snapshot, true, nil
}

func decodeHistorySnapshot(projectID string, snapshot historySnapshotRow) (ProjectDetail, error) {
	if err := verifyHistorySnapshot(snapshot); err != nil {
		return ProjectDetail{}, err
	}
	var detail ProjectDetail
	if err := json.Unmarshal(snapshot.StateJSON, &detail); err != nil {
		return ProjectDetail{}, fmt.Errorf("snapshot state is invalid: %w", err)
	}
	detail = normalizeProjectDetail(detail)
	if detail.Project.ID != projectID || detail.Project.Revision != snapshot.Revision ||
		detail.Project.Generation != snapshot.Generation {
		return ProjectDetail{}, newHistoryEvidenceConflict("snapshot state identity is inconsistent at revision %d", snapshot.Revision)
	}
	return detail, nil
}

func verifyHistorySnapshot(snapshot historySnapshotRow) error {
	if snapshot.Schema != ProjectSnapshotSchema {
		return newHistoryEvidenceConflict("snapshot schema %q is unsupported at revision %d", snapshot.Schema, snapshot.Revision)
	}
	canonical, err := canonicalJSON(snapshot.StateJSON)
	if err != nil {
		return fmt.Errorf("snapshot state is not valid JSON: %w", err)
	}
	if sha256Hex(canonical) != snapshot.StateHash {
		return newHistoryEvidenceConflict("snapshot state hash mismatch at revision %d", snapshot.Revision)
	}
	return nil
}
