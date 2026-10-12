package config

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

type ActiveDispatcherPolicy struct {
	ConfigRevision      string
	PollIntervalSeconds int
	MaxWorkers          int
	MaxRunningProjects  int
}

// ReadActiveDispatcherPolicy reads the global policy from the active immutable
// revision through the caller's transaction.
func ReadActiveDispatcherPolicy(
	ctx context.Context,
	db database.DBTX,
) (ActiveDispatcherPolicy, error) {
	id, err := activeRevisionID(ctx, db)
	if err != nil {
		return ActiveDispatcherPolicy{}, err
	}
	if id == "" {
		return ActiveDispatcherPolicy{}, &DomainError{Code: ErrorNotFound, Detail: "active Config Revision not found"}
	}
	draft, err := readPrivateDraft(ctx, db, id)
	if err != nil {
		return ActiveDispatcherPolicy{}, err
	}
	return ActiveDispatcherPolicy{
		ConfigRevision: id, PollIntervalSeconds: draft.Runtime.SchedulerIntervalSeconds,
		MaxWorkers: draft.Runtime.MaxWorkers, MaxRunningProjects: draft.Runtime.MaxRunningProjects,
	}, nil
}

// ReadExecutionConfig returns the immutable revision in the shape consumed by
// the Dispatcher. Secret values are never included; secret version IDs remain
// references in the Worker Catalog and are resolved only for an authorized Run.
func (service *Service) ReadExecutionConfig(ctx context.Context, id string) (*DispatchConfig, error) {
	if service == nil || service.db == nil {
		return nil, fmt.Errorf("configuration service is required")
	}
	conn, err := service.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	draft, err := readPrivateDraft(ctx, conn, id)
	if err != nil {
		return nil, err
	}
	return dispatchConfigFromDraft(id, draft)
}

// DispatchConfigFromRevision reconstructs the immutable execution view from the
// public Revision representation returned by the Control Plane. Secret values
// remain server-side; only opaque Secret Version references are copied into the
// Worker Catalog input.
func DispatchConfigFromRevision(revision ConfigurationRevision) (*DispatchConfig, error) {
	if !validConfigurationRevisionID(revision.ID) {
		return nil, invalidConfiguration("Config Revision ID must be a SHA-256 identity")
	}
	draft := cloneDraft(revision.Configuration)
	for _, reference := range revision.SecretReferences {
		workerDraft := findWorker(&draft, reference.WorkerName)
		if workerDraft == nil {
			return nil, invalidConfiguration("Config Revision Secret reference targets an unknown Worker")
		}
		if reference.Variable == "" || reference.VersionID == "" {
			return nil, invalidConfiguration("Config Revision Secret reference is incomplete")
		}
		if workerDraft.SecretVersions == nil {
			workerDraft.SecretVersions = make(map[string]string)
		}
		workerDraft.SecretVersions[reference.Variable] = reference.VersionID
	}
	draft = normalizeRevisionConfiguration(draft)
	document, err := marshalDraft(draft)
	if err != nil {
		return nil, fmt.Errorf("marshal Config Revision identity: %w", err)
	}
	digest := sha256.Sum256(document)
	if expected := "sha256:" + hex.EncodeToString(digest[:]); revision.ID != expected {
		return nil, invalidConfiguration("Config Revision identity does not match its contents")
	}
	if problems := validateDraft(draft); len(problems) > 0 {
		return nil, fmt.Errorf("Config Revision is incomplete at %s", problems[0].Path)
	}
	if err := validateRevisionCatalog(draft); err != nil {
		return nil, fmt.Errorf("validate Config Revision Worker Catalog: %w", err)
	}
	return dispatchConfigFromDraft(revision.ID, draft)
}

func (service *Service) ReadActiveExecutionConfig(ctx context.Context) (*DispatchConfig, error) {
	if service == nil || service.db == nil {
		return nil, fmt.Errorf("configuration service is required")
	}
	state, err := LoadDeploymentState(ctx, service.db)
	if err != nil {
		return nil, err
	}
	if state.ActiveRevision == "" {
		return nil, &DomainError{Code: ErrorNotFound, Detail: "active Config Revision not found"}
	}
	return service.ReadExecutionConfig(ctx, state.ActiveRevision)
}

func readPrivateDraft(ctx context.Context, db database.DBTX, id string) (DynamicDraft, error) {
	if !validConfigurationRevisionID(id) {
		return DynamicDraft{}, invalidConfiguration("Config Revision ID must be a SHA-256 identity")
	}
	var document string
	if err := db.QueryRowContext(ctx, "SELECT document_json FROM configuration_revisions WHERE id = ?", id).Scan(&document); err != nil {
		if err == sql.ErrNoRows {
			return DynamicDraft{}, &DomainError{Code: ErrorNotFound, Detail: "Config Revision not found"}
		}
		return DynamicDraft{}, fmt.Errorf("read Config Revision: %w", err)
	}
	var draft DynamicDraft
	if err := unmarshalDraft([]byte(document), &draft); err != nil {
		return DynamicDraft{}, fmt.Errorf("decode Config Revision: %w", err)
	}
	return draft, nil
}

