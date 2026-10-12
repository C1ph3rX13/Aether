package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

func TestRoutesMapWrappedDomainErrorsAndConflictEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	revision, draftVersion := int64(7), int64(3)
	for _, test := range []struct {
		name, path, body string
		err              error
		status           int
		payload          string
	}{
		{"Project conflict", "/projects", `{}`, &project.DomainError{Code: project.ErrorConflict, Detail: "changed", CurrentRevision: &revision}, 409, `{"code":"conflict","detail":"changed","current_revision":7}`},
		{"Project not found", "/projects", `{}`, project.ErrProjectNotFound, 404, `{"code":"not_found","detail":"wrapped: Project not found"}`},
		{"Project configuration required", "/projects", `{}`, &project.DomainError{Code: project.ErrorConfigurationRequired, Detail: "configure"}, 503, `{"code":"configuration_required","detail":"configure"}`},
		{"Project unknown", "/projects", `{}`, &project.DomainError{Code: "future_project_code", Detail: "future project detail"}, 500, `{"code":"internal_error","detail":"future project detail"}`},
		{"Run fenced", "/dispatcher-leases/actions/acquire", `{"instance_id":"d1","ttl_seconds":60}`, &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "fenced"}, 403, `{"code":"fenced","detail":"fenced"}`},
		{"Run retry deferred", "/dispatcher-leases/actions/acquire", `{"instance_id":"d1","ttl_seconds":60}`, &rundomain.DomainError{Code: rundomain.ErrorRetryDeferred, Detail: "later"}, 503, `{"code":"retry_deferred","detail":"later"}`},
		{"Run unknown", "/dispatcher-leases/actions/acquire", `{"instance_id":"d1","ttl_seconds":60}`, &rundomain.DomainError{Code: "future_run_code", Detail: "future run detail"}, 500, `{"code":"internal_error","detail":"future run detail"}`},
		{"Configuration conflict", "/configuration", `{}`, &config.DomainError{Code: config.ErrorConflict, Detail: "changed", CurrentDraftVersion: &draftVersion}, 409, `{"code":"conflict","detail":"changed","current_draft_version":3}`},
		{"Configuration validation", "/configuration", `{}`, &config.DomainError{Code: config.ErrorValidation, Detail: "invalid", ValidationProblems: []config.ValidationProblem{{Path: "workers", Message: "required"}}}, 400, `{"code":"validation_failed","detail":"invalid","validation_problems":[{"path":"workers","message":"required"}]}`},
		{"Configuration provider", "/configuration", `{}`, &config.DomainError{Code: config.ErrorProvider, Detail: "unavailable"}, 503, `{"code":"provider_check_failed","detail":"unavailable"}`},
		{"Configuration unknown", "/configuration", `{}`, &config.DomainError{Code: "future_configuration_code", Detail: "future configuration detail"}, 500, `{"code":"internal_error","detail":"future configuration detail"}`},
		{"Unexpected error", "/configuration", `{}`, fmt.Errorf("database failed"), 500, `{"code":"internal_error","detail":"wrapped: database failed"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("wrapped: %w", test.err)
			router := gin.New()
			group := router.Group("/api/v1")
			RegisterProjects(group, nil, nil, &requestCommands{err: wrapped}, nil)
			RegisterRuns(group, &requestRuns{err: wrapped})
			RegisterConfiguration(group, &requestConfiguration{err: wrapped})
			method := http.MethodPost
			if test.path == "/configuration" {
				method = http.MethodPut
			}
			response := serveJSON(router, method, "/api/v1"+test.path, test.body)
			var got, want map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(test.payload), &want); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || !reflect.DeepEqual(got, want) {
				t.Fatalf("status=%d body=%s want status=%d body=%s", response.Code, response.Body.String(), test.status, test.payload)
			}
		})
	}
}

func TestClassifyHTTPErrorCoversEveryDefinedCode(t *testing.T) {
	tests := []struct {
		code   string
		status int
	}{
		{string(rundomain.ErrorInvalidArgument), http.StatusBadRequest},
		{string(rundomain.ErrorNotFound), http.StatusNotFound},
		{string(rundomain.ErrorProjectInactive), http.StatusConflict},
		{string(rundomain.ErrorProjectPaused), http.StatusConflict},
		{string(rundomain.ErrorProjectCompleted), http.StatusConflict},
		{string(rundomain.ErrorBlackboardChanged), http.StatusConflict},
		{string(rundomain.ErrorFactRetracted), http.StatusConflict},
		{string(rundomain.ErrorIntentCancelled), http.StatusConflict},
		{string(rundomain.ErrorOperatorCompleted), http.StatusConflict},
		{string(rundomain.ErrorProjectDeleted), http.StatusConflict},
		{string(rundomain.ErrorStaleInput), http.StatusConflict},
		{string(rundomain.ErrorTargetUnavailable), http.StatusConflict},
		{string(rundomain.ErrorWorkClaimed), http.StatusConflict},
		{string(rundomain.ErrorInvalidResult), http.StatusConflict},
		{string(rundomain.ErrorFenced), http.StatusForbidden},
		{string(rundomain.ErrorResultConflict), http.StatusConflict},
		{string(rundomain.ErrorNotReady), http.StatusServiceUnavailable},
		{string(rundomain.ErrorConfigurationRequired), http.StatusServiceUnavailable},
		{string(rundomain.ErrorCapacityExceeded), http.StatusConflict},
		{string(rundomain.ErrorRetryExhausted), http.StatusConflict},
		{string(rundomain.ErrorRetryDeferred), http.StatusServiceUnavailable},
		{string(rundomain.ErrorExecutionContractMismatch), http.StatusConflict},
		{string(rundomain.ErrorDispatcherRestarted), http.StatusConflict},
		{string(rundomain.ErrorDispatcherLeaseHeld), http.StatusConflict},
		{string(rundomain.ErrorConflict), http.StatusConflict},
		{string(rundomain.ErrorForbidden), http.StatusForbidden},
		{string(rundomain.ErrorInternal), http.StatusInternalServerError},
		{string(project.ErrorInvalidArgument), http.StatusBadRequest},
		{string(project.ErrorNotFound), http.StatusNotFound},
		{string(project.ErrorForbidden), http.StatusForbidden},
		{string(project.ErrorConflict), http.StatusConflict},
		{string(project.ErrorInternal), http.StatusInternalServerError},
		{string(project.ErrorConfigurationRequired), http.StatusServiceUnavailable},
		{string(config.ErrorInvalidArgument), http.StatusBadRequest},
		{string(config.ErrorConflict), http.StatusConflict},
		{string(config.ErrorValidation), http.StatusBadRequest},
		{string(config.ErrorProvider), http.StatusServiceUnavailable},
		{string(config.ErrorNotFound), http.StatusNotFound},
		{string(config.ErrorInternal), http.StatusInternalServerError},
	}
	for _, test := range tests {
		status, code := classifyHTTPError(test.code)
		if status != test.status || code != test.code {
			t.Errorf("classifyHTTPError(%q) = (%d, %q), want (%d, %q)", test.code, status, code, test.status, test.code)
		}
	}
	for _, code := range []string{"future_run_code", "future_project_code", "future_configuration_code"} {
		status, canonical := classifyHTTPError(code)
		if status != http.StatusInternalServerError || canonical != string(rundomain.ErrorInternal) {
			t.Errorf("classifyHTTPError(%q) = (%d, %q), want (500, %q)", code, status, canonical, rundomain.ErrorInternal)
		}
	}
}
