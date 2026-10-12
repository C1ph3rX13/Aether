package project

import "context"

// RunAccess is the narrow Project role consumed by the Run module. It exposes
// only the transaction-backed Project decisions and projections needed by Run
// admission and result delivery; it has no Project command capabilities.
type RunAccess struct {
	core *serviceCore
}

// NewRunAccess creates the Run-facing Project role without requiring the Run
// service, so the two services can be composed without a dependency cycle.
func NewRunAccess(options ...ServiceOptions) *RunAccess {
	var configured ServiceOptions
	if len(options) > 0 {
		configured = options[0]
	}
	return newRunAccess(configured)
}

func newRunAccess(configured ServiceOptions) *RunAccess {
	return &RunAccess{core: newServiceCore(configured)}
}

// ExecutionAdmission is the Project-owned decision input for creating an
// Execution Run. It intentionally contains no Run or persistence types.
type ExecutionAdmission struct {
	Status             ProjectStatus
	Generation         int
	Revision           int64
	ConfigRevision     string
	TargetAvailable    bool
	Input              ExecutionInputProjection
	IntentLeaseSeconds int
	ReasonLeaseSeconds int
}

func (core *serviceCore) ExecutionAdmission(
	ctx context.Context,
	tx DBTX,
	projectID string,
	task RunTask,
	targetIntentID *string,
) (ExecutionAdmission, error) {
	input, err := core.query.GetExecutionInput(ctx, tx, projectID)
	if err != nil {
		return ExecutionAdmission{}, err
	}
	meta := input.Project
	admission := ExecutionAdmission{
		Status: meta.Status, Generation: meta.Generation, Revision: meta.Revision, ConfigRevision: meta.ConfigRevision,
		IntentLeaseSeconds: core.runtime.IntentLeaseSeconds, ReasonLeaseSeconds: core.runtime.ReasonLeaseSeconds,
		TargetAvailable: targetIntentID == nil, Input: input,
	}
	if targetIntentID == nil {
		return admission, nil
	}
	intent := executionTargetIntent(input.Intents, *targetIntentID)
	if intent == nil {
		return admission, nil
	}
	wantKind := IntentKindExplore
	if task == RunTaskBootstrap {
		wantKind = IntentKindBootstrap
	}
	admission.TargetAvailable = intent.IsOpen() && intent.Kind == wantKind
	if admission.TargetAvailable && task == RunTaskBootstrap {
		admission.TargetAvailable, err = core.BootstrapTargetEligible(ctx, tx, projectID, *targetIntentID)
	}
	if err != nil || !admission.TargetAvailable {
		return admission, err
	}
	usableFacts := make(map[string]bool, len(input.Facts))
	for _, fact := range input.Facts {
		usableFacts[fact.ID] = fact.IsActive()
	}
	for _, source := range intent.From {
		if !usableFacts[source] {
			admission.TargetAvailable = false
			return admission, nil
		}
	}
	return admission, nil
}

func executionTargetIntent(intents []Intent, targetID string) *Intent {
	for index := range intents {
		if intents[index].ID == targetID {
			return &intents[index]
		}
	}
	return nil
}

func (core *serviceCore) ExecutionRevision(ctx context.Context, tx DBTX, projectID string) (int64, error) {
	meta, err := core.projects.GetMeta(ctx, tx, projectID)
	return meta.Revision, err
}

func (s *Service) ExecutionAdmission(
	ctx context.Context, tx DBTX, projectID string, task RunTask, targetIntentID *string,
) (ExecutionAdmission, error) {
	return s.serviceCore.ExecutionAdmission(ctx, tx, projectID, task, targetIntentID)
}

func (s *Service) ExecutionRevision(ctx context.Context, tx DBTX, projectID string) (int64, error) {
	return s.serviceCore.ExecutionRevision(ctx, tx, projectID)
}
