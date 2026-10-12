package controlplane

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

// NewControlPlaneTestHost creates a Host with seeded configuration and
// without production configuration and admission gates.
func NewControlPlaneTestHost(dbPath string, staticFS fs.FS, options ControlPlaneHostOptions) (*ControlPlaneHost, error) {
	return newControlPlaneHostWithDatabase(database.NewDB(), dbPath, staticFS, options, false)
}

func initializeControlPlaneTestConfiguration(
	ctx context.Context,
	db *database.DB,
	operatorName, deploymentID string,
) (string, error) {
	if strings.TrimSpace(deploymentID) == "" {
		deploymentID = "test-deployment"
	}
	state, err := config.InitializeDeployment(ctx, db, deploymentID)
	if err != nil {
		return "", fmt.Errorf("initialize test Deployment Configuration: %w", err)
	}
	if state.ActiveRevision != "" {
		return state.ActiveRevision, nil
	}
	service, err := config.NewService(db, config.ServiceOptions{
		OperatorName: operatorName,
		Healthcheck: func(context.Context, worker.CatalogWorker, map[string]string) error {
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	draft := config.RecommendedDynamicDraft()
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "test-worker", AgentType: "codex", ProviderName: "test-provider",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 1, Priority: 1,
		AgentEnv: map[string]string{
			"CODEX_BASE_URL": "https://provider.invalid", "CODEX_MODEL": "test-model",
		},
		TimeoutOverrides: map[string]int{},
	}}
	read, err := service.SaveDraft(ctx, config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: state.DraftVersion, Draft: draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"test-worker": {"OPENAI_API_KEY": {Value: "test-secret"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save test Deployment Configuration: %w", err)
	}
	return read.ActiveRevision.ID, nil
}
