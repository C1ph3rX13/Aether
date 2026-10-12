package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
	"github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/spf13/cobra"
)

type runOptions struct {
	deploymentPath string
}

func newRunCommand() *cobra.Command {
	options := runOptions{}
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the trusted local Web application and Dispatcher",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return options.run(cmd.Context())
		},
	}
	command.Flags().StringVarP(&options.deploymentPath, "deployment", "d", "", "Bootstrap Manifest TOML path")
	if err := command.MarkFlagRequired("deployment"); err != nil {
		panic(err)
	}
	return command
}

func (options runOptions) run(parent context.Context) error {
	manifest, err := loadBootstrapManifest(options.deploymentPath)
	if err != nil {
		return err
	}
	application, err := controlplane.NewControlPlaneHost(
		manifest.Server.DatabasePath,
		webFS(),
		controlplane.ControlPlaneHostOptions{
			OperatorName: manifest.Operator.Name,
			DeploymentID: manifest.Deployment.ID,
		},
	)
	if err != nil {
		return err
	}
	defer func() {
		if shutdownErr := application.Shutdown(); shutdownErr != nil {
			fmt.Fprintf(os.Stderr, "aether run: Control Plane shutdown warning: %v\n", shutdownErr)
		}
	}()

	address := net.JoinHostPort(manifest.Server.ListenHost, strconv.Itoa(manifest.Server.ListenPort))
	fmt.Fprintf(os.Stderr, "aether run: trusted local mode listening on %s (deployment=%s)\n", address, manifest.Deployment.ID)
	server := newHTTPServer(address, application.Handler())
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dispatcherErr := make(chan error, 1)
	go func() {
		dispatcherErr <- runTrustedDispatcher(ctx, application, manifest)
		if ctx.Err() == nil {
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stop()
		<-dispatcherErr
		return fmt.Errorf("server error: %w", err)
	}
	if err := <-dispatcherErr; err != nil {
		return fmt.Errorf("dispatcher error: %w", err)
	}
	return nil
}

func runTrustedDispatcher(ctx context.Context, application *controlplane.ControlPlaneHost, manifest config.BootstrapManifest) error {
	reportedDormant := false
	for {
		active, err := application.HasActiveConfiguration(ctx)
		if err != nil {
			return err
		}
		if !active {
			if !reportedDormant {
				fmt.Fprintln(os.Stderr, "aether run: Dispatcher dormant (configuration_required)")
				reportedDormant = true
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return nil
			case <-timer.C:
			}
			continue
		}
		break
	}

	control, err := controlplane.NewInProcessRunControl(application.ControlPlane())
	if err != nil {
		return err
	}
	defer control.Close()
	leaseRunner, err := scheduler.NewDispatcherLeaseRunner(control, scheduler.DispatcherLeaseRunnerConfig{
		InstanceID: "trusted-local", LeaseTTL: dispatcherLeaseTTL,
		RenewInterval: dispatcherRenewInterval, CallTimeout: dispatcherCallTimeout,
	})
	if err != nil {
		return err
	}
	err = leaseRunner.RunFailClosed(ctx, func(workContext context.Context, lease run.DispatcherLease, failClosed func(error)) error {
		cfg, err := application.ActiveExecutionConfig(workContext)
		if err != nil {
			return fmt.Errorf("load active Dynamic Configuration: %w", err)
		}
		secrets, err := application.RunSecretProvider()
		if err != nil {
			return err
		}
		readinessSecrets, err := application.RevisionSecretProvider(cfg.ConfigRevision)
		if err != nil {
			return err
		}
		return runProductionScheduler(
			workContext, control, control, control, cfg, lease,
			manifest.Deployment.ID, manifest.Dispatcher.DockerHost, secrets, readinessSecrets,
			func(secretContext context.Context, revision string) (run.SecretEnvironmentProvider, error) {
				return application.RevisionSecretProvider(revision)
			}, application.ExecutionConfig, false, failClosed,
		)
	})
	if ctx.Err() != nil {
		return nil
	}
	return err
}
