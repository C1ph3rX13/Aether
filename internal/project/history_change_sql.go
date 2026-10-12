package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// historyChangeStore persists the replayable notification log. Blackboard
// tables remain the source of truth; these records describe committed revision
// changes.
type historyChangeStore struct{}

// MaximumReplayChanges bounds one incremental response. Clients that fall
// farther behind receive replay_required and refresh the current projection.
const MaximumReplayChanges = 1024
const MaximumReplayBytes int64 = 4 * 1024 * 1024

func (s *historyChangeStore) Append(ctx context.Context, tx DBTX, change BlackboardChange) error {
	if change.PayloadSchema == "" {
		change.PayloadSchema = LegacyHistorySchema
	}
	switch change.PayloadSchema {
	case LegacyHistorySchema:
		if !json.Valid(change.Data) {
			return fmt.Errorf("legacy blackboard change data is not valid JSON")
		}
		if change.Source.Kind == "" {
			change.Source = ChangeSource{Kind: "migration", Name: "Legacy history"}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_changes
			    (project_id, revision, generation, kind, entity_type, entity_id, data,
			     payload_schema, source_kind, source_name, source_run_id, delta,
			     previous_hash, change_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', NULL, NULL, ?)`,
			change.ProjectID, change.Revision, change.Generation, change.Kind,
			change.EntityType, change.EntityID, string(change.Data), change.PayloadSchema,
			change.Source.Kind, change.Source.Name, change.Source.RunID, change.CreatedAt)
		return err
	case DeltaHistorySchema:
		if !json.Valid(change.Delta) {
			return fmt.Errorf("blackboard change delta is not valid JSON")
		}
		delta, err := decodeHistoryDelta(change.Delta)
		if err != nil {
			return err
		}
		if delta.Kind != change.Kind {
			return fmt.Errorf("blackboard change delta kind %q does not match %q", delta.Kind, change.Kind)
		}
		if change.Source.Kind == "" || change.Source.Name == "" {
			return fmt.Errorf("blackboard change source is incomplete")
		}
		if change.PreviousHash == "" {
			change.PreviousHash, err = previousHistoryHash(ctx, tx, change.ProjectID, change.Revision)
			if err != nil {
				return err
			}
		}
		canonicalDelta, err := canonicalJSON(change.Delta)
		if err != nil {
			return fmt.Errorf("canonicalize blackboard delta: %w", err)
		}
		change.Delta = canonicalDelta
		change.ChangeHash = calculateChangeHash(change)
		data := change.Data
		if !json.Valid(data) || len(data) == 0 {
			data, err = marshalHistoryEnvelope(change.Source, delta)
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO blackboard_changes
			    (project_id, revision, generation, kind, entity_type, entity_id, data,
			     payload_schema, source_kind, source_name, source_run_id, delta,
			     previous_hash, change_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			change.ProjectID, change.Revision, change.Generation, change.Kind,
			change.EntityType, change.EntityID, string(data), change.PayloadSchema,
			change.Source.Kind, change.Source.Name, change.Source.RunID, string(change.Delta),
			change.PreviousHash, change.ChangeHash, change.CreatedAt)
		return err
	default:
		return fmt.Errorf("blackboard change payload schema %q is unsupported", change.PayloadSchema)
	}
}

func previousHistoryHash(ctx context.Context, conn DBTX, projectID string, revision int64) (string, error) {
	var hash sql.NullString
	err := conn.QueryRowContext(ctx, `
		SELECT change_hash
		FROM blackboard_changes
		WHERE project_id = ? AND payload_schema = ? AND revision < ?
		ORDER BY revision DESC LIMIT 1`, projectID, DeltaHistorySchema, revision).Scan(&hash)
	if err == nil && hash.Valid {
		return hash.String, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	err = conn.QueryRowContext(ctx, `
		SELECT state_hash
		FROM project_history_snapshots
		WHERE project_id = ? AND revision < ?
		ORDER BY revision DESC LIMIT 1`, projectID, revision).Scan(&hash)
	if err == sql.ErrNoRows || !hash.Valid {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash.String, nil
}

type changeHashEnvelope struct {
	Schema       string          `json:"schema"`
	ProjectID    string          `json:"project_id"`
	Revision     int64           `json:"revision"`
	Generation   int             `json:"generation"`
	Kind         ChangeKind      `json:"kind"`
	EntityType   string          `json:"entity_type"`
	EntityID     *string         `json:"entity_id,omitempty"`
	Source       ChangeSource    `json:"source"`
	Delta        json.RawMessage `json:"delta"`
	PreviousHash string          `json:"previous_hash,omitempty"`
	CreatedAt    string          `json:"created_at"`
}

func calculateChangeHash(change BlackboardChange) string {
	envelope := changeHashEnvelope{
		Schema: DeltaHistorySchema, ProjectID: change.ProjectID, Revision: change.Revision,
		Generation: change.Generation, Kind: change.Kind, EntityType: change.EntityType,
		EntityID: change.EntityID, Source: change.Source, Delta: change.Delta,
		PreviousHash: change.PreviousHash, CreatedAt: change.CreatedAt,
	}
	return sha256Hex(mustMarshal(envelope))
}

func (s *historyChangeStore) ReplayAfter(ctx context.Context, conn DBTX, projectID string, afterRevision int64) (ChangeReplay, error) {
	replay := ChangeReplay{
		ProjectID:     projectID,
		AfterRevision: afterRevision,
		Changes:       []BlackboardChange{},
	}
	if err := conn.QueryRowContext(ctx,
		"SELECT blackboard_revision FROM projects WHERE id = ?", projectID,
	).Scan(&replay.CurrentRevision); err != nil {
		return ChangeReplay{}, err
	}

	var earliest sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		"SELECT MIN(revision) FROM blackboard_changes WHERE project_id = ?", projectID,
	).Scan(&earliest); err != nil {
		return ChangeReplay{}, err
	}
	if earliest.Valid {
		replay.EarliestRevision = &earliest.Int64
	}

	if afterRevision < replay.CurrentRevision && (!earliest.Valid || afterRevision < earliest.Int64-1) {
		replay.ReplayRequired = true
		return replay, nil
	}
	var replayCount int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM (
			SELECT revision
			FROM blackboard_changes
			WHERE project_id = ? AND revision > ?
			ORDER BY revision
			LIMIT ?
		)`, projectID, afterRevision, MaximumReplayChanges+1,
	).Scan(&replayCount); err != nil {
		return ChangeReplay{}, err
	}
	if replayCount > MaximumReplayChanges {
		replay.ReplayRequired = true
		return replay, nil
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT project_id, revision, generation, kind, entity_type, entity_id, data,
		       payload_schema, source_kind, source_name, source_run_id, delta,
		       previous_hash, change_hash, created_at
		FROM blackboard_changes
		WHERE project_id = ? AND revision > ?
		ORDER BY revision
		LIMIT ?`, projectID, afterRevision, MaximumReplayChanges+1)
	if err != nil {
		return ChangeReplay{}, err
	}
	defer rows.Close()

	var replayBytes int64
	for rows.Next() {
		change, err := scanBlackboardChange(rows)
		if err != nil {
			return ChangeReplay{}, err
		}
		if change.PayloadSchema == DeltaHistorySchema {
			if _, err := decodeHistoryDelta(change.Delta); err != nil {
				return ChangeReplay{}, fmt.Errorf("unsupported history delta at revision %d: %w", change.Revision, err)
			}
		}
		if change.PayloadSchema == DeltaHistorySchema && storedHistoryDataMatchesDelta(change) {
			compatibilityData, compatibilityErr := compatibilityHistoryData(ctx, conn, change)
			if compatibilityErr == nil {
				change.Data = compatibilityData
			}
		}
		replayBytes += int64(len(change.Data))
		if replayBytes > MaximumReplayBytes {
			replay.ReplayRequired = true
			replay.Changes = nil
			return replay, nil
		}
		replay.Changes = append(replay.Changes, change)
	}
	if err := rows.Err(); err != nil {
		return ChangeReplay{}, err
	}
	return replay, nil
}

type changeScanner interface {
	Scan(dest ...any) error
}

type unsupportedHistorySchemaError struct {
	schema   string
	project  string
	revision int64
}

func (e *unsupportedHistorySchemaError) Error() string {
	return fmt.Sprintf("history payload schema %q is unsupported at %s revision %d", e.schema, e.project, e.revision)
}

func scanBlackboardChange(row changeScanner) (BlackboardChange, error) {
	var change BlackboardChange
	var entityID, sourceKind, sourceName, sourceRunID, payloadSchema, delta, previousHash, changeHash sql.NullString
	var data []byte
	if err := row.Scan(
		&change.ProjectID, &change.Revision, &change.Generation, &change.Kind,
		&change.EntityType, &entityID, &data, &payloadSchema, &sourceKind, &sourceName,
		&sourceRunID, &delta, &previousHash, &changeHash, &change.CreatedAt,
	); err != nil {
		return BlackboardChange{}, err
	}
	if entityID.Valid {
		change.EntityID = &entityID.String
	}
	change.Data = append(json.RawMessage(nil), data...)
	if !json.Valid(change.Data) {
		return BlackboardChange{}, fmt.Errorf("invalid JSON in blackboard change %s revision %d", change.ProjectID, change.Revision)
	}
	if payloadSchema.Valid {
		change.PayloadSchema = payloadSchema.String
	}
	if change.PayloadSchema != LegacyHistorySchema && change.PayloadSchema != DeltaHistorySchema {
		return BlackboardChange{}, &unsupportedHistorySchemaError{
			schema: change.PayloadSchema, project: change.ProjectID, revision: change.Revision,
		}
	}
	if sourceKind.Valid {
		change.Source.Kind = sourceKind.String
	}
	if sourceName.Valid {
		change.Source.Name = sourceName.String
	}
	if sourceRunID.Valid {
		change.Source.RunID = &sourceRunID.String
	}
	if delta.Valid {
		change.Delta = append(json.RawMessage(nil), delta.String...)
	}
	if previousHash.Valid {
		change.PreviousHash = previousHash.String
	}
	if changeHash.Valid {
		change.ChangeHash = changeHash.String
	}
	if change.PayloadSchema == DeltaHistorySchema && !json.Valid(change.Delta) {
		return BlackboardChange{}, fmt.Errorf("invalid delta in blackboard change %s revision %d", change.ProjectID, change.Revision)
	}
	return change, nil
}

func (s *historyChangeStore) GetRevision(ctx context.Context, conn DBTX, projectID string, revision int64) (BlackboardChange, error) {
	return scanBlackboardChange(conn.QueryRowContext(ctx, `
		SELECT project_id, revision, generation, kind, entity_type, entity_id, data,
		       payload_schema, source_kind, source_name, source_run_id, delta,
		       previous_hash, change_hash, created_at
		FROM blackboard_changes WHERE project_id = ? AND revision = ?`, projectID, revision))
}
