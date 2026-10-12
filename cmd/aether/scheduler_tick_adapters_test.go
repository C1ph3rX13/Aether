package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/api"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
	"github.com/gin-gonic/gin"
)

type schedulerControlContract interface {
	scheduler.SchedulingControl
	scheduler.ProjectInventoryControl
	scheduler.RunRecoveryControl
	scheduler.RunLifecycleControl
	scheduler.ProjectCleanupControl
}

func TestRunSchedulerTickWorksThroughBothControlPlaneAdapters(t *testing.T) {
	factories := map[string]func(*testing.T, controlplane.ControlPlaneCapability) (schedulerControlContract, func()){
		"in-process": func(t *testing.T, service controlplane.ControlPlaneCapability) (schedulerControlContract, func()) {
			control, err := controlplane.NewInProcessRunControl(service)
			if err != nil {
				t.Fatal(err)
			}
			return control, func() {}
		},
		"http-v1": func(t *testing.T, service controlplane.ControlPlaneCapability) (schedulerControlContract, func()) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			api.RegisterRuns(router.Group("/api/v1"), service)
			server := httptest.NewServer(router)
			control, err := controlplane.NewHTTPRunControl(server.URL + "/api/v1")
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			return control, func() {
				control.Close()
				server.Close()
			}
		},
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "aether.db")
			application, err := controlplane.NewControlPlaneTestHost(databasePath, nil, controlplane.ControlPlaneHostOptions{OperatorName: "test-operator"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if shutdownErr := application.Shutdown(); shutdownErr != nil {
					t.Errorf("Control Plane shutdown: %v", shutdownErr)
				}
			})
			controlPlane := application.ControlPlane()
			activeConfig, err := application.ActiveExecutionConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = createProjectThroughHandler(t, application.Handler(), "scheduler tick", "origin", "goal", false); err != nil {
				t.Fatal(err)
			}
			lease, err := controlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
				InstanceID: "dispatcher-" + name, TTL: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			control, cleanup := factory(t, controlPlane)
			defer cleanup()
			catalog, err := worker.NewCatalogForRevision(activeConfig.ConfigRevision, worker.CatalogSource{
				ImageReference: "registry.example/aether@sha256:test",
				Workers: []worker.CatalogWorker{{
					Name: "mock", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskReason}, MaxRunning: 1,
				}},
				Prompts: map[rundomain.TaskType]worker.PromptSet{
					rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
					rundomain.TaskExplore:   {Primary: "explore", Fallback: "explore conclude", Finalization: "finalize explore"},
					rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "bootstrap fallback", Finalization: "finalize bootstrap"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime := testutil.NewMemoryRuntime()
			runtime.ConfigureDefaultExecution(testutil.ExecutionBehavior{
				Output: rundomain.ProcessOutput{
					Exited: true, ExitCode: 0,
					Stdout: `{"outcome":{"kind":"no_change"}}` + "\n",
				},
			})
			policy := scheduler.RunSchedulerPolicy{
				PollInterval: time.Second, MaxWorkers: 1, MaxProjectWorkers: 1, MaxRunningProjects: 1,
				ExecutionPolicies: executionPolicyTableForWorkers(
					[]string{catalog.Workers()[0].Name}, rundomain.DefaultRetryPolicy(), 1, 1, 0, 1, 4, 6, 1,
				),
			}
			executions, err := NewRunExecutionFactory(catalog, runtime, nil)
			if err != nil {
				t.Fatal(err)
			}
			sut, err := scheduler.NewRunScheduler(scheduler.RunSchedulerControls{
				Planning: control, Recovery: control, Lifecycle: control,
			}, scheduler.RunSchedulerConfig{
				ConfigRevision: activeConfig.ConfigRevision,
				Policy:         policy, Catalog: catalog, Resources: runtime,
				Executions: executions,
				Lease:      lease, DeploymentID: "deployment-" + name, Once: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := sut.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot := runtime.Snapshot()
			if len(snapshot.Launches) != 1 || len(snapshot.CompletedRunIDs) != 1 {
				t.Fatalf("runtime lifecycle: %+v", snapshot)
			}
		})
	}
}

func createProjectThroughHandler(t *testing.T, handler http.Handler, title, origin, goal string, bootstrapEnabled bool) error {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"title": title, "origin": origin, "goal": goal,
		"bootstrap_enabled": bootstrapEnabled,
	})
	if err != nil {
		return err
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(payload))
	httpRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusCreated {
		return fmt.Errorf("create Project status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return nil
}