func dispatchConfigFromDraft(id string, draft DynamicDraft) (*DispatchConfig, error) {
	if problems := validateDraft(draft); len(problems) > 0 {
		return nil, fmt.Errorf("Config Revision is invalid at %s", problems[0].Path)
	}
	policies := make(map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy, len(draft.Workers))
	for _, configured := range draft.Workers {
		policies[configured.Name] = make(map[rundomain.TaskType]rundomain.TaskExecutionPolicy, len(configured.TaskTypes))
		for _, taskName := range configured.TaskTypes {
			task := rundomain.TaskType(taskName)
			policy, err := taskExecutionPolicy(draft, configured, task)
			if err != nil {
				return nil, err
			}
			policies[configured.Name][task] = policy
		}
	}
	return &DispatchConfig{
		ConfigRevision: id,
		Runtime: RuntimeConfig{
			Interval: draft.Runtime.SchedulerIntervalSeconds, MaxWorkers: draft.Runtime.MaxWorkers,
			MaxRunningProjects: draft.Runtime.MaxRunningProjects, MaxProjectWorkers: draft.Runtime.MaxProjectWorkers,
			HealthcheckTimeout: draft.Runtime.HealthcheckTimeoutSeconds, WorkerHealthcheck: draft.Runtime.WorkerHealthcheck,
			PromptGroup: draft.Runtime.PromptGroup,
		},
		Container: ContainerConfig{
			Image: draft.Container.Image, NetworkMode: draft.Container.NetworkMode,
			CapAdd: append([]string(nil), draft.Container.CapAdd...), CompletedAction: CompletedAction(draft.Container.CompletedAction),
			StdoutLimitBytes: draft.Container.StdoutLimitBytes, StderrLimitBytes: draft.Container.StderrLimitBytes,
		},
		Workers: workerConfigsFromDraft(draft), ExecutionPolicies: policies,
	}, nil
}

// AuthorizeRunAdmission rebuilds all execution values from the Project-bound
// revision and applies current global and bound Project/Worker capacities.
func (service *Service) AuthorizeRunAdmission(
	ctx context.Context,
	tx database.DBTX,
	request rundomain.StartRequest,
	state rundomain.ProjectAdmission,
	now time.Time,
) (rundomain.AdmissionAuthorization, error) {
	if service == nil {
		return rundomain.AdmissionAuthorization{}, &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "configuration service is required"}
	}
	if state.ConfigRevision == "" {
		return rundomain.AdmissionAuthorization{}, &rundomain.DomainError{Code: rundomain.ErrorConfigurationRequired, Detail: "Project has no bound Config Revision"}
	}
	bound, err := readPrivateDraft(ctx, tx, state.ConfigRevision)
	if err != nil {
		return rundomain.AdmissionAuthorization{}, fmt.Errorf("load Project Config Revision: %w", err)
	}
	activeID, err := activeRevisionID(ctx, tx)
	if err != nil {
		return rundomain.AdmissionAuthorization{}, err
	}
	if activeID == "" {
		return rundomain.AdmissionAuthorization{}, &rundomain.DomainError{Code: rundomain.ErrorConfigurationRequired, Detail: "an active Config Revision is required"}
	}
	active, err := readPrivateDraft(ctx, tx, activeID)
	if err != nil {
		return rundomain.AdmissionAuthorization{}, fmt.Errorf("load active Config Revision: %w", err)
	}
	catalog, err := worker.NewCatalogFromConfig(worker.CatalogConfiguration{
		Revision: state.ConfigRevision, ImageReference: bound.Container.Image,
		PromptGroup: bound.Runtime.PromptGroup, Workers: workerConfigsFromDraft(bound),
	})
	if err != nil {
		return rundomain.AdmissionAuthorization{}, &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "Project Config Revision cannot build a Worker Catalog"}
	}
	selectedName := request.Spec.Worker.Name
	selected, ok := catalog.Worker(selectedName)
	if !ok || !containsTask(selected.TaskTypes, request.Task) {
		return rundomain.AdmissionAuthorization{}, &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "selected Worker is not qualified for this task"}
	}
	promptHash, err := catalog.PromptTemplateHash(request.Task)
	if err != nil {
		return rundomain.AdmissionAuthorization{}, err
	}
	policy, err := taskExecutionPolicy(bound, findWorkerByName(bound.Workers, selectedName), request.Task)
	if err != nil {
		return rundomain.AdmissionAuthorization{}, &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "Project Config Revision execution policy is invalid"}
	}
	authoritative := rundomain.AdmissionSpecSnapshot{
		ConfigRevision: state.ConfigRevision, PromptTemplateHash: promptHash,
		Worker:   rundomain.WorkerSnapshot{Name: selected.Name, Adapter: selected.Adapter, Model: selected.Model, Capabilities: selected.Capabilities, SecretReferences: selected.SecretReferences},
		Image:    rundomain.ImageSnapshot{RequestedRef: bound.Container.Image},
		Timeouts: policy.Timeouts, ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: policy.MaxProposedIntents,
	}
	if err := enforceAdmissionCapacity(ctx, tx, request.ProjectID, selectedName, state.ConfigRevision, active.Runtime, bound.Runtime, selected.MaxRunning, now.UnixNano()); err != nil {
		return rundomain.AdmissionAuthorization{}, err
	}
	return rundomain.AdmissionAuthorization{Spec: authoritative, RetryPolicy: policy.RetryPolicy}, nil
}

