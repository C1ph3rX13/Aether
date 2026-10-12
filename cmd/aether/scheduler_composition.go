package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	dockerruntime "github.com/C1ph3rX13/Aether/internal/docker"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/C1ph3rX13/Aether/internal/worker"
	dockerclient "github.com/moby/moby/client"
)

const (
	dispatcherLeaseTTL             = 30 * time.Second
	dispatcherRenewInterval        = 10 * time.Second
	dispatcherCallTimeout          = 5 * time.Second
	revisionReadinessRetryInterval = 30 * time.Second
)

type schedulerRevisionConfiguration struct {
	catalog          *worker.Catalog
	policy           scheduler.RunSchedulerPolicy
	project          dockerruntime.ProjectConfig
	outputLimits     dockerruntime.OutputLimits
	readinessMode    string
	readinessTimeout time.Duration
}

type schedulerRevisionStore struct {
	mu      sync.Mutex
	load    func(context.Context, string) (*config.DispatchConfig, error)
	entries map[string]*schedulerRevisionConfiguration
}

func newSchedulerRevisionStore(
	initial *config.DispatchConfig,
	load func(context.Context, string) (*config.DispatchConfig, error),
) (*schedulerRevisionStore, error) {
	configured, err := buildSchedulerRevisionConfiguration(initial)
	if err != nil {
		return nil, err
	}
	return &schedulerRevisionStore{
		load:    load,
		entries: map[string]*schedulerRevisionConfiguration{initial.ConfigRevision: configured},
	}, nil
}

func (store *schedulerRevisionStore) get(
	ctx context.Context,
	revision string,
) (*schedulerRevisionConfiguration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if configured := store.entries[revision]; configured != nil {
		return configured, nil
	}
	if store.load == nil {
		return nil, fmt.Errorf("Config Revision loader is unavailable")
	}
	loaded, err := store.load(ctx, revision)
	if err != nil {
		return nil, err
	}
	if loaded == nil || loaded.ConfigRevision != revision {
		return nil, fmt.Errorf("Config Revision loader returned mismatched configuration")
	}
	configured, err := buildSchedulerRevisionConfiguration(loaded)
	if err != nil {
		return nil, err
	}
	store.entries[revision] = configured
	return configured, nil
}

func buildSchedulerRevisionConfiguration(
	cfg *config.DispatchConfig,
) (*schedulerRevisionConfiguration, error) {
	if cfg == nil || cfg.ConfigRevision == "" {
		return nil, fmt.Errorf("Config Revision is required")
	}
	catalog, err := loadWorkerCatalog(cfg)
	if err != nil {
		return nil, err
	}
	policy, err := runSchedulerPolicy(cfg)
	if err != nil {
		return nil, err
	}
	project := dockerruntime.ProjectConfig{
		Image: cfg.Container.Image, NetworkMode: cfg.Container.NetworkMode,
		CapAdd:          append([]string(nil), cfg.Container.CapAdd...),
		CompletedAction: string(cfg.Container.CompletedAction),
	}
	limits := dockerruntime.OutputLimits{
		StdoutBytes: cfg.Container.StdoutLimitBytes,
		StderrBytes: cfg.Container.StderrLimitBytes,
	}
	return &schedulerRevisionConfiguration{
		catalog: catalog, policy: policy, project: project, outputLimits: limits,
		readinessMode:    cfg.Runtime.WorkerHealthcheck,
		readinessTimeout: time.Duration(cfg.Runtime.HealthcheckTimeout) * time.Second,
	}, nil
}

type schedulerRevisionReadiness struct {
	provider *productionReadiness
	mode     string
	ready    map[string]bool
	prepared bool
	retryAt  time.Time
}

func allWorkersReady(readiness map[string]bool) bool {
	if len(readiness) == 0 {
		return false
	}
	for _, ready := range readiness {
		if !ready {
			return false
		}
	}
	return true
}

