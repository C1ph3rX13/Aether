package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const maximumSecretBytes = 64 * 1024

type SecretProvider interface {
	ResolveSecret(context.Context, string) ([]byte, error)
}

type SecretEnvironmentProvider interface {
	ResolveSecrets(context.Context, []SecretReference) (*ResolvedSecrets, error)
}

type WorkerScopedSecretEnvironmentProvider interface {
	SecretEnvironmentProvider
	ResolveWorkerSecrets(context.Context, string) (*ResolvedSecrets, error)
}

type RunScopedSecretEnvironmentProvider interface {
	SecretEnvironmentProvider
	ResolveRunSecrets(context.Context, StartedRun, []SecretReference) (*ResolvedSecrets, error)
}

type RunSecretRequest struct {
	RunID                  RunID        `json:"run_id"`
	FencingToken           FencingToken `json:"fencing_token"`
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
}

func (request RunSecretRequest) Validate() error {
	if request.RunID == "" || request.FencingToken <= 0 ||
		!bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "Run Secret authorization is invalid")
	}
	return nil
}

type SecretResolver struct{ providers map[string]SecretProvider }

func NewSecretResolver(providers map[string]SecretProvider) (*SecretResolver, error) {
	if len(providers) == 0 {
		return nil, newError(ErrorInvalidArgument, "at least one Secret Provider is required")
	}
	cloned := make(map[string]SecretProvider, len(providers))
	for name, provider := range providers {
		if name == "" || len(name) > 64 || provider == nil {
			return nil, newError(ErrorInvalidArgument, "Secret Provider registration is invalid")
		}
		cloned[name] = provider
	}
	return &SecretResolver{providers: cloned}, nil
}

func (resolver *SecretResolver) ResolveSecrets(ctx context.Context, references []SecretReference) (*ResolvedSecrets, error) {
	if resolver == nil {
		return nil, newError(ErrorInvalidArgument, "Secret Resolver is required")
	}
	result := &ResolvedSecrets{values: make(map[string][]byte, len(references))}
	for _, reference := range references {
		if !bounded(reference.EnvironmentVariable, 128) || !bounded(reference.Provider, 64) || !bounded(reference.Key, 512) {
			result.Close()
			return nil, newError(ErrorInvalidArgument, "Secret Reference is invalid")
		}
		if _, duplicate := result.values[reference.EnvironmentVariable]; duplicate {
			result.Close()
			return nil, newError(ErrorInvalidArgument, "Secret environment variables must be unique")
		}
		provider := resolver.providers[reference.Provider]
		if provider == nil {
			result.Close()
			return nil, fmt.Errorf("Secret Provider %q is unavailable", reference.Provider)
		}
		value, err := provider.ResolveSecret(ctx, reference.Key)
		if err != nil {
			result.Close()
			return nil, fmt.Errorf("resolve Secret Reference %s with %s: %w", reference.EnvironmentVariable, reference.Provider, err)
		}
		if len(value) == 0 || len(value) > maximumSecretBytes || bytes.IndexByte(value, 0) >= 0 {
			clear(value)
			result.Close()
			return nil, fmt.Errorf("Secret Reference %s resolved to an invalid value", reference.EnvironmentVariable)
		}
		result.values[reference.EnvironmentVariable] = append([]byte(nil), value...)
		clear(value)
	}
	return result, nil
}

func NewResolvedSecrets(values map[string][]byte) *ResolvedSecrets {
	copied := make(map[string][]byte, len(values))
	for key, value := range values {
		copied[key] = append([]byte(nil), value...)
	}
	return &ResolvedSecrets{values: copied}
}

type ResolvedSecrets struct{ values map[string][]byte }

func (secrets *ResolvedSecrets) Environment() map[string]string {
	if secrets == nil {
		return nil
	}
	environment := make(map[string]string, len(secrets.values))
	for key, value := range secrets.values {
		environment[key] = string(value)
	}
	return environment
}

func (secrets *ResolvedSecrets) Redact(message string) string {
	if secrets == nil {
		return message
	}
	for _, value := range secrets.values {
		message = strings.ReplaceAll(message, string(value), "[REDACTED]")
	}
	return message
}

func (secrets *ResolvedSecrets) Contains(content []byte) bool {
	if secrets == nil {
		return false
	}
	for _, value := range secrets.values {
		if bytes.Contains(content, value) {
			return true
		}
		encoded, err := json.Marshal(string(value))
		if err == nil && len(encoded) >= 2 && bytes.Contains(content, encoded[1:len(encoded)-1]) {
			return true
		}
	}
	return false
}

func (secrets *ResolvedSecrets) Close() {
	if secrets == nil {
		return
	}
	for key, value := range secrets.values {
		clear(value)
		delete(secrets.values, key)
	}
}

var _ SecretEnvironmentProvider = (*SecretResolver)(nil)
