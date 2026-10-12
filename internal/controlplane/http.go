package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

const (
	httpControlResponseBytes int64 = 1024 * 1024
	httpSchedulingBytes            = 64 * 1024 * 1024
	httpErrorPreviewBytes    int64 = 64 * 1024
)

// HTTPRunControl connects separately invoked native processes. It owns transport,
// response bounds and domain-error decoding so Scheduler sees one contract in
// trusted unified mode and separated native process invocation.
type HTTPRunControl struct {
	baseURL string
	client  *http.Client
}

func NewHTTPRunControl(baseURL string) (*HTTPRunControl, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("HTTP Control Plane URL is invalid")
	}
	return &HTTPRunControl{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (control *HTTPRunControl) SchedulingSnapshot(ctx context.Context, request scheduler.SchedulingSnapshotRequest) (scheduler.ControlSchedulingSnapshot, error) {
	var snapshot scheduler.ControlSchedulingSnapshot
	err := control.command(ctx, "/scheduling/actions/snapshot", request, &snapshot, httpSchedulingBytes)
	if err == nil {
		err = snapshot.Validate(request)
	}
	return snapshot, err
}

func (control *HTTPRunControl) ProjectRuntimeStatuses(ctx context.Context, request rundomain.ProjectRuntimeStatusRequest) ([]rundomain.ProjectRuntimeStatusResult, error) {
	var response struct {
		Statuses []rundomain.ProjectRuntimeStatusResult `json:"statuses"`
	}
	err := control.command(ctx, "/projects/actions/runtime-statuses", request, &response, httpControlResponseBytes)
	if err == nil {
		err = rundomain.ValidateProjectRuntimeStatusResults(request.ProjectIDs, response.Statuses)
	}
	return response.Statuses, err
}

func (control *HTTPRunControl) StartRun(ctx context.Context, request rundomain.StartRequest) (*rundomain.AdmittedRun, error) {
	var admitted rundomain.AdmittedRun
	if err := control.command(ctx, "/runs/actions/start", request, &admitted, httpSchedulingBytes); err != nil {
		return nil, err
	}
	if err := admitted.Validate(request); err != nil {
		return &admitted, err
	}
	return &admitted, nil
}
func (control *HTTPRunControl) BeginProcessRecovery(ctx context.Context, request rundomain.BeginProcessRecoveryRequest) (rundomain.ProcessRecoveryReservation, error) {
	var reservation rundomain.ProcessRecoveryReservation
	if err := control.command(ctx, "/runs/"+url.PathEscape(string(request.RunID))+"/actions/begin-process-recovery", request, &reservation, httpControlResponseBytes); err != nil {
		return rundomain.ProcessRecoveryReservation{}, err
	}
	if err := reservation.Validate(); err != nil {
		return rundomain.ProcessRecoveryReservation{}, err
	}
	return reservation, nil
}
func (control *HTTPRunControl) RenewRun(ctx context.Context, request rundomain.RenewRunRequest) (time.Time, error) {
	var response struct {
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	err := control.command(
		ctx, "/runs/"+url.PathEscape(string(request.RunID))+"/actions/renew", request,
		&response, httpControlResponseBytes,
	)
	return response.LeaseExpiresAt, err
}
func (control *HTTPRunControl) FinishRun(ctx context.Context, request rundomain.FinishRequest) (*rundomain.FinishReceipt, error) {
	var receipt rundomain.FinishReceipt
	if err := control.command(
		ctx, "/runs/"+url.PathEscape(string(request.RunID))+"/actions/finish", request,
		&receipt, httpControlResponseBytes,
	); err != nil {
		return nil, err
	}
	if receipt.RunID != request.RunID {
		return nil, &rundomain.DomainError{
			Code:   rundomain.ErrorExecutionContractMismatch,
			Detail: "HTTP Finish receipt Run identity does not match its request",
		}
	}
	if receipt.CaptureDiagnostic != request.CaptureDiagnostic {
		return nil, &rundomain.DomainError{
			Code:   rundomain.ErrorExecutionContractMismatch,
			Detail: "HTTP Finish receipt capture diagnostic does not match its request",
		}
	}
	if receipt.ResultEvidenceSource != request.ResultEvidenceSource {
		return nil, &rundomain.DomainError{
			Code:   rundomain.ErrorExecutionContractMismatch,
			Detail: "HTTP Finish receipt result evidence source does not match its request",
		}
	}
	if request.Termination != nil &&
		(receipt.Status != request.Termination.Status || receipt.Code != request.Termination.Code ||
			receipt.CaptureFailureReason != request.Termination.CaptureFailureReason) {
		return nil, &rundomain.DomainError{
			Code:   rundomain.ErrorExecutionContractMismatch,
			Detail: "HTTP Finish receipt terminal evidence does not match its request",
		}
	}
	return &receipt, nil
}
func (control *HTTPRunControl) RecoverRuns(ctx context.Context, options rundomain.RecoveryOptions) ([]rundomain.RunID, error) {
	var response struct {
		RunIDs []rundomain.RunID `json:"run_ids"`
	}
	err := control.command(ctx, "/runs/actions/recover", map[string]any{
		"dispatcher_id": options.DispatcherID, "dispatcher_fencing_token": options.DispatcherFencingToken,
		"reason": options.Reason,
	}, &response, httpControlResponseBytes)
	return response.RunIDs, err
}
func (control *HTTPRunControl) RecordRecoveryEvent(ctx context.Context, record rundomain.RecoveryRecord) error {
	return control.command(
		ctx, "/runs/"+url.PathEscape(string(record.RunID))+"/actions/record-recovery", record,
		nil, httpControlResponseBytes,
	)
}

func (control *HTTPRunControl) RecordExecutionHandle(ctx context.Context, request rundomain.ExecutionHandleRecordRequest) error {
	return control.command(
		ctx, "/runs/"+url.PathEscape(string(request.Handle.RunID))+"/actions/record-exec", request,
		nil, httpControlResponseBytes,
	)
}

func (control *HTTPRunControl) RecordProjectContainer(ctx context.Context, request rundomain.ProjectContainerRecordRequest) error {
	return control.command(
		ctx, "/runs/"+url.PathEscape(string(request.Handle.RunID))+"/actions/record-project-container", request,
		nil, httpControlResponseBytes,
	)
}

func (control *HTTPRunControl) AuthorizeRunContinuation(ctx context.Context, request rundomain.RunContinuationRequest) error {
	return control.command(
		ctx, "/runs/"+url.PathEscape(string(request.RunID))+"/actions/authorize-continuation", request,
		nil, httpControlResponseBytes,
	)
}

func (control *HTTPRunControl) ListExecutionHandles(ctx context.Context, request rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error) {
	var response struct {
		Handles []rundomain.ExecutionHandle `json:"handles"`
	}
	if err := control.command(ctx, "/runs/actions/list-execs", request, &response, httpControlResponseBytes); err != nil {
		return nil, err
	}
	return response.Handles, nil
}

func (control *HTTPRunControl) MarkExecutionHandleStopped(ctx context.Context, request rundomain.ExecutionHandleStopRequest) error {
	return control.command(ctx, "/runs/"+url.PathEscape(string(request.Handle.RunID))+"/actions/record-exec-stop", request, nil, httpControlResponseBytes)
}

func (control *HTTPRunControl) LeaseProjectCleanupJobs(ctx context.Context, request rundomain.CleanupLeaseRequest) ([]rundomain.ProjectCleanupJob, error) {
	var response struct {
		Jobs []rundomain.ProjectCleanupJob `json:"jobs"`
	}
	err := control.command(ctx, "/cleanup-jobs/actions/lease", request, &response, httpControlResponseBytes)
	return response.Jobs, err
}

func (control *HTTPRunControl) CompleteProjectCleanupJob(ctx context.Context, request rundomain.CleanupCompletionRequest) error {
	return control.command(ctx, "/cleanup-jobs/"+url.PathEscape(request.JobID)+"/actions/complete", request, nil, httpControlResponseBytes)
}

func (control *HTTPRunControl) AcquireDispatcherLease(
	ctx context.Context,
	request rundomain.DispatcherLeaseRequest,
) (rundomain.DispatcherLease, error) {
	var lease rundomain.DispatcherLease
	err := control.command(ctx, "/dispatcher-leases/actions/acquire", map[string]any{
		"instance_id": request.InstanceID, "ttl_seconds": int64(request.TTL / time.Second),
	}, &lease, httpControlResponseBytes)
	return lease, err
}

func (control *HTTPRunControl) RenewDispatcherLease(
	ctx context.Context,
	lease rundomain.DispatcherLease,
	ttl time.Duration,
) (rundomain.DispatcherLease, error) {
	var renewed rundomain.DispatcherLease
	err := control.command(
		ctx, "/dispatcher-leases/"+url.PathEscape(lease.InstanceID)+"/actions/renew", map[string]any{
			"instance_id": lease.InstanceID, "fencing_token": lease.FencingToken,
			"ttl_seconds": int64(ttl / time.Second),
		}, &renewed, httpControlResponseBytes,
	)
	return renewed, err
}

func (control *HTTPRunControl) ReleaseDispatcherLease(ctx context.Context, lease rundomain.DispatcherLease) error {
	return control.command(
		ctx, "/dispatcher-leases/"+url.PathEscape(lease.InstanceID)+"/actions/release", map[string]any{
			"instance_id": lease.InstanceID, "fencing_token": lease.FencingToken,
		}, nil, httpControlResponseBytes,
	)
}

func (control *HTTPRunControl) ReadActiveExecutionConfig(ctx context.Context) (*config.DispatchConfig, error) {
	var revision config.ConfigurationRevision
	if err := control.get(ctx, "/configuration/revisions/active", &revision, httpControlResponseBytes); err != nil {
		return nil, err
	}
	return config.DispatchConfigFromRevision(revision)
}

func (control *HTTPRunControl) ReadExecutionConfig(ctx context.Context, revision string) (*config.DispatchConfig, error) {
	var public config.ConfigurationRevision
	if err := control.get(ctx, "/configuration/revisions/"+url.PathEscape(revision), &public, httpControlResponseBytes); err != nil {
		return nil, err
	}
	return config.DispatchConfigFromRevision(public)
}

func (control *HTTPRunControl) ResolveRunSecrets(
	ctx context.Context, started rundomain.StartedRun, references []rundomain.SecretReference,
) (*rundomain.ResolvedSecrets, error) {
	var response struct {
		Environment map[string]string `json:"environment"`
	}
	if err := control.command(ctx, "/runs/"+url.PathEscape(string(started.Run.ID))+"/actions/resolve-secrets", rundomain.RunSecretRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}, &response, httpControlResponseBytes); err != nil {
		return nil, err
	}
	return resolvedSecretsFromEnvironment(response.Environment, references)
}

func (control *HTTPRunControl) ResolveRevisionSecrets(
	ctx context.Context, revision, workerName string, lease rundomain.DispatcherLease,
) (*rundomain.ResolvedSecrets, error) {
	var response struct {
		Environment map[string]string `json:"environment"`
	}
	if err := control.command(ctx, "/configuration/revisions/"+url.PathEscape(revision)+"/workers/"+url.PathEscape(workerName)+"/actions/resolve-secrets", map[string]any{
		"instance_id": lease.InstanceID, "fencing_token": lease.FencingToken,
	}, &response, httpControlResponseBytes); err != nil {
		return nil, err
	}
	return rundomain.NewResolvedSecrets(stringMapBytes(response.Environment)), nil
}

func resolvedSecretsFromEnvironment(environment map[string]string, references []rundomain.SecretReference) (*rundomain.ResolvedSecrets, error) {
	expected := make(map[string]struct{}, len(references))
	for _, reference := range references {
		expected[reference.EnvironmentVariable] = struct{}{}
	}
	if len(environment) != len(expected) {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorExecutionContractMismatch, Detail: "HTTP Secret response did not match the Run Worker contract"}
	}
	for variable := range environment {
		if _, ok := expected[variable]; !ok {
			return nil, &rundomain.DomainError{Code: rundomain.ErrorExecutionContractMismatch, Detail: "HTTP Secret response did not match the Run Worker contract"}
		}
	}
	return rundomain.NewResolvedSecrets(stringMapBytes(environment)), nil
}

