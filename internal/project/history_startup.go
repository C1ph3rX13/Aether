package project

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

const historySnapshotBatchSize = 32

// InitializeHistory creates the one truthful cutover snapshot for Projects
// that existed before the delta format, then verifies every stored snapshot.
// It runs in small write transactions so a large database does not hold
// SQLite's writer lock for the entire backfill.
func InitializeHistory(ctx context.Context, db *database.DB) error {
	for {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		ids, queryErr := historyCutoverSnapshotCandidates(ctx, tx, historySnapshotBatchSize)
		if queryErr != nil {
			_ = tx.Rollback()
			return queryErr
		}
		for _, projectID := range ids {
			detail, detailErr := newProjectQuery().GetDetail(ctx, tx, projectID)
			if detailErr != nil {
				_ = tx.Rollback()
				return fmt.Errorf("load Project %s for history cutover: %w", projectID, detailErr)
			}
			meta := detail.Project
			if err := storeHistorySnapshot(ctx, tx, detail, meta.Revision, meta.Generation, "cutover", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("store Project %s history cutover: %w", projectID, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if len(ids) < historySnapshotBatchSize {
			break
		}
	}
	return verifyHistorySnapshotIntegrity(ctx, db)
}

func historyCutoverSnapshotCandidates(ctx context.Context, tx *sql.Tx, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id
		FROM projects AS p
		WHERE NOT EXISTS (
			SELECT 1 FROM project_history_snapshots AS s WHERE s.project_id = p.id
		)
		ORDER BY p.id
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, err
		}
		ids = append(ids, projectID)
	}
	return ids, rows.Err()
}

// verifyHistorySnapshotIntegrity is owned by the Project domain because it
// understands the snapshot schema and canonical state representation.
func verifyHistorySnapshotIntegrity(ctx context.Context, db *database.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT project_id, revision, schema, state_json, state_hash
		FROM project_history_snapshots
		ORDER BY project_id, revision`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var projectID, schema, stateHash string
		var revision int64
		var stateJSON []byte
		if err := rows.Scan(&projectID, &revision, &schema, &stateJSON, &stateHash); err != nil {
			return err
		}
		if schema != ProjectSnapshotSchema {
			return fmt.Errorf("unsupported Project history snapshot schema %q at %s revision %d", schema, projectID, revision)
		}
		canonical, err := canonicalJSON(stateJSON)
		if err != nil {
			return fmt.Errorf("invalid Project history snapshot at %s revision %d: %w", projectID, revision, err)
		}
		if sha256Hex(canonical) != stateHash {
			return fmt.Errorf("Project history snapshot hash mismatch at %s revision %d", projectID, revision)
		}
	}
	return rows.Err()
}
