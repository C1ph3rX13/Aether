package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/C1ph3rX13/Aether/internal/api"
	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	"github.com/C1ph3rX13/Aether/internal/query"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

type controlPlaneModule struct {
	projects     *project.Service
	projectReads *query.Service
	runs         *rundomain.Service
	controlPlane *ControlPlane
	changeStream *api.ChangeStream
}

func newControlPlaneModule(
	db *database.DB,
	operatorName string,
	requireConfigured func(context.Context) error,
	configRevisionProvider project.ConfigRevisionProvider,
	authority rundomain.AdmissionAuthority,
	dispatcherPolicy DispatcherPolicyProvider,
) (*controlPlaneModule, error) {
	changeStream := api.NewChangeStream()
	projectOptions := project.ServiceOptions{
		OperatorName: operatorName, ConfigRevision: configRevisionProvider,
	}
	projectForRun := project.NewRunAccess(projectOptions)
	runProjectAccess, err := NewRunProjectPort(projectForRun)
	if err != nil {
		return nil, fmt.Errorf("configure Run Project access: %w", err)
	}
	runReadiness := rundomain.StartReadinessFunc(func(ctx context.Context, _ rundomain.StartRequest) error {
		if requireConfigured == nil {
			return nil
		}
		err := requireConfigured(ctx)
		var projectErr *project.DomainError
		if errors.As(err, &projectErr) && projectErr.Code == project.ErrorConfigurationRequired {
			return &rundomain.DomainError{Code: rundomain.ErrorConfigurationRequired, Detail: projectErr.Detail}
		}
		return err
	})
	runService := rundomain.NewService(db, runProjectAccess, changeStream, rundomain.ServiceOptions{
		OperatorName: operatorName,
		Readiness:    runReadiness,
		Authority:    authority,
	})
	projectExecution, err := NewProjectExecutionPort(runService)
	if err != nil {
		return nil, fmt.Errorf("configure Project execution adapter: %w", err)
	}
	projectService, err := project.NewService(db, changeStream, projectExecution, projectOptions)
	if err != nil {
		return nil, fmt.Errorf("configure Project service: %w", err)
	}
	controlPlane, err := newControlPlane(db, projectService, runService, dispatcherPolicy)
	if err != nil {
		return nil, fmt.Errorf("configure control plane: %w", err)
	}
	return &controlPlaneModule{
		projects: projectService, projectReads: query.NewService(db, runService),
		runs: runService, controlPlane: controlPlane, changeStream: changeStream,
	}, nil
}

func activeDispatcherPolicyProvider(
	ctx context.Context,
	tx database.DBTX,
) (scheduler.ControlDispatcherPolicy, error) {
	policy, err := config.ReadActiveDispatcherPolicy(ctx, tx)
	if err != nil {
		return scheduler.ControlDispatcherPolicy{}, err
	}
	return scheduler.ControlDispatcherPolicy{
		ConfigRevision: policy.ConfigRevision, PollIntervalSeconds: policy.PollIntervalSeconds,
		MaxWorkers: policy.MaxWorkers, MaxRunningProjects: policy.MaxRunningProjects,
	}, nil
}
