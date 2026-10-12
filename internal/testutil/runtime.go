package testutil

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

const memoryStoppedProcessFileMaximumBytes = int64(8 << 20)

// ExecutionKey identifies one Run execution phase for deterministic test setup.
type ExecutionKey struct {
	RunID rundomain.RunID
	Phase rundomain.ExecutionPhase
}

// ExecutionBehavior describes the observable result of one execution phase.
type ExecutionBehavior struct {
	Output          rundomain.ProcessOutput
	StoppedFilePath string
	StoppedFile     []byte
	StoppedFiles    map[string][]byte
	LaunchError     error
	WaitError       error
	StopError       error
}

// MemoryRuntime is the shared in-memory Runtime adapter for backend tests.
type MemoryRuntime struct {
	mu                 sync.Mutex
	activeRuns         map[rundomain.RunID]*memoryRun
	behaviors          map[ExecutionKey]ExecutionBehavior
	defaultBehavior    ExecutionBehavior
	launchCounts       map[ExecutionKey]int
	launches           []rundomain.LaunchSpec
	failed             []rundomain.LaunchSpec
	processes          []*memoryProcess
	completed          []rundomain.RunID
	stopped            []rundomain.ExecutionHandle
	completeFailures   map[rundomain.RunID]failureBehavior
	handleStopFailures map[string]failureBehavior
	completeAttempts   map[rundomain.RunID]int
	handleStopAttempts map[string]int
}

type failureBehavior struct {
	attempts int
	err      error
}

type memoryRun struct {
	id      string
	binding memoryRunBinding
}

type memoryRunBinding struct {
	ProjectID         string
	Task              rundomain.TaskType
	FencingToken      rundomain.FencingToken
	ConfigRevision    string
	Image             string
	RenderedInputHash rundomain.RenderedInputHash
}

type memoryProcess struct {
	mu           sync.Mutex
	id           string
	runID        rundomain.RunID
	phase        rundomain.ExecutionPhase
	output       rundomain.ProcessOutput
	waitError    error
	stopError    error
	stoppedPath  string
	stoppedFile  []byte
	stoppedFiles map[string][]byte
	fileReader   rundomain.StoppedProcessFileReader
	waitCalls    int
	stopCalls    int
	stopped      bool
}

// RuntimeSnapshot is an isolated view of MemoryRuntime state.
type RuntimeSnapshot struct {
	ActiveRunIDs                map[rundomain.RunID]string
	Launches                    []rundomain.LaunchSpec
	FailedLaunches              []rundomain.LaunchSpec
	Processes                   []ProcessSnapshot
	CompletedRunIDs             []rundomain.RunID
	StoppedExecutionHandles     []rundomain.ExecutionHandle
	CompleteRunAttempts         map[rundomain.RunID]int
	ExecutionHandleStopAttempts map[string]int
}

// ProcessSnapshot reports the externally relevant state of one launched Process.
type ProcessSnapshot struct {
	ID        string
	RunID     rundomain.RunID
	Phase     rundomain.ExecutionPhase
	Output    rundomain.ProcessOutput
	WaitCalls int
	StopCalls int
	Stopped   bool
}

func NewMemoryRuntime() *MemoryRuntime {
	return &MemoryRuntime{
		activeRuns:         make(map[rundomain.RunID]*memoryRun),
		behaviors:          make(map[ExecutionKey]ExecutionBehavior),
		launchCounts:       make(map[ExecutionKey]int),
		completeFailures:   make(map[rundomain.RunID]failureBehavior),
		defaultBehavior:    ExecutionBehavior{Output: rundomain.ProcessOutput{Exited: true}},
		handleStopFailures: make(map[string]failureBehavior),
		completeAttempts:   make(map[rundomain.RunID]int),
		handleStopAttempts: make(map[string]int),
	}
}

func (runtime *MemoryRuntime) ConfigureExecution(key ExecutionKey, behavior ExecutionBehavior) {
	runtime.mu.Lock()
	runtime.behaviors[key] = behavior
	runtime.mu.Unlock()
}

func (runtime *MemoryRuntime) ConfigureDefaultExecution(behavior ExecutionBehavior) {
	runtime.mu.Lock()
	runtime.defaultBehavior = behavior
	runtime.mu.Unlock()
}

