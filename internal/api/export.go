package api

import (
	"context"
	"net/http"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type ProjectExporter interface {
	Export(context.Context, string) (project.ProjectDetail, error)
}

func RegisterExport(g *gin.RouterGroup, svc ProjectExporter) {
	g.GET("/projects/:id/export", exportProject(svc))
}

func exportProject(svc ProjectExporter) gin.HandlerFunc {
	return func(c *gin.Context) {
		format := c.DefaultQuery("format", "yaml")
		if format != "yaml" && format != "json" {
			writeCodedError(c, http.StatusBadRequest, rundomain.ErrorInvalidArgument, "Supported formats: yaml, json")
			return
		}
		detail, err := svc.Export(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		if format == "json" {
			c.JSON(http.StatusOK, detail)
			return
		}
		data, err := yaml.Marshal(detail)
		if err != nil {
			writeCodedError(c, http.StatusInternalServerError, rundomain.ErrorInternal, err.Error())
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", data)
	}
}
