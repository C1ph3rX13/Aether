package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
	"github.com/spf13/cobra"
)

func TestTrustedRunDispatcherRemainsDormantWithoutLeaseOrDocker(t *testing.T) {
	host, err := controlplane.NewControlPlaneHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "operator", DeploymentID: "deployment"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runTrustedDispatcher(ctx, host, config.BootstrapManifest{}) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertStartupReadiness(t, host.Handler(), http.StatusServiceUnavailable, "configuration_required")
}

func TestProductionCommandsRequireExplicitDeploymentManifest(t *testing.T) {
	for _, newCommand := range []func() *cobra.Command{newServeCommand, newDispatchCommand, newRunCommand} {
		command := newCommand()
		command.SetArgs(nil)
		command.SilenceErrors = true
		command.SilenceUsage = true
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "required flag(s) \"deployment\"") {
			t.Fatalf("%s without --deployment error = %v", command.Name(), err)
		}
	}
}

func TestWaitForActiveConfigurationStaysDormantUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":"not_ready","reason":"configuration_required"}`))
		time.AfterFunc(10*time.Millisecond, cancel)
	}))
	defer server.Close()

	configured, err := waitForActiveConfiguration(ctx, server.URL+"/api/v1", false)
	if err != nil {
		t.Fatal(err)
	}
	if configured || calls != 1 {
		t.Fatalf("configured=%t readiness calls=%d", configured, calls)
	}
}

func TestWaitForActiveConfigurationFailsOneShotWhenConfigurationIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":"not_ready","reason":"configuration_required"}`))
	}))
	defer server.Close()

	_, err := waitForActiveConfiguration(t.Context(), server.URL+"/api/v1", true)
	if err == nil || !strings.Contains(err.Error(), "configuration_required") {
		t.Fatalf("one-shot error = %v", err)
	}
}

func TestWaitForActiveConfigurationAllowsLeaseAcquisitionAfterConfigurationActivation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":"not_ready","reason":"dispatcher_unavailable"}`))
	}))
	defer server.Close()

	configured, err := waitForActiveConfiguration(t.Context(), server.URL+"/api/v1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !configured {
		t.Fatal("active configuration was not admitted to the Dispatcher startup boundary")
	}
}

func TestWaitForActiveConfigurationReturnsCancellationWithoutError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := waitForActiveConfiguration(ctx, "http://127.0.0.1:1/api/v1", false)
	if err != nil {
		t.Fatalf("cancelled readiness error = %v", err)
	}
}

func TestProductionCommandsRejectPositionalStartupInputs(t *testing.T) {
	for _, newCommand := range []func() *cobra.Command{newServeCommand, newDispatchCommand, newRunCommand} {
		command := newCommand()
		if err := command.Args(command, []string{"legacy.yaml"}); err == nil {
			t.Fatalf("%s accepted positional startup input", command.Name())
		}
	}
}

func assertStartupReadiness(t *testing.T, handler http.Handler, wantStatus int, wantReason string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("readiness status=%d body=%s, want %d", response.Code, response.Body.String(), wantStatus)
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Reason != wantReason {
		t.Fatalf("readiness reason=%q, want %q", payload.Reason, wantReason)
	}
}
