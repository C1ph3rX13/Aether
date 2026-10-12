package controlplane_test

import (
	"bytes"
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
	"github.com/C1ph3rX13/Aether/internal/database"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestManagedProjectRunsThroughBoundRevisionAdmissionAndCommit(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer provider.Close()

	databasePath := filepath.Join(t.TempDir(), "aether.db")
	host, err := controlplane.NewControlPlaneHost(
		databasePath, nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "operator", DeploymentID: "deployment"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	draft := config.RecommendedDynamicDraft()
	draft.Runtime.MaxWorkers = 3
	draft.Runtime.MaxRunningProjects = 3
	draft.Runtime.MaxProjectWorkers = 1
	draft.Runtime.WorkerHealthcheck = "disabled"
	draft.Workers = []config.DynamicWorkerDraft{
		acceptanceWorker("codex-one", provider.URL, 1),
		acceptanceWorker("codex-two", provider.URL, 1),
	}
	read := saveDraft(t, host.Handler(), 1, draft, map[string]map[string]config.WorkerSecretChange{
		"codex-one": {"OPENAI_API_KEY": {Value: "fixture-secret-one"}},
		"codex-two": {"OPENAI_API_KEY": {Value: "fixture-secret-two"}},
	})
	var effective config.ConfigurationRevision
	requestJSON(t, host.Handler(), http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, &effective)
	if effective.ID == "" {
		t.Fatal("activation returned no Config Revision")
	}

	first := createProject(t, host.Handler(), "first", true)
	second := createProject(t, host.Handler(), "second", false)
	if first.Project.ConfigRevision != effective.ID || second.Project.ConfigRevision != effective.ID {
		t.Fatalf("Project bindings: first=%q second=%q revision=%q", first.Project.ConfigRevision, second.Project.ConfigRevision, effective.ID)
	}

	activeDraft := config.RecommendedDynamicDraft()
	activeDraft.Runtime.MaxWorkers = 3
	activeDraft.Runtime.MaxRunningProjects = 3
	activeDraft.Runtime.MaxProjectWorkers = 3
	activeDraft.Runtime.WorkerHealthcheck = "disabled"
	activeDraft.Workers = []config.DynamicWorkerDraft{acceptanceWorker("active-only", provider.URL, 3)}
	read = saveDraft(t, host.Handler(), read.DraftVersion, activeDraft, map[string]map[string]config.WorkerSecretChange{
		"active-only": {"OPENAI_API_KEY": {Value: "active-secret"}},
	})
	var activeRevision config.ConfigurationRevision
	requestJSON(t, host.Handler(), http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, &activeRevision)

	lease, err := host.ControlPlane().AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "fixture-dispatcher", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	control := host.ControlPlane()
	planning, err := controlplane.NewInProcessRunControl(control)
	if err != nil {
		t.Fatal(err)
	}
	defer planning.Close()
	scheduling, err := planning.SchedulingSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskBootstrap, rundomain.TaskReason, rundomain.TaskExplore},
		CandidateLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduling.Work) == 0 {
		t.Fatal("Scheduling Snapshot contains no bound Project work")
	}
	if scheduling.DispatcherPolicy.ConfigRevision != activeRevision.ID ||
		scheduling.DispatcherPolicy.PollIntervalSeconds != activeDraft.Runtime.SchedulerIntervalSeconds ||
		scheduling.DispatcherPolicy.MaxWorkers != activeDraft.Runtime.MaxWorkers ||
		scheduling.DispatcherPolicy.MaxRunningProjects != activeDraft.Runtime.MaxRunningProjects {
		t.Fatalf("active Dispatcher policy: %+v", scheduling.DispatcherPolicy)
	}
	for _, work := range scheduling.Work {
		if work.ConfigRevision != effective.ID {
			t.Fatalf("Scheduling Project Config Revision=%q, want %q", work.ConfigRevision, effective.ID)
		}
	}
	boundCfg, err := host.ExecutionConfig(t.Context(), effective.ID)
	if err != nil {
		t.Fatal(err)
	}
	activeCfg, err := host.ActiveExecutionConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if activeCfg.ConfigRevision != activeRevision.ID || boundCfg.ConfigRevision == activeCfg.ConfigRevision {
		t.Fatalf("active revision=%q bound revision=%q", activeCfg.ConfigRevision, boundCfg.ConfigRevision)
	}
	catalog, err := worker.NewCatalogFromConfig(worker.CatalogConfiguration{
		Revision: boundCfg.ConfigRevision, ImageReference: boundCfg.Container.Image,
		PromptGroup: boundCfg.Runtime.PromptGroup, Workers: boundCfg.Workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := func(project acceptanceProject, workerName string, task rundomain.TaskType, target *string) (*rundomain.AdmittedRun, error) {
		input := rundomain.InputSnapshot{Generation: project.Generation, Revision: project.Revision, TargetIntentID: target}
		workKey, err := rundomain.WorkKeyFor(project.ID, task, input)
		if err != nil {
			return nil, err
		}
		workerSnapshot, err := catalog.WorkerSnapshotForTask("codex-one", task)
		if err != nil {
			return nil, err
		}
		promptHash, err := catalog.PromptTemplateHash(task)
		if err != nil {
			return nil, err
		}
		snapshot := workerSnapshot
		snapshot.Name = workerName
		timeouts := rundomain.TimeoutSnapshot{RunSeconds: 100, PhaseSeconds: 30, GraceSeconds: 1}
		if task != rundomain.TaskReason {
			timeouts.FallbackPhaseSeconds = 20
			timeouts.RecoveryPhaseSeconds = 10
			timeouts.RunSeconds = 200
		}
		maxIntents := 0
		if task == rundomain.TaskReason {
			maxIntents = 1
		}
		return control.StartRun(t.Context(), rundomain.StartRequest{
			ProjectID: project.ID, Task: task, CandidateGeneration: project.Generation, CandidateRevision: project.Revision,
			TargetIntentID: target,
			WorkKey:        workKey, DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
			Spec: rundomain.AdmissionSpecSnapshot{
				ConfigRevision: "sha256:" + strings.Repeat("0", 64), PromptTemplateHash: promptHash,
				Worker: snapshot, Image: rundomain.ImageSnapshot{RequestedRef: "caller-image"},
				Timeouts:     timeouts,
				ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: maxIntents,
			},
			RetryPolicy: rundomain.DefaultRetryPolicy(),
		})
	}

	started, err := start(first.Project, "codex-one", rundomain.TaskReason, nil)
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.Spec.ConfigRevision != effective.ID || started.Run.Spec.Image.RequestedRef != boundCfg.Container.Image ||
		started.Run.Spec.Worker.Name != "codex-one" {
		t.Fatalf("authoritative Execution Spec: %+v", started.Run.Spec)
	}
	if stringsContainsJSON(t, started.Run.Spec, "fixture-secret") {
		t.Fatal("Execution Spec contains Secret plaintext")
	}

	bootstrapTarget := first.Intents[0].ID
	if _, err := start(first.Project, "codex-two", rundomain.TaskBootstrap, &bootstrapTarget); !isRunError(err, rundomain.ErrorCapacityExceeded) {
		t.Fatalf("Project capacity error=%v, want %s", err, rundomain.ErrorCapacityExceeded)
	}
	if _, err := start(second.Project, "codex-one", rundomain.TaskReason, nil); !isRunError(err, rundomain.ErrorCapacityExceeded) {
		t.Fatalf("Worker capacity error=%v, want %s", err, rundomain.ErrorCapacityExceeded)
	}
	if _, err := start(second.Project, "unrelated-worker", rundomain.TaskReason, nil); !isRunError(err, rundomain.ErrorNotReady) {
		t.Fatalf("unqualified Worker error=%v, want %s", err, rundomain.ErrorNotReady)
	}

	activeDraft.Runtime.MaxWorkers = 1
	activeDraft.Runtime.MaxRunningProjects = 1
	activeDraft.Runtime.MaxProjectWorkers = 1
	read = saveDraft(t, host.Handler(), read.DraftVersion, activeDraft, nil)
	requestJSON[config.ConfigurationRevision](t, host.Handler(), http.MethodGet, "/api/v1/configuration/revisions/active", nil, http.StatusOK, &config.ConfigurationRevision{})
	if _, err := start(second.Project, "codex-two", rundomain.TaskReason, nil); !isRunError(err, rundomain.ErrorCapacityExceeded) {
		t.Fatalf("global capacity error=%v, want %s", err, rundomain.ErrorCapacityExceeded)
	}

	providerForRun, err := host.RunSecretProvider()
	if err != nil {
		t.Fatal(err)
	}
	scoped := providerForRun.(rundomain.RunScopedSecretEnvironmentProvider)
	resolved, err := scoped.ResolveRunSecrets(t.Context(), started.StartedRun, started.Run.Spec.Worker.SecretReferences)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Environment()["OPENAI_API_KEY"] != "fixture-secret-one" {
		t.Fatal("authorized Run did not resolve its Secret")
	}
	resolved.Close()

	if _, err := providerForRun.ResolveSecrets(t.Context(), started.Run.Spec.Worker.SecretReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("unscoped Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}
	tamperedOwner := started.StartedRun
	tamperedOwner.Run.ProjectID = second.Project.ID
	if _, err := scoped.ResolveRunSecrets(t.Context(), tamperedOwner, started.Run.Spec.Worker.SecretReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("wrong-owner Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}
	tamperedFencing := started.StartedRun
	tamperedFencing.Run.FencingToken++
	if _, err := scoped.ResolveRunSecrets(t.Context(), tamperedFencing, started.Run.Spec.Worker.SecretReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("fenced Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}
	wrongReferences := append([]rundomain.SecretReference(nil), started.Run.Spec.Worker.SecretReferences...)
	wrongReferences[0].Key = "secret-version-not-authorized"
	if _, err := scoped.ResolveRunSecrets(t.Context(), started.StartedRun, wrongReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("wrong-reference Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}

	secondConnection := database.NewDB()
	if err := secondConnection.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	expireClaim := func(expires int64) {
		t.Helper()
		tx, err := secondConnection.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(t.Context(), "UPDATE execution_claims SET lease_expires_unix_nano = ? WHERE run_id = ?", expires, started.Run.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	expireClaim(time.Now().Add(-time.Minute).UnixNano())
	if _, err := scoped.ResolveRunSecrets(t.Context(), started.StartedRun, started.Run.Spec.Worker.SecretReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("expired-claim Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}
	expireClaim(time.Now().Add(time.Minute).UnixNano())
	expireDispatcher := func(expires int64) {
		t.Helper()
		tx, err := secondConnection.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(t.Context(), "UPDATE dispatcher_lease SET lease_expires_unix_nano = ? WHERE lease_name = 'global'", expires); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	expireDispatcher(time.Now().Add(-time.Minute).UnixNano())
	if _, err := scoped.ResolveRunSecrets(t.Context(), started.StartedRun, started.Run.Spec.Worker.SecretReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("fenced-Dispatcher Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}
	expireDispatcher(time.Now().Add(time.Minute).UnixNano())
	if err := secondConnection.Close(); err != nil {
		t.Fatal(err)
	}

	runtime := testutil.NewMemoryRuntime()
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: started.Run.ID, Phase: rundomain.PhaseReason}, testutil.ExecutionBehavior{
		Output: rundomain.ProcessOutput{
			Exited: true, ExitCode: 0,
			Stdout: `{"outcome":{"kind":"no_change"}}`,
			Stderr: "session id: 12345678-abcd-4abc-8def-123456789abc",
		},
	})
	binding, err := rundomain.NewExecutionBinding(runtime, providerForRun, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := catalog.BindExecution(binding, worker.ExecutionBindingRequest{
		Task: started.Run.Task, WorkerName: started.Run.Spec.Worker.Name,
		BlackboardInput: started.BlackboardInput, MaxIntents: started.Run.Spec.MaxProposedIntents,
	}, started.StartedRun)
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(t.Context(), started.StartedRun)
	if completion.Kind != rundomain.CompletionResultReady || completion.OutcomeKind != rundomain.OutcomeNoChange {
		t.Fatalf("Agent completion=%+v", completion)
	}
	finishRequest, err := completion.FinishRequest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.FinishRun(t.Context(), finishRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.ResolveRunSecrets(t.Context(), started.StartedRun, started.Run.Spec.Worker.SecretReferences); !isRunError(err, rundomain.ErrorForbidden) {
		t.Fatalf("terminal Run Secret resolution error=%v, want %s", err, rundomain.ErrorForbidden)
	}
}

type acceptanceProject struct {
	ID             string `json:"id"`
	ConfigRevision string `json:"config_revision"`
	Generation     int    `json:"generation"`
	Revision       int64  `json:"revision"`
}

type acceptanceIntent struct {
	ID string `json:"id"`
}

type acceptanceProjectResponse struct {
	Project acceptanceProject  `json:"project"`
	Intents []acceptanceIntent `json:"intents"`
}

func acceptanceWorker(name, providerURL string, maxRunning int) config.DynamicWorkerDraft {
	return config.DynamicWorkerDraft{
		Name: name, AgentType: "codex", ProviderName: "fixture",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: maxRunning,
		Priority: 1, AgentEnv: map[string]string{"CODEX_BASE_URL": providerURL, "CODEX_MODEL": "fixture"},
		TimeoutOverrides: map[string]int{},
	}
}

func saveDraft(t *testing.T, handler http.Handler, expectedVersion int64, draft config.DynamicDraft, secrets map[string]map[string]config.WorkerSecretChange) config.ConfigurationRead {
	t.Helper()
	var read config.ConfigurationRead
	requestJSON(t, handler, http.MethodPut, "/api/v1/configuration", config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: expectedVersion, Draft: draft, Secrets: secrets,
	}, http.StatusOK, &read)
	return read
}

func createProject(t *testing.T, handler http.Handler, title string, bootstrap bool) acceptanceProjectResponse {
	t.Helper()
	var response acceptanceProjectResponse
	requestJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": title, "origin": "origin", "goal": "goal", "bootstrap_enabled": bootstrap,
	}, http.StatusCreated, &response)
	return response
}

func requestJSON[T any](t *testing.T, handler http.Handler, method, path string, body any, wantStatus int, target *T) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: got %d, want %d; body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

func isRunError(err error, want rundomain.ErrorCode) bool {
	var domainErr *rundomain.DomainError
	return errors.As(err, &domainErr) && domainErr.Code == want
}

func stringsContainsJSON(t *testing.T, value any, needle string) bool {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(encoded, []byte(needle))
}
