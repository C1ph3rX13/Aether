package controlplane_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestHTTPRunControlPreservesLeaseAndFencingErrors(t *testing.T) {
	control, cleanup := newHTTPRunControl(t)
	defer cleanup()

	first, err := control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-http-a", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-http-b", TTL: 30 * time.Second,
	})
	assertRunErrorCode(t, err, rundomain.ErrorDispatcherLeaseHeld)

	renewed, err := control.RenewDispatcherLease(t.Context(), first, 30*time.Second)
	if err != nil || renewed.FencingToken != first.FencingToken {
		t.Fatalf("renewed lease=%+v error=%v", renewed, err)
	}
	err = control.RecordRecoveryEvent(t.Context(), rundomain.RecoveryRecord{
		RunID: "run-missing", DispatcherID: renewed.InstanceID,
		DispatcherFencingToken: renewed.FencingToken + 1,
		Status:                 rundomain.RecoveryDisposed, Reason: "test fencing",
	})
	assertRunErrorCode(t, err, rundomain.ErrorFenced)
	if err := control.ReleaseDispatcherLease(t.Context(), renewed); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRunControlLoadsImmutableRevisionAndAuthorizesRevisionSecrets(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer provider.Close()

	host, err := controlplane.NewControlPlaneHost(filepath.Join(t.TempDir(), "aether.db"), nil, controlplane.ControlPlaneHostOptions{
		OperatorName: "operator", DeploymentID: "deployment",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	draft := config.RecommendedDynamicDraft()
	draft.Runtime.WorkerHealthcheck = "disabled"
	draft.Workers = []config.DynamicWorkerDraft{acceptanceWorker("remote-codex", provider.URL, 1)}
	_ = saveDraft(t, host.Handler(), 1, draft, map[string]map[string]config.WorkerSecretChange{
		"remote-codex": {"OPENAI_API_KEY": {Value: "remote-secret"}},
	})
	var revision config.ConfigurationRevision
	requestJSON(t, host.Handler(), http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, &revision)

	server := httptest.NewServer(host.Handler())
	defer server.Close()
	control, err := controlplane.NewHTTPRunControl(server.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	loaded, err := control.ReadActiveExecutionConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigRevision != revision.ID || len(loaded.Workers) != 1 || loaded.Workers[0].SecretVersionIDs["OPENAI_API_KEY"] == "" {
		t.Fatalf("remote execution config = %+v", loaded)
	}
	if loaded.Workers[0].Env["OPENAI_API_KEY"] == "remote-secret" {
		t.Fatal("remote execution config exposed Secret plaintext")
	}

	lease, err := control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "remote-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := rundomain.SecretReference{
		EnvironmentVariable: "OPENAI_API_KEY", Provider: "configuration",
		Key: revision.SecretReferences[0].VersionID,
	}
	resolved, err := control.ResolveRevisionSecrets(t.Context(), revision.ID, "remote-codex", lease)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Environment()[reference.EnvironmentVariable] != "remote-secret" {
		t.Fatal("remote revision Secret resolution returned the wrong value")
	}
	resolved.Close()

	lease.FencingToken++
	if _, err := control.ResolveRevisionSecrets(t.Context(), revision.ID, "remote-codex", lease); !isRunError(err, rundomain.ErrorFenced) {
		t.Fatalf("stale revision Secret authorization error=%v, want %s", err, rundomain.ErrorFenced)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/configuration/revisions/"+revision.ID+"/workers/remote-codex/actions/resolve-secrets",
		strings.NewReader(`{"instance_id":"remote-dispatcher","fencing_token":1,"references":[{"environment_variable":"OPENAI_API_KEY","provider":"environment","key":"arbitrary"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("caller-supplied Secret references status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManagedHTTPRunAdmissionSecretsAndCommit(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer provider.Close()

	host, err := controlplane.NewControlPlaneHost(filepath.Join(t.TempDir(), "aether.db"), nil, controlplane.ControlPlaneHostOptions{
		OperatorName: "operator", DeploymentID: "deployment",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	var failure struct {
		Code string `json:"code"`
	}
	requestJSON(t, host.Handler(), http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "blocked", "origin": "origin", "goal": "goal",
	}, http.StatusServiceUnavailable, &failure)
	if failure.Code != string(rundomain.ErrorConfigurationRequired) {
		t.Fatalf("fresh deployment Project error=%q", failure.Code)
	}

	draft := config.RecommendedDynamicDraft()
	draft.Runtime.WorkerHealthcheck = "disabled"
	draft.Workers = []config.DynamicWorkerDraft{acceptanceWorker("remote-codex", provider.URL, 1)}
	_ = saveDraft(t, host.Handler(), 1, draft, map[string]map[string]config.WorkerSecretChange{
		"remote-codex": {"OPENAI_API_KEY": {Value: "run-secret"}},
	})
	var revision config.ConfigurationRevision
	requestJSON(t, host.Handler(), http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, &revision)
	project := createProject(t, host.Handler(), "HTTP Run Secret", false)

	server := httptest.NewServer(host.Handler())
	defer server.Close()
	control, err := controlplane.NewHTTPRunControl(server.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	lease, err := control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "remote-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := control.ReadExecutionConfig(t.Context(), revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := worker.NewCatalogFromConfig(worker.CatalogConfiguration{
		Revision: bound.ConfigRevision, ImageReference: bound.Container.Image,
		PromptGroup: bound.Runtime.PromptGroup, Workers: bound.Workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := rundomain.InputSnapshot{Generation: project.Project.Generation, Revision: project.Project.Revision}
	workKey, err := rundomain.WorkKeyFor(project.Project.ID, rundomain.TaskReason, input)
	if err != nil {
		t.Fatal(err)
	}
	workerSnapshot, err := catalog.WorkerSnapshotForTask("remote-codex", rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	promptHash, err := catalog.PromptTemplateHash(rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	policy := bound.ExecutionPolicies[workerSnapshot.Name][rundomain.TaskReason]
	admitted, err := control.StartRun(t.Context(), rundomain.StartRequest{
		ProjectID: project.Project.ID, Task: rundomain.TaskReason,
		CandidateGeneration: project.Project.Generation, CandidateRevision: project.Project.Revision,
		WorkKey: workKey, DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: bound.ConfigRevision, PromptTemplateHash: promptHash,
			Worker: workerSnapshot, Image: rundomain.ImageSnapshot{RequestedRef: bound.Container.Image},
			Timeouts:     policy.Timeouts,
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: policy.MaxProposedIntents,
		}, RetryPolicy: policy.RetryPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := control.ResolveRunSecrets(t.Context(), admitted.StartedRun, admitted.Run.Spec.Worker.SecretReferences)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Environment()["OPENAI_API_KEY"] != "run-secret" {
		t.Fatal("HTTP Run Secret resolution did not use the persisted Execution Spec")
	}
	resolved.Close()
	receipt, err := control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: admitted.Run.ID, FencingToken: admitted.Run.FencingToken,
		Candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","from":["origin"],"description":"Goal reached"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusCommitted || receipt.OutcomeKind != rundomain.OutcomeCompleteProject || receipt.ProjectRevision != project.Project.Revision+1 {
		t.Fatalf("HTTP completion receipt=%+v", receipt)
	}
	var completed acceptanceProjectResponse
	requestJSON(t, host.Handler(), http.MethodGet, "/api/v1/projects/"+project.Project.ID, nil, http.StatusOK, &completed)
	if completed.Project.ConfigRevision != revision.ID || completed.Project.Revision != receipt.ProjectRevision {
		t.Fatalf("committed Project binding/revision=%+v", completed.Project)
	}
}

func TestHTTPRunControlBoundsErrorResponses(t *testing.T) {
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(strings.Repeat("x", 65*1024)))
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	_, err = control.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher", TTL: 30 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversized error response: %v", err)
	}
}

func TestHTTPRunControlRejectsIncompleteSchedulingSnapshot(t *testing.T) {
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	_, err = control.SchedulingSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: "dispatcher", DispatcherFencingToken: 1,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "snapshot is invalid") {
		t.Fatalf("incomplete scheduling response: %v", err)
	}
}

func TestHTTPRunControlReturnsAdmittedRunWithTransportContractMismatch(t *testing.T) {
	request, admitted := httpAdmittedRunFixture(t)
	admitted.Run.Spec.PromptTemplateHash = strings.Repeat("f", 64)
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(admitted); err != nil {
			t.Fatal(err)
		}
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	returned, err := control.StartRun(t.Context(), request)
	assertRunErrorCode(t, err, rundomain.ErrorExecutionContractMismatch)
	if returned == nil || returned.Run.ID != admitted.Run.ID || returned.Run.FencingToken != admitted.Run.FencingToken {
		t.Fatalf("transport mismatch lost admitted Run identity: %+v", returned)
	}
	if strings.Contains(err.Error(), strings.Repeat("f", 64)) {
		t.Fatalf("transport mismatch exposed Prompt identity: %v", err)
	}
}

func TestHTTPRunControlRejectsFinishReceiptForDifferentRun(t *testing.T) {
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(rundomain.FinishReceipt{
			RunID: "different-run", Status: rundomain.StatusFailed,
			Code: rundomain.ErrorExecutionContractMismatch,
		})
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	_, err = control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: "offending-run", FencingToken: 1,
		Termination: &rundomain.RunTermination{
			Status: rundomain.StatusFailed, Code: rundomain.ErrorExecutionContractMismatch,
		},
	})
	assertRunErrorCode(t, err, rundomain.ErrorExecutionContractMismatch)
	if strings.Contains(err.Error(), "different-run") || strings.Contains(err.Error(), "offending-run") {
		t.Fatalf("Finish receipt mismatch exposed Run identity: %v", err)
	}
}

func TestHTTPRunControlRejectsFinishReceiptWithDifferentCaptureFailureReason(t *testing.T) {
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(rundomain.FinishReceipt{
			RunID: "run-capture", Status: rundomain.StatusFailed,
			Code: rundomain.ExecutionCaptureFailed,
		})
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	_, err = control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: "run-capture", FencingToken: 1,
		Termination: &rundomain.RunTermination{
			Status: rundomain.StatusFailed, Code: rundomain.ExecutionCaptureFailed,
			CaptureFailureReason: rundomain.CaptureFailureInvalidCandidate,
		},
	})
	assertRunErrorCode(t, err, rundomain.ErrorExecutionContractMismatch)
}

func TestHTTPRunControlRejectsFinishReceiptWithDifferentCaptureDiagnostic(t *testing.T) {
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(rundomain.FinishReceipt{
			RunID: "run-repaired", Status: rundomain.StatusCommitted,
			CaptureDiagnostic: rundomain.CaptureDiagnosticResultFileInvalid,
		})
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	_, err = control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: "run-repaired", FencingToken: 1,
		Candidate: []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`),
	})
	assertRunErrorCode(t, err, rundomain.ErrorExecutionContractMismatch)
}

func TestHTTPRunControlRejectsFinishReceiptWithDifferentResultEvidenceSource(t *testing.T) {
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(rundomain.FinishReceipt{
			RunID: "run-source", Status: rundomain.StatusCommitted,
			ResultEvidenceSource: "",
		})
	}))
	defer serverHTTP.Close()
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	_, err = control.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: "run-source", FencingToken: 1,
		Candidate:            []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`),
		ResultEvidenceSource: rundomain.ResultEvidenceSourceFile,
	})
	assertRunErrorCode(t, err, rundomain.ErrorExecutionContractMismatch)
	if strings.Contains(err.Error(), "run-source") {
		t.Fatalf("Finish receipt source mismatch exposed Run identity: %v", err)
	}
}

func httpAdmittedRunFixture(t *testing.T) (rundomain.StartRequest, rundomain.AdmittedRun) {
	t.Helper()
	input := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 1,
		Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "origin", Status: "active"},
			{ID: "goal", Description: "goal", Status: "active"},
		},
	}
	identity, err := input.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	inputSnapshot := rundomain.InputSnapshot{Generation: 1, Revision: 1, Identity: identity}
	workKey, err := rundomain.WorkKeyFor("project-http", rundomain.TaskReason, inputSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := rundomain.ClaimScopeFor("project-http", rundomain.TaskReason, inputSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	promptHash := strings.Repeat("a", 64)
	request := rundomain.StartRequest{
		ProjectID: "project-http", Task: rundomain.TaskReason,
		CandidateGeneration: 1, CandidateRevision: 1, WorkKey: workKey,
		DispatcherID: "dispatcher-http", DispatcherFencingToken: 1,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", PromptTemplateHash: promptHash,
			Worker:       rundomain.WorkerSnapshot{Name: "reasoner", Adapter: "mock"},
			Image:        rundomain.ImageSnapshot{RequestedRef: "worker:v1"},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 3,
		},
		RetryPolicy: rundomain.DefaultRetryPolicy(),
	}
	now := time.Now().UTC()
	admitted := rundomain.AdmittedRun{
		StartedRun: rundomain.StartedRun{
			Run: rundomain.ExecutionRun{
				ID: "run-http", ProjectID: request.ProjectID, WorkKey: workKey, ClaimScope: scope,
				Attempt: 1, Status: rundomain.StatusActive, FencingToken: 1,
				DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken,
				Task: request.Task, StartedAt: now,
				Spec: rundomain.ExecutionSpecSnapshot{
					Schema: rundomain.ExecutionSpecSchemaV2, ConfigRevision: request.Spec.ConfigRevision,
					PromptTemplateHash: request.Spec.PromptTemplateHash, Input: inputSnapshot,
					Worker: request.Spec.Worker, Image: request.Spec.Image, Timeouts: request.Spec.Timeouts,
					ResultSchema: request.Spec.ResultSchema, MaxProposedIntents: request.Spec.MaxProposedIntents,
				},
			},
			Claim: rundomain.Claim{
				WorkKey: workKey, Scope: scope, RunID: "run-http", ProjectID: request.ProjectID,
				Task: request.Task, FencingToken: 1, DispatcherID: request.DispatcherID,
				ClaimedAt: now, LeaseExpiresAt: now.Add(time.Minute),
			},
		},
		BlackboardInput: &input,
	}
	return request, admitted
}

func newHTTPRunControl(t *testing.T) (*controlplane.HTTPRunControl, func()) {
	t.Helper()
	application, err := controlplane.NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, controlplane.ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	router := application.Handler()
	serverHTTP := httptest.NewServer(router)
	control, err := controlplane.NewHTTPRunControl(serverHTTP.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	return control, func() {
		control.Close()
		serverHTTP.Close()
		application.Shutdown()
	}
}

func assertRunErrorCode(t *testing.T, err error, code rundomain.ErrorCode) {
	t.Helper()
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != code {
		t.Fatalf("domain error=%v, want code %s", err, code)
	}
}
