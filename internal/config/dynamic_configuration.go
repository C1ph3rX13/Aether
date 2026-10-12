package config

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
	"github.com/google/uuid"
)

const (
	maxConfigurationWorkers  = 256
	maxWorkerNameBytes       = 256
	maxAgentEnvironmentVars  = 256
	maxAgentEnvironmentBytes = 256 * 1024
	maxWorkerPriority        = 1024
	maxWorkerRunning         = 1024
)

type DynamicSecretState struct {
	Configured bool   `json:"configured"`
	Mask       string `json:"mask,omitempty"`
}

type DynamicWorkerDraft struct {
	Name             string                        `json:"name"`
	AgentType        string                        `json:"agent_type"`
	ProviderName     string                        `json:"provider_name,omitempty"`
	TaskTypes        []string                      `json:"task_types"`
	MaxRunning       int                           `json:"max_running"`
	Priority         int                           `json:"priority"`
	Capabilities     []string                      `json:"capabilities"`
	AgentEnv         map[string]string             `json:"agent_env"`
	TimeoutOverrides map[string]int                `json:"timeout_overrides"`
	Secrets          map[string]DynamicSecretState `json:"secrets,omitempty"`

	// SecretVersions is private state. It is persisted in the Draft document
	// and never appears in a public Draft or configuration response.
	SecretVersions map[string]string `json:"-"`
}

type WorkerSecretChange struct {
	Value string `json:"value,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}

type DynamicDraftSaveRequest struct {
	ExpectedDraftVersion int64                                    `json:"expected_draft_version"`
	Draft                DynamicDraft                             `json:"draft"`
	Secrets              map[string]map[string]WorkerSecretChange `json:"secrets,omitempty"`
}

type DynamicWorkerDeleteRequest struct {
	ExpectedDraftVersion int64 `json:"expected_draft_version"`
}

type ValidationProblem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ActiveRevisionSummary struct {
	ID string `json:"id"`
}

type ConfigurationRead struct {
	DeploymentID       string                           `json:"deployment_id"`
	DraftVersion       int64                            `json:"draft_version"`
	Draft              DynamicDraft                     `json:"draft"`
	ActiveRevision     *ActiveRevisionSummary           `json:"active_revision,omitempty"`
	ValidationProblems []ValidationProblem              `json:"validation_problems"`
	AgentDefinitions   []worker.AgentDefinitionMetadata `json:"agent_definitions"`
	ProviderPresets    []worker.ProviderPreset          `json:"provider_presets"`
	PromptGroups       []string                         `json:"prompt_groups"`
	RecommendedDraft   DynamicDraft                     `json:"recommended_draft"`
}

type ErrorCode string

const (
	ErrorInvalidArgument ErrorCode = "invalid_argument"
	ErrorConflict        ErrorCode = "conflict"
	ErrorValidation      ErrorCode = "validation_failed"
	ErrorProvider        ErrorCode = "provider_check_failed"
	ErrorNotFound        ErrorCode = "not_found"
	ErrorInternal        ErrorCode = "internal_error"
)

type DomainError struct {
	Code                ErrorCode
	Detail              string
	CurrentDraftVersion *int64
	ValidationProblems  []ValidationProblem
}

func (e *DomainError) Error() string { return e.Detail }

func invalidConfiguration(detail string) *DomainError {
	return &DomainError{Code: ErrorInvalidArgument, Detail: detail}
}

func configurationConflict(detail string, version int64) *DomainError {
	return &DomainError{Code: ErrorConflict, Detail: detail, CurrentDraftVersion: &version}
}

func validationError(problems []ValidationProblem) *DomainError {
	return &DomainError{Code: ErrorValidation, Detail: "Dynamic Configuration Draft is invalid", ValidationProblems: problems}
}

type ProviderHealthcheckFunc func(context.Context, worker.CatalogWorker, map[string]string) error

type ServiceOptions struct {
	OperatorName       string
	Healthcheck        ProviderHealthcheckFunc
	HealthcheckTimeout time.Duration
}

type Service struct {
	db           *database.DB
	operatorName string
	healthcheck  ProviderHealthcheckFunc
}

func NewService(db *database.DB, options ServiceOptions) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("configuration database is required")
	}
	operatorName := strings.TrimSpace(options.OperatorName)
	if operatorName == "" {
		return nil, fmt.Errorf("configuration Operator name is required")
	}
	healthcheck := options.Healthcheck
	if healthcheck == nil {
		timeout := options.HealthcheckTimeout
		if timeout == 0 {
			timeout = 20 * time.Second
		}
		healthcheck = DefaultProviderHealthcheck(timeout)
	}
	return &Service{db: db, operatorName: operatorName, healthcheck: healthcheck}, nil
}

func (service *Service) Read(ctx context.Context) (*ConfigurationRead, error) {
	state, err := LoadDeploymentState(ctx, service.db)
	if err != nil {
		return nil, err
	}
	return service.readState(ctx, state)
}

func (service *Service) SaveDraft(ctx context.Context, request DynamicDraftSaveRequest) (*ConfigurationRead, error) {
	if request.ExpectedDraftVersion <= 0 {
		return nil, invalidConfiguration("expected_draft_version must be a positive integer")
	}
	state, err := LoadDeploymentState(ctx, service.db)
	if err != nil {
		return nil, err
	}
	if state.DraftVersion != request.ExpectedDraftVersion {
		return nil, configurationConflict("Dynamic Configuration Draft version changed", state.DraftVersion)
	}
	candidate := cloneDraft(request.Draft)
	secretValues, pending, err := service.prepareCandidate(ctx, state.Draft, &candidate, request.Secrets)
	if err != nil {
		return nil, err
	}
	for index := range pending {
		pending[index].ID = "secret_" + uuid.NewString()
		configured := findWorker(&candidate, pending[index].WorkerName)
		configured.SecretVersions[pending[index].VariableName] = pending[index].ID
	}
	if problems := validateDraft(candidate); len(problems) > 0 {
		return nil, validationError(problems)
	}
	if err := service.checkChangedWorkers(ctx, state.Draft, candidate, secretValues, pending); err != nil {
		return nil, err
	}

	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentVersion int64
	if err := tx.QueryRowContext(ctx, "SELECT draft_version FROM configuration_state WHERE singleton = 1").Scan(&currentVersion); err != nil {
		return nil, fmt.Errorf("read Dynamic Configuration Draft version: %w", err)
	}
	if currentVersion != request.ExpectedDraftVersion {
		return nil, configurationConflict("Dynamic Configuration Draft version changed", currentVersion)
	}
	for index := range pending {
		id := pending[index].ID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO worker_secret_versions (id, worker_name, agent_type, variable_name, secret_value, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, id, pending[index].WorkerName, pending[index].AgentType,
			pending[index].VariableName, pending[index].Value, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, fmt.Errorf("save Worker Secret Version: %w", err)
		}
	}
	draftJSON, err := marshalDraft(candidate)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE configuration_state SET draft_version = draft_version + 1, draft_json = ?, updated_at = ?
		WHERE singleton = 1 AND draft_version = ?`, string(draftJSON), time.Now().UTC().Format(time.RFC3339Nano), request.ExpectedDraftVersion)
	if err != nil {
		return nil, fmt.Errorf("save Dynamic Configuration Draft: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		if err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT draft_version FROM configuration_state WHERE singleton = 1").Scan(&currentVersion); err != nil {
			return nil, err
		}
		return nil, configurationConflict("Dynamic Configuration Draft version changed", currentVersion)
	}
	revision, err := service.publishConfiguration(ctx, tx, candidate)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return service.readState(ctx, &DeploymentState{DeploymentID: state.DeploymentID, DraftVersion: currentVersion + 1, Draft: candidate, ActiveRevision: revision})
}

