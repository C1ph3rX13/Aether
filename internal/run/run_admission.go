package run

import "context"

type StartReadiness interface {
	CheckStartReadiness(context.Context, StartRequest) error
}

type RunStarter interface {
	StartRun(context.Context, StartRequest) (*AdmittedRun, error)
}

// AdmittedStarter guarantees readiness is checked before a command can create a Run or Claim.
type AdmittedStarter struct {
	Readiness StartReadiness
	Starter   RunStarter
}

func (starter AdmittedStarter) StartRun(ctx context.Context, request StartRequest) (*AdmittedRun, error) {
	if starter.Readiness == nil || starter.Starter == nil {
		return nil, newError(ErrorInvalidArgument, "Run admission dependencies are required")
	}
	if err := starter.Readiness.CheckStartReadiness(ctx, request); err != nil {
		return nil, err
	}
	return starter.Starter.StartRun(ctx, request)
}

type StartReadinessFunc func(context.Context, StartRequest) error

func (check StartReadinessFunc) CheckStartReadiness(ctx context.Context, request StartRequest) error {
	return check(ctx, request)
}

type CompositeStartReadiness []StartReadiness

func (checks CompositeStartReadiness) CheckStartReadiness(ctx context.Context, request StartRequest) error {
	for _, check := range checks {
		if check == nil {
			return newError(ErrorInvalidArgument, "Run readiness check is required")
		}
		if err := check.CheckStartReadiness(ctx, request); err != nil {
			return err
		}
	}
	return nil
}