func activeRevisionID(ctx context.Context, db database.DBTX) (string, error) {
	var id sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT active_revision FROM configuration_state WHERE singleton = 1").Scan(&id); err != nil {
		return "", fmt.Errorf("read active Config Revision: %w", err)
	}
	return id.String, nil
}

func workerConfigsFromDraft(draft DynamicDraft) []worker.WorkerConfig {
	result := make([]worker.WorkerConfig, len(draft.Workers))
	for index, configured := range draft.Workers {
		internal := configured.AgentType
		if internal == "claude_code" {
			internal = string(worker.WorkerTypeClaudeCode)
		}
		environment := cloneStringMap(configured.AgentEnv)
		secretVersionIDs := make(map[string]string, len(configured.SecretVersions))
		for variable, versionID := range configured.SecretVersions {
			environment[variable] = versionID
			secretVersionIDs[variable] = versionID
		}
		result[index] = worker.WorkerConfig{
			Name: configured.Name, Type: worker.WorkerType(internal),
			TaskTypes: toWorkerTasks(configured.TaskTypes), MaxRunning: configured.MaxRunning,
			Priority: configured.Priority, Capabilities: append([]string(nil), configured.Capabilities...),
			Env: environment, SecretVersionIDs: secretVersionIDs,
		}
	}
	return result
}

func containsTask(tasks []rundomain.TaskType, wanted rundomain.TaskType) bool {
	for _, task := range tasks {
		if task == wanted {
			return true
		}
	}
	return false
}

func findWorkerByName(workers []DynamicWorkerDraft, name string) DynamicWorkerDraft {
	for _, configured := range workers {
		if configured.Name == name {
			return configured
		}
	}
	return DynamicWorkerDraft{}
}

func enforceAdmissionCapacity(
	ctx context.Context,
	tx database.DBTX,
	projectID, workerName, configRevision string,
	global, projectRuntime DynamicRuntimeDraft,
	workerLimit int,
	nowUnixNano int64,
) error {
	var activeRuns, activeProjects, projectRuns, workerRuns int
	queries := []struct {
		destination *int
		query       string
		args        []any
	}{
		{&activeRuns, `SELECT COUNT(*) FROM execution_runs r JOIN execution_claims c ON c.run_id = r.id
			WHERE r.status = 'active' AND c.lease_expires_unix_nano > ?`, []any{nowUnixNano}},
		{&activeProjects, `SELECT COUNT(DISTINCT r.project_id) FROM execution_runs r JOIN execution_claims c ON c.run_id = r.id
			WHERE r.status = 'active' AND c.lease_expires_unix_nano > ?`, []any{nowUnixNano}},
		{&projectRuns, `SELECT COUNT(*) FROM execution_runs r JOIN execution_claims c ON c.run_id = r.id
			WHERE r.project_id = ? AND r.status = 'active' AND c.lease_expires_unix_nano > ?`, []any{projectID, nowUnixNano}},
		{&workerRuns, `SELECT COUNT(*) FROM execution_runs r JOIN execution_claims c ON c.run_id = r.id
			WHERE r.config_revision = ? AND r.worker_name = ? AND r.status = 'active' AND c.lease_expires_unix_nano > ?`, []any{configRevision, workerName, nowUnixNano}},
	}
	for _, item := range queries {
		if err := tx.QueryRowContext(ctx, item.query, item.args...).Scan(item.destination); err != nil {
			return fmt.Errorf("read Run capacity: %w", err)
		}
	}
	if activeRuns >= global.MaxWorkers || activeProjects >= global.MaxRunningProjects && projectRuns == 0 || projectRuns >= projectRuntime.MaxProjectWorkers || workerRuns >= workerLimit {
		return &rundomain.DomainError{Code: rundomain.ErrorCapacityExceeded, Detail: "Run capacity is exhausted"}
	}
	return nil
}

var _ rundomain.AdmissionAuthority = (*Service)(nil)
