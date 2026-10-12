package project

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/C1ph3rX13/Aether/internal/database"
)

// serviceCore contains Project persistence and domain helpers shared by the
// command and Run-facing roles. It has no execution-coordination dependency.
type serviceCore struct {
	projects       *projectStore
	facts          *FactStore
	intents        *IntentStore
	hints          *HintStore
	completions    *CompletionStore
	changes        *historyChangeStore
	history        *HistoryQueries
	runtime        RuntimePolicy
	operatorName   string
	configRevision ConfigRevisionProvider
	query          *projectQuery
}

// Service orchestrates project-related business logic.
type Service struct {
	*serviceCore
	db        *database.DB
	publisher EventPublisher
	execution ExecutionPort
}

// RuntimePolicy contains deployment-owned coordination leases. It is immutable
// for the lifetime of a Service instance.
type RuntimePolicy struct {
	IntentLeaseSeconds int
	ReasonLeaseSeconds int
}

func (policy RuntimePolicy) normalized() RuntimePolicy {
	if policy.IntentLeaseSeconds == 0 {
		policy.IntentLeaseSeconds = 15
	}
	if policy.ReasonLeaseSeconds == 0 {
		policy.ReasonLeaseSeconds = 15
	}
	return policy
}

type ServiceOptions struct {
	RuntimePolicy  RuntimePolicy
	OperatorName   string
	ConfigRevision ConfigRevisionProvider
}

// NewService creates a command-capable Service with its default stores.
// Execution coordination is required because every command-capable Project
// service must be able to coordinate the Run ledger transactionally.
func NewService(db *database.DB, publisher EventPublisher, execution ExecutionPort, options ...ServiceOptions) (*Service, error) {
	if db == nil {
		return nil, Internal("database is required")
	}
	if isNilExecutionCoordination(execution) {
		return nil, Internal("execution coordination is required")
	}
	var configured ServiceOptions
	if len(options) > 0 {
		configured = options[0]
	}
	return newService(db, publisher, execution, configured), nil
}

func isNilExecutionCoordination(execution ExecutionPort) bool {
	if execution == nil {
		return true
	}
	value := reflect.ValueOf(execution)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func newService(db *database.DB, publisher EventPublisher, execution ExecutionPort, configured ServiceOptions) *Service {
	return &Service{
		serviceCore: newServiceCore(configured),
		db:          db, publisher: publisher, execution: execution,
	}
}

func newServiceCore(configured ServiceOptions) *serviceCore {
	operatorName := strings.TrimSpace(configured.OperatorName)
	if operatorName == "" {
		operatorName = "Operator"
	}
	return &serviceCore{
		projects:       &projectStore{},
		facts:          &FactStore{},
		intents:        &IntentStore{},
		hints:          &HintStore{},
		completions:    &CompletionStore{},
		changes:        &historyChangeStore{},
		history:        NewHistoryQueries(),
		runtime:        configured.RuntimePolicy.normalized(),
		operatorName:   operatorName,
		configRevision: configured.ConfigRevision,
		query:          newProjectQuery(),
	}
}

// Get returns a full project detail.
func (s *Service) Get(ctx context.Context, projectID string) (ProjectDetail, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return ProjectDetail{}, err
	}
	defer conn.Close()

	if err := s.requireProjectExists(ctx, conn, projectID); err != nil {
		return ProjectDetail{}, err
	}
	return s.query.GetDetail(ctx, conn, projectID)
}

// Create creates a new project with origin/goal facts and initial hints.
func (s *Service) Create(ctx context.Context, req CreateProjectRequest) (ProjectDetail, error) {
	validated, domainErr := ValidateCreateProjectRequest(req)
	if domainErr != nil {
		return ProjectDetail{}, domainErr
	}
	req = *validated

	receipt, err := commitProjectRevision(
		ctx, s, projectRevisionTarget{}, createProjectRevisionCommand{Request: req},
	)
	if err != nil {
		return ProjectDetail{}, err
	}
	detail := receipt.Mutation.Detail
	detail.Project = receipt.Project
	return detail, nil
}

type createProjectRevisionCommand struct {
	Request CreateProjectRequest
}

func (createProjectRevisionCommand) validateRevisionProject(ProjectMeta) error {
	return nil
}

