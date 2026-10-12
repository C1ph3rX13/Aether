package run

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type runtimeTestDouble struct {
	mu       sync.Mutex
	outputs  []runtimeTestResult
	launches []LaunchSpec
}

type runtimeTestResult struct {
	output ProcessOutput
	err    error
}

type runtimeTestSnapshot struct {
	Launches []LaunchSpec
}

type runtimeTestProcess struct {
	mu        sync.Mutex
	output    ProcessOutput
	waitError error
}

func newRuntimeTestDouble() *runtimeTestDouble {
	return &runtimeTestDouble{}
}

func (runtime *runtimeTestDouble) QueueOutput(output ProcessOutput) {
	runtime.QueueResult(output, nil)
}

func (runtime *runtimeTestDouble) QueueResult(output ProcessOutput, err error) {
	runtime.mu.Lock()
	runtime.outputs = append(runtime.outputs, runtimeTestResult{output: output, err: err})
	runtime.mu.Unlock()
}

func (runtime *runtimeTestDouble) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if err := ValidateLaunchSpec(spec); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.launches = append(runtime.launches, cloneRuntimeTestLaunch(spec))
	result := runtimeTestResult{output: ProcessOutput{Exited: true}}
	if len(runtime.outputs) > 0 {
		result = runtime.outputs[0]
		runtime.outputs = runtime.outputs[1:]
	}
	return &runtimeTestProcess{output: result.output, waitError: result.err}, nil
}

func (runtime *runtimeTestDouble) Snapshot() runtimeTestSnapshot {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	launches := make([]LaunchSpec, len(runtime.launches))
	for index, launch := range runtime.launches {
		launches[index] = cloneRuntimeTestLaunch(launch)
	}
	return runtimeTestSnapshot{Launches: launches}
}

func (process *runtimeTestProcess) Wait(ctx context.Context) (ProcessOutput, error) {
	select {
	case <-ctx.Done():
		if err := process.Stop(context.Background(), time.Second); err != nil {
			return process.result(), err
		}
		return process.result(), nil
	default:
		return process.result(), process.waitError
	}
}

func (process *runtimeTestProcess) Stop(ctx context.Context, grace time.Duration) error {
	if grace <= 0 || grace > MaximumStopGrace {
		return fmt.Errorf("process stop grace is invalid")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	process.output.Cancelled = true
	if process.output.ExitCode == 0 {
		process.output.ExitCode = 130
	}
	return nil
}

func (process *runtimeTestProcess) result() ProcessOutput {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.output
}

func cloneRuntimeTestLaunch(spec LaunchSpec) LaunchSpec {
	spec.Command = append([]string(nil), spec.Command...)
	environment := spec.Environment
	spec.Environment = make(map[string]string, len(environment))
	for key, value := range environment {
		spec.Environment[key] = value
	}
	if spec.RenderedInput != nil {
		spec.RenderedInput = &RenderedInput{
			Path: spec.RenderedInput.Path, Bytes: append([]byte(nil), spec.RenderedInput.Bytes...), Hash: spec.RenderedInput.Hash,
		}
	}
	return spec
}

var _ Runtime = (*runtimeTestDouble)(nil)
var _ Process = (*runtimeTestProcess)(nil)
