package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/C1ph3rX13/Aether/internal/project"
	"github.com/C1ph3rX13/Aether/internal/query"
	"github.com/gin-gonic/gin"
)

type ProjectReader interface {
	ListProjectCatalog(context.Context) ([]query.ProjectCatalogEntry, error)
	ListProjectLog(context.Context, string, query.ProjectLogQuery) (query.LogPage, error)
	GetWorkspaceExecutionEvidence(context.Context, string, *string) (query.WorkspaceExecutionEvidence, error)
}

func listProjectCatalog(service ProjectReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		entries, err := service.ListProjectCatalog(c.Request.Context())
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, entries)
	}
}

func getWorkspaceExecutionEvidence(service ProjectReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		var selectedIntentID *string
		if value := strings.TrimSpace(c.Query("intent_id")); value != "" {
			selectedIntentID = &value
		}
		evidence, err := service.GetWorkspaceExecutionEvidence(
			c.Request.Context(),
			c.Param("id"),
			selectedIntentID,
		)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, evidence)
	}
}

func listProjectLog(service ProjectReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		var logQuery query.ProjectLogQuery
		if value, present := c.GetQuery("before_revision"); present {
			beforeRevision, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				writeError(c, project.InvalidArgument("before_revision must be an integer"))
				return
			}
			logQuery.BeforeRevision = beforeRevision
		}
		if value, present := c.GetQuery("limit"); present {
			limit, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				writeError(c, project.InvalidArgument("limit must be an integer"))
				return
			}
			logQuery.Limit = int(limit)
		}
		page, err := service.ListProjectLog(c.Request.Context(), c.Param("id"), logQuery)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	}
}
