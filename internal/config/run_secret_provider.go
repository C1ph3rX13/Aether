package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// RunSecretProvider is the Server-owned Secret Store adapter used after Run
// Admission. Every resolution rechecks the durable Run, Claim, Project
// binding, and immutable Worker Secret Version; it keeps no cross-Run cache.
type RunSecretProvider struct{ db *database.DB }

func NewRunSecretProvider(db *database.DB) (*RunSecretProvider, error) {
	if db == nil {
		return nil, fmt.Errorf("Secret Store database is required")
	}
	return &RunSecretProvider{db: db}, nil
}

func (provider *RunSecretProvider) ResolveSecrets(context.Context, []rundomain.SecretReference) (*rundomain.ResolvedSecrets, error) {
	return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Secret resolution requires an authorized Run"}
}

func (provider *RunSecretProvider) ResolveRunSecrets(
	ctx context.Context,
	started rundomain.StartedRun,
	references []rundomain.SecretReference,
) (*rundomain.ResolvedSecrets, error) {
	return provider.resolveRunSecrets(ctx, rundomain.RunSecretRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}, references, false, started.Run.ProjectID)
}

// ResolveAuthorizedRunSecrets derives the Worker Secret References from the
// immutable Execution Spec stored for the Run. Callers submit identity only.
func (provider *RunSecretProvider) ResolveAuthorizedRunSecrets(
	ctx context.Context, request rundomain.RunSecretRequest,
) (*rundomain.ResolvedSecrets, error) {
	return provider.resolveRunSecrets(ctx, request, nil, true, "")
}

func (provider *RunSecretProvider) resolveRunSecrets(
	ctx context.Context,
	request rundomain.RunSecretRequest,
	references []rundomain.SecretReference,
	deriveReferences bool,
	expectedProjectID string,
) (*rundomain.ResolvedSecrets, error) {
	if provider == nil || provider.db == nil {
		return nil, fmt.Errorf("Secret Store is unavailable")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	conn, err := provider.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var projectID, dispatcherID, specJSON, projectConfig string
	var claimDispatcherID sql.NullString
	var fencingToken, dispatcherFencingToken int64
	var claimFencingToken, expires sql.NullInt64
	var status string
	now := time.Now().UTC().UnixNano()
	err = conn.QueryRowContext(ctx, `
		SELECT r.project_id, r.status, r.fencing_token, r.dispatcher_id,
			r.dispatcher_fencing_token, r.execution_spec_json, p.config_revision,
			c.fencing_token, c.dispatcher_id, c.lease_expires_unix_nano
		FROM execution_runs r
		JOIN projects p ON p.id = r.project_id
		JOIN dispatcher_lease lease ON lease.lease_name = 'global'
			AND lease.instance_id = r.dispatcher_id
			AND lease.fencing_token = r.dispatcher_fencing_token
			AND lease.lease_expires_unix_nano > ?
		LEFT JOIN execution_claims c ON c.run_id = r.id
		WHERE r.id = ?`, now, request.RunID,
	).Scan(&projectID, &status, &fencingToken, &dispatcherID, &dispatcherFencingToken, &specJSON, &projectConfig, &claimFencingToken, &claimDispatcherID, &expires)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run is not authorized for Secret resolution"}
		}
		return nil, err
	}
	if status != string(rundomain.StatusActive) || projectID == "" ||
		(expectedProjectID != "" && projectID != expectedProjectID) ||
		fencingToken != int64(request.FencingToken) || dispatcherID != request.DispatcherID ||
		dispatcherFencingToken != int64(request.DispatcherFencingToken) ||
		!claimFencingToken.Valid || claimFencingToken.Int64 != fencingToken ||
		!claimDispatcherID.Valid || claimDispatcherID.String != dispatcherID ||
		!expires.Valid || expires.Int64 <= now {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run ownership no longer authorizes Secret resolution"}
	}
	var storedSpec rundomain.ExecutionSpecSnapshot
	if err := json.Unmarshal([]byte(specJSON), &storedSpec); err != nil {
		return nil, fmt.Errorf("decode stored Execution Spec: %w", err)
	}
	if storedSpec.ConfigRevision == "" || storedSpec.ConfigRevision != projectConfig {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run Config Revision is not authorized"}
	}
	if deriveReferences {
		references = storedSpec.Worker.SecretReferences
	} else if !slices.Equal(storedSpec.Worker.SecretReferences, references) {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Secret references do not match the authorized Run"}
	}
	if len(references) == 0 {
		return rundomain.NewResolvedSecrets(nil), nil
	}
	draft, err := readPrivateDraft(ctx, conn, storedSpec.ConfigRevision)
	if err != nil {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run Config Revision is unavailable"}
	}
	configured := findWorkerByName(draft.Workers, storedSpec.Worker.Name)
	if configured.Name == "" {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run Worker is unavailable"}
	}
	values := make(map[string][]byte, len(references))
	for _, reference := range references {
		if configured.SecretVersions[reference.EnvironmentVariable] != reference.Key {
			clearSecretValues(values)
			return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run Secret reference is not authorized"}
		}
		var value []byte
		if err := conn.QueryRowContext(ctx, `
			SELECT secret_value FROM worker_secret_versions
			WHERE id = ? AND worker_name = ? AND variable_name = ?`,
			reference.Key, configured.Name, reference.EnvironmentVariable,
		).Scan(&value); err != nil {
			clearSecretValues(values)
			return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Run Secret Version is unavailable"}
		}
		values[reference.EnvironmentVariable] = value
	}
	resolved := rundomain.NewResolvedSecrets(values)
	clearSecretValues(values)
	return resolved, nil
}