func runProductionScheduler(
	ctx context.Context,
	planning scheduler.SchedulingControl,
	recovery scheduler.RunRecoveryControl,
	lifecycle scheduler.RunLifecycleControl,
	cfg *config.DispatchConfig,
	lease rundomain.DispatcherLease,
	deploymentID string,
	dockerHost string,
	secrets rundomain.SecretEnvironmentProvider,
	readinessSecrets rundomain.SecretEnvironmentProvider,
	readinessSecretForRevision func(context.Context, string) (rundomain.SecretEnvironmentProvider, error),
	loadConfig func(context.Context, string) (*config.DispatchConfig, error),
	once bool,
	failClosed func(error),
) error {
	revisions, err := newSchedulerRevisionStore(cfg, loadConfig)
	if err != nil {
		return err
	}
	initial, err := revisions.get(ctx, cfg.ConfigRevision)
	if err != nil {
		return err
	}
	catalog := initial.catalog
	catalogForRevision := func(loadContext context.Context, revision string) (*worker.Catalog, error) {
		configured, err := revisions.get(loadContext, revision)
		if err != nil {
			return nil, err
		}
		return configured.catalog, nil
	}
	policyForRevision := func(loadContext context.Context, revision string) (scheduler.RunSchedulerPolicy, error) {
		configured, err := revisions.get(loadContext, revision)
		if err != nil {
			return scheduler.RunSchedulerPolicy{}, err
		}
		return configured.policy, nil
	}
	client, err := dockerclient.New(dockerclient.WithHost(dockerHost))
	if err != nil {
		return fmt.Errorf("initialize Docker client: %w", err)
	}
	runtime, err := dockerruntime.NewRuntime(client, dockerruntime.Options{
		DeploymentID: deploymentID, DispatcherID: lease.InstanceID,
		Project: initial.project,
		ProjectConfigForRevision: func(configContext context.Context, revision string) (dockerruntime.ProjectConfig, error) {
			configured, err := revisions.get(configContext, revision)
			if err != nil {
				return dockerruntime.ProjectConfig{}, err
			}
			return configured.project, nil
		},
		OutputLimits: initial.outputLimits,
		OutputLimitsForRevision: func(configContext context.Context, revision string) (dockerruntime.OutputLimits, error) {
			configured, err := revisions.get(configContext, revision)
			if err != nil {
				return dockerruntime.OutputLimits{}, err
			}
			return configured.outputLimits, nil
		},
	})
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("initialize Run Docker runtime: %w", err)
	}
	defer runtime.Close()
	if secrets == nil || readinessSecrets == nil || readinessSecretForRevision == nil {
		return fmt.Errorf("Revision-scoped Secret Providers are required")
	}
	readiness, err := newProductionReadiness(catalog, readinessSecrets, initial.readinessTimeout)
	if err != nil {
		return err
	}
	readinessByRevision := map[string]*schedulerRevisionReadiness{
		cfg.ConfigRevision: {provider: readiness, mode: initial.readinessMode},
	}
	readinessForRevision := func(readinessContext context.Context, revision string) (*schedulerRevisionReadiness, error) {
		if existing := readinessByRevision[revision]; existing != nil {
			return existing, nil
		}
		configured, err := revisions.get(readinessContext, revision)
		if err != nil {
			return nil, err
		}
		revisionSecrets, err := readinessSecretForRevision(readinessContext, revision)
		if err != nil {
			return nil, err
		}
		provider, err := newProductionReadiness(
			configured.catalog, revisionSecrets, configured.readinessTimeout,
		)
		if err != nil {
			return nil, err
		}
		state := &schedulerRevisionReadiness{provider: provider, mode: configured.readinessMode}
		readinessByRevision[revision] = state
		return state, nil
	}
	prepareRevisionWorkers := func(prepareContext context.Context, revision string) (map[string]bool, error) {
		state, err := readinessForRevision(prepareContext, revision)
		if err != nil {
			return nil, err
		}
		if state.prepared && (allWorkersReady(state.ready) || time.Now().UTC().Before(state.retryAt)) {
			return state.ready, nil
		}
		state.ready, err = state.provider.prepareWorkers(prepareContext, state.mode)
		if err != nil {
			return nil, err
		}
		state.prepared = true
		state.retryAt = time.Now().UTC().Add(revisionReadinessRetryInterval)
		return state.ready, nil
	}
	prepareWorkerRevision := func(prepareContext context.Context, revision, workerName string) (bool, error) {
		state, err := readinessForRevision(prepareContext, revision)
		if err != nil {
			return false, err
		}
		if state.mode != "startup_and_task" {
			return true, nil
		}
		ready, err := state.provider.prepareWorker(prepareContext, state.mode, workerName)
		if err != nil {
			return false, err
		}
		return ready, nil
	}
	policy := initial.policy
	executionControl, ok := lifecycle.(rundomain.ExecutionControlStore)
	if !ok {
		return fmt.Errorf("Run lifecycle control does not support execution persistence and continuation fencing")
	}
	cleanupControl, ok := lifecycle.(scheduler.ProjectCleanupControl)
	if !ok {
		return fmt.Errorf("Run lifecycle control does not support Project cleanup jobs")
	}
	inventoryControl, ok := planning.(scheduler.ProjectInventoryControl)
	if !ok {
		return fmt.Errorf("Run planning control does not support Project runtime inventory")
	}
	executions, err := NewRunExecutionFactory(catalog, runtime, secrets, executionControl)
	if err != nil {
		return err
	}
	runScheduler, err := scheduler.NewRunScheduler(scheduler.RunSchedulerControls{
		Planning: planning, Recovery: recovery, Lifecycle: lifecycle,
		Cleanup: cleanupControl, Inventory: inventoryControl,
	}, scheduler.RunSchedulerConfig{
		ConfigRevision: cfg.ConfigRevision,
		Policy:         policy, PolicyForRevision: policyForRevision,
		Catalog: catalog, CatalogForRevision: catalogForRevision,
		Resources: runtime, Executions: executions,
		Lease: lease, DeploymentID: deploymentID, Once: once, FailClosed: failClosed,
		PrepareRevisionWorkers: prepareRevisionWorkers,
		PrepareRevisionWorker:  prepareWorkerRevision,
	})
	if err != nil {
		return err
	}
	return runScheduler.Run(ctx)
}
