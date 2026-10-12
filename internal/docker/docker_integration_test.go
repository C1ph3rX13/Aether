package docker

import (
	"os"
	"testing"

	"github.com/moby/moby/client"
)

const aetherWorkerDockerImage = "ghcr.io/c1ph3rx13/aether-worker:latest"

func dockerIntegrationImage(t *testing.T) string {
	t.Helper()
	if !dockerIntegrationEnabled {
		t.Skip("real Docker integration skipped: run with -tags dockerintegration")
	}

	dockerClient, err := client.New(client.FromEnv)
	if err != nil {
		if os.Getenv("AETHER_REQUIRE_DOCKER_TESTS") == "1" {
			t.Fatalf("required real Docker integration: initialize Docker client: %v", err)
		}
		t.Skipf("real Docker integration skipped: initialize Docker client: %v", err)
	}
	defer dockerClient.Close()

	image := os.Getenv("AETHER_DOCKER_TEST_IMAGE")
	if image == "" {
		image = aetherWorkerDockerImage
	}
	if _, err := dockerClient.ImageInspect(t.Context(), image); err != nil {
		if os.Getenv("AETHER_REQUIRE_DOCKER_TESTS") == "1" {
			t.Fatalf("required real Docker integration: image %q is not provisioned in Docker: %v", image, err)
		}
		t.Skipf("real Docker integration skipped: image %q is not provisioned in Docker: %v", image, err)
	}
	return image
}