// RevisionSecretProvider is the bounded pre-Admission Secret view used by a
// Worker readiness check. It accepts only Secret Version references that are
// recorded by the requested immutable Config Revision.
type RevisionSecretProvider struct {
	db       *database.DB
	revision string
}

func NewRevisionSecretProvider(db *database.DB, revision string) (*RevisionSecretProvider, error) {
	if db == nil || !validConfigurationRevisionID(revision) {
		return nil, fmt.Errorf("Config Revision Secret Provider identity is invalid")
	}
	return &RevisionSecretProvider{db: db, revision: revision}, nil
}

func (provider *RevisionSecretProvider) ResolveSecrets(context.Context, []rundomain.SecretReference) (*rundomain.ResolvedSecrets, error) {
	return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Worker Secret resolution requires an authorized Worker"}
}

func (provider *RevisionSecretProvider) ResolveWorkerSecrets(
	ctx context.Context, workerName string,
) (*rundomain.ResolvedSecrets, error) {
	if provider == nil || provider.db == nil || workerName == "" {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Worker readiness Secret authorization is invalid"}
	}
	conn, err := provider.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	draft, err := readPrivateDraft(ctx, conn, provider.revision)
	if err != nil {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Worker readiness Config Revision is unavailable"}
	}
	configured := findWorker(&draft, workerName)
	if configured == nil {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Worker readiness Worker is unavailable"}
	}
	variables := make([]string, 0, len(configured.SecretVersions))
	for variable := range configured.SecretVersions {
		variables = append(variables, variable)
	}
	slices.Sort(variables)
	values := make(map[string][]byte, len(variables))
	for _, variable := range variables {
		versionID := configured.SecretVersions[variable]
		var value []byte
		if err := conn.QueryRowContext(ctx, `
			SELECT secret_value FROM worker_secret_versions
			WHERE id = ? AND worker_name = ? AND variable_name = ?`,
			versionID, configured.Name, variable,
		).Scan(&value); err != nil {
			clearSecretValues(values)
			return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "Worker readiness Secret Version is unavailable"}
		}
		values[variable] = value
	}
	resolved := rundomain.NewResolvedSecrets(values)
	clearSecretValues(values)
	return resolved, nil
}

func clearSecretValues(values map[string][]byte) {
	for key, value := range values {
		clear(value)
		delete(values, key)
	}
}

var _ rundomain.RunScopedSecretEnvironmentProvider = (*RunSecretProvider)(nil)
var _ rundomain.WorkerScopedSecretEnvironmentProvider = (*RevisionSecretProvider)(nil)
