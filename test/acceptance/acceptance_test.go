package acceptance_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
)

type projectMetaWire struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Generation int    `json:"generation"`
	Revision   int64  `json:"revision"`
}

type projectDetailWire struct {
	Project projectMetaWire   `json:"project"`
	Facts   []json.RawMessage `json:"facts"`
	Intents []json.RawMessage `json:"intents"`
	Hints   []json.RawMessage `json:"hints"`
}

type hintWire struct {
	Creator string `json:"creator"`
}

type hintReceiptWire struct {
	Hint            hintWire `json:"hint"`
	ProjectRevision int64    `json:"project_revision"`
}

type completeReceiptWire struct {
	Project    projectMetaWire `json:"project"`
	Completion struct {
		Actor string `json:"actor"`
	} `json:"completion"`
}

type reopenReceiptWire struct {
	Project  projectMetaWire `json:"project"`
	Hint     hintWire        `json:"hint"`
	Previous struct {
		Description string `json:"description"`
	} `json:"previous_completion"`
}

func TestPublicProjectLifecycleContract(t *testing.T) {
	host := newAcceptanceHost(t)
	router := host.Handler()

	requestJSON[map[string]any](t, router, http.MethodGet, "/api/v1/settings", nil, http.StatusNotFound, nil)

	var created projectDetailWire
	requestJSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "Baseline Project", "origin": "external entry", "goal": "reach goal", "bootstrap_enabled": false,
	}, http.StatusCreated, &created)
	if created.Project.ID != "proj_001" || created.Project.Revision != 1 {
		t.Fatalf("created Project: %+v", created.Project)
	}

	var hint hintReceiptWire
	requestJSON(t, router, http.MethodPost, "/api/v1/projects/proj_001/hints", map[string]any{
		"content": "check port", "expected_revision": 1,
	}, http.StatusCreated, &hint)
	if hint.Hint.Creator != "test-operator" || hint.ProjectRevision != 2 {
		t.Fatalf("Hint receipt: %+v", hint)
	}

	var completed completeReceiptWire
	requestJSON(t, router, http.MethodPost, "/api/v1/projects/proj_001/actions/complete", map[string]any{
		"from": []string{"origin"}, "description": "goal reached", "expected_revision": 2,
	}, http.StatusOK, &completed)
	if completed.Project.Status != "completed" || completed.Project.Revision != 3 || completed.Completion.Actor != "test-operator" {
		t.Fatalf("completion receipt: %+v", completed)
	}

	var reopened reopenReceiptWire
	requestJSON(t, router, http.MethodPost, "/api/v1/projects/proj_001/actions/reopen", map[string]any{
		"feedback": "new evidence", "expected_revision": 3,
	}, http.StatusOK, &reopened)
	if reopened.Project.Status != "active" || reopened.Project.Generation != 2 || reopened.Project.Revision != 4 {
		t.Fatalf("reopen receipt: %+v", reopened)
	}
	if reopened.Hint.Creator != "test-operator" || reopened.Previous.Description != "goal reached" {
		t.Fatalf("reopen evidence: %+v", reopened)
	}

	var final projectDetailWire
	requestJSON(t, router, http.MethodGet, "/api/v1/projects/proj_001", nil, http.StatusOK, &final)
	if len(final.Facts) != 2 || len(final.Intents) != 1 || len(final.Hints) != 2 {
		t.Fatalf("final Blackboard: facts=%d intents=%d hints=%d", len(final.Facts), len(final.Intents), len(final.Hints))
	}

	for _, path := range []string{
		"/api/v1/projects/proj_001/intents",
		"/api/v1/projects/proj_001/intents/i001/actions/renew",
		"/api/v1/projects/proj_001/intents/i001/actions/release",
		"/api/v1/projects/proj_001/intents/i001/actions/conclude",
		"/api/v1/projects/proj_001/reason/actions/claim",
		"/api/v1/projects/proj_001/reason/actions/renew",
		"/api/v1/projects/proj_001/reason/actions/release",
	} {
		requestJSON[map[string]any](t, router, http.MethodPost, path, map[string]any{}, http.StatusNotFound, nil)
	}
}

func TestSeparatedDeploymentAPI(t *testing.T) {
	host := newAcceptanceHost(t)
	router := host.Handler()

	requestJSON[map[string]any](t, router, http.MethodGet, "/api/v1/health", nil, http.StatusOK, nil)
	requestJSON[map[string]any](t, router, http.MethodGet, "/api/v1/settings", nil, http.StatusNotFound, nil)
	requestJSON[map[string]any](t, router, http.MethodGet, "/api/dispatcher/running-tasks", nil, http.StatusNotFound, nil)
	var projects []projectDetailWire
	requestJSON(t, router, http.MethodGet, "/api/v1/projects", nil, http.StatusOK, &projects)
	if len(projects) != 0 {
		t.Fatalf("separated API returned %d Projects", len(projects))
	}
}

func newAcceptanceHost(t *testing.T) *controlplane.ControlPlaneHost {
	t.Helper()
	host, err := controlplane.NewControlPlaneHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "test-operator", DeploymentID: "acceptance-deployment"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(); err != nil {
			t.Errorf("Control Plane shutdown: %v", err)
		}
	})
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(provider.Close)
	var read config.ConfigurationRead
	requestJSON(t, host.Handler(), http.MethodGet, "/api/v1/configuration", nil, http.StatusOK, &read)
	draft := read.RecommendedDraft
	draft.Runtime.WorkerHealthcheck = "disabled"
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "acceptance-worker", AgentType: "codex", ProviderName: "fixture",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 1, Priority: 1,
		AgentEnv: map[string]string{"CODEX_BASE_URL": provider.URL, "CODEX_MODEL": "fixture"},
	}}
	requestJSON(t, host.Handler(), http.MethodPut, "/api/v1/configuration", config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: read.DraftVersion, Draft: draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"acceptance-worker": {"OPENAI_API_KEY": {Value: "acceptance-secret"}},
		},
	}, http.StatusOK, &read)
	requestJSON[config.ConfigurationRevision](t, host.Handler(), http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, nil)
	return host
}

func requestJSON[T any](t *testing.T, handler http.Handler, method, path string, body any, wantStatus int, target *T) {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &payload)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s: got %d, want %d; body: %s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
			t.Fatalf("decode %s %s: %v; body: %s", method, path, err, recorder.Body.String())
		}
	}
}
