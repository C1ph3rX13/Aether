package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *Service) FinishRun(ctx context.Context, request FinishRequest) (*FinishReceipt, error) {
	if request.RunID == "" || request.FencingToken <= 0 {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "run ID and fencing token are required"}
	}
	if (len(request.Candidate) == 0) == (request.Termination == nil) {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "exactly one result or termination is required"}
	}
	if !request.CaptureDiagnostic.Valid() {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "capture diagnostic is invalid"}
	}
	if !request.ResultEvidenceSource.Valid() {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "result evidence source is invalid"}
	}
	if len(request.Candidate) == 0 && request.ResultEvidenceSource != "" {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "result evidence source requires a candidate"}
	}
	if len(request.Candidate) > 0 && request.CaptureDiagnostic != "" {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "candidate capture diagnostic is invalid"}
	}
	if request.Termination != nil && request.ResultEvidenceSource != "" {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "technical termination cannot claim candidate capture evidence"}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stored, err := s.ledger.GetForFinish(ctx, tx, request.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &DomainError{Code: ErrorNotFound, Detail: "execution run not found"}
	}
	if err != nil {
		return nil, err
	}
	run := stored.Run
	if request.FencingToken != run.FencingToken {
		return nil, &DomainError{Code: ErrorFenced, Detail: "fencing token does not own the execution run"}
	}
	now := s.clock()
	if run.Status == StatusActive {
		leaseMatches := false
		if run.DispatcherFencingToken == 0 {
			leaseMatches, err = s.ledger.DispatcherInstanceOwnsLease(ctx, tx, run.DispatcherID, now.UnixNano())
		} else {
			leaseMatches, err = s.ledger.DispatcherLeaseMatches(
				ctx, tx, run.DispatcherID, run.DispatcherFencingToken, now.UnixNano(),
			)
		}
		if err != nil {
			return nil, err
		}
		if !leaseMatches {
			return nil, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease does not authorize Run completion"}
		}
	}
	if request.Termination != nil {
		return s.finishRunTermination(ctx, tx, stored, *request.Termination, request.CaptureDiagnostic)
	}
	if run.Spec.ResultSchema != ResultSchemaV1 {
		return nil, &DomainError{Code: ErrorInvalidResult, Detail: "execution run uses an unsupported result schema"}
	}
	maxIntents := run.Spec.MaxProposedIntents
	if run.Task == TaskReason && maxIntents <= 0 {
		maxIntents = 64 // v5 snapshots predate the explicit per-Run limit.
	}
	result, canonical, resultHash, err := DecodeRunResult(request.Candidate, run.Task, maxIntents)
	if err != nil {
		return nil, err
	}

	if run.Status != StatusActive {
		if run.ResultHash != resultHash || run.CaptureDiagnostic != request.CaptureDiagnostic ||
			run.ResultEvidenceSource != request.ResultEvidenceSource {
			return nil, &DomainError{Code: ErrorResultConflict, Detail: "execution run already finished with a different result"}
		}
		if !stored.ReceiptJSON.Valid {
			return nil, fmt.Errorf("finished execution run %s is missing its receipt", run.ID)
		}
		var receipt FinishReceipt
		if err := json.Unmarshal([]byte(stored.ReceiptJSON.String), &receipt); err != nil {
			return nil, fmt.Errorf("decode finish receipt: %w", err)
		}
		receipt.Replayed = true
		return &receipt, nil
	}

	nowText := now.Format(time.RFC3339Nano)
	claimMatches, err := s.ledger.ClaimMatches(ctx, tx, run, now.UnixNano())
	if err != nil {
		return nil, err
	}
	if !claimMatches {
		return nil, &DomainError{Code: ErrorFenced, Detail: "execution run no longer has a current claim"}
	}

	if s.projects == nil {
		return nil, &DomainError{Code: ErrorInternal, Detail: "Project result application is not configured"}
	}
	applied, err := s.projects.ApplyRunResult(ctx, tx, applyProjectResultCommand(run, result.Outcome, nowText))
	if err != nil {
		return nil, err
	}
	receipt := FinishReceipt{
		RunID:                run.ID,
		CaptureDiagnostic:    request.CaptureDiagnostic,
		ResultEvidenceSource: request.ResultEvidenceSource,
		OutcomeKind:          result.Outcome.Kind,
		ResultHash:           resultHash,
		ProjectRevision:      applied.ProjectRevision,
		FinishedAt:           now,
	}
	var change *ProjectChange
	switch {
	case result.Outcome.Kind == OutcomeDecline:
		receipt.Status = StatusDeclined
	case applied.Stale:
		receipt.Status = StatusStale
		receipt.Code = ErrorStaleInput
	default:
		receipt.Status = StatusCommitted
		change = applied.Change
		receipt.CreatedFactIDs = applied.CreatedFactIDs
		receipt.CreatedIntentIDs = applied.CreatedIntentIDs
		receipt.CompletionIntentID = applied.CompletionIntentID
		if run.Task == TaskReason {
			if err := s.ledger.RecordReasonCheckpoint(
				ctx, tx, run.ProjectID, run.Spec.Input.Generation,
				run.Spec.Input.Revision, run.ID, nowText,
			); err != nil {
				return nil, err
			}
		}
	}

	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return nil, fmt.Errorf("marshal finish receipt: %w", err)
	}
	rows, err := s.ledger.Finish(
		ctx, tx, run.ID, receipt.Status, resultHash, result.Outcome.Kind,
		receipt.Code, request.CaptureDiagnostic, request.ResultEvidenceSource,
		string(canonical), string(receiptJSON), nowText,
	)
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, &DomainError{Code: ErrorResultConflict, Detail: "execution run changed while finishing"}
	}
	if err := s.ledger.ApplyRetryOutcome(ctx, tx, run, receipt.Status, receipt.Code, nowText); err != nil {
		return nil, err
	}
	released, err := s.ledger.ReleaseClaim(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	if released != 1 {
		return nil, &DomainError{Code: ErrorFenced, Detail: "execution claim changed while finishing"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if change != nil && s.publisher != nil {
		s.publisher.PublishProjectChange(*change)
	}
	s.publishActivityChanged(run.ProjectID)
	return &receipt, nil
}

func (s *Service) finishRunTermination(
	ctx context.Context,
	tx *sql.Tx,
	stored storedRun,
	termination RunTermination,
	captureDiagnostic CaptureDiagnostic,
) (*FinishReceipt, error) {
	if termination.Status != StatusFailed && termination.Status != StatusCancelled &&
		termination.Status != StatusInterrupted {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "execution termination status is invalid"}
	}
	if termination.Status == StatusFailed && termination.Code == "" {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "failed execution requires an error code"}
	}
	if !termination.CaptureFailureReason.Valid() {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "capture failure reason is invalid"}
	}
	if !captureDiagnostic.Valid() {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "capture diagnostic is invalid"}
	}
	if termination.CaptureFailureReason != "" &&
		(termination.Status != StatusFailed || termination.Code != ExecutionCaptureFailed) {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "capture failure reason requires capture_failed"}
	}
	if captureDiagnostic != "" &&
		(termination.Status != StatusFailed ||
			(termination.Code != ExecutionCaptureFailed && termination.Code != ExecutionInvalidResult)) {
		return nil, &DomainError{Code: ErrorInvalidArgument, Detail: "capture diagnostic requires capture_failed or invalid_result"}
	}
	run := stored.Run
	if run.Status != StatusActive {
		if run.Status != termination.Status || run.ErrorCode != termination.Code ||
			run.CaptureFailureReason != termination.CaptureFailureReason ||
			run.CaptureDiagnostic != captureDiagnostic {
			return nil, &DomainError{Code: ErrorResultConflict, Detail: "execution run already finished with a different termination"}
		}
		if !stored.ReceiptJSON.Valid {
			return nil, fmt.Errorf("finished execution run %s is missing its receipt", run.ID)
		}
		var receipt FinishReceipt
		if err := json.Unmarshal([]byte(stored.ReceiptJSON.String), &receipt); err != nil {
			return nil, fmt.Errorf("decode finish receipt: %w", err)
		}
		receipt.Replayed = true
		return &receipt, nil
	}

	now := s.clock()
	nowText := now.Format(time.RFC3339Nano)
	claimMatches, err := s.ledger.ClaimMatches(ctx, tx, run, now.UnixNano())
	if err != nil {
		return nil, err
	}
	if !claimMatches {
		return nil, &DomainError{Code: ErrorFenced, Detail: "execution run no longer has a current claim"}
	}
	if s.projects == nil {
		return nil, &DomainError{Code: ErrorInternal, Detail: "Project revision lookup is not configured"}
	}
	revision, err := s.projects.Revision(ctx, tx, run.ProjectID)
	if err != nil {
		return nil, err
	}
	receipt := FinishReceipt{
		RunID: run.ID, Status: termination.Status, Code: termination.Code,
		CaptureFailureReason: termination.CaptureFailureReason,
		CaptureDiagnostic:    captureDiagnostic,
		ProjectRevision:      revision, FinishedAt: now,
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return nil, fmt.Errorf("marshal finish receipt: %w", err)
	}
	rows, err := s.ledger.FinishWithoutResult(
		ctx, tx, run.ID, termination.Status, termination.Code,
		termination.CaptureFailureReason, captureDiagnostic,
		string(receiptJSON), nowText,
	)
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, &DomainError{Code: ErrorResultConflict, Detail: "execution run changed while finishing"}
	}
	if err := s.ledger.ApplyRetryOutcome(ctx, tx, run, receipt.Status, receipt.Code, nowText); err != nil {
		return nil, err
	}
	released, err := s.ledger.ReleaseClaim(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	if released != 1 {
		return nil, &DomainError{Code: ErrorFenced, Detail: "execution claim changed while finishing"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.publishActivityChanged(run.ProjectID)
	return &receipt, nil
}

func applyProjectResultCommand(
	run ExecutionRun,
	outcome Outcome,
	appliedAt string,
) ApplyProjectResultCommand {
	return ApplyProjectResultCommand{
		RunID: string(run.ID), ProjectID: run.ProjectID, TargetIntent: run.TargetIntentID,
		Task: run.Task, Generation: run.Spec.Input.Generation,
		InputRevision: run.Spec.Input.Revision, Worker: run.Spec.Worker.Name,
		AppliedAt: appliedAt,
		Outcome:   cloneOutcome(outcome),
	}
}

func cloneOutcome(outcome Outcome) Outcome {
	copy := outcome
	copy.From = append([]string(nil), outcome.From...)
	if outcome.Fact != nil {
		fact := *outcome.Fact
		copy.Fact = &fact
	}
	copy.Intents = append([]ProposedIntent(nil), outcome.Intents...)
	for index := range copy.Intents {
		copy.Intents[index].From = append([]string(nil), copy.Intents[index].From...)
	}
	return copy
}
