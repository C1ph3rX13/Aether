package main

import (
	"context"
	"fmt"
	"os"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

type remoteRunSecrets struct {
	control *controlplane.HTTPRunControl
}

func (provider remoteRunSecrets) ResolveSecrets(
	context.Context, []rundomain.SecretReference,
) (*rundomain.ResolvedSecrets, error) {
	return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Secret resolution requires an authorized Run"}
}

func (provider remoteRunSecrets) ResolveRunSecrets(
	ctx context.Context, started rundomain.StartedRun, references []rundomain.SecretReference,
) (*rundomain.ResolvedSecrets, error) {
	return provider.control.ResolveRunSecrets(ctx, started, references)
}

type remoteRevisionSecrets struct {
	control  *controlplane.HTTPRunControl
	revision string
	lease    rundomain.DispatcherLease
}

func (provider remoteRevisionSecrets) ResolveSecrets(
	context.Context, []rundomain.SecretReference,
) (*rundomain.ResolvedSecrets, error) {
	return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Worker Secret resolution requires an authorized Worker"}
}

func (provider remoteRevisionSecrets) ResolveWorkerSecrets(
	ctx context.Context, workerName string,
) (*rundomain.ResolvedSecrets, error) {
	return provider.control.ResolveRevisionSecrets(ctx, provider.revision, workerName, provider.lease)
}

func runSeparatedDispatcher(
	parent context.Context,
	manifest config.BootstrapManifest,
	once bool,
	healthcheckOnly bool,
) error {
	control, err := controlplane.NewHTTPRunControl(manifest.Dispatcher.ControlPlaneURL)
	if err != nil {
		return err
	}
	defer control.Close()

	ctx := parent
	configured, err := waitForActiveConfiguration(ctx, manifest.Dispatcher.ControlPlaneURL, once || healthcheckOnly)
	if err != nil || !configured {
		return err
	}

	leaseRunner, err := scheduler.NewDispatcherLeaseRunner(control, scheduler.DispatcherLeaseRunnerConfig{
		InstanceID: dispatcherInstanceID(manifest.Deployment.ID),
		LeaseTTL:   dispatcherLeaseTTL, RenewInterval: dispatcherRenewInterval, CallTimeout: dispatcherCallTimeout,
	})
	if err != nil {
		return err
	}
	err = leaseRunner.RunFailClosed(ctx, func(workContext context.Context, lease rundomain.DispatcherLease, failClosed func(error)) error {
		cfg, err := control.ReadActiveExecutionConfig(workContext)
		if err != nil {
			return fmt.Errorf("load active Dynamic Configuration: %w", err)
		}
		readinessSecrets := remoteRevisionSecrets{control: control, revision: cfg.ConfigRevision, lease: lease}
		if healthcheckOnly {
			return runProductionStartupHealthchecks(workContext, cfg, readinessSecrets)
		}
		return runProductionScheduler(
			workContext, control, control, control, cfg, lease,
			manifest.Deployment.ID, manifest.Dispatcher.DockerHost,
			remoteRunSecrets{control: control}, readinessSecrets,
			func(secretContext context.Context, revision string) (rundomain.SecretEnvironmentProvider, error) {
				return remoteRevisionSecrets{control: control, revision: revision, lease: lease}, nil
			},
			control.ReadExecutionConfig, once, failClosed,
		)
	})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func dispatcherInstanceID(deploymentID string) string {
	return fmt.Sprintf("%s-dispatcher-%d", deploymentID, os.Getpid())
}