func (service *Service) DeleteWorker(ctx context.Context, workerName string, request DynamicWorkerDeleteRequest) (*ConfigurationRead, error) {
	if request.ExpectedDraftVersion <= 0 {
		return nil, invalidConfiguration("expected_draft_version must be a positive integer")
	}
	workerName = strings.TrimSpace(workerName)
	if workerName == "" {
		return nil, invalidConfiguration("Worker name is required")
	}
	state, err := LoadDeploymentState(ctx, service.db)
	if err != nil {
		return nil, err
	}
	if state.DraftVersion != request.ExpectedDraftVersion {
		return nil, configurationConflict("Dynamic Configuration Draft version changed", state.DraftVersion)
	}
	index := -1
	for candidateIndex, candidate := range state.Draft.Workers {
		if candidate.Name == workerName {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return nil, &DomainError{Code: ErrorNotFound, Detail: fmt.Sprintf("Worker %q not found", workerName)}
	}
	candidate := cloneDraft(state.Draft)
	candidate.Workers = append(candidate.Workers[:index], candidate.Workers[index+1:]...)
	if problems := validateDraft(candidate); len(problems) > 0 {
		return nil, validationError(problems)
	}
	draftJSON, err := marshalDraft(candidate)
	if err != nil {
		return nil, err
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentVersion int64
	if err := tx.QueryRowContext(ctx, "SELECT draft_version FROM configuration_state WHERE singleton = 1").Scan(&currentVersion); err != nil {
		return nil, fmt.Errorf("read Dynamic Configuration Draft version: %w", err)
	}
	if currentVersion != request.ExpectedDraftVersion {
		return nil, configurationConflict("Dynamic Configuration Draft version changed", currentVersion)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE configuration_state SET draft_version = draft_version + 1, draft_json = ?, updated_at = ?
		WHERE singleton = 1 AND draft_version = ?`, string(draftJSON), time.Now().UTC().Format(time.RFC3339Nano), request.ExpectedDraftVersion)
	if err != nil {
		return nil, fmt.Errorf("delete Worker from Dynamic Configuration Draft: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		if err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT draft_version FROM configuration_state WHERE singleton = 1").Scan(&currentVersion); err != nil {
			return nil, err
		}
		return nil, configurationConflict("Dynamic Configuration Draft version changed", currentVersion)
	}
	revision, err := service.publishConfiguration(ctx, tx, candidate)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return service.readState(ctx, &DeploymentState{DeploymentID: state.DeploymentID, DraftVersion: currentVersion + 1, Draft: candidate, ActiveRevision: revision})
}

type pendingSecret struct {
	ID, WorkerName, AgentType, VariableName, Value string
}

func (service *Service) prepareCandidate(ctx context.Context, current DynamicDraft, candidate *DynamicDraft, changes map[string]map[string]WorkerSecretChange) (map[string]map[string]string, []pendingSecret, error) {
	values := make(map[string]map[string]string)
	pending := make([]pendingSecret, 0)
	currentValues, err := service.loadSecretValues(ctx, current)
	if err != nil {
		return nil, nil, err
	}
	for workerName := range changes {
		if findWorker(candidate, workerName) == nil {
			return nil, nil, invalidConfiguration(fmt.Sprintf("Secret changes target unknown Worker %q", workerName))
		}
	}
	for index := range candidate.Workers {
		workerDraft := &candidate.Workers[index]
		workerDraft.SecretVersions = map[string]string{}
		currentWorker := findWorker(&current, workerDraft.Name)
		if currentWorker == nil || currentWorker.AgentType != workerDraft.AgentType {
			workerDraft.SecretVersions = map[string]string{}
		}
		definition, definitionErr := definitionForDraft(workerDraft.AgentType)
		if definitionErr != nil {
			continue
		}
		secretSlots := map[string]bool{}
		for _, slot := range definition.RequiredSecretVariables() {
			secretSlots[slot] = true
		}
		workerValues := make(map[string]string)
		for _, variable := range definition.RequiredSecretVariables() {
			if currentWorker != nil && currentWorker.AgentType == workerDraft.AgentType {
				if id := currentWorker.SecretVersions[variable]; id != "" {
					workerDraft.SecretVersions[variable] = id
					workerValues[variable] = currentValues[id]
				}
			}
			change, present := changes[workerDraft.Name][variable]
			if !present || change.Value == "" {
				continue
			}
			if change.Clear {
				return nil, nil, invalidConfiguration(fmt.Sprintf("secret change %s.%s cannot replace and clear at once", workerDraft.Name, variable))
			}
			workerValues[variable] = change.Value
			delete(workerDraft.SecretVersions, variable)
		}
		for variable, change := range changes[workerDraft.Name] {
			if _, allowed := secretSlots[variable]; !allowed {
				return nil, nil, invalidConfiguration(fmt.Sprintf("secret slot %s.%s is not declared by its Agent Definition", workerDraft.Name, variable))
			}
			if change.Clear && change.Value != "" {
				return nil, nil, invalidConfiguration(fmt.Sprintf("secret change %s.%s cannot replace and clear at once", workerDraft.Name, variable))
			}
			if change.Clear {
				delete(workerDraft.SecretVersions, variable)
				delete(workerValues, variable)
			}
		}
		values[workerDraft.Name] = workerValues
		for variable, value := range workerValues {
			if currentWorker == nil || currentWorker.AgentType != workerDraft.AgentType || currentWorker.SecretVersions[variable] != workerDraft.SecretVersions[variable] || changes[workerDraft.Name][variable].Value != "" {
				pending = append(pending, pendingSecret{WorkerName: workerDraft.Name, AgentType: workerDraft.AgentType, VariableName: variable, Value: value})
				delete(workerDraft.SecretVersions, variable)
			}
		}
	}
	return values, pending, nil
}

func (service *Service) loadSecretValues(ctx context.Context, draft DynamicDraft) (map[string]string, error) {
	ids := map[string]struct{}{}
	for _, configured := range draft.Workers {
		for _, id := range configured.SecretVersions {
			if id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	values := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return values, nil
	}
	conn, err := service.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	for id := range ids {
		var value string
		if err := conn.QueryRowContext(ctx, "SELECT secret_value FROM worker_secret_versions WHERE id = ?", id).Scan(&value); err != nil {
			return nil, fmt.Errorf("load Worker Secret Version: %w", err)
		}
		values[id] = value
	}
	return values, nil
}

func (service *Service) checkChangedWorkers(ctx context.Context, current, candidate DynamicDraft, secretValues map[string]map[string]string, pending []pendingSecret) error {
	pendingByWorker := map[string]bool{}
	for _, change := range pending {
		pendingByWorker[change.WorkerName] = true
	}
	for _, configured := range candidate.Workers {
		if !workerChanged(current, candidate, configured.Name) && !pendingByWorker[configured.Name] {
			continue
		}
		catalogWorker, err := catalogWorkerForDraft(configured, secretValues[configured.Name])
		if err != nil {
			return validationError([]ValidationProblem{{Path: "workers." + configured.Name, Message: err.Error()}})
		}
		if err := service.healthcheck(ctx, catalogWorker, secretValues[configured.Name]); err != nil {
			return &DomainError{Code: ErrorProvider, Detail: fmt.Sprintf("Worker %q Provider healthcheck failed: %s", configured.Name, compactHealthcheckError(err, secretValues[configured.Name]))}
		}
	}
	return nil
}

func compactHealthcheckError(err error, secrets map[string]string) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	if len(message) > 200 {
		return message[:200] + "..."
	}
	return message
}

func (service *Service) readState(_ context.Context, state *DeploymentState) (*ConfigurationRead, error) {
	public := publicDraft(state.Draft)
	read := &ConfigurationRead{
		DeploymentID: state.DeploymentID, DraftVersion: state.DraftVersion, Draft: public,
		ValidationProblems: append(validateDraft(state.Draft), coverageProblems(state.Draft)...),
		AgentDefinitions:   worker.SupportedAgentDefinitionMetadata(), ProviderPresets: worker.ProviderPresets(),
		PromptGroups:     worker.BundledPromptGroups(),
		RecommendedDraft: publicDraft(RecommendedDynamicDraft()),
	}
	if state.ActiveRevision != "" {
		read.ActiveRevision = &ActiveRevisionSummary{ID: state.ActiveRevision}
	}
	return read, nil
}

func publicDraft(draft DynamicDraft) DynamicDraft {
	result := cloneDraft(draft)
	for index := range result.Workers {
		configured := &result.Workers[index]
		configured.Secrets = map[string]DynamicSecretState{}
		if definition, err := definitionForDraft(configured.AgentType); err == nil {
			for _, variable := range definition.RequiredSecretVariables() {
				configured.Secrets[variable] = DynamicSecretState{}
			}
		}
		for variable, id := range configured.SecretVersions {
			if id != "" {
				configured.Secrets[variable] = DynamicSecretState{Configured: true, Mask: "********"}
			}
		}
		configured.SecretVersions = nil
	}
	return result
}

func validateDraft(draft DynamicDraft) []ValidationProblem {
	var problems []ValidationProblem
	add := func(path, message string) {
		problems = append(problems, ValidationProblem{Path: path, Message: message})
	}
	if draft.Runtime.SchedulerIntervalSeconds <= 0 || draft.Runtime.SchedulerIntervalSeconds > 3600 {
		add("runtime.scheduler_interval_seconds", "must be between 1 and 3600")
	}
	if draft.Runtime.MaxWorkers <= 0 || draft.Runtime.MaxWorkers > maxWorkerRunning {
		add("runtime.max_workers", "must be between 1 and 1024")
	}
	if draft.Runtime.MaxRunningProjects <= 0 || draft.Runtime.MaxRunningProjects > maxWorkerRunning {
		add("runtime.max_running_projects", "must be between 1 and 1024")
	}
	if draft.Runtime.MaxProjectWorkers <= 0 || draft.Runtime.MaxProjectWorkers > maxWorkerRunning {
		add("runtime.max_project_workers", "must be between 1 and 1024")
	}
	if draft.Runtime.HealthcheckTimeoutSeconds <= 0 || draft.Runtime.HealthcheckTimeoutSeconds > 300 {
		add("runtime.healthcheck_timeout_seconds", "must be between 1 and 300")
	}
	if draft.Runtime.WorkerHealthcheck != "disabled" && draft.Runtime.WorkerHealthcheck != "startup_only" && draft.Runtime.WorkerHealthcheck != "startup_and_task" {
		add("runtime.worker_healthcheck", "must be disabled, startup_only, or startup_and_task")
	}
	if err := worker.ValidatePromptGroup(draft.Runtime.PromptGroup); err != nil {
		add("runtime.prompt_group", err.Error())
	}

	validateTaskDraft(&problems, draft)
	validateRetryDraft(&problems, draft)
	if strings.TrimSpace(draft.Container.Image) == "" {
		add("container.image", "is required")
	}
	if strings.TrimSpace(draft.Container.Image) != draft.Container.Image || len(draft.Container.Image) > 512 || strings.IndexByte(draft.Container.Image, 0) >= 0 {
		add("container.image", "must be canonical, at most 512 bytes, and NUL-free")
	}
	if strings.TrimSpace(draft.Container.NetworkMode) == "" {
		add("container.network_mode", "is required")
	}
	if strings.TrimSpace(draft.Container.NetworkMode) != draft.Container.NetworkMode || len(draft.Container.NetworkMode) > 256 || strings.IndexByte(draft.Container.NetworkMode, 0) >= 0 {
		add("container.network_mode", "must be canonical, at most 256 bytes, and NUL-free")
	}
	seenContainerCapabilities := map[string]bool{}
	for index, capability := range draft.Container.CapAdd {
		path := fmt.Sprintf("container.cap_add[%d]", index)
		if strings.TrimSpace(capability) == "" || strings.TrimSpace(capability) != capability || len(capability) > 128 || strings.IndexByte(capability, 0) >= 0 {
			add(path, "must be canonical, non-empty, at most 128 bytes, and NUL-free")
		}
		if seenContainerCapabilities[capability] {
			add(path, "contains a duplicate capability")
		}
		seenContainerCapabilities[capability] = true
	}
	if draft.Container.CompletedAction != "" && draft.Container.CompletedAction != "stop" && draft.Container.CompletedAction != "remove" {
		add("container.completed_action", "must be stop or remove")
	}
	if draft.Container.CompletedAction == "" {
		add("container.completed_action", "is required")
	}
	if draft.Container.StdoutLimitBytes != 0 && (draft.Container.StdoutLimitBytes < MinOutputLimitBytes || draft.Container.StdoutLimitBytes > MaxOutputLimitBytes) {
		add("container.stdout_limit_bytes", "is outside the allowed output limit")
	}
	if draft.Container.StderrLimitBytes != 0 && (draft.Container.StderrLimitBytes < MinOutputLimitBytes || draft.Container.StderrLimitBytes > MaxOutputLimitBytes) {
		add("container.stderr_limit_bytes", "is outside the allowed output limit")
	}
	if draft.Container.StdoutLimitBytes == 0 {
		add("container.stdout_limit_bytes", "is required")
	}
	if draft.Container.StderrLimitBytes == 0 {
		add("container.stderr_limit_bytes", "is required")
	}
	if len(draft.Workers) > maxConfigurationWorkers {
		add("workers", "contains too many Workers")
	}
	seen := map[string]bool{}
	for index, configured := range draft.Workers {
		path := fmt.Sprintf("workers[%d]", index)
		if configured.Name == "" || strings.TrimSpace(configured.Name) != configured.Name || strings.IndexByte(configured.Name, 0) >= 0 || len(configured.Name) > maxWorkerNameBytes {
			add(path+".name", "must be canonical, non-empty, NUL-free, and at most 256 bytes")
		}
		if seen[configured.Name] {
			add(path+".name", "Worker names must be unique")
		}
		seen[configured.Name] = true
		if _, err := definitionForDraft(configured.AgentType); err != nil {
			add(path+".agent_type", err.Error())
		}
		if configured.ProviderName != "" && len(configured.ProviderName) > 256 {
			add(path+".provider_name", "is too long")
		}
		seenTasks := map[string]bool{}
		for _, task := range configured.TaskTypes {
			if task != string(rundomain.TaskBootstrap) && task != string(rundomain.TaskReason) && task != string(rundomain.TaskExplore) {
				add(path+".task_types", "contains an unsupported task")
			}
			if seenTasks[task] {
				add(path+".task_types", "contains a duplicate task")
			}
			seenTasks[task] = true
		}
		if len(configured.TaskTypes) == 0 {
			add(path+".task_types", "must not be empty")
		}
		if configured.MaxRunning < 0 || configured.MaxRunning > maxWorkerRunning {
			add(path+".max_running", "must be between 1 and 1024")
		}
		if configured.MaxRunning == 0 {
			add(path+".max_running", "must be between 1 and 1024")
		}
		if configured.Priority < 0 || configured.Priority > maxWorkerPriority {
			add(path+".priority", "must be between 0 and 1024")
		}
		if len(configured.AgentEnv) > maxAgentEnvironmentVars {
			add(path+".agent_env", "contains too many variables")
		}
		environmentBytes := 0
		definition, definitionErr := definitionForDraft(configured.AgentType)
		secretSlots := map[string]bool{}
		if definitionErr == nil {
			for _, slot := range definition.RequiredSecretVariables() {
				secretSlots[slot] = true
			}
		}
		for variable, value := range configured.AgentEnv {
			if variable == "" || len(variable) > 128 || strings.ContainsAny(variable, "=\x00") {
				add(path+".agent_env", "contains an invalid variable name")
			}
			if value == "" || strings.IndexByte(value, 0) >= 0 {
				add(path+".agent_env."+variable, "must be non-empty and NUL-free")
			}
			if reservedEnvironmentVariable(variable) {
				add(path+".agent_env."+variable, "is reserved by Aether")
			}
			if secretSlots[variable] {
				add(path+".agent_env."+variable, "is a Definition Secret slot")
			}
			environmentBytes += len(variable) + len(value) + 1
			if environmentBytes > maxAgentEnvironmentBytes {
				add(path+".agent_env", "exceeds the Exec environment byte limit")
			}
			if definitionErr == nil && strings.HasSuffix(variable, "BASE_URL") && containsString(definition.RequiredEnvironmentVariables(), variable) && !validHTTPURL(value) {
				add(path+".agent_env."+variable, "must be an HTTP URL")
			}
		}
		if definitionErr == nil {
			if err := definition.ValidateAdapterEnvironment(configured.Name, configured.AgentEnv); err != nil {
				add(path+".agent_env", err.Error())
			}
		}
		for phase, seconds := range configured.TimeoutOverrides {
			if !validTimeoutPhase(phase) {
				add(path+".timeout_overrides."+phase, "is not a supported task phase")
			}
			task, _, _ := strings.Cut(phase, ".")
			if validTimeoutPhase(phase) && !seenTasks[task] {
				add(path+".timeout_overrides."+phase, "requires the Worker to support "+task)
			}
			if seconds <= 0 || seconds > 24*60*60 {
				add(path+".timeout_overrides."+phase, "must be between 1 and 86400 seconds")
			}
		}
		seenCapabilities := map[string]bool{}
		for _, capability := range configured.Capabilities {
			if capability != string(worker.ProcessRecoveryCapability) {
				add(path+".capabilities", "contains an unsupported capability")
			}
			if seenCapabilities[capability] {
				add(path+".capabilities", "contains a duplicate capability")
			}
			seenCapabilities[capability] = true
		}

		if definitionErr == nil {
			for _, required := range definition.RequiredEnvironmentVariables() {
				if strings.TrimSpace(configured.AgentEnv[required]) == "" {
					add(path+".agent_env."+required, "required")
				}
			}
			for _, required := range definition.RequiredSecretVariables() {
				if configured.SecretVersions[required] == "" {
					add(path+".secrets."+required, "required")
				}
			}
		}

	}

	sortValidationProblems(problems)
	return problems
}

func sortValidationProblems(problems []ValidationProblem) {
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].Path != problems[j].Path {
			return problems[i].Path < problems[j].Path
		}
		return problems[i].Message < problems[j].Message
	})
}

func validateTaskDraft(problems *[]ValidationProblem, draft DynamicDraft) {
	checkPhased := func(name string, task DynamicPhasedTaskDraft) {
		values := []struct {
			name  string
			value int
		}{{"primary_seconds", task.PrimarySeconds}, {"conclude_seconds", task.ConcludeSeconds}, {"recovery_seconds", task.RecoverySeconds}}
		for _, value := range values {
			if value.value < 0 || value.value > 24*60*60 {
				*problems = append(*problems, ValidationProblem{Path: "tasks." + name + "." + value.name, Message: "must be between 1 and 86400 seconds"})
			}
			if value.value == 0 {
				*problems = append(*problems, ValidationProblem{Path: "tasks." + name + "." + value.name, Message: "is required"})
			}
		}
	}
	checkPhased("bootstrap", draft.Tasks.Bootstrap)
	checkPhased("explore", draft.Tasks.Explore)
	if draft.Tasks.Reason.PrimarySeconds < 0 || draft.Tasks.Reason.PrimarySeconds > 24*60*60 || draft.Tasks.Reason.PrimarySeconds == 0 {
		*problems = append(*problems, ValidationProblem{Path: "tasks.reason.primary_seconds", Message: "must be between 1 and 86400 seconds"})
	}
	if draft.Tasks.Reason.MaxIntents < 0 || draft.Tasks.Reason.MaxIntents > rundomain.MaximumReasonProposedIntents || draft.Tasks.Reason.MaxIntents == 0 {
		*problems = append(*problems, ValidationProblem{Path: "tasks.reason.max_intents", Message: fmt.Sprintf("must be between 1 and %d", rundomain.MaximumReasonProposedIntents)})
	}
}

func validateRetryDraft(problems *[]ValidationProblem, draft DynamicDraft) {
	for name, retry := range map[string]DynamicRetryPolicyDraft{"bootstrap": draft.Retry.Bootstrap, "reason": draft.Retry.Reason, "explore": draft.Retry.Explore} {
		if retry.MaxAttempts < 0 || retry.MaxAttempts > rundomain.MaximumRetryAttempts || retry.MaxAttempts == 0 {
			*problems = append(*problems, ValidationProblem{Path: "retry." + name + ".max_attempts", Message: fmt.Sprintf("must be between 1 and %d", rundomain.MaximumRetryAttempts)})
		}
		if retry.InitialIntervalSeconds < 0 || retry.MaxIntervalSeconds < 0 || retry.InitialIntervalSeconds > retry.MaxIntervalSeconds {
			*problems = append(*problems, ValidationProblem{Path: "retry." + name, Message: "backoff intervals are invalid"})
		}
		if retry.InitialIntervalSeconds == 0 || retry.MaxIntervalSeconds == 0 {
			*problems = append(*problems, ValidationProblem{Path: "retry." + name, Message: "backoff intervals are required"})
		}
		if math.IsNaN(retry.Multiplier) || math.IsInf(retry.Multiplier, 0) || retry.Multiplier < 0 || retry.Multiplier > 10 || retry.Multiplier < 1 {
			*problems = append(*problems, ValidationProblem{Path: "retry." + name + ".multiplier", Message: "must be between 1 and 10"})
		}
		if retry.InitialIntervalSeconds > 24*60*60 || retry.MaxIntervalSeconds > 24*60*60 {
			*problems = append(*problems, ValidationProblem{Path: "retry." + name, Message: "backoff intervals must not exceed 86400 seconds"})
		}
	}
}

func definitionForDraft(agentType string) (worker.AdapterDefinition, error) {
	internal := agentType
	if agentType == "claude_code" {
		internal = string(worker.WorkerTypeClaudeCode)
	}
	if agentType != "claude_code" && agentType != string(worker.WorkerTypePi) && agentType != string(worker.WorkerTypeCodex) {
		return worker.AdapterDefinition{}, fmt.Errorf("unsupported Agent Type %q", agentType)
	}
	return worker.LookupAdapterDefinition(internal)
}

func catalogWorkerForDraft(configured DynamicWorkerDraft, secrets map[string]string) (worker.CatalogWorker, error) {
	internal := configured.AgentType
	if internal == "claude_code" {
		internal = string(worker.WorkerTypeClaudeCode)
	}
	environment := cloneStringMap(configured.AgentEnv)
	secretVersionIDs := map[string]string{}
	definition, err := worker.LookupAdapterDefinition(internal)
	if err != nil {
		return worker.CatalogWorker{}, err
	}
	for _, slot := range definition.RequiredSecretVariables() {
		if value := secrets[slot]; value != "" {
			environment[slot] = value
			secretVersionIDs[slot] = slot
		}
	}
	source, err := worker.CatalogSourceFromConfig(worker.CatalogConfiguration{ImageReference: "configuration-check", PromptGroup: "default", Workers: []worker.WorkerConfig{{Name: configured.Name, Type: worker.WorkerType(internal), TaskTypes: toWorkerTasks(configured.TaskTypes), MaxRunning: configured.MaxRunning, Priority: configured.Priority, Capabilities: configured.Capabilities, Env: environment, SecretVersionIDs: secretVersionIDs}}})
	if err != nil || len(source.Workers) != 1 {
		return worker.CatalogWorker{}, err
	}
	return source.Workers[0], nil
}

func toWorkerTasks(tasks []string) []worker.TaskType {
	result := make([]worker.TaskType, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, worker.TaskType(task))
	}
	return result
}

func workerChanged(current, candidate DynamicDraft, name string) bool {
	left, right := findWorker(&current, name), findWorker(&candidate, name)
	if left == nil || right == nil {
		return left != right
	}
	leftCopy, rightCopy := *left, *right
	leftCopy.SecretVersions = nil
	rightCopy.SecretVersions = nil
	leftCopy.Secrets = nil
	rightCopy.Secrets = nil
	l, _ := json.Marshal(leftCopy)
	r, _ := json.Marshal(rightCopy)
	return string(l) != string(r)
}

func findWorker(draft *DynamicDraft, name string) *DynamicWorkerDraft {
	for index := range draft.Workers {
		if draft.Workers[index].Name == name {
			return &draft.Workers[index]
		}
	}
	return nil
}

func cloneDraft(draft DynamicDraft) DynamicDraft {
	result := draft
	result.Container.CapAdd = append([]string(nil), draft.Container.CapAdd...)
	result.Workers = make([]DynamicWorkerDraft, len(draft.Workers))
	for index, configured := range draft.Workers {
		result.Workers[index] = configured
		result.Workers[index].TaskTypes = append([]string(nil), configured.TaskTypes...)
		result.Workers[index].Capabilities = append([]string(nil), configured.Capabilities...)
		result.Workers[index].AgentEnv = cloneStringMap(configured.AgentEnv)
		result.Workers[index].TimeoutOverrides = cloneIntMap(configured.TimeoutOverrides)
		result.Workers[index].Secrets = cloneSecretStates(configured.Secrets)
		result.Workers[index].SecretVersions = cloneStringMap(configured.SecretVersions)
	}
	return result
}
func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
func cloneIntMap(values map[string]int) map[string]int {
	if values == nil {
		return map[string]int{}
	}
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
func cloneSecretStates(values map[string]DynamicSecretState) map[string]DynamicSecretState {
	if values == nil {
		return nil
	}
	result := make(map[string]DynamicSecretState, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

type storedWorkerDraft struct {
	DynamicWorkerDraft
	SecretVersions map[string]string `json:"secret_versions,omitempty"`
}
type storedDraft struct {
	Runtime   DynamicRuntimeDraft   `json:"runtime"`
	Tasks     DynamicTasksDraft     `json:"tasks"`
	Retry     DynamicRetryDraft     `json:"retry"`
	Container DynamicContainerDraft `json:"container"`
	Workers   []storedWorkerDraft   `json:"workers"`
}

func marshalDraft(draft DynamicDraft) ([]byte, error) {
	stored := storedDraft{Runtime: draft.Runtime, Tasks: draft.Tasks, Retry: draft.Retry, Container: draft.Container, Workers: make([]storedWorkerDraft, len(draft.Workers))}
	for index, configured := range draft.Workers {
		stored.Workers[index] = storedWorkerDraft{DynamicWorkerDraft: configured, SecretVersions: configured.SecretVersions}
		stored.Workers[index].DynamicWorkerDraft.Secrets = nil
		stored.Workers[index].DynamicWorkerDraft.SecretVersions = nil
	}
	return json.Marshal(stored)
}
func unmarshalDraft(data []byte, draft *DynamicDraft) error {
	var stored storedDraft
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	*draft = DynamicDraft{Runtime: stored.Runtime, Tasks: stored.Tasks, Retry: stored.Retry, Container: stored.Container, Workers: make([]DynamicWorkerDraft, len(stored.Workers))}
	for index, configured := range stored.Workers {
		draft.Workers[index] = configured.DynamicWorkerDraft
		draft.Workers[index].SecretVersions = configured.SecretVersions
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func reservedEnvironmentVariable(variable string) bool {
	switch variable {
	case "AETHER_RESULT_PROTOCOL", "HOME", "LD_LIBRARY_PATH", "LD_PRELOAD", "MOCK_CONTEXT", "MOCK_PHASE", "PATH", "PWD", "SHELL", "TMPDIR":
		return true
	}
	return false
}
func validTimeoutPhase(phase string) bool {
	switch phase {
	case "bootstrap.primary", "bootstrap.conclude", "bootstrap.recovery", "reason.primary", "explore.primary", "explore.conclude", "explore.recovery":
		return true
	}
	return false
}
func validHTTPURL(value string) bool {
	parsed, err := http.NewRequest(http.MethodGet, value, nil)
	return err == nil && (parsed.URL.Scheme == "http" || parsed.URL.Scheme == "https") && parsed.URL.Host != "" && parsed.URL.User == nil && parsed.URL.RawQuery == "" && parsed.URL.Fragment == ""
}

// DefaultProviderHealthcheck adapts the existing Worker HTTP checker to
// candidate Secret values held only for the bounded healthcheck call.
func DefaultProviderHealthcheck(timeout time.Duration) ProviderHealthcheckFunc {
	return func(ctx context.Context, candidate worker.CatalogWorker, values map[string]string) error {
		provider := staticSecretProvider{values: values}
		resolver, err := rundomain.NewSecretResolver(map[string]rundomain.SecretProvider{"configuration": provider})
		if err != nil {
			return err
		}
		checker, err := worker.NewHTTPProviderHealthChecker(http.DefaultClient, resolver, timeout)
		if err != nil {
			return err
		}
		return checker.CheckProvider(ctx, candidate)
	}
}

type staticSecretProvider struct{ values map[string]string }

func (provider staticSecretProvider) ResolveSecret(_ context.Context, key string) ([]byte, error) {
	value := provider.values[key]
	if value == "" {
		return nil, fmt.Errorf("candidate Secret is not configured")
	}
	return []byte(value), nil
}

// coverageProblems describes creation eligibility, not configuration validity.
func coverageProblems(draft DynamicDraft) []ValidationProblem {
	coverage := map[string]bool{}
	for _, configured := range draft.Workers {
		for _, task := range configured.TaskTypes {
			coverage[task] = true
		}
	}
	var problems []ValidationProblem
	for _, task := range []string{"bootstrap", "reason", "explore"} {
		if !coverage[task] {
			problems = append(problems, ValidationProblem{Path: "workers", Message: "Worker coverage is missing for " + task})
		}
	}
	return problems
}

// ProjectCreationRevision reads eligibility and binding in the creation transaction.
func ProjectCreationRevision(ctx context.Context, db database.DBTX) (string, error) {
	id, err := ActiveRevisionID(ctx, db)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", &DomainError{Code: ErrorValidation, Detail: "Worker coverage is missing for bootstrap, reason, explore"}
	}
	draft, err := readPrivateDraft(ctx, db, id)
	if err != nil {
		return "", err
	}
	if problems := coverageProblems(draft); len(problems) > 0 {
		messages := make([]string, len(problems))
		for i, problem := range problems {
			messages[i] = problem.Message
		}
		return "", &DomainError{Code: ErrorValidation, Detail: strings.Join(messages, "; "), ValidationProblems: problems}
	}
	return id, nil
}
