package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

// Service owns the Execution Run ledger, claims, retry chains and the atomic
// handoff of validated results to Project.
type Service struct {
	db           *database.DB
	ledger       *ledgerStore
	projects     ProjectAccess
	publisher    projectChangePublisher
	readiness    StartReadiness
	authority    AdmissionAuthority
	clock        func() time.Time
	newRunID     func() RunID
	operatorName string
}

type ServiceOptions struct {
	OperatorName string
	Readiness    StartReadiness
	Authority    AdmissionAuthority
	Clock        func() time.Time
	NewRunID     func() RunID
}

func NewService(
	db *database.DB,
	projects ProjectAccess,
	publisher projectChangePublisher,
	options ServiceOptions,
) *Service {
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	if options.NewRunID == nil {
		options.NewRunID = NewRunID
	}
	return &Service{
		db: db, ledger: &ledgerStore{}, projects: projects, publisher: publisher,
		readiness: options.Readiness, authority: options.Authority, clock: options.Clock, newRunID: options.NewRunID,
		operatorName: options.OperatorName,
	}
}

func (s *Service) StartRun(ctx context.Context, request StartRequest) (*AdmittedRun, error) {
	request, err := ValidateStartRequest(request)
	if err != nil {
		return nil, err
	}
	if s.readiness == nil {
		return nil, &DomainError{Code: ErrorNotReady, Detail: "Run readiness is not configured"}
	}
	if err := s.readiness.CheckStartReadiness(ctx, request); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	if !leaseMatches {
		return nil, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize Run creation"}
	}

	if s.projects == nil {
		return nil, &DomainError{Code: ErrorInternal, Detail: "Project admission is not configured"}
	}
	state, err := s.projects.Admission(
		ctx, tx, request.ProjectID, request.Task, request.TargetIntentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &DomainError{Code: ErrorNotFound, Detail: "project not found"}
	}
	if err != nil {
		return nil, err
	}
	if !state.Active {
		return nil, &DomainError{Code: ErrorProjectInactive, Detail: "project is not active"}
	}
	if state.Generation != request.CandidateGeneration ||
		(request.Task != TaskExplore && state.Revision != request.CandidateRevision) {
		return nil, &DomainError{Code: ErrorStaleInput, Detail: "project input version changed"}
	}

	if request.TargetIntentID != nil && !state.TargetAvailable {
		return nil, &DomainError{Code: ErrorTargetUnavailable, Detail: "target intent is not available"}
	}
	if s.authority != nil {
		authorization, err := s.authority.AuthorizeRunAdmission(ctx, tx, request, state, now)
		if err != nil {
			return nil, err
		}
		request.Spec = authorization.Spec
		request.RetryPolicy = authorization.RetryPolicy
	}
	executionSpec, err := admittedExecutionSpec(request.Task, request.Spec, state)
	if err != nil {
		return nil, err
	}
	workKey, err := WorkKeyFor(request.ProjectID, request.Task, executionSpec.Input)
	if err != nil {
		return nil, err
	}
	if workKey != request.WorkKey {
		return nil, &DomainError{Code: ErrorStaleInput, Detail: "admitted input changed the candidate Work Key"}
	}
	claimScope, err := ClaimScopeFor(request.ProjectID, request.Task, executionSpec.Input)
	if err != nil {
		return nil, err
	}
	unresolved, err := s.ledger.HasUnresolvedExecutionHandles(ctx, tx, claimScope)
	if err != nil {
		return nil, err
	}
	if unresolved {
		return nil, &DomainError{Code: ErrorRetryDeferred, Detail: "previous execution handles must be stopped before replacement"}
	}

	expired, err := s.ledger.GetExpiredClaimRun(ctx, tx, claimScope, now.UnixNano())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		expiredRun := expired.Run
		receipt := FinishReceipt{
			RunID: expiredRun.ID, Status: StatusInterrupted,
			Code: ExecutionLeaseExpired, ProjectRevision: state.Revision,
			FinishedAt: now,
		}
		receiptJSON, err := json.Marshal(receipt)
		if err != nil {
			return nil, fmt.Errorf("marshal expired lease receipt: %w", err)
		}
		rows, err := s.ledger.FinishWithoutResult(
			ctx, tx, expiredRun.ID, receipt.Status, receipt.Code, "", "",
			string(receiptJSON), now.Format(time.RFC3339Nano),
		)
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, &DomainError{Code: ErrorResultConflict, Detail: "expired execution Run changed while interrupting"}
		}
		if err := s.ledger.ApplyRetryOutcome(ctx, tx, expiredRun, receipt.Status, receipt.Code, now.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
		if _, err := s.ledger.ReleaseClaim(ctx, tx, expiredRun); err != nil {
			return nil, err
		}
		if expiredRun.WorkKey == workKey {
			retryState, err := s.ledger.WorkRetryState(ctx, tx, workKey)
			if err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			if retryState == RetryExhausted {
				return nil, &DomainError{Code: ErrorRetryExhausted, Detail: "technical retry budget is exhausted"}
			}
			return nil, &DomainError{Code: ErrorRetryDeferred, Detail: "expired execution Run entered retry backoff"}
		}
	}
	claimed, err := s.ledger.HasCompetingClaim(
		ctx, tx, claimScope,
	)
	if err != nil {
		return nil, err
	}
	if claimed {
		return nil, &DomainError{Code: ErrorWorkClaimed, Detail: "work item already has an active claim"}
	}

	leaseSeconds := state.IntentLeaseSeconds
	if request.Task == TaskReason {
		leaseSeconds = state.ReasonLeaseSeconds
	}
	if leaseSeconds <= 0 || leaseSeconds > int(MaximumLeaseExtension/time.Second) {
		return nil, fmt.Errorf("invalid database lease policy for %s", request.Task)
	}
	fencingToken, attempt, previousRunID, err := s.ledger.AllocateExecutionRight(
		ctx, tx, workKey, claimScope, request.ProjectID, request.Task,
		executionSpec.Input.TargetIntentID, executionSpec.Worker.Name,
		request.RetryPolicy, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, err
	}
	runID := s.newRunID()
	started := StartedRun{
		Run: ExecutionRun{
			ID:                     runID,
			ProjectID:              request.ProjectID,
			TargetIntentID:         executionSpec.Input.TargetIntentID,
			WorkKey:                workKey,
			ClaimScope:             claimScope,
			Attempt:                attempt,
			PreviousRunID:          previousRunID,
			Status:                 StatusActive,
			FencingToken:           fencingToken,
			DispatcherID:           request.DispatcherID,
			DispatcherFencingToken: request.DispatcherFencingToken,
			Task:                   request.Task,
			Spec:                   executionSpec,
			StartedAt:              now,
		},
		Claim: Claim{
			WorkKey:        workKey,
			Scope:          claimScope,
			RunID:          runID,
			ProjectID:      request.ProjectID,
			TargetIntentID: executionSpec.Input.TargetIntentID,
			Task:           request.Task,
			FencingToken:   fencingToken,
			DispatcherID:   request.DispatcherID,
			ClaimedAt:      now,
			LeaseExpiresAt: now.Add(time.Duration(leaseSeconds) * time.Second),
		},
	}
	admitted := &AdmittedRun{
		StartedRun:      started,
		BlackboardInput: state.BlackboardInput,
		BootstrapInput:  state.BootstrapInput,
	}
	if err := admitted.Validate(request); err != nil {
		return nil, err
	}
	if err := s.ledger.CreateRunAndClaim(ctx, tx, started); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.publishActivityChanged(request.ProjectID)
	return admitted, nil
}

