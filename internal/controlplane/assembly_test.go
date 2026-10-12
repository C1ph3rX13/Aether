package controlplane

import (
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// testAssembly exposes the Host's private modules to coordination tests.
// Startup and resource ownership stay with the Host.
type testAssembly struct {
	host           *ControlPlaneHost
	DB             *database.DB
	ProjectService *project.Service
	RunService     *rundomain.Service
	ControlPlane   *ControlPlane
}

func newTestAssembly(dbPath string, options ControlPlaneHostOptions) (*testAssembly, error) {
	host, err := NewControlPlaneTestHost(dbPath, nil, options)
	if err != nil {
		return nil, err
	}
	return &testAssembly{
		host: host, DB: host.db, ProjectService: host.module.projects,
		RunService: host.module.runs, ControlPlane: host.module.controlPlane,
	}, nil
}

func (assembly *testAssembly) Shutdown() error {
	return assembly.host.Shutdown()
}