func (runtime *MemoryRuntime) FailCompleteRunAttempts(runID rundomain.RunID, attempts int, failure error) error {
	if runID == "" || len(runID) > 256 || attempts <= 0 || failure == nil {
		return fmt.Errorf("CompleteRun failure injection is invalid")
	}
	runtime.mu.Lock()
	runtime.completeFailures[runID] = failureBehavior{attempts: attempts, err: failure}
	runtime.mu.Unlock()
	return nil
}

func (runtime *MemoryRuntime) FailExecutionHandleStopAttempts(execID string, attempts int, failure error) error {
	if execID == "" || len(execID) > 512 || attempts <= 0 || failure == nil {
		return fmt.Errorf("StopExecutionHandle failure injection is invalid")
	}
	runtime.mu.Lock()
	runtime.handleStopFailures[execID] = failureBehavior{attempts: attempts, err: failure}
	runtime.mu.Unlock()
	return nil
}

func (runtime *MemoryRuntime) Launch(ctx context.Context, spec rundomain.LaunchSpec) (rundomain.Process, error) {
	if err := rundomain.ValidateLaunchSpec(spec); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	behavior := runtime.defaultBehavior
	if configured, ok := runtime.behaviors[ExecutionKey{RunID: spec.RunID, Phase: spec.Phase}]; ok {
		behavior = configured
	}
	if behavior.LaunchError != nil {
		runtime.failed = append(runtime.failed, cloneLaunchSpec(spec))
		return nil, behavior.LaunchError
	}
	binding := bindingFor(spec)
	active := runtime.activeRuns[spec.RunID]
	if active == nil {
		active = &memoryRun{id: "memory-execution-" + string(spec.RunID), binding: binding}
		runtime.activeRuns[spec.RunID] = active
	} else if active.binding != binding {
		return nil, fmt.Errorf("RunID %s is already bound to another memory execution", spec.RunID)
	}
	runtime.launches = append(runtime.launches, cloneLaunchSpec(spec))
	key := ExecutionKey{RunID: spec.RunID, Phase: spec.Phase}
	runtime.launchCounts[key]++
	process := &memoryProcess{
		id:    fmt.Sprintf("memory-process-%s-%s-%d", spec.RunID, spec.Phase, runtime.launchCounts[key]),
		runID: spec.RunID, phase: spec.Phase,
		output: behavior.Output, stoppedPath: behavior.StoppedFilePath,
		stoppedFile:  append([]byte(nil), behavior.StoppedFile...),
		stoppedFiles: cloneStoppedFiles(behavior.StoppedFiles),
		waitError:    behavior.WaitError, stopError: behavior.StopError,
	}
	runtime.processes = append(runtime.processes, process)
	return process, nil
}

func (runtime *MemoryRuntime) CompleteRun(ctx context.Context, runID rundomain.RunID) error {
	if runID == "" || len(runID) > 256 {
		return fmt.Errorf("Run completion identity is invalid")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.completeAttempts[runID]++
	failure := runtime.completeFailures[runID]
	if runtime.completeAttempts[runID] <= failure.attempts {
		return failure.err
	}
	if _, active := runtime.activeRuns[runID]; active {
		delete(runtime.activeRuns, runID)
		runtime.completed = append(runtime.completed, runID)
	}
	return nil
}

func (runtime *MemoryRuntime) StopExecutionHandle(ctx context.Context, handle rundomain.ExecutionHandle) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.handleStopAttempts[handle.ExecID]++
	failure := runtime.handleStopFailures[handle.ExecID]
	if runtime.handleStopAttempts[handle.ExecID] <= failure.attempts {
		return failure.err
	}
	for _, stopped := range runtime.stopped {
		if stopped.ExecID == handle.ExecID {
			return nil
		}
	}
	runtime.stopped = append(runtime.stopped, handle)
	return nil
}

