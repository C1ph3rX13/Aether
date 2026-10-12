package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/moby/moby/client"
)

const (
	execInspectTimeout      = 5 * time.Second
	processErrorExitInspect = rundomain.ProcessErrorCode("process_exit_inspect_error")
)

type execResult struct {
	stdout           string
	stderr           string
	exitCode         int
	exitCodeObserved bool
	timedOut         bool
	cancelled        bool
	errorCode        rundomain.ProcessErrorCode
	fileReader       rundomain.StoppedProcessFileReader
}

// execProcess owns one Docker Exec attach stream from creation through close.
type execProcess struct {
	client      *client.Client
	containerID string
	pidFile     string
	command     []string
	environment []string
	output      *processOutput

	mu                   sync.Mutex
	execID               string
	attach               net.Conn
	attachCloseRequested bool
	result               execResult
	done                 chan struct{}
}

func newExecProcess(
	dockerClient *client.Client,
	containerID string,
	pidFile string,
	command []string,
	environment map[string]string,
	limits OutputLimits,
	capture rundomain.ProcessOutputCapture,
) (*execProcess, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &execProcess{
		client: dockerClient, containerID: containerID, pidFile: pidFile,
		command:     append([]string(nil), command...),
		environment: environmentSlice(environment),
		output:      newProcessOutputWithCapture(limits, capture), done: make(chan struct{}),
	}, nil
}

func (process *execProcess) prepare(ctx context.Context) error {
	environment := process.takeEnvironment()
	created, err := process.client.ExecCreate(ctx, process.containerID, client.ExecCreateOptions{
		Cmd: process.command, Env: environment, AttachStdout: true, AttachStderr: true,
	})
	for index := range environment {
		environment[index] = ""
	}
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	process.mu.Lock()
	process.execID = created.ID
	process.mu.Unlock()
	return nil
}

func (process *execProcess) start(ctx context.Context) error {
	process.mu.Lock()
	execID := process.execID
	process.mu.Unlock()
	if execID == "" {
		if err := process.prepare(ctx); err != nil {
			return err
		}
		process.mu.Lock()
		execID = process.execID
		process.mu.Unlock()
	}
	attached, err := process.client.ExecAttach(ctx, execID, client.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	process.mu.Lock()
	process.attach = attached.Conn
	process.mu.Unlock()

	go process.collect(attached)
	return nil
}

func (process *execProcess) collect(attached client.ExecAttachResult) {
	defer close(process.done)
	defer attached.Close()

	streamErr := process.output.drain(attached.Reader)
	stdout, stderr, overflowed := process.output.result()
	exitCode, exitCodeObserved := process.inspectExitCode()
	process.mu.Lock()
	closeRequested := process.attachCloseRequested
	process.mu.Unlock()
	errorCode := processOutputError(streamErr, closeRequested, overflowed)
	if errorCode == "" && !process.containerRunning() {
		errorCode = rundomain.ProcessErrorContainerState
	}
	var fileReader rundomain.StoppedProcessFileReader
	if exitCodeObserved {
		fileReader = newStoppedProcessFileReader(process.client, process.containerID)
	}

	process.mu.Lock()
	process.result = execResult{
		stdout: stdout, stderr: stderr,
		exitCode: exitCode, exitCodeObserved: exitCodeObserved,
		errorCode: errorCode, fileReader: fileReader,
	}
	if !exitCodeObserved && process.result.errorCode == "" && !closeRequested {
		process.result.errorCode = processErrorExitInspect
	}
	process.mu.Unlock()
}

func (process *execProcess) containerRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), execInspectTimeout)
	defer cancel()
	inspection, err := process.client.ContainerInspect(ctx, process.containerID, client.ContainerInspectOptions{})
	return err == nil && inspection.Container.State != nil && inspection.Container.State.Running
}

func (process *execProcess) wait(ctx context.Context) execResult {
	select {
	case <-process.done:
		return process.snapshot()
	case <-ctx.Done():
	}

	process.closeAttach()
	<-process.done
	result := process.snapshot()
	result.timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	result.cancelled = !result.timedOut
	return result
}

func (process *execProcess) closeAttach() {
	process.mu.Lock()
	process.attachCloseRequested = true
	attach := process.attach
	process.mu.Unlock()
	if attach != nil {
		_ = attach.Close()
	}
}

