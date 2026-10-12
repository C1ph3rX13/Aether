package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

type LivenessProbe interface {
	Ping(context.Context) error
}

type ReadinessProbe interface {
	HasActiveDispatcherLease(context.Context) (bool, error)
}

type ConfigurationReadinessProbe interface {
	HasActiveConfiguration(context.Context) (bool, error)
}

func RegisterHealth(
	group *gin.RouterGroup,
	liveness LivenessProbe,
	readiness ReadinessProbe,
	configuration ConfigurationReadinessProbe,
) {
	group.GET("/health", func(c *gin.Context) {
		if err := liveness.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "reason": "database_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	group.GET("/ready", func(c *gin.Context) {
		if err := liveness.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "database_unavailable"})
			return
		}
		configured := true
		if configuration != nil {
			var err error
			configured, err = configuration.HasActiveConfiguration(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "database_unavailable"})
				return
			}
		}
		if !configured {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "configuration_required"})
			return
		}
		active, err := readiness.HasActiveDispatcherLease(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "database_unavailable"})
			return
		}
		if !active {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "dispatcher_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
}
