package config

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

type ConfigurationRevision struct {
	ID               string                         `json:"id"`
	Configuration    DynamicDraft                   `json:"configuration"`
	SecretReferences []ConfigurationSecretReference `json:"secret_references"`
	CreatedAt        string                         `json:"created_at"`
	CreatedBy        string                         `json:"created_by"`
}

type ConfigurationSecretReference struct {
	WorkerName string `json:"worker_name"`
	Variable   string `json:"variable"`
	VersionID  string `json:"version_id"`
}

// publishConfiguration runs in the same transaction as the configuration save.
func (service *Service) publishConfiguration(ctx context.Context, tx database.DBTX, draft DynamicDraft) (string, error) {
	problems, err := validateRevisionSecretReferences(ctx, tx, draft)
	if err != nil {
		return "", err
	}
	if len(problems) > 0 {
		return "", validationError(problems)
	}
	if err := validateRevisionCatalog(draft); err != nil {
		return "", validationError([]ValidationProblem{{Path: "workers", Message: err.Error()}})
	}
	document, err := marshalDraft(normalizeRevisionConfiguration(draft))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	id := "sha256:" + hex.EncodeToString(digest[:])
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO configuration_revisions (id, document_json, created_at, created_by) VALUES (?, ?, ?, ?)`, id, string(document), createdAt, service.operatorName); err != nil {
		return "", fmt.Errorf("save Config Revision: %w", err)
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT document_json FROM configuration_revisions WHERE id = ?`, id).Scan(&stored); err != nil {
		return "", err
	}
	if stored != string(document) {
		return "", &DomainError{Code: ErrorInternal, Detail: "Config Revision hash collision"}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE configuration_state SET active_revision = ? WHERE singleton = 1`, id); err != nil {
		return "", err
	}
	return id, nil
}

func (service *Service) ReadRevision(ctx context.Context, id string) (*ConfigurationRevision, error) {
	if !validConfigurationRevisionID(id) {
		return nil, invalidConfiguration("Config Revision ID must be a SHA-256 identity")
	}
	conn, err := service.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return readConfigurationRevision(ctx, conn, id)
}

func (service *Service) ReadActiveRevision(ctx context.Context) (*ConfigurationRevision, error) {
	tx, err := service.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT active_revision FROM configuration_state WHERE singleton = 1").Scan(&id); err != nil {
		return nil, fmt.Errorf("read active Config Revision: %w", err)
	}
	if !id.Valid || id.String == "" {
		return nil, &DomainError{Code: ErrorNotFound, Detail: "active Config Revision not found"}
	}
	revision, err := readConfigurationRevision(ctx, tx, id.String)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revision, nil
}

func readConfigurationRevision(ctx context.Context, db database.DBTX, id string) (*ConfigurationRevision, error) {
	var document, createdAt, createdBy string
	if err := db.QueryRowContext(ctx, `
		SELECT document_json, created_at, created_by FROM configuration_revisions WHERE id = ?`, id,
	).Scan(&document, &createdAt, &createdBy); err != nil {
		if err == sql.ErrNoRows {
			return nil, &DomainError{Code: ErrorNotFound, Detail: "Config Revision not found"}
		}
		return nil, fmt.Errorf("read Config Revision: %w", err)
	}
	var configuration DynamicDraft
	if err := unmarshalDraft([]byte(document), &configuration); err != nil {
		return nil, fmt.Errorf("decode Config Revision: %w", err)
	}
	return &ConfigurationRevision{
		ID: id, Configuration: publicDraft(configuration),
		SecretReferences: configurationSecretReferences(configuration),
		CreatedAt:        createdAt,
		CreatedBy:        createdBy,
	}, nil
}

func validateRevisionSecretReferences(ctx context.Context, db database.DBTX, draft DynamicDraft) ([]ValidationProblem, error) {
	var problems []ValidationProblem
	for index, configured := range draft.Workers {
		definition, err := definitionForDraft(configured.AgentType)
		if err != nil {
			continue
		}
		allowed := make(map[string]bool, len(definition.RequiredSecretVariables()))
		for _, variable := range definition.RequiredSecretVariables() {
			allowed[variable] = true
		}
		for variable, id := range configured.SecretVersions {
			path := fmt.Sprintf("workers[%d].secrets.%s", index, variable)
			if !allowed[variable] {
				problems = append(problems, ValidationProblem{Path: path, Message: "is not declared by its Agent Definition"})
				continue
			}
			var workerName, agentType, storedVariable string
			err := db.QueryRowContext(ctx, `
				SELECT worker_name, agent_type, variable_name FROM worker_secret_versions WHERE id = ?`, id,
			).Scan(&workerName, &agentType, &storedVariable)
			if err == sql.ErrNoRows {
				problems = append(problems, ValidationProblem{Path: path, Message: "references a missing Worker Secret Version"})
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("validate Worker Secret Version reference: %w", err)
			}
			if workerName != configured.Name || agentType != configured.AgentType || storedVariable != variable {
				problems = append(problems, ValidationProblem{Path: path, Message: "references another Worker or Secret slot"})
			}
		}
	}
	return problems, nil
}

func validateRevisionCatalog(draft DynamicDraft) error {
	workers := make([]worker.WorkerConfig, len(draft.Workers))
	for index, configured := range draft.Workers {
		agentType := configured.AgentType
		if agentType == "claude_code" {
			agentType = string(worker.WorkerTypeClaudeCode)
		}
		environment := cloneStringMap(configured.AgentEnv)
		secretVersionIDs := make(map[string]string, len(configured.SecretVersions))
		for variable, versionID := range configured.SecretVersions {
			environment[variable] = versionID
			secretVersionIDs[variable] = versionID
		}
		workers[index] = worker.WorkerConfig{
			Name: configured.Name, Type: worker.WorkerType(agentType),
			TaskTypes: toWorkerTasks(configured.TaskTypes), MaxRunning: configured.MaxRunning,
			Priority: configured.Priority, Capabilities: append([]string(nil), configured.Capabilities...),
			Env: environment, SecretVersionIDs: secretVersionIDs,
		}
	}
	return worker.ValidateCatalogConfiguration(worker.CatalogConfiguration{
		ImageReference: draft.Container.Image,
		PromptGroup:    draft.Runtime.PromptGroup,
		Workers:        workers,
	})
}

func configurationSecretReferences(draft DynamicDraft) []ConfigurationSecretReference {
	references := make([]ConfigurationSecretReference, 0)
	for _, configured := range draft.Workers {
		for variable, versionID := range configured.SecretVersions {
			if versionID != "" {
				references = append(references, ConfigurationSecretReference{
					WorkerName: configured.Name, Variable: variable, VersionID: versionID,
				})
			}
		}
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].WorkerName != references[j].WorkerName {
			return references[i].WorkerName < references[j].WorkerName
		}
		return references[i].Variable < references[j].Variable
	})
	return references
}

func normalizeRevisionConfiguration(draft DynamicDraft) DynamicDraft {
	normalized := cloneDraft(draft)
	sort.Strings(normalized.Container.CapAdd)
	for index := range normalized.Workers {
		sort.Strings(normalized.Workers[index].TaskTypes)
		sort.Strings(normalized.Workers[index].Capabilities)
	}
	sort.Slice(normalized.Workers, func(i, j int) bool {
		return normalized.Workers[i].Name < normalized.Workers[j].Name
	})
	return normalized
}

func validConfigurationRevisionID(id string) bool {
	if len(id) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(id, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(id, "sha256:")
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == digest
}