func admittedExecutionSpec(
	task TaskType,
	spec AdmissionSpecSnapshot,
	state ProjectAdmission,
) (ExecutionSpecSnapshot, error) {
	var input InputSnapshot
	switch task {
	case TaskReason, TaskExplore:
		if state.BlackboardInput == nil || state.BootstrapInput != nil {
			return ExecutionSpecSnapshot{}, newError(ErrorInternal, "Project admission returned an invalid Blackboard input union")
		}
		identity, err := state.BlackboardInput.InputIdentityV1()
		if err != nil {
			return ExecutionSpecSnapshot{}, err
		}
		input = InputSnapshot{
			Generation: state.BlackboardInput.Generation,
			Revision:   state.BlackboardInput.Revision,
			Identity:   identity,
		}
		if state.BlackboardInput.TargetIntentID != "" {
			target := state.BlackboardInput.TargetIntentID
			input.TargetIntentID = &target
		}
	case TaskBootstrap:
		if state.BootstrapInput == nil || state.BlackboardInput != nil {
			return ExecutionSpecSnapshot{}, newError(ErrorInternal, "Project admission returned an invalid Bootstrap input union")
		}
		identity, err := state.BootstrapInput.InputIdentityV1()
		if err != nil {
			return ExecutionSpecSnapshot{}, err
		}
		target := state.BootstrapInput.TargetIntentID
		input = InputSnapshot{
			Generation:     state.BootstrapInput.Generation,
			Revision:       state.BootstrapInput.Revision,
			Identity:       identity,
			TargetIntentID: &target,
		}
	default:
		return ExecutionSpecSnapshot{}, newError(ErrorInvalidArgument, "task type is invalid")
	}
	return (ExecutionSpecSnapshot{
		Schema: ExecutionSpecSchemaV2, ConfigRevision: spec.ConfigRevision, PromptTemplateHash: spec.PromptTemplateHash,
		Input: input, Worker: spec.Worker, Image: spec.Image, Timeouts: spec.Timeouts,
		ResultSchema: spec.ResultSchema, MaxProposedIntents: spec.MaxProposedIntents,
	}).normalized()
}

