package run

import (
	"fmt"
	"slices"
	"time"
)

type TaskType string

type RunID string
type WorkKey string
type ClaimScope string
type FencingToken int64

const (
	TaskReason    TaskType = "reason"
	TaskExplore   TaskType = "explore"
	TaskBootstrap TaskType = "bootstrap"
)

func (t TaskType) valid() bool {
	switch t {
	case TaskReason, TaskExplore, TaskBootstrap:
		return true
	default:
		return false
	}
}

type Status string

const (
	StatusActive      Status = "active"
	StatusCommitted   Status = "committed"
	StatusDeclined    Status = "declined"
	StatusStale       Status = "stale"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

type StartRequest struct {
	ProjectID              string                `json:"project_id"`
	Task                   TaskType              `json:"task"`
	CandidateGeneration    int                   `json:"candidate_generation"`
	CandidateRevision      int64                 `json:"candidate_revision"`
	TargetIntentID         *string               `json:"target_intent_id,omitempty"`
	WorkKey                WorkKey               `json:"work_key"`
	DispatcherID           string                `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken          `json:"dispatcher_fencing_token"`
	Spec                   AdmissionSpecSnapshot `json:"execution_spec"`
	RetryPolicy            RetryPolicy           `json:"retry_policy"`
}

// ExecutionRun is one auditable attempt. Runtime stages are observations, not states.
type ExecutionRun struct {
	ID                     RunID                 `json:"id"`
	ProjectID              string                `json:"project_id"`
	TargetIntentID         *string               `json:"target_intent_id,omitempty"`
	WorkKey                WorkKey               `json:"work_key"`
	ClaimScope             ClaimScope            `json:"claim_scope"`
	Attempt                int64                 `json:"attempt"`
	PreviousRunID          *RunID                `json:"previous_run_id,omitempty"`
	NextEligibleAt         *time.Time            `json:"next_eligible_at,omitempty"`
	Status                 Status                `json:"status"`
	FencingToken           FencingToken          `json:"fencing_token"`
	DispatcherID           string                `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken          `json:"dispatcher_fencing_token"`
	Task                   TaskType              `json:"task"`
	Spec                   ExecutionSpecSnapshot `json:"execution_spec"`
	StartedAt              time.Time             `json:"started_at"`
	FinishedAt             *time.Time            `json:"finished_at,omitempty"`
	ResultHash             string                `json:"result_hash,omitempty"`
	OutcomeKind            OutcomeKind           `json:"outcome_kind,omitempty"`
	ErrorCode              ErrorCode             `json:"error_code,omitempty"`
	CaptureFailureReason   CaptureFailureReason  `json:"capture_failure_reason,omitempty"`
	CaptureDiagnostic      CaptureDiagnostic     `json:"capture_diagnostic,omitempty"`
	ResultEvidenceSource   ResultEvidenceSource  `json:"result_evidence_source,omitempty"`
	RecoveryUsed           bool                  `json:"recovery_used,omitempty"`
	RecoveryPhase          ExecutionPhase        `json:"recovery_phase,omitempty"`
	RecoveryStartedAt      *time.Time            `json:"recovery_started_at,omitempty"`
}

type Claim struct {
	WorkKey        WorkKey      `json:"work_key"`
	Scope          ClaimScope   `json:"claim_scope"`
	RunID          RunID        `json:"run_id"`
	ProjectID      string       `json:"project_id"`
	TargetIntentID *string      `json:"target_intent_id,omitempty"`
	Task           TaskType     `json:"task"`
	FencingToken   FencingToken `json:"fencing_token"`
	DispatcherID   string       `json:"dispatcher_id"`
	ClaimedAt      time.Time    `json:"claimed_at"`
	LeaseExpiresAt time.Time    `json:"lease_expires_at"`
}

type StartedRun struct {
	Run   ExecutionRun `json:"run"`
	Claim Claim        `json:"claim"`
}

// AdmittedRun combines durable execution ownership with the normalized,
// task-discriminated Project input materialized by Run Admission.
type AdmittedRun struct {
	StartedRun
	BlackboardInput *BlackboardInput `json:"blackboard_input,omitempty"`
	BootstrapInput  *BootstrapInput  `json:"bootstrap_input,omitempty"`
}

func (admitted AdmittedRun) Validate(request StartRequest) error {
	request, err := ValidateStartRequest(request)
	if err != nil {
		return err
	}
	started := admitted.StartedRun
	run := started.Run
	claim := started.Claim
	if run.Status != StatusActive || run.ID == "" || run.FencingToken <= 0 ||
		run.DispatcherFencingToken <= 0 || claim.RunID != run.ID ||
		claim.FencingToken != run.FencingToken || claim.ProjectID != run.ProjectID ||
		claim.DispatcherID != run.DispatcherID ||
		claim.Scope != run.ClaimScope || claim.WorkKey != run.WorkKey ||
		run.ProjectID != request.ProjectID ||
		run.WorkKey != request.WorkKey || run.DispatcherID != request.DispatcherID ||
		run.DispatcherFencingToken != request.DispatcherFencingToken {
		return executionContractError("Admitted Run ownership evidence does not match its request")
	}
	if claim.Task != run.Task || run.Task != request.Task ||
		!sameOptionalString(run.TargetIntentID, request.TargetIntentID) ||
		!sameOptionalString(claim.TargetIntentID, request.TargetIntentID) ||
		!sameOptionalString(run.Spec.Input.TargetIntentID, request.TargetIntentID) {
		return executionContractError("Admitted Run Task or Target evidence does not match its request")
	}
	if request.Spec.ConfigRevision != run.Spec.ConfigRevision {
		return executionContractError("Admitted Run Config Revision does not match its request")
	}
	if request.Spec.PromptTemplateHash != run.Spec.PromptTemplateHash {
		return executionContractError("Admitted Run Prompt Template identity does not match its request")
	}
	if request.Spec.Worker.Name != run.Spec.Worker.Name ||
		request.Spec.Worker.Adapter != run.Spec.Worker.Adapter ||
		request.Spec.Worker.Model != run.Spec.Worker.Model ||
		!slices.Equal(request.Spec.Worker.Capabilities, run.Spec.Worker.Capabilities) ||
		!slices.Equal(request.Spec.Worker.SecretReferences, run.Spec.Worker.SecretReferences) {
		return executionContractError("Admitted Run Worker evidence does not match its request")
	}
	if request.Spec.Image != run.Spec.Image {
		return executionContractError("Admitted Run Image evidence does not match its request")
	}
	if request.Spec.Timeouts != run.Spec.Timeouts {
		return executionContractError("Admitted Run Timeout evidence does not match its request")
	}
	if request.Spec.ResultSchema != run.Spec.ResultSchema {
		return executionContractError("Admitted Run Result Schema evidence does not match its request")
	}
	if request.Spec.MaxProposedIntents != run.Spec.MaxProposedIntents {
		return executionContractError("Admitted Run Max Proposed Intents evidence does not match its request")
	}
	if _, err := run.Spec.normalized(); err != nil {
		return executionContractError("Admitted Run Execution Spec is invalid")
	}
	if run.Spec.Input.Generation != request.CandidateGeneration ||
		(request.Task != TaskExplore && run.Spec.Input.Revision != request.CandidateRevision) ||
		(request.Task == TaskExplore && run.Spec.Input.Revision < request.CandidateRevision) {
		return executionContractError("Admitted Run Input coordinates do not match its candidate")
	}
	switch request.Task {
	case TaskReason, TaskExplore:
		if admitted.BlackboardInput == nil || admitted.BootstrapInput != nil {
			return executionContractError("Admitted Run Task and Input discriminant do not match")
		}
		if !admitted.BlackboardInput.MatchesInputSnapshot(run.Spec.Input) {
			return executionContractError("Admitted Run Blackboard Input identity does not match")
		}
	case TaskBootstrap:
		if admitted.BootstrapInput == nil || admitted.BlackboardInput != nil {
			return executionContractError("Admitted Run Task and Input discriminant do not match")
		}
		if !admitted.BootstrapInput.MatchesInputSnapshot(run.Spec.Input) {
			return executionContractError("Admitted Run Bootstrap Input identity does not match")
		}
	default:
		return executionContractError("Admitted Run Task evidence is invalid")
	}
	return nil
}

func executionContractError(detail string) *DomainError {
	return newError(ErrorExecutionContractMismatch, detail)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (spec AdmissionSpecSnapshot) MatchesExecutionSpec(execution ExecutionSpecSnapshot) bool {
	normalized, err := spec.normalized()
	if err != nil {
		return false
	}
	return normalized.ConfigRevision == execution.ConfigRevision &&
		normalized.PromptTemplateHash == execution.PromptTemplateHash &&
		normalized.Worker.Name == execution.Worker.Name &&
		normalized.Worker.Adapter == execution.Worker.Adapter &&
		normalized.Worker.Model == execution.Worker.Model &&
		slices.Equal(normalized.Worker.Capabilities, execution.Worker.Capabilities) &&
		slices.Equal(normalized.Worker.SecretReferences, execution.Worker.SecretReferences) &&
		normalized.Image == execution.Image && normalized.Timeouts == execution.Timeouts &&
		normalized.ResultSchema == execution.ResultSchema &&
		normalized.MaxProposedIntents == execution.MaxProposedIntents
}

func ValidateStartRequest(request StartRequest) (StartRequest, error) {
	if request.ProjectID == "" {
		return StartRequest{}, newError(ErrorInvalidArgument, "project ID is required")
	}
	if !request.Task.valid() {
		return StartRequest{}, newError(ErrorInvalidArgument, "task type is invalid")
	}
	if !bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 {
		return StartRequest{}, newError(ErrorInvalidArgument, "Dispatcher Lease identity is required")
	}
	if request.CandidateGeneration <= 0 || request.CandidateRevision < 0 {
		return StartRequest{}, newError(ErrorInvalidArgument, "candidate generation or revision is invalid")
	}
	normalized, err := request.Spec.normalized()
	if err != nil {
		return StartRequest{}, err
	}
	request.Spec = normalized
	request.RetryPolicy, err = request.RetryPolicy.Normalized()
	if err != nil {
		return StartRequest{}, err
	}
	target := request.TargetIntentID
	if err := validateTaskExecutionConfiguration(
		request.Task, target, request.Spec.Timeouts, request.Spec.MaxProposedIntents,
	); err != nil {
		return StartRequest{}, err
	}
	expectedWorkKey, err := WorkKeyFor(request.ProjectID, request.Task, InputSnapshot{
		Generation:     request.CandidateGeneration,
		Revision:       request.CandidateRevision,
		TargetIntentID: request.TargetIntentID,
	})
	if err != nil || expectedWorkKey != request.WorkKey {
		return StartRequest{}, newError(ErrorInvalidArgument, "candidate Work Key is invalid")
	}
	return request, nil
}

func validateTaskExecutionConfiguration(
	task TaskType,
	target *string,
	timeouts TimeoutSnapshot,
	maxProposedIntents int,
) error {
	if task == TaskReason && target != nil {
		return newError(ErrorInvalidArgument, "reason input cannot target an intent")
	}
	if task != TaskReason && (target == nil || *target == "") {
		return newError(ErrorInvalidArgument, fmt.Sprintf("%s input requires a target intent", task))
	}
	return validateTaskExecutionPolicy(task, timeouts, maxProposedIntents)
}

func validateTaskExecutionPolicy(task TaskType, timeouts TimeoutSnapshot, maxProposedIntents int) error {
	if task == TaskReason && (maxProposedIntents <= 0 || maxProposedIntents > MaximumReasonProposedIntents) {
		return newError(ErrorInvalidArgument, fmt.Sprintf("reason result limit must be between 1 and %d", MaximumReasonProposedIntents))
	}
	if (task == TaskBootstrap || task == TaskExplore) && timeouts.FallbackPhaseSeconds <= 0 {
		return newError(ErrorInvalidArgument, fmt.Sprintf("%s fallback timeout is required", task))
	}
	if task == TaskReason && timeouts.FallbackPhaseSeconds != 0 {
		return newError(ErrorInvalidArgument, "reason cannot define a fallback timeout")
	}
	if task != TaskReason && maxProposedIntents != 0 {
		return newError(ErrorInvalidArgument, "only reason may define a proposed-intent limit")
	}
	return nil
}

// WorkKeyFor derives the only valid identity for a task's retry and claim chain.
func WorkKeyFor(projectID string, task TaskType, input InputSnapshot) (WorkKey, error) {
	if projectID == "" || !task.valid() {
		return "", newError(ErrorInvalidArgument, "cannot derive work key from invalid identity")
	}
	if task == TaskReason {
		return WorkKey(fmt.Sprintf("reason:%s:g%d:r%d", projectID, input.Generation, input.Revision)), nil
	}
	if input.TargetIntentID == nil || *input.TargetIntentID == "" {
		return "", newError(ErrorInvalidArgument, "target intent is required to derive work key")
	}
	return WorkKey(fmt.Sprintf("intent:%s:%s:%s", projectID, *input.TargetIntentID, task)), nil
}

// ClaimScopeFor derives the ownership scope whose fencing token must increase
// across Reason revisions and across task types targeting the same Intent.
func ClaimScopeFor(projectID string, task TaskType, input InputSnapshot) (ClaimScope, error) {
	if projectID == "" || !task.valid() {
		return "", newError(ErrorInvalidArgument, "cannot derive claim scope from invalid identity")
	}
	if task == TaskReason {
		return ClaimScope("reason:" + projectID), nil
	}
	if input.TargetIntentID == nil || *input.TargetIntentID == "" {
		return "", newError(ErrorInvalidArgument, "target intent is required to derive claim scope")
	}
	return ClaimScope(fmt.Sprintf("intent:%s:%s", projectID, *input.TargetIntentID)), nil
}