func (runtime *MemoryRuntime) Snapshot() RuntimeSnapshot {
	runtime.mu.Lock()
	activeRuns := make(map[rundomain.RunID]string, len(runtime.activeRuns))
	for runID, active := range runtime.activeRuns {
		activeRuns[runID] = active.id
	}
	launches := make([]rundomain.LaunchSpec, len(runtime.launches))
	for index, launch := range runtime.launches {
		launches[index] = cloneLaunchSpec(launch)
	}
	failed := make([]rundomain.LaunchSpec, len(runtime.failed))
	for index, launch := range runtime.failed {
		failed[index] = cloneLaunchSpec(launch)
	}
	processes := append([]*memoryProcess(nil), runtime.processes...)
	completed := append([]rundomain.RunID(nil), runtime.completed...)
	stopped := append([]rundomain.ExecutionHandle(nil), runtime.stopped...)
	completeAttempts := make(map[rundomain.RunID]int, len(runtime.completeAttempts))
	for runID, attempts := range runtime.completeAttempts {
		completeAttempts[runID] = attempts
	}
	handleStopAttempts := make(map[string]int, len(runtime.handleStopAttempts))
	for execID, attempts := range runtime.handleStopAttempts {
		handleStopAttempts[execID] = attempts
	}
	runtime.mu.Unlock()

	processSnapshots := make([]ProcessSnapshot, len(processes))
	for index, process := range processes {
		processSnapshots[index] = process.snapshot()
	}
	sort.Slice(launches, func(first, second int) bool {
		return launchSpecLess(launches[first], launches[second])
	})
	sort.Slice(failed, func(first, second int) bool {
		return launchSpecLess(failed[first], failed[second])
	})
	sort.Slice(processSnapshots, func(first, second int) bool {
		if processSnapshots[first].RunID != processSnapshots[second].RunID {
			return processSnapshots[first].RunID < processSnapshots[second].RunID
		}
		if phaseOrder(processSnapshots[first].Phase) != phaseOrder(processSnapshots[second].Phase) {
			return phaseOrder(processSnapshots[first].Phase) < phaseOrder(processSnapshots[second].Phase)
		}
		return processSnapshots[first].ID < processSnapshots[second].ID
	})
	sort.Slice(completed, func(first, second int) bool { return completed[first] < completed[second] })
	sort.Slice(stopped, func(first, second int) bool { return stopped[first].ExecID < stopped[second].ExecID })
	return RuntimeSnapshot{
		ActiveRunIDs: activeRuns, Launches: launches, FailedLaunches: failed, Processes: processSnapshots,
		CompletedRunIDs: completed, StoppedExecutionHandles: stopped,
		CompleteRunAttempts: completeAttempts, ExecutionHandleStopAttempts: handleStopAttempts,
	}
}

func launchSpecLess(first, second rundomain.LaunchSpec) bool {
	if first.RunID != second.RunID {
		return first.RunID < second.RunID
	}
	if phaseOrder(first.Phase) != phaseOrder(second.Phase) {
		return phaseOrder(first.Phase) < phaseOrder(second.Phase)
	}
	return first.ProjectID < second.ProjectID
}

func phaseOrder(phase rundomain.ExecutionPhase) int {
	switch phase {
	case rundomain.PhaseReasonFinalization, rundomain.PhaseExploreConclude, rundomain.PhaseExploreRecovery,
		rundomain.PhaseExploreConcludeRecovery, rundomain.PhaseBootstrapConclude, rundomain.PhaseBootstrapRecovery:
		return 1
	default:
		return 0
	}
}

func (process *memoryProcess) Wait(ctx context.Context) (rundomain.ProcessOutput, error) {
	process.mu.Lock()
	process.waitCalls++
	process.mu.Unlock()
	select {
	case <-ctx.Done():
		err := process.Stop(context.Background(), time.Second)
		output, _ := process.result()
		return output, err
	default:
		return process.result()
	}
}

