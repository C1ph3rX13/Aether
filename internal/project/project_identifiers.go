package project

import (
	"context"
	"fmt"
	"time"
)

const (
	BootstrapIntentDescription = "bootstrap"
	BootstrapIntentCreator     = "dispatcher.bootstrap"
)

func UTCNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func NextProjectID(ctx context.Context, conn DBTX) (string, error) {
	if _, err := conn.ExecContext(ctx, "UPDATE counters SET value = value + 1 WHERE name = 'project'"); err != nil {
		return "", err
	}
	var value int
	if err := conn.QueryRowContext(ctx, "SELECT value FROM counters WHERE name = 'project'").Scan(&value); err != nil {
		return "", err
	}
	return fmt.Sprintf("proj_%03d", value), nil
}

func nextScopedID(ctx context.Context, conn DBTX, kind, prefix, projectID string) (string, error) {
	if _, err := conn.ExecContext(ctx,
		"INSERT OR IGNORE INTO scoped_counters (project_id, kind, value) VALUES (?, ?, 0)",
		projectID, kind); err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE scoped_counters SET value = value + 1 WHERE project_id = ? AND kind = ?",
		projectID, kind); err != nil {
		return "", err
	}
	var value int
	if err := conn.QueryRowContext(ctx,
		"SELECT value FROM scoped_counters WHERE project_id = ? AND kind = ?",
		projectID, kind).Scan(&value); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%03d", prefix, value), nil
}

func NextFactID(ctx context.Context, conn DBTX, projectID string) (string, error) {
	return nextScopedID(ctx, conn, "fact", "f", projectID)
}

func NextIntentID(ctx context.Context, conn DBTX, projectID string) (string, error) {
	return nextScopedID(ctx, conn, "intent", "i", projectID)
}

func NextHintID(ctx context.Context, conn DBTX, projectID string) (string, error) {
	return nextScopedID(ctx, conn, "hint", "h", projectID)
}
