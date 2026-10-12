package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestProductionReadinessPublishesPartialProviderResults(t *testing.T) {
	readiness := readinessForTest(t, func(_ context.Context, configured worker.CatalogWorker) error {
		if configured.Name == "failed" {
			return errors.New("provider unavailable")
		}
		return nil
	})
	ready, err := readiness.prepareWorkers(t.Context(), "startup_only")
	if err != nil {
		t.Fatal(err)
	}
	if !ready["healthy"] || ready["failed"] {
		t.Fatalf("readiness=%#v", ready)
	}
}

func TestProductionReadinessDisabledSkipsProviderChecks(t *testing.T) {
	calls := 0
	readiness := readinessForTest(t, func(context.Context, worker.CatalogWorker) error {
		calls++
		return errors.New("must not run")
	})
	ready, err := readiness.prepareWorkers(t.Context(), "disabled")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !ready["healthy"] || !ready["failed"] {
		t.Fatalf("calls=%d readiness=%#v", calls, ready)
	}
}

func TestProductionReadinessTaskCheckUpdatesSelectedWorker(t *testing.T) {
	readiness := readinessForTest(t, func(_ context.Context, configured worker.CatalogWorker) error {
		if configured.Name == "failed" {
			return errors.New("provider unavailable")
		}
		return nil
	})
	ready, err := readiness.prepareWorker(t.Context(), "startup_and_task", "healthy")
	if err != nil || !ready {
		t.Fatalf("healthy Worker readiness = %t, %v", ready, err)
	}
	ready, err = readiness.prepareWorker(t.Context(), "startup_and_task", "failed")
	if err != nil || ready {
		t.Fatalf("failed Worker readiness = %t, %v", ready, err)
	}
	if readiness.providers.IsReady(readiness.catalog.Revision(), "failed") {
		t.Fatal("failed Worker remained ready")
	}
	if _, err := readiness.prepareWorker(t.Context(), "startup_and_task", "missing"); err == nil {
		t.Fatal("missing Worker was accepted")
	}
}

func TestProductionStartupHealthcheckRequiresImageAndOneProvider(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		readiness := readinessForTest(t, func(context.Context, worker.CatalogWorker) error {
			return nil
		})
		if err := readiness.runStartupHealthchecksOnly(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("startup error = %v", err)
		}
	})
	t.Run("all Providers failed", func(t *testing.T) {
		readiness := readinessForTest(t, func(context.Context, worker.CatalogWorker) error {
			return errors.New("provider unavailable")
		})
		err := readiness.runStartupHealthchecksOnly(t.Context())
		if err == nil || !strings.Contains(err.Error(), "startup healthchecks failed for all workers") {
			t.Fatalf("startup error = %v", err)
		}
	})
	t.Run("partial Provider readiness", func(t *testing.T) {
		readiness := readinessForTest(t, func(_ context.Context, configured worker.CatalogWorker) error {
			if configured.Name == "failed" {
				return errors.New("provider unavailable")
			}
			return nil
		})
		if err := readiness.runStartupHealthchecksOnly(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func readinessForTest(
	t *testing.T,
	check worker.ProviderHealthCheckerFunc,
) *productionReadiness {
	t.Helper()
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "example/aether@sha256:" + strings.Repeat("a", 64),
		Workers: []worker.CatalogWorker{
			{
				Name: "healthy", Adapter: "mock", MaxRunning: 1,
				TaskTypes: []rundomain.TaskType{rundomain.TaskReason}, Capabilities: []string{"reason"},
			},
			{
				Name: "failed", Adapter: "mock", MaxRunning: 1,
				TaskTypes: []rundomain.TaskType{rundomain.TaskExplore}, Capabilities: []string{"explore"},
			},
		},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
			rundomain.TaskExplore:   {Primary: "explore", Fallback: "explore fallback", Finalization: "finalize explore"},
			rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "bootstrap fallback", Finalization: "finalize bootstrap"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &productionReadiness{
		catalog: catalog, providers: worker.NewProviderReadiness(), checker: check,
	}
}
