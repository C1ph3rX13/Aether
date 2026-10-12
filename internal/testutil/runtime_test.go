package testutil_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
)

func TestMemoryRuntimeLaunchesConfiguredExecutionAndReturnsIsolatedState(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	spec := validLaunchSpec("run-1", "project-1")
	spec.ProtocolFile = &rundomain.ProtocolFile{InvocationID: "12345678-abcd-4abc-8def-123456789abc", Filename: "output-schema.json", Bytes: []byte(`{"type":"object"}`)}
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: spec.RunID, Phase: spec.Phase}, testutil.ExecutionBehavior{
		Output: rundomain.ProcessOutput{Stdout: "result", Stderr: "diagnostic", ExitCode: 7},
	})

	process, err := runtime.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output.Stdout != "result" || output.Stderr != "diagnostic" || output.ExitCode != 7 {
		t.Fatalf("Process output = %+v", output)
	}

	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 1 || snapshot.ActiveRunIDs[spec.RunID] == "" {
		t.Fatalf("active Runs = %+v", snapshot.ActiveRunIDs)
	}
	if len(snapshot.Launches) != 1 || snapshot.Launches[0].RunID != spec.RunID {
		t.Fatalf("launches = %+v", snapshot.Launches)
	}
	if len(snapshot.Processes) != 1 || snapshot.Processes[0].WaitCalls != 1 || snapshot.Processes[0].Output != output {
		t.Fatalf("processes = %+v", snapshot.Processes)
	}

	spec.ProtocolFile.Bytes[0] = 'x'
	snapshot.Launches[0].ProtocolFile.Bytes[0] = 'y'
	snapshot.Launches[0].ProtocolFile.InvocationID = "changed"
	snapshot.Launches[0].Command[0] = "changed"
	snapshot.Launches[0].Environment["TOKEN"] = "changed"
	snapshot.ActiveRunIDs[spec.RunID] = "changed"
	unchanged := runtime.Snapshot()
	if unchanged.Launches[0].Command[0] != "agent" || unchanged.Launches[0].Environment["TOKEN"] != "secret" ||
		unchanged.ActiveRunIDs[spec.RunID] == "changed" || unchanged.Launches[0].ProtocolFile.InvocationID != "12345678-abcd-4abc-8def-123456789abc" || string(unchanged.Launches[0].ProtocolFile.Bytes) != `{"type":"object"}` {
		t.Fatalf("Snapshot exposed mutable Runtime state: %+v", unchanged)
	}
}

func TestMemoryRuntimeUsesDefaultExecutionWithoutAKnownRunID(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	runtime.ConfigureDefaultExecution(testutil.ExecutionBehavior{
		Output: rundomain.ProcessOutput{Stdout: "default result"},
	})
	spec := validLaunchSpec("generated-at-admission", "project-1")
	process, err := runtime.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output.Stdout != "default result" {
		t.Fatalf("default Process output = %+v", output)
	}
}

func TestMemoryRuntimeProvidesOneShotFileEvidenceOnlyAfterStoppedOutput(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	spec := validLaunchSpec("stopped-file", "project-1")
	const evidencePath = "/tmp/session.jsonl"
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: spec.RunID, Phase: spec.Phase}, testutil.ExecutionBehavior{
		Output: rundomain.ProcessOutput{Exited: true}, StoppedFilePath: evidencePath, StoppedFile: []byte("evidence"),
	})
	process, err := runtime.Launch(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if output.StoppedProcessFileReader == nil {
		t.Fatal("stopped Process output omitted file evidence capability")
	}
	content, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), evidencePath)
	if err != nil || string(content) != "evidence" {
		t.Fatalf("stopped file evidence=%q err=%v", content, err)
	}
	if _, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), evidencePath); err == nil {
		t.Fatal("stopped file evidence capability was reusable")
	}
	repeated, err := process.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if repeated.StoppedProcessFileReader != output.StoppedProcessFileReader {
		t.Fatal("repeated Wait replaced the one-shot file evidence capability")
	}
	if _, err := readSingleStoppedTestFile(repeated.StoppedProcessFileReader, t.Context(), evidencePath); err == nil {
		t.Fatal("repeated Wait bypassed one-shot file evidence")
	}

	running := validLaunchSpec("unconfirmed-file", "project-1")
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: running.RunID, Phase: running.Phase}, testutil.ExecutionBehavior{
		StoppedFilePath: evidencePath, StoppedFile: []byte("unconfirmed"),
	})
	process, err = runtime.Launch(t.Context(), running)
	if err != nil {
		t.Fatal(err)
	}
	output, err = process.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if output.StoppedProcessFileReader != nil {
		t.Fatal("unconfirmed Process output exposed file evidence capability")
	}
}