func (s *Service) publishActivityChanged(projectID string) {
	if publisher, ok := s.publisher.(activityChangePublisher); ok {
		publisher.PublishActivityChanged(projectID)
	}
}

func (s *Service) BeginProcessRecovery(
	ctx context.Context,
	request BeginProcessRecoveryRequest,
) (ProcessRecoveryReservation, error) {
	if err := request.Validate(); err != nil {
		return ProcessRecoveryReservation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProcessRecoveryReservation{}, err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return ProcessRecoveryReservation{}, err
	}
	if !leaseMatches {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize process recovery"}
	}
	stored, err := s.ledger.GetForFinish(ctx, tx, request.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorNotFound, Detail: "execution run not found"}
	}
	if err != nil {
		return ProcessRecoveryReservation{}, err
	}
	run := stored.Run
	if run.Status != StatusActive || run.FencingToken != request.FencingToken ||
		run.DispatcherID != request.DispatcherID || run.DispatcherFencingToken != request.DispatcherFencingToken ||
		run.Spec.Timeouts.RecoveryPhaseSeconds <= 0 {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorFenced, Detail: "execution Run is not eligible for process recovery"}
	}
	if (request.Mode == ProcessRecoveryExplorePrimary || request.Mode == ProcessRecoveryExploreConclude) && run.Task != TaskExplore ||
		(request.Mode == ProcessRecoveryBootstrapPrimary || request.Mode == ProcessRecoveryBootstrapConclude) && run.Task != TaskBootstrap {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorFenced, Detail: "execution Run task does not match process recovery mode"}
	}
	if run.RecoveryUsed {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorConflict, Detail: "process recovery opportunity is already reserved"}
	}
	if !run.Spec.Worker.SupportsProcessRecovery() {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorForbidden, Detail: "Worker does not advertise process recovery"}
	}
	claimMatches, err := s.ledger.ClaimMatches(ctx, tx, run, now.UnixNano())
	if err != nil {
		return ProcessRecoveryReservation{}, err
	}
	if !claimMatches {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorFenced, Detail: "execution claim does not authorize process recovery"}
	}
	if s.projects == nil {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorInternal, Detail: "Project admission is not configured"}
	}
	state, err := s.projects.Admission(ctx, tx, run.ProjectID, run.Task, run.TargetIntentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorNotFound, Detail: "project not found"}
	}
	if err != nil {
		return ProcessRecoveryReservation{}, err
	}
	if !state.Active {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorProjectInactive, Detail: "project is not active"}
	}
	if run.TargetIntentID != nil && !state.TargetAvailable {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorTargetUnavailable, Detail: "target intent is not available"}
	}
	if !recoveryDeadlineAvailable(run, now) {
		return ProcessRecoveryReservation{}, &DomainError{Code: ExecutionDeadline, Detail: "execution Run does not have enough deadline for process recovery"}
	}
	rows, err := s.ledger.ReserveProcessRecovery(ctx, tx, request, now.Format(time.RFC3339Nano))
	if err != nil {
		return ProcessRecoveryReservation{}, err
	}
	if rows != 1 {
		return ProcessRecoveryReservation{}, &DomainError{Code: ErrorConflict, Detail: "process recovery opportunity was reserved by another Dispatcher"}
	}
	if err := tx.Commit(); err != nil {
		return ProcessRecoveryReservation{}, err
	}
	return ProcessRecoveryReservation{
		RunID: run.ID, FencingToken: run.FencingToken, Mode: request.Mode,
		Phase: processRecoveryPhase(request.Mode), ReservedAt: now,
		PhaseTimeout: time.Duration(run.Spec.Timeouts.RecoveryPhaseSeconds) * time.Second,
	}, nil
}

