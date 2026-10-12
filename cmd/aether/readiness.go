package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

type productionReadiness struct {
	catalog   *worker.Catalog
	providers *worker.ProviderReadiness
	checker   worker.ProviderHealthChecker
}

func loadWorkerCatalog(cfg *config.DispatchConfig) (*worker.Catalog, error) {
	source, err := worker.CatalogSourceFromConfig(worker.CatalogConfiguration{
		ImageReference: cfg.Container.Image, PromptGroup: cfg.Runtime.PromptGroup, Workers: cfg.Workers,
	})
	if err != nil {
		return nil, fmt.Errorf("build Worker Catalog: %w", err)
	}
	catalog, err := worker.NewCatalogForRevision(cfg.ConfigRevision, source)
	if err != nil {
		return nil, fmt.Errorf("build Worker Catalog: %w", err)
	}
	return catalog, nil
}

func newProductionReadiness(
	catalog *worker.Catalog,
	secrets rundomain.SecretEnvironmentProvider,
	timeout time.Duration,
) (*productionReadiness, error) {
	checker, err := worker.NewHTTPProviderHealthChecker(http.DefaultClient, secrets, timeout)
	if err != nil {
		return nil, err
	}
	return &productionReadiness{
		catalog: catalog, providers: worker.NewProviderReadiness(), checker: checker,
	}, nil
}

func (readiness *productionReadiness) prepareWorkers(
	ctx context.Context,
	healthcheckMode string,
) (map[string]bool, error) {
	if healthcheckMode == "disabled" {
		ready := make(map[string]bool, len(readiness.catalog.Workers()))
		for _, configured := range readiness.catalog.Workers() {
			readiness.providers.MarkReady(readiness.catalog.Revision(), configured.Name)
			ready[configured.Name] = true
		}
		return ready, nil
	}
	ready, err := readiness.refreshProviders(ctx)
	if ctx.Err() != nil {
		return ready, ctx.Err()
	}
	if err != nil {
		slog.Warn("Provider readiness check failed", "error", err)
	}
	return ready, nil
}

func (readiness *productionReadiness) prepareWorker(
	ctx context.Context,
	healthcheckMode string,
	workerName string,
) (bool, error) {
	if healthcheckMode != "startup_and_task" {
		return true, nil
	}
	configured, ok := readiness.catalog.Worker(workerName)
	if !ok {
		return false, fmt.Errorf("Worker %s is unavailable for readiness check", workerName)
	}
	if err := readiness.checker.CheckProvider(ctx, configured); err != nil {
		readiness.providers.MarkNotReady(readiness.catalog.Revision(), workerName)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		slog.Warn("Worker Provider readiness check failed", "worker", workerName, "error", err)
		return false, nil
	}
	readiness.providers.MarkReady(readiness.catalog.Revision(), workerName)
	return true, nil
}

func (readiness *productionReadiness) refreshProviders(ctx context.Context) (map[string]bool, error) {
	err := readiness.providers.Refresh(ctx, readiness.catalog, readiness.checker)
	ready := make(map[string]bool, len(readiness.catalog.Workers()))
	for _, configured := range readiness.catalog.Workers() {
		ready[configured.Name] = readiness.providers.IsReady(readiness.catalog.Revision(), configured.Name)
	}
	return ready, err
}

func (readiness *productionReadiness) runStartupHealthchecksOnly(ctx context.Context) error {
	ready, checkErr := readiness.refreshProviders(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	healthy := 0
	for _, workerReady := range ready {
		if workerReady {
			healthy++
		}
	}
	slog.Info("startup Provider healthcheck results",
		"total", len(ready), "healthy", healthy, "unhealthy", len(ready)-healthy)
	if checkErr != nil && healthy > 0 {
		slog.Warn("some Workers failed startup Provider healthchecks", "error", checkErr)
	}
	if healthy == 0 {
		if checkErr != nil {
			return fmt.Errorf("startup healthchecks failed for all workers: %w", checkErr)
		}
		return fmt.Errorf("startup healthchecks failed for all workers")
	}
	return nil
}

func runProductionStartupHealthchecks(
	ctx context.Context,
	cfg *config.DispatchConfig,
	secrets rundomain.SecretEnvironmentProvider,
) error {
	catalog, err := loadWorkerCatalog(cfg)
	if err != nil {
		return err
	}
	readiness, err := newProductionReadiness(catalog, secrets, time.Duration(cfg.Runtime.HealthcheckTimeout)*time.Second)
	if err != nil {
		return err
	}
	return readiness.runStartupHealthchecksOnly(ctx)
}