func TestMemoryRuntimeFileEvidenceRejectsWrongPathAndConsumesAttempt(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	spec := validLaunchSpec("exact-stopped-file", "project-1")
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: spec.RunID, Phase: spec.Phase}, testutil.ExecutionBehavior{
		Output:          rundomain.ProcessOutput{Exited: true},
		StoppedFilePath: "/tmp/exact.jsonl", StoppedFile: []byte("evidence"),
	})
	process, err := runtime.Launch(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), "/tmp/other.jsonl"); err == nil {
		t.Fatal("wrong stopped file path was accepted")
	}
	if _, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), "/tmp/exact.jsonl"); err == nil {
		t.Fatal("failed stopped file request did not consume the one attempt")
	}
}

func TestMemoryRuntimeTracksProcessAndRunLifecycle(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	spec := validLaunchSpec("run-lifecycle", "project-1")
	process, err := runtime.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Stop(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !output.Cancelled || output.ExitCode != 130 {
		t.Fatalf("stopped Process output = %+v", output)
	}

	handle := rundomain.ExecutionHandle{
		RunID: spec.RunID, ProjectID: spec.ProjectID, Phase: spec.Phase,
		ContainerID: "container-1", ExecID: "exec-1", StartedAt: time.Unix(1, 0).UTC(),
	}
	if err := runtime.StopExecutionHandle(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteRun(context.Background(), spec.RunID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteRun(context.Background(), spec.RunID); err != nil {
		t.Fatalf("idempotent CompleteRun: %v", err)
	}

	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 0 || len(snapshot.CompletedRunIDs) != 1 || snapshot.CompletedRunIDs[0] != spec.RunID {
		t.Fatalf("Run lifecycle = %+v", snapshot)
	}
	if len(snapshot.StoppedExecutionHandles) != 1 || snapshot.StoppedExecutionHandles[0] != handle {
		t.Fatalf("stopped Execution Handles = %+v", snapshot.StoppedExecutionHandles)
	}
	if len(snapshot.Processes) != 1 || !snapshot.Processes[0].Stopped || snapshot.Processes[0].StopCalls != 1 {
		t.Fatalf("Process lifecycle = %+v", snapshot.Processes)
	}
}

func TestMemoryRuntimeStopsProcessWhenWaitIsCancelled(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	process, err := runtime.Launch(context.Background(), validLaunchSpec("run-cancelled", "project-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := process.Wait(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Cancelled || output.ExitCode != 130 {
		t.Fatalf("cancelled Wait output = %+v", output)
	}
	snapshot := runtime.Snapshot()
	if len(snapshot.Processes) != 1 || snapshot.Processes[0].WaitCalls != 1 ||
		snapshot.Processes[0].StopCalls != 1 || !snapshot.Processes[0].Stopped {
		t.Fatalf("cancelled Process state = %+v", snapshot.Processes)
	}
}

func TestMemoryRuntimePropagatesConfiguredExecutionFailures(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	launchFailure := errors.New("launch failed")
	waitFailure := errors.New("wait failed")
	stopFailure := errors.New("stop failed")

	launchSpec := validLaunchSpec("run-launch-failure", "project-1")
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: launchSpec.RunID, Phase: launchSpec.Phase}, testutil.ExecutionBehavior{
		LaunchError: launchFailure,
	})
	if _, err := runtime.Launch(context.Background(), launchSpec); !errors.Is(err, launchFailure) {
		t.Fatalf("Launch error = %v", err)
	}

	waitSpec := validLaunchSpec("run-wait-failure", "project-1")
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: waitSpec.RunID, Phase: waitSpec.Phase}, testutil.ExecutionBehavior{
		Output: rundomain.ProcessOutput{Stdout: "partial", ExitCode: 9}, WaitError: waitFailure,
	})
	waitProcess, err := runtime.Launch(context.Background(), waitSpec)
	if err != nil {
		t.Fatal(err)
	}
	waitOutput, err := waitProcess.Wait(context.Background())
	if !errors.Is(err, waitFailure) || waitOutput.Stdout != "partial" || waitOutput.ExitCode != 9 {
		t.Fatalf("Wait output = %+v, error = %v", waitOutput, err)
	}

	stopSpec := validLaunchSpec("run-stop-failure", "project-1")
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: stopSpec.RunID, Phase: stopSpec.Phase}, testutil.ExecutionBehavior{
		StopError: stopFailure,
	})
	stopProcess, err := runtime.Launch(context.Background(), stopSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := stopProcess.Stop(context.Background(), time.Second); !errors.Is(err, stopFailure) {
		t.Fatalf("Stop error = %v", err)
	}

	snapshot := runtime.Snapshot()
	if len(snapshot.FailedLaunches) != 1 || snapshot.FailedLaunches[0].RunID != launchSpec.RunID {
		t.Fatalf("failed Launches = %+v", snapshot.FailedLaunches)
	}
	if _, active := snapshot.ActiveRunIDs[launchSpec.RunID]; active {
		t.Fatalf("failed Launch became active: %+v", snapshot.ActiveRunIDs)
	}
	processes := make(map[rundomain.RunID]testutil.ProcessSnapshot, len(snapshot.Processes))
	for _, process := range snapshot.Processes {
		processes[process.RunID] = process
	}
	if processes[waitSpec.RunID].WaitCalls != 1 || processes[stopSpec.RunID].StopCalls != 1 || processes[stopSpec.RunID].Stopped {
		t.Fatalf("failure state = %+v", snapshot.Processes)
	}
}

