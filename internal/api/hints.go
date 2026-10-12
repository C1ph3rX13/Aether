package api

import (
	"net/http"

	"github.com/C1ph3rX13/Aether/internal/project"
	"github.com/gin-gonic/gin"
)

func withdrawHint(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.WithdrawHintRequest
		if !bindProjectJSON(c, &req) {
			return
		}
		hint, err := svc.WithdrawHint(c.Request.Context(), c.Param("id"), c.Param("hid"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, hint)
	}
}

func listHints(svc ProjectQueries) gin.HandlerFunc {
	return func(c *gin.Context) {
		hints, err := svc.ListHints(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, hints)
	}
}

func createHint(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.CreateHintRequest
		if !bindProjectJSON(c, &req) {
			return
		}

		result, err := svc.CreateHint(c.Request.Context(), c.Param("id"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, result)
	}
}
