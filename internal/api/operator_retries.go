package api

import (
	"context"
	"net/http"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type OperatorRetryCommands interface {
	RetryExhaustedWork(context.Context, string, rundomain.RetryExhaustedRequest) (rundomain.RetryExhaustedReceipt, error)
}

func RegisterOperatorRetries(group *gin.RouterGroup, service OperatorRetryCommands) {
	group.POST("/projects/:id/actions/retry-exhausted", func(c *gin.Context) {
		var request rundomain.RetryExhaustedRequest
		if !bindProjectJSON(c, &request) {
			return
		}
		receipt, err := service.RetryExhaustedWork(c.Request.Context(), c.Param("id"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, receipt)
	})
}
