package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *serviceCore) appendDeltaChange(
	ctx context.Context,
	tx DBTX,
	projectID string,
	generation int,
	revision int64,
	kind ChangeKind,
	entityType string,
	entityID *string,
	before, after ProjectDetail,
	source ChangeSource,
	result map[string]any,
	createdAt string,
) (BlackboardChange, error) {
	delta, err := buildHistoryDelta(kind, before, after, result)
	if err != nil {
		return BlackboardChange{}, err
	}
	if err := requireInitializedHistory(ctx, tx, projectID, revision, kind); err != nil {
		return BlackboardChange{}, err
	}
	deltaData, err := marshalHistoryDelta(delta)
	if err != nil {
		return BlackboardChange{}, err
	}
	envelope, err := buildStoredHistoryCompatibilityPayload(BlackboardChange{
		ProjectID: projectID, Revision: revision, Generation: generation,
		Kind: kind, EntityType: entityType, EntityID: entityID,
		PayloadSchema: DeltaHistorySchema, Source: source, Delta: deltaData,
	})
	if err != nil {
		return BlackboardChange{}, err
	}
	change := BlackboardChange{
		ProjectID: projectID, Revision: revision, Generation: generation,
		Kind: kind, EntityType: entityType, EntityID: entityID,
		Data: envelope, PayloadSchema: DeltaHistorySchema, Source: source,
		Delta: deltaData, CreatedAt: createdAt,
	}
	if err := s.changes.Append(ctx, tx, change); err != nil {
		return BlackboardChange{}, err
	}
	if snapshotKind := historySnapshotKind(change); snapshotKind != "" {
		// Snapshot boundaries are the deliberate points where a full Project
		// read is worthwhile. Command deltas only carry touched entities, so
		// using the command projection here would create an incomplete replay
		// anchor for later history reads.
		snapshotDetail, err := s.query.GetDetail(ctx, tx, projectID)
		if err != nil {
			return BlackboardChange{}, fmt.Errorf("load Project history snapshot: %w", err)
		}
		if err := s.storeHistorySnapshot(ctx, tx, snapshotDetail, revision, generation, snapshotKind, createdAt); err != nil {
			return BlackboardChange{}, err
		}
	}
	// Append calculates these values from the canonical delta. Keep the
	// published value consistent with the persisted row.
	var persistedDelta []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT previous_hash, change_hash, delta
		FROM blackboard_changes WHERE project_id = ? AND revision = ?`, projectID, revision).
		Scan(&change.PreviousHash, &change.ChangeHash, &persistedDelta); err != nil {
		return BlackboardChange{}, err
	}
	change.Delta = persistedDelta
	return change, nil
}

func requireInitializedHistory(
	ctx context.Context,
	tx DBTX,
	projectID string,
	revision int64,
	kind ChangeKind,
) error {
	if revision == 1 && kind == ChangeProjectCreated {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM project_history_snapshots WHERE project_id = ?)", projectID,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	return fmt.Errorf("Project History is not initialized for Project %s", projectID)
}

func (s *Service) ListChanges(ctx context.Context, projectID string, afterRevision int64) (ChangeReplay, error) {
	if afterRevision < 0 {
		return ChangeReplay{}, InvalidArgument("after_revision must not be negative")
	}
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return ChangeReplay{}, err
	}
	defer conn.Close()
	replay, err := s.changes.ReplayAfter(ctx, conn, projectID, afterRevision)
	if err == sql.ErrNoRows {
		return ChangeReplay{}, ErrProjectNotFound
	}
	if err != nil {
		var unsupported *unsupportedHistorySchemaError
		if errors.As(err, &unsupported) {
			return ChangeReplay{}, Conflict(unsupported.Error())
		}
		if strings.Contains(err.Error(), "unsupported history delta") {
			return ChangeReplay{}, Conflict(err.Error())
		}
		return ChangeReplay{}, err
	}
	if afterRevision > replay.CurrentRevision {
		return ChangeReplay{}, InvalidArgument("after_revision is newer than the project")
	}
	return replay, nil
}

func validHistoryKind(kind ChangeKind) bool {
	switch kind {
	case ChangeProjectBaseline, ChangeProjectCreated, ChangeProjectRenamed,
		ChangeProjectPaused, ChangeProjectResumed, ChangeIntentCreated,
		ChangeIntentConcluded, ChangeIntentCancelled, ChangeHintCreated,
		ChangeHintWithdrawn, ChangeFactRetracted, ChangeFactSuperseded,
		ChangeProjectCompleted, ChangeProjectReopened, ChangeIntentsProposed,
		ChangeBootstrapCompleted:
		return true
	default:
		return false
	}
}
func (s *Service) GetBlackboardRevision(ctx context.Context, projectID string, revision int64) (ProjectDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectDetail{}, err
	}
	defer tx.Rollback()
	return s.history.ReadRevisionSnapshot(ctx, tx, projectID, revision)
}
