package api

import (
	"net/http"

	"github.com/C1ph3rX13/Aether/internal/project"
	"github.com/gin-gonic/gin"
)

func retractFact(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.RetractFactRequest
		if !bindProjectJSON(c, &req) {
			return
		}
		fact, err := svc.RetractFact(c.Request.Context(), c.Param("id"), c.Param("fid"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, fact)
	}
}

func supersedeFact(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.SupersedeFactRequest
		if !bindProjectJSON(c, &req) {
			return
		}
		result, err := svc.SupersedeFact(c.Request.Context(), c.Param("id"), c.Param("fid"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, result)
	}
}

func cancelIntent(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.CancelIntentRequest
		if !bindProjectJSON(c, &req) {
			return
		}
		result, err := svc.CancelIntent(c.Request.Context(), c.Param("id"), c.Param("iid"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
