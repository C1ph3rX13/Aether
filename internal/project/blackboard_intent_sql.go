package project

import (
	"context"
	"database/sql"
	"fmt"
)

// IntentStore encapsulates SQL queries for the intents and intent_sources tables.
type IntentStore struct{}

// ListByProject returns all intents for a project.
func (r *IntentStore) ListByProject(ctx context.Context, conn DBTX, projectID string) ([]Intent, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT id, kind, description, creator, to_fact_id, created_at,
		        concluded_at, cancelled_at, cancelled_by, cancellation_reason
		 FROM intents WHERE project_id = ? ORDER BY created_at, id`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	intents := []Intent{}
	for rows.Next() {
		i, err := scanIntentRow(rows)
		if err != nil {
			return nil, err
		}
		i.From = []string{} // filled below
		intents = append(intents, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-fetch sources
	sources, err := r.listSourcesByProject(ctx, conn, projectID)
	if err != nil {
		return nil, err
	}
	for idx := range intents {
		if srcs, ok := sources[intents[idx].ID]; ok {
			intents[idx].From = srcs
		}
	}
	return intents, nil
}

// GetByID returns a single intent.
func (r *IntentStore) GetByID(ctx context.Context, conn DBTX, projectID, intentID string) (*Intent, error) {
	i, err := scanIntentRow(conn.QueryRowContext(ctx, `
		SELECT id, kind, description, creator, to_fact_id, created_at,
		       concluded_at, cancelled_at, cancelled_by, cancellation_reason
		FROM intents WHERE id = ? AND project_id = ?`, intentID, projectID))
	if err != nil {
		return nil, err
	}

	// Get sources
	fromRows, err := conn.QueryContext(ctx,
		"SELECT fact_id FROM intent_sources WHERE intent_id = ? AND project_id = ?", i.ID, projectID)
	if err != nil {
		return nil, err
	}
	defer fromRows.Close()
	i.From = []string{}
	for fromRows.Next() {
		var fid string
		if err := fromRows.Scan(&fid); err != nil {
			return nil, err
		}
		i.From = append(i.From, fid)
	}
	return &i, fromRows.Err()
}

// Create inserts a new intent with its source facts.
func (r *IntentStore) Create(ctx context.Context, tx DBTX, id, projectID string, kind IntentKind, description, creator string, from []string, toFactID *string, now string) error {
	if !validIntentKind(kind) {
		return fmt.Errorf("invalid intent kind %q", kind)
	}
	var concludedAt *string
	if toFactID != nil {
		concludedAt = &now
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO intents (id, project_id, kind, description, creator, to_fact_id, concluded_at, created_at, sources_locked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)",
		id, projectID, kind, description, creator, toFactID, concludedAt, now); err != nil {
		return err
	}
	for _, fid := range from {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO intent_sources (intent_id, project_id, fact_id) VALUES (?, ?, ?)",
			id, projectID, fid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE intents SET sources_locked = 1 WHERE id = ? AND project_id = ?",
		id, projectID); err != nil {
		return err
	}
	return nil
}

// ConcludeFromRun projects a conclusion after ownership and fencing have been
// checked by the Run ledger.
func (r *IntentStore) ConcludeFromRun(ctx context.Context, conn DBTX, projectID, intentID, toFactID, now string) (int64, error) {
	result, err := conn.ExecContext(ctx,
		"UPDATE intents SET to_fact_id = ?, concluded_at = ? WHERE id = ? AND project_id = ? AND to_fact_id IS NULL AND cancelled_at IS NULL",
		toFactID, now, intentID, projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Cancel marks an open intent as cancelled exactly once.
func (r *IntentStore) Cancel(ctx context.Context, tx DBTX, projectID, intentID, actor, reason, now string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE intents
		SET cancelled_at = ?, cancelled_by = ?, cancellation_reason = ?
		WHERE id = ? AND project_id = ? AND to_fact_id IS NULL AND cancelled_at IS NULL`,
		now, actor, reason, intentID, projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// FindConcludedWithGoal returns Completion intent IDs targeting the goal Fact.
func (r *IntentStore) FindConcludedWithGoal(ctx context.Context, conn DBTX, projectID string) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT id FROM intents WHERE project_id = ? AND kind = ? AND to_fact_id = 'goal' ORDER BY created_at, id",
		projectID, IntentKindCompletion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// listSourcesByProject returns a map from intent_id to its source fact IDs.
func (r *IntentStore) listSourcesByProject(ctx context.Context, conn DBTX, projectID string) (map[string][]string, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT intent_id, fact_id FROM intent_sources WHERE project_id = ? ORDER BY intent_id, fact_id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string][]string)
	for rows.Next() {
		var intentID, factID string
		if err := rows.Scan(&intentID, &factID); err != nil {
			return nil, err
		}
		m[intentID] = append(m[intentID], factID)
	}
	return m, rows.Err()
}

type intentScanner interface {
	Scan(dest ...any) error
}

// scanIntentRow scans a single intent cursor without its source facts.
func scanIntentRow(row intentScanner) (Intent, error) {
	var i Intent
	var kind string
	var toFactID, concludedAt, cancelledAt, cancelledBy, cancellationReason sql.NullString
	if err := row.Scan(
		&i.ID, &kind, &i.Description, &i.Creator, &toFactID, &i.CreatedAt,
		&concludedAt, &cancelledAt, &cancelledBy, &cancellationReason,
	); err != nil {
		return i, err
	}
	i.Kind = IntentKind(kind)
	if !validIntentKind(i.Kind) {
		return i, fmt.Errorf("invalid persisted intent kind %q", kind)
	}
	if toFactID.Valid {
		i.To = &toFactID.String
	}
	if concludedAt.Valid {
		i.ConcludedAt = &concludedAt.String
	}
	if cancelledAt.Valid {
		i.CancelledAt = &cancelledAt.String
	}
	if cancelledBy.Valid {
		i.CancelledBy = &cancelledBy.String
	}
	if cancellationReason.Valid {
		i.CancellationReason = &cancellationReason.String
	}
	i.Status = IntentStatusOpen
	if i.CancelledAt != nil {
		i.Status = IntentStatusCancelled
	} else if i.To != nil {
		i.Status = IntentStatusConcluded
	}
	return i, nil
}

func validIntentKind(kind IntentKind) bool {
	return kind == IntentKindBootstrap || kind == IntentKindExplore || kind == IntentKindCompletion
}
