package api

import (
	"errors"
	"net/http"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

// writeError maps domain failures to stable HTTP status and error codes.
func writeError(c *gin.Context, err error) {
	var domainErr *rundomain.DomainError
	if errors.As(err, &domainErr) {
		status, code := classifyHTTPError(string(domainErr.Code))
		writeCodedError(c, status, rundomain.ErrorCode(code), domainErr.Detail)
		return
	}
	var projectErr *project.DomainError
	if errors.As(err, &projectErr) {
		status, code := classifyHTTPError(string(projectErr.Code))
		payload := gin.H{"code": code, "detail": projectErr.Detail}
		if projectErr.CurrentRevision != nil {
			payload["current_revision"] = *projectErr.CurrentRevision
		}
		c.JSON(status, payload)
		return
	}
	var configurationErr *config.DomainError
	if errors.As(err, &configurationErr) {
		status, code := classifyHTTPError(string(configurationErr.Code))
		payload := gin.H{"code": code, "detail": configurationErr.Detail}
		if configurationErr.CurrentDraftVersion != nil {
			payload["current_draft_version"] = *configurationErr.CurrentDraftVersion
		}
		if len(configurationErr.ValidationProblems) > 0 {
			payload["validation_problems"] = configurationErr.ValidationProblems
		}
		c.JSON(status, payload)
		return
	}
	if errors.Is(err, project.ErrProjectNotFound) {
		writeCodedError(c, http.StatusNotFound, rundomain.ErrorNotFound, err.Error())
		return
	}
	writeCodedError(c, http.StatusInternalServerError, rundomain.ErrorInternal, err.Error())
}

func writeCodedError(c *gin.Context, status int, code rundomain.ErrorCode, detail string) {
	c.JSON(status, gin.H{"code": code, "detail": detail})
}

// classifyHTTPError owns the transport mapping for every domain error code.
// Unknown codes fail closed as internal errors while retaining their detail.
func classifyHTTPError(code string) (int, string) {
	switch code {
	case string(rundomain.ErrorInvalidArgument), string(config.ErrorValidation):
		return http.StatusBadRequest, code
	case string(rundomain.ErrorNotFound):
		return http.StatusNotFound, code
	case string(rundomain.ErrorFenced), string(rundomain.ErrorForbidden):
		return http.StatusForbidden, code
	case string(rundomain.ErrorNotReady), string(rundomain.ErrorConfigurationRequired),
		string(rundomain.ErrorRetryDeferred), string(config.ErrorProvider):
		return http.StatusServiceUnavailable, code
	case string(rundomain.ErrorConflict),
		string(rundomain.ErrorProjectInactive),
		string(rundomain.ErrorProjectPaused),
		string(rundomain.ErrorProjectCompleted),
		string(rundomain.ErrorBlackboardChanged),
		string(rundomain.ErrorFactRetracted),
		string(rundomain.ErrorIntentCancelled),
		string(rundomain.ErrorOperatorCompleted),
		string(rundomain.ErrorProjectDeleted),
		string(rundomain.ErrorStaleInput),
		string(rundomain.ErrorTargetUnavailable),
		string(rundomain.ErrorWorkClaimed),
		string(rundomain.ErrorInvalidResult),
		string(rundomain.ErrorResultConflict),
		string(rundomain.ErrorCapacityExceeded),
		string(rundomain.ErrorRetryExhausted),
		string(rundomain.ErrorExecutionContractMismatch),
		string(rundomain.ErrorDispatcherRestarted),
		string(rundomain.ErrorDispatcherLeaseHeld):
		return http.StatusConflict, code
	case string(rundomain.ErrorInternal):
		return http.StatusInternalServerError, code
	default:
		return http.StatusInternalServerError, string(rundomain.ErrorInternal)
	}
}
