package docker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type process struct {
	runtime         *Runtime
	containerID     string
	runID           rundomain.RunID
	phase           rundomain.ExecutionPhase
	exec            *execProcess
	stopGrace       time.Duration
	deadline        time.Duration
	startedAt       time.Time
	done            chan struct{}
	stopMu          sync.Mutex
	stopped         bool
	deadlineReached bool
	stopDone        chan struct{}
	stopErr         error
	output          rundomain.ProcessOutput
	registration    rundomain.ExecutionHandleRegistration
	handle          rundomain.ExecutionHandle
}

var _ rundomain.Process = (*process)(nil)

func newProjectProcess(
	runtime *Runtime,
	containerID string,
	runID rundomain.RunID,
	exec *execProcess,
	deadline time.Duration,
	stopGrace time.Duration,
	phase rundomain.ExecutionPhase,
	registration rundomain.ExecutionHandleRegistration,
	handle rundomain.ExecutionHandle,
) *process {
	process := &process{
		runtime: runtime, containerID: containerID, runID: runID, phase: phase, exec: exec,
		stopGrace: stopGrace, deadline: deadline, startedAt: time.Now(), done: make(chan struct{}),
		registration: registration, handle: handle,
	}
	go process.collect()
	go process.watchDeadline()
	return process
}

func (p *process) Handle() rundomain.ExecutionHandle {
	return p.handle
}

func (p *process) collect() {
	result := p.exec.wait(context.Background())
	p.stopMu.Lock()
	stopped := p.stopped
	deadlineReached := p.deadlineReached
	timedOut := deadlineReached || phaseTimedOut(result.timedOut, result.exitCode, stopped, time.Since(p.startedAt), p.deadline)
	p.output = rundomain.ProcessOutput{
		Stdout: result.stdout, Stderr: result.stderr, Exited: result.exitCodeObserved, ExitCode: result.exitCode,
		TimedOut: timedOut, Cancelled: result.cancelled || (stopped && !deadlineReached), ErrorCode: result.errorCode,
		StoppedProcessFileReader: result.fileReader,
	}
	p.stopMu.Unlock()
	p.runtime.forgetProjectExec(p.runID, p.exec)
	if p.registration != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := p.registration.Stopped(ctx, p.handle)
		cancel()
		if err != nil {
			slog.Warn("mark Project Exec stopped", "run_id", p.runID, "exec_id", p.handle.ExecID, "error", err)
		}
	}
	close(p.done)
}

func phaseTimedOut(reported bool, exitCode int, stopped bool, elapsed, deadline time.Duration) bool {
	return reported || !stopped && elapsed >= deadline && (exitCode == 124 || exitCode == 137)
}

func (p *process) Wait(ctx context.Context) (rundomain.ProcessOutput, error) {
	select {
	case <-p.done:
		return p.result(), nil
	case <-ctx.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), p.stopGrace+5*time.Second)
	err := p.Stop(stopCtx, p.stopGrace)
	cancel()
	select {
	case <-p.done:
		return p.result(), err
	case <-time.After(p.stopGrace + 5*time.Second):
		if err != nil {
			return rundomain.ProcessOutput{Cancelled: true, ExitCode: 137}, fmt.Errorf("Project Exec did not stop after Wait cancellation: %w", err)
		}
		return rundomain.ProcessOutput{Cancelled: true, ExitCode: 137}, fmt.Errorf("Project Exec did not stop after Wait cancellation")
	}
}

func (p *process) Stop(ctx context.Context, grace time.Duration) error {
	return p.stop(ctx, grace, false)
}

func (p *process) stop(ctx context.Context, grace time.Duration, deadlineReached bool) error {
	if grace <= 0 || grace > rundomain.MaximumStopGrace {
		return fmt.Errorf("process stop grace is invalid")
	}
	p.stopMu.Lock()
	if p.stopDone != nil {
		done := p.stopDone
		p.stopMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			p.stopMu.Lock()
			err := p.stopErr
			p.stopMu.Unlock()
			return err
		}
	}
	p.stopped = true
	p.deadlineReached = deadlineReached
	p.stopDone = make(chan struct{})
	done := p.stopDone
	p.stopMu.Unlock()

	err := p.exec.stop(ctx, grace)
	p.stopMu.Lock()
	p.stopErr = err
	close(done)
	p.stopMu.Unlock()
	return err
}

func (p *process) watchDeadline() {
	timer := time.NewTimer(p.deadline)
	defer timer.Stop()
	select {
	case <-p.done:
		return
	case <-timer.C:
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.stopGrace+5*time.Second)
	err := p.stop(ctx, p.stopGrace, true)
	cancel()
	if err != nil {
		slog.Warn("phase deadline cleanup failed", "run_id", p.runID, "exec_id", p.handle.ExecID, "error", err)
	}
}

func (p *process) result() rundomain.ProcessOutput {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	return p.output
}
