package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type HistoryQueries interface {
	ListChanges(context.Context, string, int64) (project.ChangeReplay, error)
	GetBlackboardRevision(context.Context, string, int64) (project.ProjectDetail, error)
}

func listProjectChanges(svc HistoryQueries) gin.HandlerFunc {
	return func(c *gin.Context) {
		afterRevision, err := strconv.ParseInt(c.DefaultQuery("after_revision", "0"), 10, 64)
		if err != nil {
			writeCodedError(c, http.StatusBadRequest, rundomain.ErrorInvalidArgument, "after_revision must be an integer")
			return
		}
		replay, err := svc.ListChanges(c.Request.Context(), c.Param("id"), afterRevision)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, replay)
	}
}

func getProjectHistoryBlackboard(service HistoryQueries) gin.HandlerFunc {
	return func(c *gin.Context) {
		revision, err := strconv.ParseInt(c.Param("revision"), 10, 64)
		if err != nil {
			writeCodedError(c, 400, rundomain.ErrorInvalidArgument, "revision is invalid")
			return
		}
		detail, err := service.GetBlackboardRevision(c.Request.Context(), c.Param("id"), revision)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	}
}