func stringMapBytes(values map[string]string) map[string][]byte {
	result := make(map[string][]byte, len(values))
	for key, value := range values {
		result[key] = []byte(value)
	}
	return result
}

func (control *HTTPRunControl) Close() { control.client.CloseIdleConnections() }

func (control *HTTPRunControl) get(
	ctx context.Context, path string, responseBody any, maximumResponseBytes int64,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, control.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("create HTTP Control Plane request: %w", err)
	}
	response, err := control.client.Do(request)
	if err != nil {
		return fmt.Errorf("execute HTTP Control Plane request: %w", err)
	}
	defer response.Body.Close()
	limit := maximumResponseBytes
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = httpErrorPreviewBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read HTTP Control Plane response: %w", err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("HTTP Control Plane response exceeded %d bytes", limit)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Code != "" {
			return fmt.Errorf("HTTP Control Plane %s: %s", failure.Code, failure.Detail)
		}
		return fmt.Errorf("HTTP Control Plane returned status %d", response.StatusCode)
	}
	if err := json.Unmarshal(body, responseBody); err != nil {
		return fmt.Errorf("decode HTTP Control Plane response: %w", err)
	}
	return nil
}

func (control *HTTPRunControl) command(
	ctx context.Context,
	path string,
	requestBody any,
	responseBody any,
	maximumResponseBytes int64,
) error {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode HTTP Control Plane command: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, control.baseURL+path, bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("create HTTP Control Plane request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := control.client.Do(request)
	if err != nil {
		return fmt.Errorf("execute HTTP Control Plane request: %w", err)
	}
	defer response.Body.Close()

	limit := maximumResponseBytes
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = httpErrorPreviewBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read HTTP Control Plane response: %w", err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("HTTP Control Plane response exceeded %d bytes", limit)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Code   rundomain.ErrorCode `json:"code"`
			Detail string              `json:"detail"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Code != "" {
			return &rundomain.DomainError{Code: failure.Code, Detail: failure.Detail}
		}
		return fmt.Errorf("HTTP Control Plane returned status %d", response.StatusCode)
	}
	if responseBody == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, responseBody); err != nil {
		return fmt.Errorf("decode HTTP Control Plane response: %w", err)
	}
	return nil
}

var _ scheduler.SchedulingControl = (*HTTPRunControl)(nil)
var _ scheduler.ProjectInventoryControl = (*HTTPRunControl)(nil)
var _ scheduler.RunRecoveryControl = (*HTTPRunControl)(nil)
var _ scheduler.RunLifecycleControl = (*HTTPRunControl)(nil)
var _ scheduler.ProjectCleanupControl = (*HTTPRunControl)(nil)
