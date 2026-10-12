package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/C1ph3rX13/Aether/internal/controlplane"
	"github.com/spf13/cobra"
)

type serveOptions struct {
	deploymentPath string
}

func newServeCommand() *cobra.Command {
	options := serveOptions{}
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the Aether Web application and Control Plane",
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

func (options serveOptions) run(parent context.Context) error {
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
			slog.Warn("Control Plane shutdown did not complete cleanly", "error", shutdownErr)
		}
	}()

	address := net.JoinHostPort(manifest.Server.ListenHost, strconv.Itoa(manifest.Server.ListenPort))
	fmt.Fprintf(os.Stderr, "aether serve listening on %s (deployment=%s)\n", address, manifest.Deployment.ID)
	server := newHTTPServer(address, application.Handler())
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("HTTP server shutdown did not complete cleanly", "error", err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
