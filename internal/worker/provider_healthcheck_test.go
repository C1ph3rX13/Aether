package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestHTTPProviderHealthCheckerBuildsAdapterRequests(t *testing.T) {
	tests := []struct {
		name             string
		adapter          string
		path             string
		modelVariable    string
		baseURLVariable  string
		secretVariable   string
		providerAPI      string
		anthropicVersion bool
	}{
		{
			name: "Claude", adapter: "claudecode", path: "/v1/messages",
			modelVariable: "ANTHROPIC_MODEL", baseURLVariable: "ANTHROPIC_BASE_URL",
			secretVariable: "ANTHROPIC_AUTH_TOKEN", anthropicVersion: true,
		},
		{
			name: "Codex", adapter: "codex", path: "/responses",
			modelVariable: "CODEX_MODEL", baseURLVariable: "CODEX_BASE_URL",
			secretVariable: "OPENAI_API_KEY",
		},
		{
			name: "Pi Anthropic", adapter: "pi", path: "/v1/messages",
			modelVariable: "PI_MODEL", baseURLVariable: "PI_BASE_URL",
			secretVariable: "PI_API_KEY", providerAPI: "anthropic-messages", anthropicVersion: true,
		},
		{
			name: "Pi Responses", adapter: "pi", path: "/responses",
			modelVariable: "PI_MODEL", baseURLVariable: "PI_BASE_URL",
			secretVariable: "PI_API_KEY", providerAPI: "openai-responses",
		},
		{
			name: "Pi Completions", adapter: "pi", path: "/chat/completions",
			modelVariable: "PI_MODEL", baseURLVariable: "PI_BASE_URL",
			secretVariable: "PI_API_KEY", providerAPI: "openai-completions",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var received map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != test.path {
					t.Errorf("request = %s %s, want POST %s", request.Method, request.URL.Path, test.path)
				}
				if got := request.Header.Get("Authorization"); got != "Bearer provider-token" {
					t.Errorf("Authorization = %q", got)
				}
				if got := request.Header.Get("anthropic-version"); (got != "") != test.anthropicVersion {
					t.Errorf("anthropic-version = %q", got)
				}
				if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
					t.Errorf("decode request: %v", err)
				}
				response.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			checker := providerCheckerForTest(t, "provider-token", time.Second)
			environment := map[string]string{
				test.modelVariable: "test-model", test.baseURLVariable: server.URL + "/",
			}
			if test.providerAPI != "" {
				environment["PI_PROVIDER_API"] = test.providerAPI
			}
			configured := CatalogWorker{
				Name: "worker", Adapter: test.adapter, Environment: environment,
				SecretReferences: []rundomain.SecretReference{{
					EnvironmentVariable: test.secretVariable, Provider: "configuration", Key: "PROVIDER_TOKEN",
				}},
			}
			if err := checker.CheckProvider(t.Context(), configured); err != nil {
				t.Fatal(err)
			}
			if received["model"] != "test-model" {
				t.Fatalf("request model = %#v", received["model"])
			}
			if test.name == "Codex" {
				if received["stream"] != false {
					t.Errorf("Codex stream = %#v", received["stream"])
				}
				input, ok := received["input"].([]any)
				if !ok || len(input) != 1 {
					t.Errorf("Codex input = %#v", received["input"])
				}
			}
		})
	}
}

func TestHTTPProviderHealthCheckerBoundsAndRedactsFailure(t *testing.T) {
	secret := "provider-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(secret + strings.Repeat("x", providerFailurePreviewLimit*2)))
	}))
	defer server.Close()
	checker := providerCheckerForTest(t, secret, time.Second)
	err := checker.CheckProvider(t.Context(), CatalogWorker{
		Name: "codex", Adapter: "codex",
		Environment: map[string]string{"CODEX_BASE_URL": server.URL, "CODEX_MODEL": "test-model"},
		SecretReferences: []rundomain.SecretReference{{
			EnvironmentVariable: "OPENAI_API_KEY", Provider: "configuration", Key: "PROVIDER_TOKEN",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("healthcheck error = %v", err)
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("healthcheck error was not redacted: %v", err)
	}
	if len(err.Error()) > providerFailurePreviewLimit+100 {
		t.Fatalf("healthcheck error is not bounded: %d bytes", len(err.Error()))
	}
}

func TestHTTPProviderHealthCheckerAppliesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	checker := providerCheckerForTest(t, "token", 20*time.Millisecond)
	err := checker.CheckProvider(context.Background(), CatalogWorker{
		Name: "codex", Adapter: "codex",
		Environment: map[string]string{"CODEX_BASE_URL": server.URL, "CODEX_MODEL": "test-model"},
		SecretReferences: []rundomain.SecretReference{{
			EnvironmentVariable: "OPENAI_API_KEY", Provider: "configuration", Key: "PROVIDER_TOKEN",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestHTTPProviderHealthCheckerChecksMockInProcess(t *testing.T) {
	checker := providerCheckerForTest(t, "unused", time.Second)
	for name, configured := range map[string]string{
		"healthy": `{"delay":[0,0],"outcomes":{"ok":1}}`,
		"failed":  `{"delay":[0,0],"outcomes":{"fail":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := checker.CheckProvider(t.Context(), CatalogWorker{
				Name: name, Adapter: "mock", Environment: map[string]string{"MOCK_HEALTHCHECK": configured},
			})
			if name == "healthy" && err != nil {
				t.Fatal(err)
			}
			if name == "failed" && err == nil {
				t.Fatal("failed Mock Provider was marked ready")
			}
		})
	}
}

type providerTestSecret struct {
	value string
}

func (provider providerTestSecret) ResolveSecret(_ context.Context, key string) ([]byte, error) {
	if key != "PROVIDER_TOKEN" || provider.value == "" {
		return nil, fmt.Errorf("test Secret %q is not configured", key)
	}
	return []byte(provider.value), nil
}

func providerCheckerForTest(t *testing.T, secret string, timeout time.Duration) *HTTPProviderHealthChecker {
	t.Helper()
	secrets, err := rundomain.NewSecretResolver(map[string]rundomain.SecretProvider{
		"configuration": providerTestSecret{value: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	checker, err := NewHTTPProviderHealthChecker(http.DefaultClient, secrets, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}