func recoveryDeadlineAvailable(run ExecutionRun, now time.Time) bool {
	deadline := run.StartedAt.Add(time.Duration(run.Spec.Timeouts.RunSeconds) * time.Second)
	required := time.Duration(run.Spec.Timeouts.RecoveryPhaseSeconds+run.Spec.Timeouts.GraceSeconds) * time.Second
	return !run.StartedAt.IsZero() && !now.After(deadline.Add(-required))
}

func (s *Service) GetRun(ctx context.Context, runID RunID) (ExecutionRun, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return ExecutionRun{}, err
	}
	defer conn.Close()
	result, err := s.ledger.Get(ctx, conn, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionRun{}, &DomainError{Code: ErrorNotFound, Detail: "execution run not found"}
	}
	return result, err
}

// ClaimIsCurrent reports whether an Execution Run still owns its persisted
// claim at the supplied instant. Callers use it only inside an existing
// transaction when coordinating a larger operation.
func (s *Service) ClaimIsCurrent(
	ctx context.Context,
	tx database.DBTX,
	execution ExecutionRun,
	nowUnixNano int64,
) (bool, error) {
	return s.ledger.ClaimMatches(ctx, tx, execution, nowUnixNano)
}

// AuthorizeDispatcher verifies a Dispatcher Lease inside a caller-owned
// transaction so a larger scheduling projection can remain revision-consistent.
func (s *Service) AuthorizeDispatcher(
	ctx context.Context,
	tx database.DBTX,
	dispatcherID string,
	fencingToken FencingToken,
) error {
	return s.AuthorizeDispatcherAt(ctx, tx, dispatcherID, fencingToken, s.SchedulingObservedAt())
}

// SchedulingObservedAt captures the one server clock value used to authorize
// and interpret a complete Scheduling Snapshot.
func (s *Service) SchedulingObservedAt() time.Time {
	return s.clock().UTC()
}

func (s *Service) AuthorizeDispatcherAt(
	ctx context.Context,
	tx database.DBTX,
	dispatcherID string,
	fencingToken FencingToken,
	observedAt time.Time,
) error {
	matched, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, dispatcherID, fencingToken, observedAt.UnixNano(),
	)
	if err != nil {
		return err
	}
	if !matched {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize scheduling snapshot"}
	}
	return nil
}

