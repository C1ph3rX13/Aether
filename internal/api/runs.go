package api

import (
	"context"
	"net/http"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// RunControl is the HTTP transport port for scheduler commands.
type RunControl interface {
	SchedulerSnapshot(context.Context, rundomain.SchedulingSnapshotRequest) (scheduler.ControlSchedulingSnapshot, error)
	ProjectRuntimeStatuses(context.Context, rundomain.ProjectRuntimeStatusRequest) ([]rundomain.ProjectRuntimeStatusResult, error)
	AcquireDispatcherLease(context.Context, rundomain.DispatcherLeaseRequest) (rundomain.DispatcherLease, error)
	RenewDispatcherLease(context.Context, rundomain.DispatcherLease, time.Duration) (rundomain.DispatcherLease, error)
	ReleaseDispatcherLease(context.Context, rundomain.DispatcherLease) error
	StartRun(context.Context, rundomain.StartRequest) (*rundomain.AdmittedRun, error)
	BeginProcessRecovery(context.Context, rundomain.BeginProcessRecoveryRequest) (rundomain.ProcessRecoveryReservation, error)
	RenewRun(context.Context, rundomain.RenewRunRequest) (time.Time, error)
	FinishRun(context.Context, rundomain.FinishRequest) (*rundomain.FinishReceipt, error)
	RecoverRuns(context.Context, rundomain.RecoveryOptions) ([]rundomain.RunID, error)
	RecordRecoveryEvent(context.Context, rundomain.RecoveryRecord) error
	RecordExecutionHandle(context.Context, rundomain.ExecutionHandleRecordRequest) error
	RecordProjectContainer(context.Context, rundomain.ProjectContainerRecordRequest) error
	AuthorizeRunContinuation(context.Context, rundomain.RunContinuationRequest) error
	ListExecutionHandles(context.Context, rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error)
	MarkExecutionHandleStopped(context.Context, rundomain.ExecutionHandleStopRequest) error
	LeaseProjectCleanupJobs(context.Context, rundomain.CleanupLeaseRequest) ([]rundomain.ProjectCleanupJob, error)
	CompleteProjectCleanupJob(context.Context, rundomain.CleanupCompletionRequest) error
}

type recoverRunsRequest struct {
	DispatcherID           string                 `json:"dispatcher_id" binding:"required"`
	DispatcherFencingToken rundomain.FencingToken `json:"dispatcher_fencing_token" binding:"required"`
	Reason                 string                 `json:"reason" binding:"required"`
}

type recordRecoveryEventRequest struct {
	RunID                  rundomain.RunID          `json:"run_id" binding:"required"`
	DispatcherID           string                   `json:"dispatcher_id" binding:"required"`
	DispatcherFencingToken rundomain.FencingToken   `json:"dispatcher_fencing_token" binding:"required"`
	Status                 rundomain.RecoveryStatus `json:"status" binding:"required"`
	Reason                 string                   `json:"reason" binding:"required"`
}

type recordExecutionHandleRequest struct {
	Handle                 rundomain.ExecutionHandle `json:"handle" binding:"required"`
	RunFencingToken        rundomain.FencingToken    `json:"run_fencing_token" binding:"required"`
	DispatcherID           string                    `json:"dispatcher_id" binding:"required"`
	DispatcherFencingToken rundomain.FencingToken    `json:"dispatcher_fencing_token" binding:"required"`
}

type listExecutionHandlesRequest struct {
	RunIDs                 []rundomain.RunID      `json:"run_ids" binding:"required"`
	DispatcherID           string                 `json:"dispatcher_id" binding:"required"`
	DispatcherFencingToken rundomain.FencingToken `json:"dispatcher_fencing_token" binding:"required"`
}

type markExecutionHandleStoppedRequest struct {
	Handle                 rundomain.ExecutionHandle `json:"handle" binding:"required"`
	DispatcherID           string                    `json:"dispatcher_id" binding:"required"`
	DispatcherFencingToken rundomain.FencingToken    `json:"dispatcher_fencing_token" binding:"required"`
}

// RegisterRuns exposes complete dispatcher commands. Resource
// identifiers live in the path; request copies are verified before execution.
func RegisterRuns(group *gin.RouterGroup, service RunControl) {
	group.POST("/projects/actions/runtime-statuses", func(c *gin.Context) {
		var request rundomain.ProjectRuntimeStatusRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		statuses, err := service.ProjectRuntimeStatuses(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"statuses": statuses})
	})

	group.POST("/scheduling/actions/snapshot", func(c *gin.Context) {
		var request rundomain.SchedulingSnapshotRequest
		if !bindJSON(c, &request, runControlSchedulingBodyBytes) {
			return
		}
		snapshot, err := service.SchedulerSnapshot(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, snapshot)
	})

	registerLeases(group, service)

	group.POST("/runs/actions/start", func(c *gin.Context) {
		var request rundomain.StartRequest
		if !bindJSON(c, &request, maximumRequestBodyBytes) {
			return
		}
		started, err := service.StartRun(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, started)
	})

	group.POST("/runs/:run_id/actions/begin-process-recovery", func(c *gin.Context) {
		var request rundomain.BeginProcessRecoveryRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		reservation, err := service.BeginProcessRecovery(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, reservation)
	})

	group.POST("/runs/:run_id/actions/renew", func(c *gin.Context) {
		var request rundomain.RenewRunRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		expiresAt, err := service.RenewRun(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"lease_expires_at": expiresAt})
	})

	group.POST("/runs/:run_id/actions/finish", func(c *gin.Context) {
		var request rundomain.FinishRequest
		if !bindJSON(c, &request, runControlFinishBodyBytes) {
			return
		}
		if string(request.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		receipt, err := service.FinishRun(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, receipt)
	})

	group.POST("/runs/actions/recover", func(c *gin.Context) {
		var request recoverRunsRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		runIDs, err := service.RecoverRuns(c.Request.Context(), rundomain.RecoveryOptions{
			DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken,
			Reason: request.Reason,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"run_ids": runIDs})
	})

	group.POST("/runs/:run_id/actions/record-recovery", func(c *gin.Context) {
		var request recordRecoveryEventRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		err := service.RecordRecoveryEvent(c.Request.Context(), rundomain.RecoveryRecord{
			RunID: request.RunID, DispatcherID: request.DispatcherID,
			DispatcherFencingToken: request.DispatcherFencingToken,
			Status:                 request.Status, Reason: request.Reason,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	group.POST("/runs/:run_id/actions/record-exec", func(c *gin.Context) {
		var request recordExecutionHandleRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.Handle.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		if err := service.RecordExecutionHandle(c.Request.Context(), rundomain.ExecutionHandleRecordRequest{
			Handle: request.Handle, RunFencingToken: request.RunFencingToken,
			DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken,
		}); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	group.POST("/runs/:run_id/actions/record-project-container", func(c *gin.Context) {
		var request rundomain.ProjectContainerRecordRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.Handle.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		if err := service.RecordProjectContainer(c.Request.Context(), request); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	group.POST("/runs/:run_id/actions/authorize-continuation", func(c *gin.Context) {
		var request rundomain.RunContinuationRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		if err := service.AuthorizeRunContinuation(c.Request.Context(), request); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	group.POST("/runs/actions/list-execs", func(c *gin.Context) {
		var request listExecutionHandlesRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		handles, err := service.ListExecutionHandles(c.Request.Context(), rundomain.ExecutionHandleListRequest{
			RunIDs: request.RunIDs, DispatcherID: request.DispatcherID,
			DispatcherFencingToken: request.DispatcherFencingToken,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"handles": handles})
	})

	group.POST("/runs/:run_id/actions/record-exec-stop", func(c *gin.Context) {
		var request markExecutionHandleStoppedRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.Handle.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		if err := service.MarkExecutionHandleStopped(c.Request.Context(), rundomain.ExecutionHandleStopRequest{
			Handle: request.Handle, DispatcherID: request.DispatcherID,
			DispatcherFencingToken: request.DispatcherFencingToken,
		}); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	group.POST("/cleanup-jobs/actions/lease", func(c *gin.Context) {
		var request rundomain.CleanupLeaseRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		jobs, err := service.LeaseProjectCleanupJobs(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"jobs": jobs})
	})
	group.POST("/cleanup-jobs/:job_id/actions/complete", func(c *gin.Context) {
		var request rundomain.CleanupCompletionRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if request.JobID != c.Param("job_id") {
			writePathIdentityError(c, "Cleanup job")
			return
		}
		if err := service.CompleteProjectCleanupJob(c.Request.Context(), request); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}

func writePathIdentityError(c *gin.Context, resource string) {
	writeCodedError(
		c, http.StatusBadRequest, rundomain.ErrorInvalidArgument,
		resource+" ID in path and request must match",
	)
}
