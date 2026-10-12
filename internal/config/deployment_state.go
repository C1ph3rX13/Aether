package config

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

const initialDraftVersion int64 = 1

// ValidateDeploymentDatabase rejects pre-Dynamic Configuration Project data.
// Schema evolution and existing configured deployments remain supported.
func ValidateDeploymentDatabase(ctx context.Context, db *database.DB) error {
	conn, err := db.GetReadConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var obsolete bool
	if err := conn.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM projects WHERE config_revision IS NULL)
	`).Scan(&obsolete); err != nil {
		return fmt.Errorf("validate deployment database: %w", err)
	}
	if obsolete {
		return fmt.Errorf("existing Projects have no Dynamic Deployment Configuration; use a new database and configure the deployment")
	}
	return nil
}

type DynamicDraft struct {
	Runtime   DynamicRuntimeDraft   `json:"runtime"`
	Tasks     DynamicTasksDraft     `json:"tasks"`
	Retry     DynamicRetryDraft     `json:"retry"`
	Container DynamicContainerDraft `json:"container"`
	Workers   []DynamicWorkerDraft  `json:"workers"`
}

type DynamicRuntimeDraft struct {
	SchedulerIntervalSeconds  int    `json:"scheduler_interval_seconds"`
	MaxWorkers                int    `json:"max_workers"`
	MaxRunningProjects        int    `json:"max_running_projects"`
	MaxProjectWorkers         int    `json:"max_project_workers"`
	HealthcheckTimeoutSeconds int    `json:"healthcheck_timeout_seconds"`
	WorkerHealthcheck         string `json:"worker_healthcheck"`
	PromptGroup               string `json:"prompt_group"`
}

type DynamicTasksDraft struct {
	Bootstrap DynamicPhasedTaskDraft `json:"bootstrap"`
	Reason    DynamicReasonTaskDraft `json:"reason"`
	Explore   DynamicPhasedTaskDraft `json:"explore"`
}

type DynamicPhasedTaskDraft struct {
	PrimarySeconds  int `json:"primary_seconds"`
	ConcludeSeconds int `json:"conclude_seconds"`
	RecoverySeconds int `json:"recovery_seconds"`
}

type DynamicReasonTaskDraft struct {
	PrimarySeconds int `json:"primary_seconds"`
	MaxIntents     int `json:"max_intents"`
}

type DynamicRetryDraft struct {
	Bootstrap DynamicRetryPolicyDraft `json:"bootstrap"`
	Reason    DynamicRetryPolicyDraft `json:"reason"`
	Explore   DynamicRetryPolicyDraft `json:"explore"`
}

type DynamicRetryPolicyDraft struct {
	MaxAttempts            int     `json:"max_attempts"`
	InitialIntervalSeconds int     `json:"initial_interval_seconds"`
	MaxIntervalSeconds     int     `json:"max_interval_seconds"`
	Multiplier             float64 `json:"multiplier"`
}

type DynamicContainerDraft struct {
	Image            string   `json:"image"`
	NetworkMode      string   `json:"network_mode"`
	CapAdd           []string `json:"cap_add"`
	CompletedAction  string   `json:"completed_action"`
	StdoutLimitBytes int64    `json:"stdout_limit_bytes"`
	StderrLimitBytes int64    `json:"stderr_limit_bytes"`
}

type DeploymentState struct {
	DeploymentID   string
	DraftVersion   int64
	Draft          DynamicDraft
	ActiveRevision string
}

func RecommendedDynamicDraft() DynamicDraft {
	retry := DynamicRetryPolicyDraft{
		MaxAttempts: 3, InitialIntervalSeconds: 30, MaxIntervalSeconds: 300, Multiplier: 2,
	}
	return DynamicDraft{
		Runtime: DynamicRuntimeDraft{
			SchedulerIntervalSeconds: 3, MaxWorkers: 8, MaxRunningProjects: 3,
			MaxProjectWorkers: 4, HealthcheckTimeoutSeconds: 20,
			WorkerHealthcheck: "startup_only", PromptGroup: "default",
		},
		Tasks: DynamicTasksDraft{
			Bootstrap: DynamicPhasedTaskDraft{PrimarySeconds: 600, ConcludeSeconds: 90, RecoverySeconds: 30},
			Reason:    DynamicReasonTaskDraft{PrimarySeconds: 450, MaxIntents: 2},
			Explore:   DynamicPhasedTaskDraft{PrimarySeconds: 600, ConcludeSeconds: 90, RecoverySeconds: 30},
		},
		Retry: DynamicRetryDraft{Bootstrap: retry, Reason: retry, Explore: retry},
		Container: DynamicContainerDraft{
			Image: "ghcr.io/c1ph3rx13/aether-worker:latest", NetworkMode: "host",
			CapAdd: []string{}, CompletedAction: "stop",
			StdoutLimitBytes: DefaultOutputLimitBytes, StderrLimitBytes: DefaultOutputLimitBytes,
		},
		Workers: []DynamicWorkerDraft{}}
}

// InitializeDeployment creates the dormant deployment state for a new database
// and rejects a Bootstrap Manifest for a different deployment identity.
func InitializeDeployment(ctx context.Context, db *database.DB, deploymentID string) (*DeploymentState, error) {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return nil, fmt.Errorf("deployment ID is required")
	}
	draft := RecommendedDynamicDraft()
	draftJSON, err := marshalDraft(draft)
	if err != nil {
		return nil, fmt.Errorf("marshal recommended Dynamic Configuration Draft: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO configuration_state (
			singleton, deployment_id, draft_version, draft_json, active_revision, updated_at
		) VALUES (1, ?, ?, ?, NULL, ?)`,
		deploymentID, initialDraftVersion, string(draftJSON), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("initialize Dynamic Configuration state: %w", err)
	}
	state, err := loadDeploymentState(ctx, tx)
	if err != nil {
		return nil, err
	}
	if state.DeploymentID != deploymentID {
		return nil, fmt.Errorf("Bootstrap deployment ID %q does not match database deployment %q", deploymentID, state.DeploymentID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return state, nil
}

func LoadDeploymentState(ctx context.Context, db *database.DB) (*DeploymentState, error) {
	conn, err := db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return loadDeploymentState(ctx, conn)
}

func loadDeploymentState(ctx context.Context, db database.DBTX) (*DeploymentState, error) {
	var state DeploymentState
	var draftJSON string
	var activeRevision sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT deployment_id, draft_version, draft_json, active_revision
		FROM configuration_state WHERE singleton = 1`,
	).Scan(&state.DeploymentID, &state.DraftVersion, &draftJSON, &activeRevision); err != nil {
		return nil, fmt.Errorf("load Dynamic Configuration state: %w", err)
	}
	if err := unmarshalDraft([]byte(draftJSON), &state.Draft); err != nil {
		return nil, fmt.Errorf("decode Dynamic Configuration Draft: %w", err)
	}
	state.ActiveRevision = activeRevision.String
	return &state, nil
}

func HasActiveRevision(ctx context.Context, db *database.DB) (bool, error) {
	state, err := LoadDeploymentState(ctx, db)
	if err != nil {
		return false, err
	}
	return state.ActiveRevision != "", nil
}

// ActiveRevisionID reads the active Config Revision through the caller's
// transaction so Project creation can bind and persist it atomically.
func ActiveRevisionID(ctx context.Context, db database.DBTX) (string, error) {
	var id sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT active_revision FROM configuration_state WHERE singleton = 1
	`).Scan(&id); err != nil {
		return "", fmt.Errorf("read active Config Revision: %w", err)
	}
	return id.String, nil
}
