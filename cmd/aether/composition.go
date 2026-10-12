package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
)

type deploymentReadiness struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func deploymentReadinessURL(controlPlaneURL string) string {
	return controlPlaneURL + "/ready"
}

func readDeploymentReadiness(ctx context.Context, controlPlaneURL string) (deploymentReadiness, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, deploymentReadinessURL(controlPlaneURL), nil)
	if err != nil {
		return deploymentReadiness{}, fmt.Errorf("create deployment readiness request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return deploymentReadiness{}, fmt.Errorf("query deployment readiness: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil {
		return deploymentReadiness{}, fmt.Errorf("read deployment readiness: %w", err)
	}
	if len(body) > 64*1024 {
		return deploymentReadiness{}, fmt.Errorf("deployment readiness response is too large")
	}
	var readiness deploymentReadiness
	if err := json.Unmarshal(body, &readiness); err != nil {
		return deploymentReadiness{}, fmt.Errorf("decode deployment readiness: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return deploymentReadiness{}, fmt.Errorf("deployment readiness returned HTTP %d", response.StatusCode)
	}
	return readiness, nil
}

func loadBootstrapManifest(path string) (config.BootstrapManifest, error) {
	return config.LoadBootstrap(path)
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1024 * 1024,
	}
}