func TestMemoryRuntimePropagatesLifecycleFailuresUntilRetrySucceeds(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	completionFailure := errors.New("completion failed")
	handleFailure := errors.New("handle stop failed")
	spec := validLaunchSpec("run-retry", "project-1")
	if err := runtime.FailCompleteRunAttempts(spec.RunID, 1, completionFailure); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteRun(context.Background(), spec.RunID); !errors.Is(err, completionFailure) {
		t.Fatalf("first CompleteRun error = %v", err)
	}
	if _, active := runtime.Snapshot().ActiveRunIDs[spec.RunID]; !active {
		t.Fatal("failed CompleteRun removed the active Run")
	}
	if err := runtime.CompleteRun(context.Background(), spec.RunID); err != nil {
		t.Fatalf("retried CompleteRun: %v", err)
	}

	handle := rundomain.ExecutionHandle{
		RunID: spec.RunID, ProjectID: spec.ProjectID, Phase: spec.Phase,
		ContainerID: "container-retry", ExecID: "exec-retry", StartedAt: time.Unix(2, 0).UTC(),
	}
	if err := runtime.FailExecutionHandleStopAttempts(handle.ExecID, 1, handleFailure); err != nil {
		t.Fatal(err)
	}
	if err := runtime.StopExecutionHandle(context.Background(), handle); !errors.Is(err, handleFailure) {
		t.Fatalf("first StopExecutionHandle error = %v", err)
	}
	if err := runtime.StopExecutionHandle(context.Background(), handle); err != nil {
		t.Fatalf("retried StopExecutionHandle: %v", err)
	}

	snapshot := runtime.Snapshot()
	if snapshot.CompleteRunAttempts[spec.RunID] != 2 || snapshot.ExecutionHandleStopAttempts[handle.ExecID] != 2 {
		t.Fatalf("lifecycle attempts = complete %+v, stop %+v",
			snapshot.CompleteRunAttempts, snapshot.ExecutionHandleStopAttempts)
	}
	if len(snapshot.CompletedRunIDs) != 1 || len(snapshot.StoppedExecutionHandles) != 1 {
		t.Fatalf("lifecycle retry state = %+v", snapshot)
	}

	unrelated := validLaunchSpec("run-without-failure", "project-2")
	if _, err := runtime.Launch(context.Background(), unrelated); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteRun(context.Background(), unrelated.RunID); err != nil {
		t.Fatalf("unrelated CompleteRun inherited failure: %v", err)
	}
	unrelatedHandle := handle
	unrelatedHandle.RunID = unrelated.RunID
	unrelatedHandle.ProjectID = unrelated.ProjectID
	unrelatedHandle.ExecID = "exec-without-failure"
	if err := runtime.StopExecutionHandle(context.Background(), unrelatedHandle); err != nil {
		t.Fatalf("unrelated StopExecutionHandle inherited failure: %v", err)
	}
}

