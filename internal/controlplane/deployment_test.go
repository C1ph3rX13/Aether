package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/version"
)

func TestNewControlPlaneHostRequiresExplicitOperatorIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "aether.db")

	application, err := NewControlPlaneTestHost(dbPath, nil, ControlPlaneHostOptions{})
	if err == nil {
		application.Shutdown()
		t.Fatal("NewControlPlaneHost() error = nil, want missing Operator error")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("database was touched before identity validation: %v", statErr)
	}
}

func TestNewControlPlaneHostRequiresExplicitDeploymentIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "aether.db")

	application, err := NewControlPlaneHost(dbPath, nil, ControlPlaneHostOptions{OperatorName: "operator"})
	if err == nil {
		application.Shutdown()
		t.Fatal("NewControlPlaneHost() error = nil, want missing Deployment error")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("database was touched before identity validation: %v", statErr)
	}
}

func TestDeploymentMetadataIsMinimalAndReadOnly(t *testing.T) {
	application, err := NewControlPlaneTestHost(
		filepath.Join(t.TempDir(), "aether.db"),
		nil,
		ControlPlaneHostOptions{OperatorName: "  deployment-operator  "},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/deployment", nil)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/deployment status=%d body=%s", response.Code, response.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 || raw["operator"] == nil || raw["build"] == nil || raw["capabilities"] == nil {
		t.Fatalf("deployment metadata exposes unexpected top-level fields: %s", response.Body.String())
	}
	assertJSONFieldCount(t, raw["operator"], 1, "operator")
	assertJSONFieldCount(t, raw["build"], 3, "build")
	assertJSONFieldCount(t, raw["capabilities"], 2, "capabilities")

	var metadata DeploymentMetadata
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Operator.Name != "deployment-operator" {
		t.Fatalf("Operator name=%q, want deployment-operator", metadata.Operator.Name)
	}
	if metadata.Build.Version != version.Version || metadata.Build.Commit != version.Commit || metadata.Build.BuiltAt != version.BuiltAt {
		t.Fatalf("build metadata=%+v", metadata.Build)
	}
	if !metadata.Capabilities.BlackboardHistory || !metadata.Capabilities.ExecutionActivity {
		t.Fatalf("implemented deployment capabilities are disabled: %+v", metadata.Capabilities)
	}
}

func assertJSONFieldCount(t *testing.T, payload json.RawMessage, expected int, field string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode %s metadata: %v", field, err)
	}
	if len(object) != expected {
		t.Fatalf("%s metadata exposes unexpected fields: %s", field, payload)
	}
}
