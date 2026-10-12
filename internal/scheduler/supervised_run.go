package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type ExecutorRun struct {
	executor rundomain.Executor
	started  rundomain.StartedRun

	waitOnce   sync.Once
	done       chan struct{}
	completion rundomain.Completion
}

func NewExecutorRun(executor rundomain.Executor, started rundomain.StartedRun) (*ExecutorRun, error) {
	if executor == nil || started.Run.ID == "" || started.Run.ID != started.Claim.RunID {
		return nil, fmt.Errorf("supervised Executor Run is invalid")
	}
	return &ExecutorRun{executor: executor, started: started, done: make(chan struct{})}, nil
}

func (run *ExecutorRun) Wait(ctx context.Context) (rundomain.Completion, error) {
	run.waitOnce.Do(func() {
		run.completion = run.executor.Execute(ctx, run.started)
		close(run.done)
	})
	<-run.done
	return run.completion, nil
}

func (run *ExecutorRun) Stop(ctx context.Context, grace time.Duration) error {
	if grace <= 0 || grace > rundomain.MaximumStopGrace {
		return fmt.Errorf("supervised Executor Run stop grace is invalid")
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
