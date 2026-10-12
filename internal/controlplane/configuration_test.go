package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/config"
)

func TestManagedHostConfigurationDraftReadAndSaveRoute(t *testing.T) {
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/responses" || request.Header.Get("Authorization") != "Bearer http-secret" {
			t.Errorf("Provider request = %s %s Authorization=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer provider.Close()

	host, err := NewControlPlaneHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{
		OperatorName: "operator", DeploymentID: "deployment",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/configuration", nil)
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("configuration GET status=%d body=%s", response.Code, response.Body.String())
	}
	var read config.ConfigurationRead
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.DraftVersion != 1 || len(read.ProviderPresets) != 3 || len(read.PromptGroups) != 2 || read.PromptGroups[0] != "default" || read.PromptGroups[1] != "mock" || read.Draft.Container.Image != "ghcr.io/c1ph3rx13/aether-worker:latest" {
		t.Fatalf("configuration read = %+v", read)
	}

	draft := config.RecommendedDynamicDraft()
	draft.Runtime.MaxWorkers = 9
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "codex-http", AgentType: "codex", ProviderName: "Test Provider",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 2, Priority: 1,
		AgentEnv:         map[string]string{"CODEX_BASE_URL": provider.URL, "CODEX_MODEL": "test-model"},
		TimeoutOverrides: map[string]int{},
	}}
	body, err := json.Marshal(config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1, Draft: draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-http": {"OPENAI_API_KEY": {Value: "http-secret"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPut, "/api/v1/configuration", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("configuration PUT status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/configuration/revisions/active", nil)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("activation status=%d body=%s", response.Code, response.Body.String())
	}
	var revision config.ConfigurationRevision
	if err := json.Unmarshal(response.Body.Bytes(), &revision); err != nil {
		t.Fatal(err)
	}
	if revision.ID == "" || providerCalls.Load() != 1 || strings.Contains(response.Body.String(), "http-secret") {
		t.Fatalf("activation response=%s Provider calls=%d", response.Body.String(), providerCalls.Load())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/configuration/revisions/active", nil)
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), revision.ID) || strings.Contains(response.Body.String(), "http-secret") {
		t.Fatalf("active Revision GET status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/configuration/revisions/"+revision.ID, nil)
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), revision.ID) {
		t.Fatalf("historical Revision GET status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/configuration/workers/codex-http", strings.NewReader(`{"expected_draft_version":2}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "codex-http") {
		t.Fatalf("configuration Worker DELETE status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/configuration/activate", strings.NewReader(`{"expected_draft_version":1}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("obsolete activation status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/configuration/revisions/not-a-revision", nil)
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid Revision GET status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "/api/v1/configuration", strings.NewReader(`{"expected_draft_version":2,"draft":{},"secret_versions":{"x":"arbitrary"}}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown configuration field status=%d body=%s", response.Code, response.Body.String())
	}
}
