package api

import (
	"context"
	"net/http"
	"strconv"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type ActivityReader interface {
	ListProjectActivity(context.Context, string, rundomain.ActivityQuery) (rundomain.ActivityPage, error)
	GetProjectActivityRunDetail(context.Context, string, rundomain.RunID) (rundomain.ActivityRunDetail, error)
	GetProjectRetryChain(context.Context, string, rundomain.RunID) ([]rundomain.ActivityRun, error)
}

func RegisterActivity(group *gin.RouterGroup, service ActivityReader) {
	activity := group.Group("/projects/:id/activity")
	activity.GET("", func(c *gin.Context) {
		limit, err := strconv.Atoi(c.DefaultQuery("limit", "0"))
		if err != nil {
			writeCodedError(c, http.StatusBadRequest, rundomain.ErrorInvalidArgument, "limit is invalid")
			return
		}
		page, err := service.ListProjectActivity(c.Request.Context(), c.Param("id"), rundomain.ActivityQuery{
			Cursor: c.Query("cursor"), Limit: limit, View: rundomain.ActivityView(c.Query("view")),
			Task: rundomain.TaskType(c.Query("task")), Search: c.Query("search"),
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	activity.GET("/:run_id", func(c *gin.Context) {
		run, err := service.GetProjectActivityRunDetail(c.Request.Context(), c.Param("id"), rundomain.RunID(c.Param("run_id")))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, run)
	})
	activity.GET("/:run_id/retry-chain", func(c *gin.Context) {
		chain, err := service.GetProjectRetryChain(c.Request.Context(), c.Param("id"), rundomain.RunID(c.Param("run_id")))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, chain)
	})
}