// InspectSchedulingWorkInTransaction projects Run-owned scheduling state in a
// caller-owned transaction after the caller has authorized the Dispatcher.
func (s *Service) InspectSchedulingWorkInTransaction(
	ctx context.Context,
	tx database.DBTX,
	queries []SchedulingWorkQuery,
	observedAt time.Time,
) ([]SchedulingWorkState, error) {
	return s.inspectSchedulingWork(ctx, tx, queries, observedAt)
}

// InspectSchedulingRevocationsInTransaction returns only observed Runs whose
// authoritative ledger ownership or Project lifecycle is no longer valid.
func (s *Service) InspectSchedulingRevocationsInTransaction(
	ctx context.Context,
	tx database.DBTX,
	request SchedulingSnapshotRequest,
	observedAt time.Time,
) ([]RunID, error) {
	return s.ledger.SchedulingRevokedRunIDs(
		ctx, tx, request.ObservedRunIDs, request.DispatcherID,
		request.DispatcherFencingToken, observedAt,
	)
}

func (s *Service) inspectSchedulingWork(
	ctx context.Context,
	tx database.DBTX,
	queries []SchedulingWorkQuery,
	observedAt time.Time,
) ([]SchedulingWorkState, error) {
	workKeys := make([]WorkKey, len(queries))
	for index := range queries {
		workKeys[index] = queries[index].WorkKey
	}
	states, err := s.ledger.SchedulingWorkStates(ctx, tx, workKeys, observedAt)
	if err != nil {
		return nil, err
	}
	reasonQueries := make([]SchedulingWorkQuery, 0, len(queries))
	reasonIndexes := make([]int, 0, len(queries))
	for index, query := range queries {
		if query.Task == TaskReason {
			reasonQueries = append(reasonQueries, query)
			reasonIndexes = append(reasonIndexes, index)
		}
	}
	checkpoints, err := s.ledger.ReasonSchedulingCheckpoints(ctx, tx, reasonQueries)
	if err != nil {
		return nil, err
	}
	ranges := make([]ProjectChangeRangeQuery, 0, len(reasonQueries))
	rangeReasonIndexes := make([]int, 0, len(reasonQueries))
	for index, query := range reasonQueries {
		evidence := checkpoints[index]
		checkpoint := evidence.Checkpoint
		if !checkpoint.Valid {
			if !query.HasOpenIntents {
				continue
			}
			checkpoint = evidence.Baseline
			if !checkpoint.Valid {
				continue
			}
		}
		if checkpoint.Int64 > query.Revision {
			continue
		}
		ranges = append(ranges, ProjectChangeRangeQuery{
			ProjectID: query.ProjectID, AfterRevision: checkpoint.Int64, ThroughRevision: query.Revision,
		})
		rangeReasonIndexes = append(rangeReasonIndexes, index)
	}
	if len(ranges) == 0 {
		return states, nil
	}
	if s.projects == nil {
		return nil, &DomainError{Code: ErrorInternal, Detail: "Project change replay is not configured"}
	}
	replays, err := s.projects.ReplayChangesBatch(ctx, tx, ranges)
	if err != nil {
		return nil, err
	}
	if len(replays) != len(ranges) {
		return nil, fmt.Errorf("Project change replay returned %d ranges for %d queries", len(replays), len(ranges))
	}
	for index, replay := range replays {
		reasonIndex := rangeReasonIndexes[index]
		if !reasonChangeRangeEligible(reasonQueries[reasonIndex], replay) {
			states[reasonIndexes[reasonIndex]].RetryState = RetryClosed
		}
	}
	return states, nil
}