// stop terminates only this Docker Exec. The Project container and sibling
// Execs remain running.
func (process *execProcess) stop(ctx context.Context, grace time.Duration) error {
	process.mu.Lock()
	execID := process.execID
	process.mu.Unlock()
	if execID == "" {
		return nil
	}
	_, stopped, err := process.waitUntilStoppable(ctx, execID)
	if err != nil {
		return err
	}
	if stopped {
		if err := process.signalPID(ctx, execID, "KILL"); err != nil {
			return err
		}
		return process.finishStopped(ctx)
	}
	if err := process.signalPID(ctx, execID, "TERM"); err != nil {
		return err
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.done:
			if err := process.signalPID(ctx, execID, "KILL"); err != nil {
				return err
			}
			return process.waitUntilStopped(ctx, execID)
		case <-deadline.C:
			if err := process.signalPID(ctx, execID, "KILL"); err != nil {
				return err
			}
			return process.waitUntilStopped(ctx, execID)
		case <-ticker.C:
			current, inspectErr := process.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
			if inspectErr != nil {
				return fmt.Errorf("inspect Project Exec after signal: %w", inspectErr)
			}
			if !current.Running {
				if err := process.signalPID(ctx, execID, "KILL"); err != nil {
					return err
				}
				return process.finishStopped(ctx)
			}
		}
	}
}

func (process *execProcess) waitUntilStopped(ctx context.Context, execID string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspection, err := process.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return fmt.Errorf("inspect Project Exec after forced termination: %w", err)
		}
		if !inspection.Running {
			return process.finishStopped(ctx)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (process *execProcess) finishStopped(ctx context.Context) error {
	process.mu.Lock()
	hasAttach := process.attach != nil
	process.mu.Unlock()
	if !hasAttach {
		return nil
	}
	process.closeAttach()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-process.done:
		return nil
	}
}

func (process *execProcess) waitUntilStoppable(ctx context.Context, execID string) (client.ExecInspectResult, bool, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspection, err := process.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return client.ExecInspectResult{}, false, fmt.Errorf("inspect Project Exec: %w", err)
		}
		if !inspection.Running {
			return inspection, true, nil
		}
		if inspection.PID > 0 {
			return inspection, false, nil
		}
		select {
		case <-ctx.Done():
			return client.ExecInspectResult{}, false, fmt.Errorf("wait for Project Exec %s PID: %w", execID, ctx.Err())
		case <-process.done:
			return inspection, true, nil
		case <-ticker.C:
		}
	}
}

func (process *execProcess) signalPID(ctx context.Context, execID, signal string) error {
	command := []string{
		"/bin/sh", "-c",
		`IFS= read -r pid < "$1" || exit 2
case "$pid" in ''|*[!0-9]*) exit 2 ;; esac
kill "-$2" "-$pid"`,
		"aether-signal", process.pidFile, signal,
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		created, err := process.client.ExecCreate(ctx, process.containerID, client.ExecCreateOptions{Cmd: command})
		if err != nil {
			return fmt.Errorf("create Project Exec %s signal helper: %w", signal, err)
		}
		if _, err := process.client.ExecStart(ctx, created.ID, client.ExecStartOptions{}); err != nil {
			return fmt.Errorf("start Project Exec %s signal helper: %w", signal, err)
		}
		exitCode, err := process.waitSignalExec(ctx, created.ID)
		if err == nil && exitCode == 0 {
			return nil
		}
		inspection, inspectErr := process.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if inspectErr != nil {
			return fmt.Errorf("inspect Project Exec after %s helper: %w", signal, inspectErr)
		}
		if !inspection.Running {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for Project Exec %s signal helper: %w", signal, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("send Project Exec %s signal: %w", signal, ctx.Err())
		case <-process.done:
			return nil
		case <-ticker.C:
		}
	}
}

func (process *execProcess) waitSignalExec(ctx context.Context, execID string) (int, error) {
	deadline := time.NewTimer(execInspectTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspection, err := process.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return 0, err
		}
		if !inspection.Running {
			return inspection.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return 0, fmt.Errorf("signal helper %s did not exit", execID)
		case <-ticker.C:
		}
	}
}

func (process *execProcess) inspectExitCode() (int, bool) {
	process.mu.Lock()
	execID := process.execID
	process.mu.Unlock()
	if execID == "" {
		return 0, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), execInspectTimeout)
	defer cancel()
	inspection, err := process.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
	if err != nil || inspection.Running {
		return 0, false
	}
	return inspection.ExitCode, true
}

func (process *execProcess) snapshot() execResult {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.result
}

func (process *execProcess) takeEnvironment() []string {
	process.mu.Lock()
	defer process.mu.Unlock()
	environment := process.environment
	process.environment = nil
	return environment
}

func environmentSlice(environment map[string]string) []string {
	result := make([]string, 0, len(environment))
	for key, value := range environment {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}
