package scheduler

import (
	"context"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type cancellationAwareExecutor struct {
	finished chan struct{}
}

func (executor *cancellationAwareExecutor) Execute(ctx context.Context, started rundomain.StartedRun) rundomain.Completion {
	<-ctx.Done()
	close(executor.finished)
	return rundomain.Completion{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken, Kind: rundomain.CompletionCancelled,
	}
}

func TestExecutorRunStopWaitsForExecutorCancellationWithoutCompletingRuntimeResources(t *testing.T) {
	executor := &cancellationAwareExecutor{finished: make(chan struct{})}
	started := rundomain.StartedRun{
		Run:   rundomain.ExecutionRun{ID: "run-test", FencingToken: 1},
		Claim: rundomain.Claim{RunID: "run-test"},
	}
	run, err := NewExecutorRun(executor, started)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(t.Context())
	waited := make(chan rundomain.Completion, 1)
	go func() {
		completion, _ := run.Wait(runContext)
		waited <- completion
	}()
	cancelRun()
	stopContext, cancelStop := context.WithTimeout(t.Context(), time.Second)
	defer cancelStop()
	if err := run.Stop(stopContext, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.finished:
	case <-time.After(time.Second):
		t.Fatal("Executor did not observe cancellation")
	}
	if completion := <-waited; completion.Kind != rundomain.CompletionCancelled {
		t.Fatalf("completion: %+v", completion)
	}
}
