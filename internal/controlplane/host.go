package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/C1ph3rX13/Aether/internal/api"
	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	run "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

// ControlPlaneHost is the production host boundary for the Control Plane
// module. Its assembly details remain private to the host.
type ControlPlaneHost struct {
	db                 *database.DB
	module             *controlPlaneModule
	configuration      *config.Service
	deploymentMetadata DeploymentMetadata
	managed            bool
	handler            http.Handler
}

// ControlPlaneHostOptions contains the deployment identity required by the
// Control Plane Host.
type ControlPlaneHostOptions struct {
	OperatorName string
	DeploymentID string
}

// NewControlPlaneHost creates a new Control Plane Host and initializes the
// database at dbPath.
func NewControlPlaneHost(dbPath string, staticFS fs.FS, options ControlPlaneHostOptions) (*ControlPlaneHost, error) {
	return newControlPlaneHostWithDatabase(database.NewDB(), dbPath, staticFS, options, true)
}

func newControlPlaneHostWithDatabase(
	db *database.DB,
	dbPath string,
	staticFS fs.FS,
	options ControlPlaneHostOptions,
	managed bool,
) (*ControlPlaneHost, error) {
	operatorName := strings.TrimSpace(options.OperatorName)
	deploymentID := strings.TrimSpace(options.DeploymentID)
	if operatorName == "" {
		return nil, fmt.Errorf("operator name is required")
	}
	if managed && deploymentID == "" {
		return nil, fmt.Errorf("deployment ID is required")
	}

	if err := db.Configure(dbPath); err != nil {
		return nil, fmt.Errorf("configure database: %w", err)
	}
	if managed {
		if err := config.ValidateDeploymentDatabase(context.Background(), db); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	if err := project.InitializeHistory(context.Background(), db); err != nil {
		initializationErr := fmt.Errorf("initialize Project History: %w", err)
		if closeErr := db.Close(); closeErr != nil {
			initializationErr = errors.Join(
				initializationErr,
				fmt.Errorf("close database after Project History initialization failure: %w", closeErr),
			)
		}
		return nil, initializationErr
	}
	testRevision := ""
	if managed {
		if _, err := config.InitializeDeployment(context.Background(), db, deploymentID); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize deployment: %w", err)
		}
	} else {
		var err error
		testRevision, err = initializeControlPlaneTestConfiguration(
			context.Background(), db, operatorName, deploymentID,
		)
		if err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}

	var requireConfigured func(context.Context) error
	configRevisionProvider := project.ConfigRevisionProvider(func(context.Context, project.DBTX) (string, error) {
		return testRevision, nil
	})
	if managed {
		requireConfigured = func(ctx context.Context) error {
			active, err := config.HasActiveRevision(ctx, db)
			if err != nil {
				return err
			}
			if !active {
				return project.ConfigurationRequired("an active Config Revision is required")
			}
			return nil
		}
		configRevisionProvider = func(ctx context.Context, tx project.DBTX) (string, error) {
			id, err := config.ProjectCreationRevision(ctx, tx)
			if err != nil {
				var configurationErr *config.DomainError
				if errors.As(err, &configurationErr) && configurationErr.Code == config.ErrorValidation {
					return "", project.ConfigurationRequired(configurationErr.Detail)
				}
				return "", err
			}
			return id, nil
		}
	}

	configuration, err := config.NewService(db, config.ServiceOptions{OperatorName: operatorName})
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	var authority run.AdmissionAuthority
	if managed {
		authority = configuration
	}
	dispatcherPolicy := DispatcherPolicyProvider(activeDispatcherPolicyProvider)
	module, err := newControlPlaneModule(
		db, operatorName, requireConfigured, configRevisionProvider, authority, dispatcherPolicy,
	)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	host := &ControlPlaneHost{
		db: db, module: module, configuration: configuration, deploymentMetadata: newDeploymentMetadata(operatorName), managed: managed,
	}
	host.handler = host.setupHandler(staticFS)
	return host, nil
}

func (host *ControlPlaneHost) Handler() http.Handler {
	return host.handler
}

func (host *ControlPlaneHost) ControlPlane() ControlPlaneCapability {
	return host.module.controlPlane
}

