package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

func TestHealthAndReadinessSeparateServerLivenessFromDispatcherLease(t *testing.T) {
	application, err := NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"),
		nil,
		ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := application.Shutdown(); shutdownErr != nil {
			t.Errorf("Control Plane shutdown: %v", shutdownErr)
		}
	})
	router := application.Handler()

	assertHealthResponse(t, router, "/api/v1/health", http.StatusOK, "ok", "")
	assertHealthResponse(t, router, "/api/v1/ready", http.StatusServiceUnavailable, "not_ready", "dispatcher_unavailable")

	runner, err := scheduler.NewDispatcherLeaseRunner(application.ControlPlane(), scheduler.DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-health-test", LeaseTTL: 5 * time.Second,
		RenewInterval: 2 * time.Second, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runner.RunFailClosed(t.Context(), func(
		ctx context.Context,
		_ rundomain.DispatcherLease,
		failClosed func(error),
	) error {
		assertHealthResponse(t, router, "/api/v1/ready", http.StatusOK, "ready", "")
		fatal := &rundomain.DomainError{
			Code:   rundomain.ErrorExecutionContractMismatch,
			Detail: "Execution Binding evidence does not match",
		}
		failClosed(fatal)
		<-ctx.Done()
		return fatal
	})
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("Dispatcher fatal readiness error: %v", err)
	}
	assertHealthResponse(t, router, "/api/v1/ready", http.StatusServiceUnavailable, "not_ready", "dispatcher_unavailable")
}

func TestHealthReportsClosedDatabaseAsUnavailable(t *testing.T) {
	application, err := NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"),
		nil,
		ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	router := application.Handler()
	application.Shutdown()

	assertHealthResponse(t, router, "/api/v1/health", http.StatusServiceUnavailable, "unhealthy", "database_unavailable")
	assertHealthResponse(t, router, "/api/v1/ready", http.StatusServiceUnavailable, "not_ready", "database_unavailable")
}

func assertHealthResponse(
	t *testing.T,
	handler http.Handler,
	path string,
	wantStatus int,
	wantState string,
	wantReason string,
) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("GET %s status=%d body=%s, want %d", path, response.Code, response.Body.String(), wantStatus)
	}
	var payload struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != wantState || payload.Reason != wantReason {
		t.Fatalf("GET %s payload=%+v, want status=%q reason=%q", path, payload, wantState, wantReason)
	}
}
