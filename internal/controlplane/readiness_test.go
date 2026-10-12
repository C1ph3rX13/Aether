package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestManagedHostStartsLiveButNotReadyUntilConfigurationIsSaved(t *testing.T) {
	host, err := NewControlPlaneHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		ControlPlaneHostOptions{OperatorName: "operator", DeploymentID: "deployment"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	assertHealthResponse(t, host.Handler(), "/api/v1/health", http.StatusOK, "ok", "")
	assertHealthResponse(t, host.Handler(), "/api/v1/ready", http.StatusServiceUnavailable, "not_ready", "configuration_required")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"title":"blocked","origin":"origin","goal":"goal"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("Project create status=%d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != string(project.ErrorConfigurationRequired) {
		t.Fatalf("Project create error=%q", failure.Code)
	}

	startRequest, err := dormantRunRequest()
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.ControlPlane().StartRun(t.Context(), startRequest)
	var runError *rundomain.DomainError
	if !errors.As(err, &runError) || runError.Code != rundomain.ErrorConfigurationRequired {
		t.Fatalf("Run create error=%v, want configuration_required", err)
	}
}

func dormantRunRequest() (rundomain.StartRequest, error) {
	workKey, err := rundomain.WorkKeyFor("project", rundomain.TaskReason, rundomain.InputSnapshot{Generation: 1, Revision: 1})
	if err != nil {
		return rundomain.StartRequest{}, err
	}
	return rundomain.StartRequest{
		ProjectID: "project", Task: rundomain.TaskReason,
		CandidateGeneration: 1, CandidateRevision: 1, WorkKey: workKey,
		DispatcherID: "dispatcher", DispatcherFencingToken: 1,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config", PromptTemplateHash: strings.Repeat("a", 64),
			Worker:       rundomain.WorkerSnapshot{Name: "reasoner", Adapter: "mock"},
			Image:        rundomain.ImageSnapshot{RequestedRef: "worker:v1"},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 3,
		},
		RetryPolicy: rundomain.DefaultRetryPolicy(),
	}, nil
}
