package api

import (
	"context"
	"net/http"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/gin-gonic/gin"
)

type Configuration interface {
	Read(context.Context) (*config.ConfigurationRead, error)
	SaveDraft(context.Context, config.DynamicDraftSaveRequest) (*config.ConfigurationRead, error)
	DeleteWorker(context.Context, string, config.DynamicWorkerDeleteRequest) (*config.ConfigurationRead, error)
	ReadActiveRevision(context.Context) (*config.ConfigurationRevision, error)
	ReadRevision(context.Context, string) (*config.ConfigurationRevision, error)
}

func RegisterConfiguration(group *gin.RouterGroup, service Configuration) {
	configuration := group.Group("/configuration")
	configuration.GET("", func(c *gin.Context) {
		read, err := service.Read(c.Request.Context())
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, read)
	})
	configuration.PUT("", func(c *gin.Context) {
		var request config.DynamicDraftSaveRequest
		if !bindJSON(c, &request, maximumRequestBodyBytes) {
			return
		}
		read, err := service.SaveDraft(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, read)
	})
	configuration.DELETE("/workers/:worker", func(c *gin.Context) {
		var request config.DynamicWorkerDeleteRequest
		if !bindJSON(c, &request, maximumRequestBodyBytes) {
			return
		}
		read, err := service.DeleteWorker(c.Request.Context(), c.Param("worker"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, read)
	})
	configuration.GET("/revisions/active", func(c *gin.Context) {
		revision, err := service.ReadActiveRevision(c.Request.Context())
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, revision)
	})
	configuration.GET("/revisions/:revision", func(c *gin.Context) {
		revision, err := service.ReadRevision(c.Request.Context(), c.Param("revision"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, revision)
	})
}
