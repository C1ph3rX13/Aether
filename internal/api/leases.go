package api

import (
	"net/http"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type acquireDispatcherLeaseRequest struct {
	InstanceID string `json:"instance_id" binding:"required"`
	TTLSeconds int64  `json:"ttl_seconds" binding:"required"`
}

type renewDispatcherLeaseRequest struct {
	InstanceID   string                 `json:"instance_id" binding:"required"`
	FencingToken rundomain.FencingToken `json:"fencing_token" binding:"required"`
	TTLSeconds   int64                  `json:"ttl_seconds" binding:"required"`
}

type releaseDispatcherLeaseRequest struct {
	InstanceID   string                 `json:"instance_id" binding:"required"`
	FencingToken rundomain.FencingToken `json:"fencing_token" binding:"required"`
}

func dispatcherLeaseTTL(seconds int64) (time.Duration, error) {
	maximumSeconds := int64(rundomain.MaximumLeaseExtension / time.Second)
	if seconds < 5 || seconds > maximumSeconds {
		return 0, &rundomain.DomainError{
			Code: rundomain.ErrorInvalidArgument, Detail: "Dispatcher Lease TTL must be between 5 and 3600 seconds",
		}
	}
	return time.Duration(seconds) * time.Second, nil
}

func registerLeases(group *gin.RouterGroup, service RunControl) {
	group.POST("/dispatcher-leases/actions/acquire", func(c *gin.Context) {
		var request acquireDispatcherLeaseRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		ttl, err := dispatcherLeaseTTL(request.TTLSeconds)
		if err != nil {
			writeError(c, err)
			return
		}
		lease, err := service.AcquireDispatcherLease(c.Request.Context(), rundomain.DispatcherLeaseRequest{
			InstanceID: request.InstanceID, TTL: ttl,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, lease)
	})

	group.POST("/dispatcher-leases/:instance_id/actions/renew", func(c *gin.Context) {
		var request renewDispatcherLeaseRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if request.InstanceID != c.Param("instance_id") {
			writePathIdentityError(c, "Dispatcher instance")
			return
		}
		ttl, err := dispatcherLeaseTTL(request.TTLSeconds)
		if err != nil {
			writeError(c, err)
			return
		}
		lease, err := service.RenewDispatcherLease(c.Request.Context(), rundomain.DispatcherLease{
			InstanceID: request.InstanceID, FencingToken: request.FencingToken,
		}, ttl)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, lease)
	})

	group.POST("/dispatcher-leases/:instance_id/actions/release", func(c *gin.Context) {
		var request releaseDispatcherLeaseRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if request.InstanceID != c.Param("instance_id") {
			writePathIdentityError(c, "Dispatcher instance")
			return
		}
		err := service.ReleaseDispatcherLease(c.Request.Context(), rundomain.DispatcherLease{
			InstanceID: request.InstanceID, FencingToken: request.FencingToken,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

}
