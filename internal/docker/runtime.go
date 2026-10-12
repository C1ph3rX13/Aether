package docker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/moby/moby/client"
)

const defaultDisposeGrace = 5 * time.Second

type Options struct {
	DeploymentID             string
	DispatcherID             string
	DisposeGrace             time.Duration
	OutputLimits             OutputLimits
	OutputLimitsForRevision  func(context.Context, string) (OutputLimits, error)
	Project                  ProjectConfig
	ProjectConfigForRevision func(context.Context, string) (ProjectConfig, error)
}

// ProjectConfig defines the unconstrained Docker container shared by all Runs
// in one Project. Resource and security restrictions are intentionally absent.
type ProjectConfig struct {
	Image           string
	NetworkMode     string
	CapAdd          []string
	CompletedAction string
}

type Runtime struct {
	client  *client.Client
	options Options
	context context.Context
	cancel  context.CancelFunc
	close   sync.Once
	closed  error
	workers sync.WaitGroup

	mu               sync.Mutex
	projects         map[string]*projectContainer
	projectRuns      map[rundomain.RunID]map[*execProcess]struct{}
	reconcileMu      sync.Mutex
	reconcilePending rundomain.ProjectRuntimeStatusLookup
	reconcileRunning bool
	reconcileClosed  bool
}

var _ rundomain.Runtime = (*Runtime)(nil)
var _ rundomain.TrackedRuntime = (*Runtime)(nil)

func NewRuntime(dockerClient *client.Client, options Options) (*Runtime, error) {
	if dockerClient == nil {
		return nil, fmt.Errorf("Docker client is required")
	}
	if err := rundomain.ValidateOwnership(rundomain.Ownership{
		DeploymentID: options.DeploymentID, DispatcherID: options.DispatcherID,
	}); err != nil {
		return nil, fmt.Errorf("Docker runtime ownership is invalid: %w", err)
	}
	if options.DisposeGrace == 0 {
		options.DisposeGrace = defaultDisposeGrace
	}
	if options.DisposeGrace < time.Second || options.DisposeGrace > time.Minute {
		return nil, fmt.Errorf("Docker runtime dispose grace is invalid")
	}
	if options.OutputLimits == (OutputLimits{}) {
		options.OutputLimits = DefaultOutputLimits()
	}
	if err := options.OutputLimits.Validate(); err != nil {
		return nil, err
	}
	if err := validateProjectConfig(options.Project); err != nil {
		return nil, err
	}
	options.Project.CapAdd = append([]string(nil), options.Project.CapAdd...)
	runtimeContext, cancel := context.WithCancel(context.Background())
	return &Runtime{
		client: dockerClient, options: options,
		context: runtimeContext, cancel: cancel,
		projects: make(map[string]*projectContainer), projectRuns: make(map[rundomain.RunID]map[*execProcess]struct{}),
	}, nil
}

func validateProjectConfig(config ProjectConfig) error {
	if strings.TrimSpace(config.Image) == "" || strings.TrimSpace(config.NetworkMode) == "" ||
		(config.CompletedAction != "stop" && config.CompletedAction != "remove") {
		return fmt.Errorf("Docker Project container configuration is invalid")
	}
	for _, capability := range config.CapAdd {
		if strings.TrimSpace(capability) == "" {
			return fmt.Errorf("Docker Project container capability is invalid")
		}
	}
	return nil
}

func (r *Runtime) projectConfig(ctx context.Context, revision string) (ProjectConfig, error) {
	config := r.options.Project
	var err error
	if r.options.ProjectConfigForRevision != nil && revision != "" {
		config, err = r.options.ProjectConfigForRevision(ctx, revision)
	}
	if err != nil {
		return ProjectConfig{}, err
	}
	if err := validateProjectConfig(config); err != nil {
		return ProjectConfig{}, err
	}
	config.CapAdd = append([]string(nil), config.CapAdd...)
	return config, nil
}

func (r *Runtime) outputLimits(ctx context.Context, revision string) (OutputLimits, error) {
	limits := r.options.OutputLimits
	var err error
	if r.options.OutputLimitsForRevision != nil && revision != "" {
		limits, err = r.options.OutputLimitsForRevision(ctx, revision)
	}
	if err != nil {
		return OutputLimits{}, err
	}
	if err := limits.Validate(); err != nil {
		return OutputLimits{}, err
	}
	return limits, nil
}

func (r *Runtime) Close() error {
	r.close.Do(func() {
		r.reconcileMu.Lock()
		r.reconcileClosed = true
		r.cancel()
		r.reconcileMu.Unlock()
		r.workers.Wait()
		r.closed = r.client.Close()
	})
	return r.closed
}

func (r *Runtime) Launch(ctx context.Context, spec rundomain.LaunchSpec) (rundomain.Process, error) {
	return r.launchProjectExec(ctx, spec, nil)
}

func (r *Runtime) LaunchTracked(
	ctx context.Context,
	spec rundomain.LaunchSpec,
	registration rundomain.ExecutionHandleRegistration,
) (rundomain.Process, error) {
	if registration == nil {
		return nil, fmt.Errorf("execution handle registration is required")
	}
	return r.launchProjectExec(ctx, spec, registration)
}