func (host *ControlPlaneHost) HasActiveConfiguration(ctx context.Context) (bool, error) {
	return config.HasActiveRevision(ctx, host.db)
}

func (host *ControlPlaneHost) ActiveExecutionConfig(ctx context.Context) (*config.DispatchConfig, error) {
	if host.configuration == nil {
		return nil, fmt.Errorf("Dynamic Configuration is unavailable")
	}
	return host.configuration.ReadActiveExecutionConfig(ctx)
}

func (host *ControlPlaneHost) ExecutionConfig(ctx context.Context, revision string) (*config.DispatchConfig, error) {
	if host.configuration == nil {
		return nil, fmt.Errorf("Dynamic Configuration is unavailable")
	}
	return host.configuration.ReadExecutionConfig(ctx, revision)
}

func (host *ControlPlaneHost) RunSecretProvider() (run.RunScopedSecretEnvironmentProvider, error) {
	return config.NewRunSecretProvider(host.db)
}

func (host *ControlPlaneHost) ResolveRunSecrets(
	ctx context.Context, request run.RunSecretRequest,
) (*run.ResolvedSecrets, error) {
	provider, err := config.NewRunSecretProvider(host.db)
	if err != nil {
		return nil, err
	}
	return provider.ResolveAuthorizedRunSecrets(ctx, request)
}

func (host *ControlPlaneHost) ResolveRevisionSecrets(
	ctx context.Context, revision, workerName string, lease run.DispatcherLease,
) (*run.ResolvedSecrets, error) {
	tx, err := host.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if err := host.module.runs.AuthorizeDispatcher(ctx, tx, lease.InstanceID, lease.FencingToken); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	provider, err := host.RevisionSecretProvider(revision)
	if err != nil {
		return nil, err
	}
	return provider.ResolveWorkerSecrets(ctx, workerName)
}

func (host *ControlPlaneHost) RevisionSecretProvider(revision string) (run.WorkerScopedSecretEnvironmentProvider, error) {
	return config.NewRevisionSecretProvider(host.db, revision)
}

func (host *ControlPlaneHost) Shutdown() error {
	if host.db == nil {
		return nil
	}
	return host.db.Close()
}

func (host *ControlPlaneHost) setupHandler(staticFS fs.FS) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// Versioned API group. There is no unversioned compatibility surface.
	v1 := r.Group("/api/v1")

	v1Module := host.module
	if host.managed {
		probe := deploymentReadinessProbe{db: host.db, runs: v1Module.runs}
		api.RegisterHealth(v1, host.db, probe, probe)
	} else {
		// Test assembly deliberately omits Deployment Configuration state.
		api.RegisterHealth(v1, host.db, v1Module.runs, nil)
	}
	v1.GET("/deployment", func(c *gin.Context) {
		c.JSON(http.StatusOK, host.deploymentMetadata)
	})

	if host.managed && host.configuration != nil {
		api.RegisterConfiguration(v1, host.configuration)
	}
	api.RegisterProjects(v1, v1Module.projectReads, v1Module.projects, v1Module.projects, v1Module.projects)
	api.RegisterActivity(v1, v1Module.runs)
	api.RegisterOperatorRetries(v1, v1Module.runs)
	api.RegisterExport(v1, v1Module.projects)
	api.RegisterRuns(v1, v1Module.controlPlane)
	api.RegisterRunSecrets(v1, host)
	api.RegisterRevisionSecrets(v1, host)
	api.RegisterEvents(v1, v1Module.changeStream, v1Module.projects)

	// Serve embedded Next.js frontend for non-API routes.
	api.RegisterStatic(r, staticFS)

	return r
}

type deploymentReadinessProbe struct {
	db   *database.DB
	runs *run.Service
}

func (probe deploymentReadinessProbe) HasActiveConfiguration(ctx context.Context) (bool, error) {
	return config.HasActiveRevision(ctx, probe.db)
}

func (probe deploymentReadinessProbe) HasActiveDispatcherLease(ctx context.Context) (bool, error) {
	return probe.runs.HasActiveDispatcherLease(ctx)
}