func TestMemoryRuntimeHasDeterministicConcurrentState(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()

	const runCount = 32
	specs := make([]rundomain.LaunchSpec, runCount)
	for index := range specs {
		runID := fmt.Sprintf("run-%02d", index)
		specs[index] = validLaunchSpec(runID, "project-"+runID)
		runtime.ConfigureExecution(
			testutil.ExecutionKey{RunID: specs[index].RunID, Phase: specs[index].Phase},
			testutil.ExecutionBehavior{Output: rundomain.ProcessOutput{Stdout: "output-" + runID}},
		)
	}

	errorsSeen := make(chan error, runCount)
	var group sync.WaitGroup
	for index := range specs {
		spec := specs[index]
		group.Add(1)
		go func() {
			defer group.Done()
			process, err := runtime.Launch(context.Background(), spec)
			if err != nil {
				errorsSeen <- err
				return
			}
			output, err := process.Wait(context.Background())
			if err != nil {
				errorsSeen <- err
				return
			}
			if output.Stdout != "output-"+string(spec.RunID) {
				errorsSeen <- fmt.Errorf("Run %s output = %+v", spec.RunID, output)
				return
			}
			if err := runtime.CompleteRun(context.Background(), spec.RunID); err != nil {
				errorsSeen <- err
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 0 || len(snapshot.Launches) != runCount ||
		len(snapshot.Processes) != runCount || len(snapshot.CompletedRunIDs) != runCount {
		t.Fatalf("concurrent Runtime state = %+v", snapshot)
	}
	for index, spec := range specs {
		if snapshot.Launches[index].RunID != spec.RunID || snapshot.Processes[index].RunID != spec.RunID ||
			snapshot.CompletedRunIDs[index] != spec.RunID {
			t.Fatalf("snapshot index %d = launch %s, Process %s, completed %s; want %s",
				index, snapshot.Launches[index].RunID, snapshot.Processes[index].RunID,
				snapshot.CompletedRunIDs[index], spec.RunID)
		}
	}
}

func TestMemoryRuntimeSerializesContendedRunProcessAndSnapshotOperations(t *testing.T) {
	runtime := testutil.NewMemoryRuntime()
	spec := validLaunchSpec("contended-run", "project-1")
	runtime.ConfigureExecution(
		testutil.ExecutionKey{RunID: spec.RunID, Phase: spec.Phase},
		testutil.ExecutionBehavior{Output: rundomain.ProcessOutput{Stdout: "contended result"}},
	)

	const operationCount = 24
	processes := make(chan rundomain.Process, operationCount)
	errorsSeen := make(chan error, operationCount*4)
	stopSnapshots := make(chan struct{})
	snapshotDone := make(chan struct{})
	go func() {
		defer close(snapshotDone)
		for {
			select {
			case <-stopSnapshots:
				return
			default:
				snapshot := runtime.Snapshot()
				if len(snapshot.Launches) != len(snapshot.Processes) || len(snapshot.CompletedRunIDs) > 1 {
					errorsSeen <- fmt.Errorf("inconsistent concurrent Snapshot: %+v", snapshot)
					return
				}
			}
		}
	}()

	var launchGroup sync.WaitGroup
	for range operationCount {
		launchGroup.Add(1)
		go func() {
			defer launchGroup.Done()
			process, err := runtime.Launch(context.Background(), spec)
			if err != nil {
				errorsSeen <- err
				return
			}
			output, err := process.Wait(context.Background())
			if err != nil || output.Stdout != "contended result" {
				errorsSeen <- fmt.Errorf("contended Wait output=%+v error=%v", output, err)
				return
			}
			processes <- process
		}()
	}
	launchGroup.Wait()
	close(processes)

	firstProcess := <-processes
	var lifecycleGroup sync.WaitGroup
	for range operationCount {
		lifecycleGroup.Add(2)
		go func() {
			defer lifecycleGroup.Done()
			if err := firstProcess.Stop(context.Background(), time.Second); err != nil {
				errorsSeen <- err
			}
		}()
		go func() {
			defer lifecycleGroup.Done()
			if err := runtime.CompleteRun(context.Background(), spec.RunID); err != nil {
				errorsSeen <- err
			}
		}()
	}
	lifecycleGroup.Wait()
	close(stopSnapshots)
	<-snapshotDone
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 0 || len(snapshot.Launches) != operationCount ||
		len(snapshot.Processes) != operationCount || len(snapshot.CompletedRunIDs) != 1 ||
		snapshot.CompleteRunAttempts[spec.RunID] != operationCount {
		t.Fatalf("contended Runtime state = %+v", snapshot)
	}
	stopCalls := 0
	for _, process := range snapshot.Processes {
		stopCalls += process.StopCalls
	}
	if stopCalls != operationCount {
		t.Fatalf("contended Stop calls = %d, want %d", stopCalls, operationCount)
	}
}

func validLaunchSpec(runID, projectID string) rundomain.LaunchSpec {
	yaml := []byte("schema: aether.blackboard/v1\ngeneration: 1\nrevision: 1\n")
	digest := sha256.Sum256(yaml)
	id := rundomain.RunID(runID)
	return rundomain.LaunchSpec{
		RunID: id, ProjectID: projectID, Task: rundomain.TaskReason, Phase: rundomain.PhaseReason, FencingToken: 1,
		ConfigRevision: "config-1", Image: "aether:test",
		Command:       []string{"agent", "run"},
		Environment:   map[string]string{"TOKEN": "secret"},
		RenderedInput: &rundomain.RenderedInput{Path: rundomain.BlackboardPath(id, rundomain.PhaseReason), Bytes: yaml, Hash: rundomain.RenderedInputHash(fmt.Sprintf("%x", digest[:]))},
		PhaseTimeout:  time.Minute, StopGrace: 5 * time.Second,
	}
}

func readSingleStoppedTestFile(reader rundomain.StoppedProcessFileReader, ctx context.Context, requestedPath string) ([]byte, error) {
	result := reader.ReadFiles(ctx, []string{requestedPath})[requestedPath]
	return result.Bytes, result.Err
}
