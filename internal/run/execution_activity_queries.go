package run

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type ActivityView string

const (
	ActivityAll       ActivityView = "all"
	ActivityActive    ActivityView = "active"
	ActivityAttention ActivityView = "attention"
	ActivityFinished  ActivityView = "finished"
)

type ActivityQuery struct {
	Cursor string
	Limit  int
	View   ActivityView
	Task   TaskType
	Search string
}

type AttentionKind string

const (
	AttentionNone           AttentionKind = ""
	AttentionFailed         AttentionKind = "failed"
	AttentionInterrupted    AttentionKind = "interrupted"
	AttentionRetryBackoff   AttentionKind = "retry_backoff"
	AttentionRetryExhausted AttentionKind = "retry_exhausted"
)

type ActivityRun struct {
	ID                   RunID                `json:"id"`
	ProjectID            string               `json:"project_id"`
	TargetIntentID       *string              `json:"target_intent_id,omitempty"`
	PreviousRunID        *RunID               `json:"previous_run_id,omitempty"`
	Task                 TaskType             `json:"task"`
	Status               Status               `json:"status"`
	Attempt              int64                `json:"attempt"`
	Worker               string               `json:"worker"`
	InputGeneration      int                  `json:"input_generation"`
	InputRevision        int64                `json:"input_revision"`
	StartedAt            time.Time            `json:"started_at"`
	FinishedAt           *time.Time           `json:"finished_at,omitempty"`
	DurationMS           *int64               `json:"duration_ms,omitempty"`
	OutcomeKind          OutcomeKind          `json:"outcome_kind,omitempty"`
	ErrorCode            ErrorCode            `json:"error_code,omitempty"`
	CaptureFailureReason CaptureFailureReason `json:"capture_failure_reason,omitempty"`
	CaptureDiagnostic    CaptureDiagnostic    `json:"capture_diagnostic,omitempty"`
	ResultEvidenceSource ResultEvidenceSource `json:"result_evidence_source,omitempty"`
	NextEligibleAt       *time.Time           `json:"next_eligible_at,omitempty"`
	Attention            AttentionKind        `json:"attention,omitempty"`
}

type ActivityPage struct {
	Runs       []ActivityRun `json:"runs"`
	NextCursor *string       `json:"next_cursor,omitempty"`
}