func (createProjectRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (command createProjectRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, execution ExecutionPort, tx DBTX, revision projectRevisionContext,
) (projectCreatedRevision, error) {
	projectID, err := NextProjectID(ctx, tx)
	if err != nil {
		return projectCreatedRevision{}, err
	}
	request := command.Request
	configRevision := ""
	if service.configRevision != nil {
		configRevision, err = service.configRevision(ctx, tx)
		if err != nil {
			return projectCreatedRevision{}, err
		}
		if strings.TrimSpace(configRevision) == "" {
			return projectCreatedRevision{}, ConfigurationRequired("an active Config Revision is required")
		}
	}
	if err := service.projects.Create(
		ctx, tx, projectID, request.Title, request.BootstrapEnabled, revision.OccurredAt, configRevision,
	); err != nil {
		return projectCreatedRevision{}, err
	}
	if err := service.facts.Create(ctx, tx, "origin", projectID, request.Origin); err != nil {
		return projectCreatedRevision{}, err
	}
	if err := service.facts.Create(ctx, tx, "goal", projectID, request.Goal); err != nil {
		return projectCreatedRevision{}, err
	}
	if request.BootstrapEnabled {
		bootstrapIntentID, err := NextIntentID(ctx, tx, projectID)
		if err != nil {
			return projectCreatedRevision{}, err
		}
		if err := service.intents.Create(
			ctx, tx, bootstrapIntentID, projectID, IntentKindBootstrap,
			BootstrapIntentDescription, BootstrapIntentCreator,
			[]string{"origin"}, nil, revision.OccurredAt,
		); err != nil {
			return projectCreatedRevision{}, err
		}
		if err := execution.EnsureReasonSchedulingBaseline(
			ctx, tx, projectID, 1, 1, revision.OccurredAt,
		); err != nil {
			return projectCreatedRevision{}, err
		}
	}
	for _, hint := range request.Hints {
		hintID, err := NextHintID(ctx, tx, projectID)
		if err != nil {
			return projectCreatedRevision{}, err
		}
		if err := service.hints.Create(
			ctx, tx, hintID, projectID, hint.Content, service.operatorName,
			revision.OccurredAt, 1,
		); err != nil {
			return projectCreatedRevision{}, err
		}
	}
	detail, err := service.query.GetDetail(ctx, tx, projectID)
	if err != nil {
		return projectCreatedRevision{}, err
	}
	mutation := projectCreatedRevision{Detail: detail}
	return mutation, nil
}

type projectCreatedRevision struct {
	Detail ProjectDetail
}

func (projectCreatedRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{}
}

func (transition projectCreatedRevision) revisionEvidence(_ int) projectRevisionEvidence {
	return projectRevisionEvidence{
		Kind: ChangeProjectCreated, EntityType: "project",
		After: projectRevisionEntities{
			Facts: transition.Detail.Facts, Intents: transition.Detail.Intents,
			Hints: transition.Detail.Hints, CompletionRecords: transition.Detail.CompletionRecords,
		},
	}
}

// UpdateTitle changes the project title.
func (s *Service) UpdateTitle(ctx context.Context, projectID string, request UpdateProjectTitleRequest) (RenameProjectReceipt, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		return RenameProjectReceipt{}, InvalidArgument("title is required")
	}
	if request.ExpectedRevision < 0 {
		return RenameProjectReceipt{}, InvalidArgument("expected_revision must be a non-negative integer")
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, request.ExpectedRevision),
		renameProjectRevisionCommand{Title: title},
	)
	if err != nil {
		return RenameProjectReceipt{}, err
	}
	return RenameProjectReceipt{Project: receipt.Project, ProjectRevision: receipt.Project.Revision}, nil
}

type renameProjectRevisionCommand struct {
	Title string
}

func (renameProjectRevisionCommand) validateRevisionProject(ProjectMeta) error {
	return nil
}

func (renameProjectRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (command renameProjectRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (projectRenamedRevision, error) {
	rows, err := service.projects.Rename(ctx, tx, revision.Project.ID, command.Title)
	if err != nil {
		return projectRenamedRevision{}, err
	}
	if rows != 1 {
		return projectRenamedRevision{}, ErrProjectNotFound
	}
	mutation := projectRenamedRevision{}
	return mutation, nil
}

type projectRenamedRevision struct{}

func (projectRenamedRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{}
}

func (projectRenamedRevision) revisionEvidence(_ int) projectRevisionEvidence {
	return projectRevisionEvidence{
		Kind: ChangeProjectRenamed, EntityType: "project",
	}
}

// DeleteConfirmed permanently deletes a Project only after title and Revision
// confirmation, preserving runtime cleanup evidence outside the cascade.
func (s *Service) DeleteConfirmed(
	ctx context.Context, projectID string, request DeleteProjectRequest,
) (DeleteProjectReceipt, error) {
	title := strings.TrimSpace(request.ExpectedTitle)
	if title == "" {
		return DeleteProjectReceipt{}, InvalidArgument("expected_title is required")
	}
	if request.ExpectedRevision < 0 {
		return DeleteProjectReceipt{}, InvalidArgument("expected_revision must be a non-negative integer")
	}
	return commitProjectDeletion(
		ctx, s, existingProjectRevisionTarget(projectID, request.ExpectedRevision),
		deleteProjectCommand{ExpectedTitle: title},
	)
}

type deleteProjectCommand struct {
	ExpectedTitle string
}

func (deleteProjectCommand) validateRevisionBeforeExpected() bool { return true }

func (command deleteProjectCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Title != command.ExpectedTitle {
		return Conflict("project title confirmation does not match")
	}
	return nil
}

func (s *Service) requireProjectExists(ctx context.Context, conn DBTX, projectID string) error {
	exists, err := s.projects.Exists(ctx, conn, projectID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrProjectNotFound
	}
	return nil
}

func (s *Service) requireProjectActive(ctx context.Context, conn DBTX, projectID string) error {
	if err := s.requireProjectExists(ctx, conn, projectID); err != nil {
		return err
	}
	status, err := s.projects.GetStatus(ctx, conn, projectID)
	if err != nil {
		return err
	}
	if status != "active" {
		return Forbidden(fmt.Sprintf("Project is %s", status))
	}
	return nil
}

func (s *Service) requireProjectHintWritable(ctx context.Context, conn DBTX, projectID string) error {
	return s.requireProjectExists(ctx, conn, projectID)
}

func (s *serviceCore) validateFactsExist(ctx context.Context, conn DBTX, projectID string, factIDs []string) error {
	for _, fid := range factIDs {
		exists, err := s.facts.IsUsableSource(ctx, conn, projectID, fid)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("Fact %s not found or retracted", fid)
		}
	}
	return nil
}

// IsProjectNotFound checks if an error is ErrProjectNotFound.
func IsProjectNotFound(err error) bool {
	return errors.Is(err, ErrProjectNotFound)
}