func reasonChangeRangeEligible(query SchedulingWorkQuery, replay ProjectChangeRange) bool {
	if replay.ReplayRequired {
		return true
	}
	for _, change := range replay.Changes {
		if change.Generation != query.Generation {
			continue
		}
		switch change.Kind {
		case ProjectChangeHintCreated, ProjectChangeHintWithdrawn, ProjectChangeIntentConcluded,
			ProjectChangeFactSuperseded, ProjectChangeFactRetracted, ProjectChangeBootstrapCompleted:
			return true
		case ProjectChangeIntentCancelled:
			if !query.HasOpenIntents {
				return true
			}
		case ProjectChangeProjectBaseline, ProjectChangeProjectCreated, ProjectChangeProjectRenamed,
			ProjectChangeProjectPaused, ProjectChangeProjectResumed, ProjectChangeIntentCreated,
			ProjectChangeProjectCompleted, ProjectChangeProjectReopened, ProjectChangeIntentsProposed:
			continue
		default:
			return true
		}
	}
	return false
}

func (s *Service) RenewRun(ctx context.Context, request RenewRunRequest) (time.Time, error) {
	if err := request.Validate(); err != nil {
		return time.Time{}, err
	}
	now := s.clock()
	expiresAt := now.Add(request.LeaseExtension)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	run, err := s.ledger.Get(ctx, tx, request.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, &DomainError{Code: ErrorNotFound, Detail: "execution run not found"}
	}
	if err != nil {
		return time.Time{}, err
	}
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, run.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return time.Time{}, err
	}
	if !leaseMatches {
		return time.Time{}, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize Run renewal"}
	}
	rows, err := s.ledger.RenewClaim(
		ctx, tx, request, now, expiresAt,
	)
	if err != nil {
		return time.Time{}, err
	}
	if rows != 1 {
		return time.Time{}, &DomainError{Code: ErrorFenced, Detail: "execution claim is no longer renewable"}
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

func (s *Service) RecoverRuns(ctx context.Context, options RecoveryOptions) ([]RunID, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, options.DispatcherID, options.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	if !leaseMatches {
		return nil, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize recovery"}
	}
	active, err := s.ledger.ListActiveRuns(ctx, tx)
	if err != nil {
		return nil, err
	}
	nowText := now.Format(time.RFC3339Nano)
	recovered := make([]RunID, 0, len(active))
	for _, stored := range active {
		run := stored.Run
		if s.projects == nil {
			return nil, &DomainError{Code: ErrorInternal, Detail: "Project revision lookup is not configured"}
		}
		revision, err := s.projects.Revision(ctx, tx, run.ProjectID)
		if err != nil {
			return nil, err
		}
		receipt := FinishReceipt{
			RunID: run.ID, Status: StatusInterrupted,
			Code:            ErrorDispatcherRestarted,
			ProjectRevision: revision, FinishedAt: now,
		}
		receiptJSON, err := json.Marshal(receipt)
		if err != nil {
			return nil, fmt.Errorf("marshal recovery receipt: %w", err)
		}
		rows, err := s.ledger.FinishWithoutResult(
			ctx, tx, run.ID, StatusInterrupted, receipt.Code, "", "",
			string(receiptJSON), nowText,
		)
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, &DomainError{Code: ErrorResultConflict, Detail: "execution run changed during recovery"}
		}
		if err := s.ledger.ApplyRetryOutcome(ctx, tx, run, StatusInterrupted, receipt.Code, nowText); err != nil {
			return nil, err
		}
		released, err := s.ledger.ReleaseClaim(ctx, tx, run)
		if err != nil {
			return nil, err
		}
		_ = released // Superseded Active Runs no longer own a Claim.
		if err := s.ledger.RecordRecoveryEvent(ctx, tx, run.ID, options.DispatcherID, string(RecoveryInterrupted), options.Reason, nowText); err != nil {
			return nil, err
		}
		recovered = append(recovered, run.ID)
	}
	pending, err := s.ledger.ListPendingRecoveryRunIDs(ctx, tx)
	if err != nil {
		return nil, err
	}
	seen := make(map[RunID]struct{}, len(recovered)+len(pending))
	unique := recovered[:0]
	for _, runID := range append(recovered, pending...) {
		if _, exists := seen[runID]; exists {
			continue
		}
		seen[runID] = struct{}{}
		unique = append(unique, runID)
	}
	recovered = unique
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recovered, nil
}