// ActivityRunDetail exposes immutable operational evidence without exposing
// prompts, output, environment variables, Secret references, files, or sessions.
type ActivityRunDetail struct {
	ActivityRun
	WorkKey                WorkKey                 `json:"work_key"`
	ClaimScope             ClaimScope              `json:"claim_scope"`
	FencingToken           FencingToken            `json:"fencing_token"`
	DispatcherID           string                  `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken            `json:"dispatcher_fencing_token"`
	ExecutionSpecSchema    string                  `json:"execution_spec_schema"`
	ConfigRevision         string                  `json:"config_revision"`
	ImageReference         string                  `json:"image_reference"`
	ImageID                string                  `json:"image_id,omitempty"`
	ImageRepoDigest        string                  `json:"image_repo_digest,omitempty"`
	InputEvidence          InputIdentity           `json:"input_evidence"`
	PromptEvidence         PromptIdentity          `json:"prompt_evidence"`
	ResultHash             string                  `json:"result_hash,omitempty"`
	ResultSchema           string                  `json:"result_schema"`
	OperatorRetries        []OperatorRetryEvidence `json:"operator_retries"`
}

type OperatorRetryEvidence struct {
	RequestID            string    `json:"request_id"`
	OperatorName         string    `json:"operator_name"`
	PreviousFailureCount int64     `json:"previous_failure_count"`
	CreatedAt            time.Time `json:"created_at"`
}

func (s *Service) ListProjectActivity(ctx context.Context, projectID string, query ActivityQuery) (ActivityPage, error) {
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 100 {
		return ActivityPage{}, newError(ErrorInvalidArgument, "limit must be between 1 and 100")
	}
	if query.View == "" {
		query.View = ActivityAll
	}
	if query.View != ActivityAll && query.View != ActivityActive && query.View != ActivityAttention && query.View != ActivityFinished {
		return ActivityPage{}, newError(ErrorInvalidArgument, "activity view is invalid")
	}
	if query.Task != "" && !query.Task.valid() {
		return ActivityPage{}, newError(ErrorInvalidArgument, "task filter is invalid")
	}
	search := strings.TrimSpace(query.Search)
	if len(search) > 128 {
		return ActivityPage{}, newError(ErrorInvalidArgument, "search is too long")
	}
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return ActivityPage{}, err
	}
	defer conn.Close()
	var exists int
	if err := conn.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", projectID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ActivityPage{}, newError(ErrorNotFound, "project not found")
		}
		return ActivityPage{}, err
	}

	where := []string{"r.project_id = ?"}
	args := []any{projectID}
	if query.Cursor != "" {
		startedAt, id, err := decodeActivityCursor(query.Cursor)
		if err != nil {
			return ActivityPage{}, newError(ErrorInvalidArgument, "activity cursor is invalid")
		}
		where = append(where, "(r.started_at < ? OR (r.started_at = ? AND r.id < ?))")
		args = append(args, startedAt, startedAt, id)
	}
	switch query.View {
	case ActivityActive:
		where = append(where, "r.status = 'active'")
	case ActivityAttention:
		where = append(where, "(r.status IN ('failed','interrupted') OR wk.retry_state IN ('backoff','exhausted'))")
	case ActivityFinished:
		where = append(where, "r.status <> 'active'")
	}
	if query.Task != "" {
		where = append(where, "r.task_type = ?")
		args = append(args, query.Task)
	}
	if search != "" {
		where = append(where, "(r.id LIKE ? ESCAPE '\\' OR COALESCE(r.target_intent_id, '') LIKE ? ESCAPE '\\' OR r.worker_name LIKE ? ESCAPE '\\')")
		pattern := "%" + escapeLike(search) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	args = append(args, query.Limit+1)
	rows, err := conn.QueryContext(ctx, activityRunQuery+`
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY r.started_at DESC, r.id DESC LIMIT ?`, args...)
	if err != nil {
		return ActivityPage{}, err
	}
	defer rows.Close()
	page := ActivityPage{Runs: []ActivityRun{}}
	for rows.Next() {
		run, err := scanActivityRun(rows)
		if err != nil {
			return ActivityPage{}, err
		}
		page.Runs = append(page.Runs, run)
	}
	if err := rows.Err(); err != nil {
		return ActivityPage{}, err
	}
	if len(page.Runs) > query.Limit {
		page.Runs = page.Runs[:query.Limit]
		last := page.Runs[len(page.Runs)-1]
		cursor := encodeActivityCursor(last.StartedAt.Format(time.RFC3339Nano), string(last.ID))
		page.NextCursor = &cursor
	}
	return page, nil
}

func (s *Service) GetProjectActivityRun(ctx context.Context, projectID string, runID RunID) (ActivityRun, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return ActivityRun{}, err
	}
	defer conn.Close()
	row := conn.QueryRowContext(ctx, activityRunQuery+`
		WHERE r.project_id = ? AND r.id = ?`, projectID, runID)
	run, err := scanActivityRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityRun{}, newError(ErrorNotFound, "execution run not found")
	}
	return run, err
}

func (s *Service) GetProjectActivityRunDetail(ctx context.Context, projectID string, runID RunID) (ActivityRunDetail, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return ActivityRunDetail{}, err
	}
	defer conn.Close()
	stored, err := s.ledger.GetForFinish(ctx, conn, runID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && stored.Run.ProjectID != projectID {
		return ActivityRunDetail{}, newError(ErrorNotFound, "execution run not found")
	}
	if err != nil {
		return ActivityRunDetail{}, err
	}
	var retryState RetryState
	if err := conn.QueryRowContext(ctx, "SELECT retry_state FROM execution_work_keys WHERE work_key = ?", stored.Run.WorkKey).Scan(&retryState); err != nil {
		return ActivityRunDetail{}, err
	}
	activity := activityRunFromExecution(stored.Run, retryState)
	retries, err := readOperatorRetries(ctx, conn, runID)
	if err != nil {
		return ActivityRunDetail{}, err
	}
	return ActivityRunDetail{
		ActivityRun: activity, WorkKey: stored.Run.WorkKey, ClaimScope: stored.Run.ClaimScope,
		FencingToken: stored.Run.FencingToken, DispatcherID: stored.Run.DispatcherID,
		DispatcherFencingToken: stored.Run.DispatcherFencingToken,
		ExecutionSpecSchema:    stored.Run.Spec.Schema,
		ConfigRevision:         stored.Run.Spec.ConfigRevision, ImageReference: stored.Run.Spec.Image.RequestedRef,
		ImageID: stored.Run.Spec.Image.ImageID, ImageRepoDigest: stored.Run.Spec.Image.RepoDigest,
		InputEvidence: stored.Run.Spec.Input.Identity, PromptEvidence: stored.Run.Spec.PromptEvidence(),
		ResultHash: stored.Run.ResultHash, ResultSchema: stored.Run.Spec.ResultSchema,
		OperatorRetries: retries,
	}, nil
}

func (s *Service) GetProjectRetryChain(ctx context.Context, projectID string, runID RunID) ([]ActivityRun, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, activityRunQuery+`
		WHERE r.project_id = ? AND r.work_key = (
			SELECT work_key FROM execution_runs WHERE project_id = ? AND id = ?
		) ORDER BY r.attempt, r.id`, projectID, projectID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chain := []ActivityRun{}
	for rows.Next() {
		run, err := scanActivityRun(rows)
		if err != nil {
			return nil, err
		}
		chain = append(chain, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, newError(ErrorNotFound, "execution run not found")
	}
	return chain, nil
}

func activityRunFromExecution(execution ExecutionRun, retryState RetryState) ActivityRun {
	run := ActivityRun{
		ID: execution.ID, ProjectID: execution.ProjectID, TargetIntentID: execution.TargetIntentID,
		PreviousRunID: execution.PreviousRunID, Task: execution.Task, Status: execution.Status,
		Attempt: execution.Attempt, Worker: execution.Spec.Worker.Name,
		InputGeneration: execution.Spec.Input.Generation, InputRevision: execution.Spec.Input.Revision,
		StartedAt: execution.StartedAt, FinishedAt: execution.FinishedAt,
		OutcomeKind: execution.OutcomeKind, ErrorCode: execution.ErrorCode,
		CaptureFailureReason: execution.CaptureFailureReason,
		CaptureDiagnostic:    execution.CaptureDiagnostic,
		ResultEvidenceSource: execution.ResultEvidenceSource,
		NextEligibleAt:       execution.NextEligibleAt, Attention: activityAttention(execution.Status, retryState),
	}
	if execution.FinishedAt != nil {
		duration := execution.FinishedAt.Sub(execution.StartedAt).Milliseconds()
		run.DurationMS = &duration
	}
	return run
}

func activityAttention(status Status, retryState RetryState) AttentionKind {
	if retryState == RetryExhausted {
		return AttentionRetryExhausted
	}
	switch status {
	case StatusFailed:
		return AttentionFailed
	case StatusInterrupted:
		return AttentionInterrupted
	}
	if retryState == RetryBackoff {
		return AttentionRetryBackoff
	}
	return AttentionNone
}

type activityScanner interface{ Scan(...any) error }

const activityRunQuery = `
SELECT r.id, r.project_id, r.target_intent_id, r.previous_run_id,
	r.task_type, r.status, r.attempt, r.worker_name,
	r.input_generation, r.input_revision, r.started_at, r.finished_at,
	r.outcome_kind, r.error_code, r.capture_failure_reason, r.capture_diagnostic, r.result_evidence_source, r.next_eligible_at,
	CASE
		WHEN wk.retry_state = 'exhausted' THEN 'retry_exhausted'
		WHEN r.status = 'failed' THEN 'failed'
		WHEN r.status = 'interrupted' THEN 'interrupted'
		WHEN wk.retry_state = 'backoff' THEN 'retry_backoff'
		ELSE ''
	END
FROM execution_runs r
JOIN execution_work_keys wk ON wk.work_key = r.work_key
`

func scanActivityRun(row activityScanner) (ActivityRun, error) {
	var run ActivityRun
	var target, previous, finished, outcome, code, captureFailureReason, captureDiagnostic, resultEvidenceSource, next sql.NullString

	var started string
	var attention string
	if err := row.Scan(&run.ID, &run.ProjectID, &target, &previous, &run.Task, &run.Status,
		&run.Attempt, &run.Worker, &run.InputGeneration, &run.InputRevision, &started, &finished,
		&outcome, &code, &captureFailureReason, &captureDiagnostic, &resultEvidenceSource, &next, &attention); err != nil {
		return ActivityRun{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return ActivityRun{}, fmt.Errorf("parse Run start: %w", err)
	}
	run.StartedAt = parsed
	if target.Valid {
		run.TargetIntentID = &target.String
	}
	if previous.Valid {
		value := RunID(previous.String)
		run.PreviousRunID = &value
	}
	if outcome.Valid {
		run.OutcomeKind = OutcomeKind(outcome.String)
	}
	if code.Valid {
		run.ErrorCode = ErrorCode(code.String)
	}
	if captureFailureReason.Valid {
		run.CaptureFailureReason = CaptureFailureReason(captureFailureReason.String)
	}
	if captureDiagnostic.Valid {
		run.CaptureDiagnostic = CaptureDiagnostic(captureDiagnostic.String)
	}
	if resultEvidenceSource.Valid {
		run.ResultEvidenceSource = ResultEvidenceSource(resultEvidenceSource.String)
	}
	if next.Valid {
		value, err := time.Parse(time.RFC3339Nano, next.String)
		if err != nil {
			return ActivityRun{}, err
		}
		run.NextEligibleAt = &value
	}
	if finished.Valid {
		value, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return ActivityRun{}, err
		}
		run.FinishedAt = &value
		duration := value.Sub(parsed).Milliseconds()
		run.DurationMS = &duration
	}
	run.Attention = AttentionKind(attention)
	return run, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

func encodeActivityCursor(startedAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(len(startedAt)) + ":" + startedAt + id))
}

func decodeActivityCursor(value string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", errors.New("invalid cursor")
	}
	length, err := strconv.Atoi(parts[0])
	if err != nil || length < 1 || len(parts[1]) <= length {
		return "", "", errors.New("invalid cursor")
	}
	return parts[1][:length], parts[1][length:], nil
}
