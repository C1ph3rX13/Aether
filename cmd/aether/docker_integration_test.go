package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	dockerruntime "github.com/C1ph3rX13/Aether/internal/docker"
	"github.com/moby/moby/client"
)

const aetherWorkerDockerImage = "ghcr.io/c1ph3rx13/aether-worker:latest"

func newDockerIntegrationRuntime(options dockerruntime.Options) (*dockerruntime.Runtime, error) {
	dockerAPI, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	runtime, err := dockerruntime.NewRuntime(dockerAPI, options)
	if err != nil {
		_ = dockerAPI.Close()
		return nil, err
	}
	return runtime, nil
}

type lookupSecretProvider struct {
	lookup func(string) (string, bool)
}

func (provider lookupSecretProvider) ResolveSecret(_ context.Context, key string) ([]byte, error) {
	value, ok := provider.lookup(key)
	if !ok || value == "" {
		return nil, fmt.Errorf("test Secret %q is not configured", key)
	}
	return []byte(value), nil
}

func dockerIntegrationImage(t *testing.T) string {
	t.Helper()
	if !dockerIntegrationEnabled {
		t.Skip("real Docker integration skipped: run with -tags dockerintegration")
	}

	dockerAPI, err := client.New(client.FromEnv)
	if err != nil {
		if os.Getenv("AETHER_REQUIRE_DOCKER_TESTS") == "1" {
			t.Fatalf("initialize Docker client: %v", err)
		}
		t.Skipf("real Docker integration skipped: initialize Docker client: %v", err)
	}
	defer dockerAPI.Close()

	image := os.Getenv("AETHER_DOCKER_TEST_IMAGE")
	if image == "" {
		image = aetherWorkerDockerImage
	}
	if _, err := dockerAPI.ImageInspect(t.Context(), image); err != nil {
		if os.Getenv("AETHER_REQUIRE_DOCKER_TESTS") == "1" {
			t.Fatalf("image %q is not provisioned in Docker: %v", image, err)
		}
		t.Skipf("real Docker integration skipped: image %q is not provisioned in Docker: %v", image, err)
	}
	return image
}
