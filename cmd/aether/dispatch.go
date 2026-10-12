package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type dispatchOptions struct {
	deploymentPath  string
	runOnce         bool
	healthcheckOnly bool
}

func newDispatchCommand() *cobra.Command {
	options := dispatchOptions{}
	command := &cobra.Command{
		Use:   "dispatch",
		Short: "Start the Aether Dispatcher",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return options.run(cmd.Context())
		},
	}
	command.Flags().StringVarP(&options.deploymentPath, "deployment", "d", "", "Bootstrap Manifest TOML path")
	command.Flags().BoolVar(&options.runOnce, "once", false, "run one scheduling iteration and exit")
	command.Flags().BoolVar(&options.healthcheckOnly, "startup-healthcheck-only", false, "run startup Worker healthchecks and exit")
	if err := command.MarkFlagRequired("deployment"); err != nil {
		panic(err)
	}
	return command
}

func (options dispatchOptions) run(parent context.Context) error {
	manifest, err := loadBootstrapManifest(options.deploymentPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runSeparatedDispatcher(ctx, manifest, options.runOnce, options.healthcheckOnly)
}

func waitForActiveConfiguration(ctx context.Context, controlPlaneURL string, failIfMissing bool) (bool, error) {
	reportedDormant := false
	for {
		readiness, err := readDeploymentReadiness(ctx, controlPlaneURL)
		if err != nil {
			if ctx.Err() != nil && !failIfMissing {
				return false, nil
			}
			return false, err
		}
		if readiness.Reason == "configuration_required" {
			if failIfMissing {
				return false, fmt.Errorf("configuration_required: save configuration before starting the Dispatcher")
			}
			if !reportedDormant {
				fmt.Fprintln(os.Stderr, "aether dispatch: dormant (configuration_required)")
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
				return false, nil
			case <-timer.C:
			}
			continue
		}
		if readiness.Status == "ready" || readiness.Reason == "dispatcher_unavailable" {
			return true, nil
		}
		return false, fmt.Errorf("deployment readiness is invalid: status=%q reason=%q", readiness.Status, readiness.Reason)
	}
}
