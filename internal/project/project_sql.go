package project

import (
	"context"
	"database/sql"
	"strings"
)

// projectStore encapsulates SQL queries for the projects table.
type projectStore struct{}

// ListMeta returns Project identity and lifecycle state without Web aggregates.
func (r *projectStore) ListMeta(ctx context.Context, conn DBTX) ([]ProjectMeta, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision, config_revision
		FROM projects
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]ProjectMeta, 0)
	for rows.Next() {
		var meta ProjectMeta
		var configRevision sql.NullString
		if err := rows.Scan(
			&meta.ID, &meta.Title, &meta.Status, &meta.BootstrapEnabled,
			&meta.CreatedAt, &meta.Generation, &meta.Revision, &configRevision,
		); err != nil {
			return nil, err
		}
		meta.ConfigRevision = configRevision.String
		projects = append(projects, meta)
	}
	return projects, rows.Err()
}

// GetMeta returns the project metadata (without facts/intents/hints).
func (r *projectStore) GetMeta(ctx context.Context, conn DBTX, projectID string) (ProjectMeta, error) {
	var meta ProjectMeta
	var configRevision sql.NullString
	if err := conn.QueryRowContext(ctx,
		"SELECT id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision, config_revision FROM projects WHERE id = ?",
		projectID).Scan(&meta.ID, &meta.Title, &meta.Status, &meta.BootstrapEnabled, &meta.CreatedAt, &meta.Generation, &meta.Revision, &configRevision); err != nil {
		return meta, err
	}
	meta.ConfigRevision = configRevision.String
	return meta, nil
}

// GetStatus returns the project status.
func (r *projectStore) GetStatus(ctx context.Context, conn DBTX, projectID string) (ProjectStatus, error) {
	var status ProjectStatus
	err := conn.QueryRowContext(ctx, "SELECT status FROM projects WHERE id = ?", projectID).Scan(&status)
	return status, err
}

// listProjectStatuses returns only existing requested Projects. The Project
// query seam turns this storage representation into ordered missing evidence.
func listProjectStatuses(ctx context.Context, conn DBTX, projectIDs []string) (map[string]ProjectStatus, error) {
	if len(projectIDs) == 0 {
		return map[string]ProjectStatus{}, nil
	}
	placeholders := make([]string, len(projectIDs))
	arguments := make([]any, len(projectIDs))
	for index, projectID := range projectIDs {
		placeholders[index], arguments[index] = "?", projectID
	}
	rows, err := conn.QueryContext(ctx,
		"SELECT id, status FROM projects WHERE id IN ("+strings.Join(placeholders, ",")+")", arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := make(map[string]ProjectStatus, len(projectIDs))
	for rows.Next() {
		var projectID string
		var status ProjectStatus
		if err := rows.Scan(&projectID, &status); err != nil {
			return nil, err
		}
		statuses[projectID] = status
	}
	return statuses, rows.Err()
}

// Exists checks if a project exists.
func (r *projectStore) Exists(ctx context.Context, conn DBTX, projectID string) (bool, error) {
	var exists int
	err := conn.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", projectID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// Create inserts a new project.
func (r *projectStore) Create(ctx context.Context, tx DBTX, id, title string, bootstrapEnabled bool, now, configRevision string) error {
	bootstrapInt := 0
	if bootstrapEnabled {
		bootstrapInt = 1
	}
	var binding any = configRevision
	if configRevision == "" {
		binding = nil
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, config_revision) VALUES (?, ?, 'active', ?, ?, ?)",
		id, title, bootstrapInt, now, binding)
	return err
}

// Rename applies the validated title mutation. Revision allocation is owned by
// the Project Revision Commit protocol.
func (r *projectStore) Rename(ctx context.Context, conn DBTX, projectID, title string) (int64, error) {
	result, err := conn.ExecContext(ctx, "UPDATE projects SET title = ? WHERE id = ?", title, projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// TransitionStatus applies one explicit lifecycle edge.
func (r *projectStore) TransitionStatus(ctx context.Context, conn DBTX, projectID string, fromStatus, toStatus ProjectStatus) (int64, error) {
	result, err := conn.ExecContext(ctx,
		"UPDATE projects SET status = ? WHERE id = ? AND status = ?",
		toStatus, projectID, fromStatus)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ReopenGeneration starts the next generation of a completed project.
func (r *projectStore) ReopenGeneration(ctx context.Context, conn DBTX, projectID string, generation int) (int64, error) {
	result, err := conn.ExecContext(ctx,
		"UPDATE projects SET status = 'active', generation = generation + 1 WHERE id = ? AND status = 'completed' AND generation = ?",
		projectID, generation)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// AdvanceRevision allocates the next revision inside a Blackboard transaction.
func (r *projectStore) AdvanceRevision(ctx context.Context, conn DBTX, projectID string) (int64, error) {
	result, err := conn.ExecContext(ctx,
		"UPDATE projects SET blackboard_revision = blackboard_revision + 1 WHERE id = ?",
		projectID)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, sql.ErrNoRows
	}
	var revision int64
	if err := conn.QueryRowContext(ctx,
		"SELECT blackboard_revision FROM projects WHERE id = ?", projectID,
	).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

// Delete removes a project.
func (r *projectStore) Delete(ctx context.Context, conn DBTX, projectID string) error {
	if _, err := conn.ExecContext(ctx, "UPDATE projects SET deleting = 1 WHERE id = ?", projectID); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", projectID)
	return err
}