func (s *Service) RecordRecoveryEvent(ctx context.Context, record RecoveryRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, record.DispatcherID, record.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return err
	}
	if !leaseMatches {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize recovery events"}
	}
	if record.Status == RecoveryDisposed {
		handles, err := s.ledger.ListExecutionHandles(ctx, tx, []RunID{record.RunID})
		if err != nil {
			return err
		}
		if len(handles) != 0 {
			return &DomainError{Code: ErrorConflict, Detail: "recovered Run still has unresolved execution handles"}
		}
	}
	if err := s.ledger.RecordRecoveryEvent(
		ctx, tx, record.RunID, record.DispatcherID, string(record.Status),
		record.Reason, now.Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) RecordExecutionHandle(ctx context.Context, request ExecutionHandleRecordRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return err
	}
	if !leaseMatches {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize execution handle recording"}
	}
	if err := s.ledger.RecordExecutionHandle(ctx, tx, request, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) RecordProjectContainer(ctx context.Context, request ProjectContainerRecordRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return err
	}
	if !leaseMatches {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize Project container recording"}
	}
	if err := s.ledger.RecordProjectContainer(ctx, tx, request, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

// AuthorizeRunContinuation is the last gate before an Explore or Bootstrap
// conclude phase. It closes the interval between a Project lifecycle command
// committing and the Scheduler delivering cancellation to the running phase.
func (s *Service) AuthorizeRunContinuation(ctx context.Context, request RunContinuationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return err
	}
	if !leaseMatches {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize Run continuation"}
	}
	run, err := s.ledger.Get(ctx, tx, request.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return &DomainError{Code: ErrorNotFound, Detail: "execution run not found"}
	}
	if err != nil {
		return err
	}
	if run.ProjectID != request.ProjectID || run.FencingToken != request.FencingToken ||
		run.DispatcherID != request.DispatcherID || run.DispatcherFencingToken != request.DispatcherFencingToken ||
		run.Status != StatusActive {
		return &DomainError{Code: ErrorFenced, Detail: "execution Run no longer authorizes continuation"}
	}
	claimMatches, err := s.ledger.ClaimMatches(ctx, tx, run, now.UnixNano())
	if err != nil {
		return err
	}
	if !claimMatches {
		return &DomainError{Code: ErrorFenced, Detail: "execution claim no longer authorizes continuation"}
	}
	if s.projects == nil {
		return &DomainError{Code: ErrorInternal, Detail: "Project admission is not configured"}
	}
	state, err := s.projects.Admission(ctx, tx, run.ProjectID, run.Task, run.TargetIntentID)
	if errors.Is(err, sql.ErrNoRows) {
		return &DomainError{Code: ErrorNotFound, Detail: "project not found"}
	}
	if err != nil {
		return err
	}
	if !state.Active {
		return &DomainError{Code: ErrorProjectInactive, Detail: "project is not active"}
	}
	if run.TargetIntentID != nil && !state.TargetAvailable {
		return &DomainError{Code: ErrorTargetUnavailable, Detail: "target intent is not available"}
	}
	return tx.Commit()
}

func (s *Service) ListExecutionHandles(ctx context.Context, request ExecutionHandleListRequest) ([]ExecutionHandle, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, s.clock().UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	if !leaseMatches {
		return nil, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize execution handle listing"}
	}
	handles, err := s.ledger.ListExecutionHandles(ctx, tx, request.RunIDs)
	if err != nil {
		return nil, err
	}
	return handles, tx.Commit()
}

func (s *Service) MarkExecutionHandleStopped(ctx context.Context, request ExecutionHandleStopRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.clock()
	leaseMatches, err := s.ledger.DispatcherLeaseMatches(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano(),
	)
	if err != nil {
		return err
	}
	if !leaseMatches {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize execution handle completion"}
	}
	if err := s.ledger.MarkExecutionHandleStopped(ctx, tx, request.Handle, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