func (process *memoryProcess) Stop(ctx context.Context, grace time.Duration) error {
	if grace <= 0 || grace > rundomain.MaximumStopGrace {
		return fmt.Errorf("process stop grace is invalid")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	process.stopCalls++
	if process.stopError != nil {
		return process.stopError
	}
	process.stopped = true
	process.output.Exited = true
	process.output.Cancelled = true
	if process.output.ExitCode == 0 {
		process.output.ExitCode = 130
	}
	return nil
}

func (process *memoryProcess) snapshot() ProcessSnapshot {
	process.mu.Lock()
	defer process.mu.Unlock()
	return ProcessSnapshot{
		ID: process.id, RunID: process.runID, Phase: process.phase, Output: process.output,
		WaitCalls: process.waitCalls, StopCalls: process.stopCalls, Stopped: process.stopped,
	}
}

func (process *memoryProcess) result() (rundomain.ProcessOutput, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	output := process.output
	if output.Exited {
		if process.fileReader == nil {
			process.fileReader = &memoryStoppedProcessFileReader{
				path: process.stoppedPath, content: append([]byte(nil), process.stoppedFile...), files: cloneStoppedFiles(process.stoppedFiles),
			}
		}
		output.StoppedProcessFileReader = process.fileReader
	}
	return output, process.waitError
}

func bindingFor(spec rundomain.LaunchSpec) memoryRunBinding {
	return memoryRunBinding{
		ProjectID: spec.ProjectID, Task: spec.Task, FencingToken: spec.FencingToken,
		ConfigRevision: spec.ConfigRevision, Image: spec.Image, RenderedInputHash: spec.RenderedInput.Hash,
	}
}

func cloneLaunchSpec(spec rundomain.LaunchSpec) rundomain.LaunchSpec {
	spec.Command = append([]string(nil), spec.Command...)
	environment := spec.Environment
	spec.Environment = make(map[string]string, len(environment))
	for key, value := range environment {
		spec.Environment[key] = value
	}
	if spec.RenderedInput != nil {
		spec.RenderedInput = &rundomain.RenderedInput{
			Path: spec.RenderedInput.Path, Bytes: append([]byte(nil), spec.RenderedInput.Bytes...), Hash: spec.RenderedInput.Hash,
		}
	}
	if spec.ProtocolFile != nil {
		file := *spec.ProtocolFile
		file.Bytes = append([]byte(nil), file.Bytes...)
		spec.ProtocolFile = &file
	}
	return spec
}

type memoryStoppedProcessFileReader struct {
	mu      sync.Mutex
	path    string
	content []byte
	files   map[string][]byte
	used    bool
}

func (reader *memoryStoppedProcessFileReader) ReadFiles(ctx context.Context, requestedPaths []string) map[string]rundomain.StoppedProcessFile {
	results := make(map[string]rundomain.StoppedProcessFile, len(requestedPaths))
	reader.mu.Lock()
	if reader.used {
		reader.mu.Unlock()
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
		}
		return results
	}
	reader.used = true
	reader.mu.Unlock()
	if len(requestedPaths) == 0 || len(requestedPaths) > 2 {
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
		}
		return results
	}
	if ctx == nil {
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
		}
		return results
	}
	seen := make(map[string]struct{}, len(requestedPaths))
	for _, requestedPath := range requestedPaths {
		if requestedPath == "" || strings.IndexByte(requestedPath, 0) >= 0 || !path.IsAbs(requestedPath) ||
			path.Clean(requestedPath) != requestedPath || strings.HasSuffix(requestedPath, "/") {
			for _, path := range requestedPaths {
				results[path] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
			}
			return results
		}
		if _, duplicate := seen[requestedPath]; duplicate {
			for _, path := range requestedPaths {
				results[path] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
			}
			return results
		}
		seen[requestedPath] = struct{}{}
	}
	select {
	case <-ctx.Done():
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: ctx.Err()}
		}
		return results
	default:
	}
	for _, requestedPath := range requestedPaths {
		content, ok := reader.files[requestedPath]
		if !ok && requestedPath == reader.path {
			content, ok = reader.content, len(reader.content) > 0
		}
		if !ok || len(content) == 0 {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
			continue
		}
		if int64(len(content)) > memoryStoppedProcessFileMaximumBytes {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: fmt.Errorf("stopped Process file evidence unavailable")}
			continue
		}
		results[requestedPath] = rundomain.StoppedProcessFile{Bytes: append([]byte(nil), content...)}
	}
	return results
}

func cloneStoppedFiles(files map[string][]byte) map[string][]byte {
	if len(files) == 0 {
		return nil
	}
	result := make(map[string][]byte, len(files))
	for path, content := range files {
		result[path] = append([]byte(nil), content...)
	}
	return result
}

var _ rundomain.Runtime = (*MemoryRuntime)(nil)
var _ rundomain.Process = (*memoryProcess)(nil)
