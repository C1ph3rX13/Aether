package controlplane

import (
	"encoding/json"
	"errors"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/project"
)

func TestManagedProjectCreationRequiresAndBindsActiveConfigRevision(t *testing.T) {
	host, err := NewControlPlaneHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{
		OperatorName: "operator", DeploymentID: "deployment",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown()

	_, err = host.module.projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "without configuration", Origin: "origin", Goal: "goal",
	})
	var configurationErr *project.DomainError
	if !errors.As(err, &configurationErr) || configurationErr.Code != project.ErrorConfigurationRequired {
		t.Fatalf("create without active Config Revision error = %v", err)
	}

	var projectCount int
	conn, err := host.db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM projects").Scan(&projectCount); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	if projectCount != 0 {
		t.Fatalf("rejected Project creation left %d Project rows", projectCount)
	}
}

func TestManagedProjectRetainsConfigRevisionAcrossSavesAndReopen(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
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

	draft := config.RecommendedDynamicDraft()
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "codex-http", AgentType: "codex", ProviderName: "Test Provider",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 2, Priority: 1,
		AgentEnv: map[string]string{
			"CODEX_BASE_URL": provider.URL, "CODEX_MODEL": "test-model",
		},
		TimeoutOverrides: map[string]int{},
	}}
	read, err := host.configuration.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1,
		Draft:                draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-http": {"OPENAI_API_KEY": {Value: "provider-secret"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := host.configuration.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	oldProject, err := host.module.projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "old binding", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldProject.Project.ConfigRevision != first.ID {
		t.Fatalf("old Project Config Revision = %q, want %q", oldProject.Project.ConfigRevision, first.ID)
	}
	completed, err := host.module.projects.CompleteProject(t.Context(), oldProject.Project.ID, project.CompleteRequest{
		From: []string{"origin"}, Description: "completed", ExpectedRevision: oldProject.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := host.module.projects.ReopenProject(t.Context(), oldProject.Project.ID, project.ReopenRequest{
		Feedback: "new generation", ExpectedRevision: completed.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Project.ConfigRevision != first.ID || reopened.Project.Generation != 2 {
		t.Fatalf("reopened Project = %+v, want Config Revision %q and generation 2", reopened.Project, first.ID)
	}

	changed := read.Draft
	changed.Runtime.MaxWorkers++
	read, err = host.configuration.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: read.DraftVersion, Draft: changed,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.configuration.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("changed configuration reused the first Config Revision")
	}

	newProject, err := host.module.projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "new binding", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if newProject.Project.ConfigRevision != second.ID {
		t.Fatalf("new Project Config Revision = %q, want %q", newProject.Project.ConfigRevision, second.ID)
	}
	currentOld, err := host.module.projects.Get(t.Context(), oldProject.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentOld.Project.ConfigRevision != first.ID {
		t.Fatalf("existing Project changed Config Revision from %q to %q", first.ID, currentOld.Project.ConfigRevision)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+oldProject.Project.ID, nil)
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Project read status=%d body=%s", response.Code, response.Body.String())
	}
	var detail project.ProjectDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Project.ConfigRevision != first.ID {
		t.Fatalf("Project read Config Revision = %q, want %q", detail.Project.ConfigRevision, first.ID)
	}

	catalogRequest := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	catalogResponse := httptest.NewRecorder()
	host.Handler().ServeHTTP(catalogResponse, catalogRequest)
	if catalogResponse.Code != http.StatusOK {
		t.Fatalf("Catalog read status=%d body=%s", catalogResponse.Code, catalogResponse.Body.String())
	}
	var catalog []struct {
		Project project.ProjectMeta `json:"project"`
	}
	if err := json.Unmarshal(catalogResponse.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("Catalog Project count = %d, want 2", len(catalog))
	}
	for _, entry := range catalog {
		want := first.ID
		if entry.Project.ID == newProject.Project.ID {
			want = second.ID
		}
		if entry.Project.ConfigRevision != want {
			t.Fatalf("Catalog Project %q Config Revision = %q, want %q", entry.Project.ID, entry.Project.ConfigRevision, want)
		}
	}
}

func TestWorkerDeletionBlocksNewProjectsButPreservesExecutionAcrossRestart(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer provider.Close()
	path := filepath.Join(t.TempDir(), "aether.db")
	options := ControlPlaneHostOptions{OperatorName: "operator", DeploymentID: "deployment"}
	host, err := NewControlPlaneHost(path, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if host != nil {
			_ = host.Shutdown()
		}
	})
	draft := config.RecommendedDynamicDraft()
	draft.Workers = []config.DynamicWorkerDraft{{Name: "original", AgentType: "codex", TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 2, AgentEnv: map[string]string{"CODEX_BASE_URL": provider.URL, "CODEX_MODEL": "original-model"}}}
	read, err := host.configuration.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 1, Draft: draft, Secrets: map[string]map[string]config.WorkerSecretChange{"original": {"OPENAI_API_KEY": {Value: "original-secret"}}}})
	if err != nil {
		t.Fatal(err)
	}
	old, err := host.module.projects.Create(t.Context(), project.CreateProjectRequest{Title: "old", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := host.configuration.DeleteWorker(t.Context(), "original", config.DynamicWorkerDeleteRequest{ExpectedDraftVersion: read.DraftVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.ValidationProblems) != 3 {
		t.Fatalf("missing coverage: %+v", deleted.ValidationProblems)
	}
	_, err = host.module.projects.Create(t.Context(), project.CreateProjectRequest{Title: "blocked", Origin: "origin", Goal: "goal"})
	var creationErr *project.DomainError
	if !errors.As(err, &creationErr) || creationErr.Code != project.ErrorConfigurationRequired {
		t.Fatalf("creation without coverage: %v", err)
	}
	changed := deleted.Draft
	changed.Runtime.MaxWorkers = 2
	saved, err := host.configuration.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: deleted.DraftVersion, Draft: changed})
	if err != nil {
		t.Fatalf("Settings cannot save without Workers: %v", err)
	}
	host.Shutdown()
	host, err = NewControlPlaneHost(path, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := host.ActiveExecutionConfig(t.Context())
	if err != nil || len(cfg.Workers) != 0 || cfg.Runtime.MaxWorkers != 2 {
		t.Fatalf("cannot restart with empty current catalog: %v", err)
	}
	bound, err := host.ExecutionConfig(t.Context(), old.Project.ConfigRevision)
	if err != nil || bound.Workers[0].Name != "original" {
		t.Fatalf("old catalog lost: %v", err)
	}
	lease, err := host.ControlPlane().AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{InstanceID: "dispatcher", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewInProcessRunControl(host.ControlPlane())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	snapshot, err := control.SchedulingSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken, SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 10})
	if err != nil || len(snapshot.Work) == 0 {
		t.Fatalf("old Project cannot schedule: %v", err)
	}
	if snapshot.DispatcherPolicy.MaxWorkers != 2 || snapshot.Work[0].ConfigRevision != old.Project.ConfigRevision {
		t.Fatal("global/bound configuration scopes changed")
	}
	catalog, err := worker.NewCatalogFromConfig(worker.CatalogConfiguration{Revision: bound.ConfigRevision, ImageReference: bound.Container.Image, PromptGroup: bound.Runtime.PromptGroup, Workers: bound.Workers})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := catalog.WorkerSnapshotForTask("original", rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := catalog.PromptTemplateHash(rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	workKey, _ := rundomain.WorkKeyFor(old.Project.ID, rundomain.TaskReason, rundomain.InputSnapshot{Generation: 1, Revision: 1})
	started, err := host.ControlPlane().StartRun(t.Context(), rundomain.StartRequest{ProjectID: old.Project.ID, Task: rundomain.TaskReason, CandidateGeneration: 1, CandidateRevision: 1, WorkKey: workKey, DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken, Spec: rundomain.AdmissionSpecSnapshot{ConfigRevision: bound.ConfigRevision, Worker: ws, Image: rundomain.ImageSnapshot{RequestedRef: bound.Container.Image}, PromptTemplateHash: hash, Timeouts: rundomain.TimeoutSnapshot{RunSeconds: 100, PhaseSeconds: 30, GraceSeconds: 1}, ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 1}})
	if err != nil || started.Run.Spec.Worker.Name != "original" {
		t.Fatalf("deleted historical Worker cannot run: %v", err)
	}
	secrets, err := host.RunSecretProvider()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := secrets.(rundomain.RunScopedSecretEnvironmentProvider).ResolveRunSecrets(t.Context(), started.StartedRun, started.Run.Spec.Worker.SecretReferences)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Environment()["OPENAI_API_KEY"] != "original-secret" {
		t.Fatal("historical Secret was changed")
	}
	resolved.Close()
	changed.Workers = []config.DynamicWorkerDraft{{Name: "replacement", AgentType: "codex", TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 1, AgentEnv: map[string]string{"CODEX_BASE_URL": provider.URL, "CODEX_MODEL": "replacement-model"}}}
	_, err = host.configuration.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: saved.DraftVersion, Draft: changed, Secrets: map[string]map[string]config.WorkerSecretChange{"replacement": {"OPENAI_API_KEY": {Value: "replacement-secret"}}}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := host.module.projects.Create(t.Context(), project.CreateProjectRequest{Title: "fresh", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	newCfg, err := host.ExecutionConfig(t.Context(), fresh.Project.ConfigRevision)
	if err != nil || len(newCfg.Workers) != 1 || newCfg.Workers[0].Name != "replacement" {
		t.Fatalf("new Project bound obsolete Worker: %v", err)
	}
}
