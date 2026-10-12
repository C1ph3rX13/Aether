package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

const providerFailurePreviewLimit = 200

// HTTPProviderHealthChecker verifies Provider/API availability in the
// dispatcher process. It never invokes an Agent Adapter or interprets Agent
// output.
type HTTPProviderHealthChecker struct {
	client  *http.Client
	secrets rundomain.SecretEnvironmentProvider
	timeout time.Duration
}

func NewHTTPProviderHealthChecker(
	client *http.Client,
	secrets rundomain.SecretEnvironmentProvider,
	timeout time.Duration,
) (*HTTPProviderHealthChecker, error) {
	if client == nil || secrets == nil || timeout <= 0 || timeout > 5*time.Minute {
		return nil, fmt.Errorf("Provider healthchecker configuration is invalid")
	}
	return &HTTPProviderHealthChecker{client: client, secrets: secrets, timeout: timeout}, nil
}

func (checker *HTTPProviderHealthChecker) CheckProvider(ctx context.Context, worker CatalogWorker) error {
	if checker == nil {
		return fmt.Errorf("Provider healthchecker is required")
	}
	definition, err := LookupAdapterDefinition(worker.Adapter)
	if err != nil {
		return fmt.Errorf("Worker %s uses unsupported Provider Adapter %q", worker.Name, worker.Adapter)
	}
	if definition.providerMode == providerHealthInProcess {
		return definition.checkProvider(worker)
	}
	if definition.providerMode != providerHealthHTTP {
		return fmt.Errorf("Worker %s uses unsupported Provider Adapter %q", worker.Name, worker.Adapter)
	}

	var resolved *rundomain.ResolvedSecrets
	if scoped, ok := checker.secrets.(rundomain.WorkerScopedSecretEnvironmentProvider); ok {
		resolved, err = scoped.ResolveWorkerSecrets(ctx, worker.Name)
	} else {
		resolved, err = checker.secrets.ResolveSecrets(ctx, worker.SecretReferences)
	}
	if err != nil {
		return err
	}
	environment := resolved.Environment()
	defer func() {
		for key := range environment {
			environment[key] = ""
			delete(environment, key)
		}
		resolved.Close()
	}()
	for key, value := range worker.Environment {
		if _, secret := environment[key]; secret {
			return fmt.Errorf("Worker %s Provider environment overlaps a Secret Reference", worker.Name)
		}
		environment[key] = value
	}

	requestContext, cancel := context.WithTimeout(ctx, checker.timeout)
	defer cancel()
	request, err := providerHealthcheckRequest(requestContext, worker, environment)
	if err != nil {
		return err
	}
	response, err := checker.client.Do(request)
	if err != nil {
		return fmt.Errorf("Provider request failed: %s", resolved.Redact(err.Error()))
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, providerFailurePreviewLimit+1))
	if readErr != nil {
		return fmt.Errorf("read Provider response: %w", readErr)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	detail := compactProviderDetail(string(body))
	if detail == "" {
		return fmt.Errorf("Provider returned HTTP %d", response.StatusCode)
	}
	return fmt.Errorf("Provider returned HTTP %d: %s", response.StatusCode, resolved.Redact(detail))
}

func providerHealthcheckRequest(
	ctx context.Context,
	worker CatalogWorker,
	environment map[string]string,
) (*http.Request, error) {
	definition, err := LookupAdapterDefinition(worker.Adapter)
	if err != nil || definition.providerRequest == nil {
		return nil, fmt.Errorf("Worker %s uses unsupported Provider Adapter %q", worker.Name, worker.Adapter)
	}
	provider := definition.providerRequest(environment)
	if provider.endpoint == "" || provider.token == "" {
		return nil, fmt.Errorf("Worker %s Provider healthcheck configuration is incomplete", worker.Name)
	}
	payload, err := json.Marshal(provider.body)
	if err != nil {
		return nil, fmt.Errorf("encode Provider healthcheck request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build Provider healthcheck request: %w", err)
	}
	request.Header = provider.headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+provider.token)
	return request, nil
}

func checkMockProvider(worker CatalogWorker) error {
	behavior, err := parseMockBehavior(worker.Environment)
	if err != nil {
		return fmt.Errorf("Worker %s Mock Provider configuration: %w", worker.Name, err)
	}
	okWeight := behavior.Healthcheck.Outcomes["ok"]
	if okWeight >= 1 || okWeight > 0 && rand.Float64() < okWeight {
		return nil
	}
	return fmt.Errorf("Mock Provider healthcheck failed")
}

func compactProviderDetail(detail string) string {
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) <= providerFailurePreviewLimit {
		return detail
	}
	return detail[:providerFailurePreviewLimit] + "..."
}

var _ ProviderHealthChecker = (*HTTPProviderHealthChecker)(nil)
