package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/C1ph3rX13/Aether/internal/controlplane"
)

func TestManagedHostRunStartAppliesFiveMiBBodyLimit(t *testing.T) {
	host, err := controlplane.NewControlPlaneHost(filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "operator", DeploymentID: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	for _, test := range []struct {
		extra  int
		status int
	}{
		{0, http.StatusBadRequest}, // The body is accepted; the empty Start command is invalid.
		{1, http.StatusRequestEntityTooLarge},
	} {
		body := `{}` + strings.Repeat(" ", 5*1024*1024-2+test.extra)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/actions/start", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		host.Handler().ServeHTTP(response, request)
		var failure struct{ Code string }
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.status || failure.Code != "invalid_argument" {
			t.Fatalf("body bytes=%d status=%d body=%s", len(body), response.Code, response.Body.String())
		}
	}
}

func TestManagedHostServesEmbeddedPagesAlongsideVersionedCommands(t *testing.T) {
	host, err := controlplane.NewControlPlaneHost(filepath.Join(t.TempDir(), "aether.db"), fstest.MapFS{
		"index.html":                    {Data: []byte("index")},
		"projects.html":                 {Data: []byte("catalog")},
		"projects/__placeholder__.html": {Data: []byte("workspace")},
		"api/v1/health.html":            {Data: []byte("static health")},
	}, controlplane.ControlPlaneHostOptions{OperatorName: "operator", DeploymentID: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	for _, test := range []struct {
		path, body string
		status     int
	}{
		{"/api/v1/health", `{"status":"ok"}`, http.StatusOK},
		{"/api/v1/missing", "404 page not found", http.StatusNotFound},
		{"/projects", "catalog", http.StatusOK},
		{"/projects/p1", "workspace", http.StatusOK},
	} {
		response := httptest.NewRecorder()
		host.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status || response.Body.String() != test.body {
			t.Errorf("GET %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestControlPlaneHostExposesHandlerCapabilityAndErrorShutdown(t *testing.T) {
	host, err := controlplane.NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}

	handler := host.Handler()
	if handler == nil {
		t.Fatal("Control Plane Host Handler() = nil")
	}
	capability := host.ControlPlane()
	if capability == nil {
		t.Fatal("Control Plane Host ControlPlane() = nil")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/deployment", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Deployment route status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}

	if err := host.Shutdown(); err != nil {
		t.Fatalf("first Control Plane shutdown: %v", err)
	}
	if err := host.Shutdown(); err != nil {
		t.Fatalf("repeated Control Plane shutdown: %v", err)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("health after shutdown status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
