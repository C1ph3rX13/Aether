package config_test

import (
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestNewDeploymentStartsWithRecommendedInactiveDraft(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	state, err := config.InitializeDeployment(t.Context(), db, "deployment-one")
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveRevision != "" || state.DraftVersion != 1 || len(state.Draft.Workers) != 0 {
		t.Fatalf("initial state = %+v", state)
	}
	if state.Draft.Tasks.Bootstrap.PrimarySeconds != 600 ||
		state.Draft.Tasks.Reason.PrimarySeconds != 450 ||
		state.Draft.Tasks.Explore.PrimarySeconds != 600 ||
		state.Draft.Container.Image != "ghcr.io/c1ph3rx13/aether-worker:latest" {
		t.Fatalf("recommended Draft = %+v", state.Draft)
	}

	loaded, err := config.LoadDeploymentState(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeploymentID != state.DeploymentID || loaded.DraftVersion != state.DraftVersion || loaded.ActiveRevision != "" {
		t.Fatalf("reloaded state = %+v", loaded)
	}
	if _, err := config.InitializeDeployment(t.Context(), db, "deployment-two"); err == nil {
		t.Fatal("different Bootstrap deployment identity reused the database")
	}
}
