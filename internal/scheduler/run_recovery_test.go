package scheduler

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type recoveryTestTrace struct {
	mu    sync.Mutex
	steps []string
}

func (trace *recoveryTestTrace) add(step string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.steps = append(trace.steps, step)
}

type recoveryTestControl struct {
	trace     *recoveryTestTrace
	recovered []rundomain.RunID
	handles   []rundomain.ExecutionHandle
	records   []rundomain.RecoveryRecord
	marked    []rundomain.ExecutionHandle
	listAuth  rundomain.ExecutionHandleListRequest
	stopAuth  []rundomain.ExecutionHandleStopRequest
	markErr   error
}

func (control *recoveryTestControl) RecoverRuns(context.Context, rundomain.RecoveryOptions) ([]rundomain.RunID, error) {
	control.trace.add("fence")
	return append([]rundomain.RunID(nil), control.recovered...), nil
}

func (control *recoveryTestControl) ListExecutionHandles(_ context.Context, request rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error) {
	control.trace.add("list")
	control.listAuth = request
	return append([]rundomain.ExecutionHandle(nil), control.handles...), nil
}

func (control *recoveryTestControl) MarkExecutionHandleStopped(_ context.Context, request rundomain.ExecutionHandleStopRequest) error {
	control.trace.add("mark:" + request.Handle.ExecID)
	control.marked = append(control.marked, request.Handle)
	control.stopAuth = append(control.stopAuth, request)
	return control.markErr
}

func (control *recoveryTestControl) RecordRecoveryEvent(_ context.Context, record rundomain.RecoveryRecord) error {
	control.trace.add("record:" + string(record.Status))
	control.records = append(control.records, record)
	return nil
}

type recoveryTestRuntime struct {
	trace   *recoveryTestTrace
	stopErr error
	stopped []rundomain.ExecutionHandle
}

func (*recoveryTestRuntime) CompleteRun(context.Context, rundomain.RunID) error { return nil }

func (runtime *recoveryTestRuntime) StopExecutionHandle(_ context.Context, handle rundomain.ExecutionHandle) error {
	runtime.trace.add("stop:" + handle.ExecID)
	runtime.stopped = append(runtime.stopped, handle)
	return runtime.stopErr
}

func TestRunRecoveryFencesBeforeStoppingPersistedExecs(t *testing.T) {
	trace := &recoveryTestTrace{}
	handle := rundomain.ExecutionHandle{RunID: "run-old", ProjectID: "project", ContainerID: "container", ExecID: "exec", Phase: rundomain.PhaseReason}
	control := &recoveryTestControl{trace: trace, recovered: []rundomain.RunID{"run-old"}, handles: []rundomain.ExecutionHandle{handle}}
	runtime := &recoveryTestRuntime{trace: trace}
	events := make(chan CoordinatorEvent, 2)
	recovery, err := NewRunRecovery(control, runtime,
		rundomain.DispatcherLease{InstanceID: "dispatcher", FencingToken: 7}, events)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recovery.Recover(t.Context(), "dispatcher startup")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovered, []rundomain.RunID{"run-old"}) || !reflect.DeepEqual(runtime.stopped, []rundomain.ExecutionHandle{handle}) ||
		!reflect.DeepEqual(control.marked, []rundomain.ExecutionHandle{handle}) {
		t.Fatalf("recovered=%v stopped=%+v marked=%+v", recovered, runtime.stopped, control.marked)
	}
	if control.listAuth.DispatcherID != "dispatcher" || control.listAuth.DispatcherFencingToken != 7 ||
		len(control.stopAuth) != 1 || control.stopAuth[0].DispatcherFencingToken != 7 {
		t.Fatalf("recovery handle authorization: list=%+v stop=%+v", control.listAuth, control.stopAuth)
	}
	trace.mu.Lock()
	steps := append([]string(nil), trace.steps...)
	trace.mu.Unlock()
	if !reflect.DeepEqual(steps, []string{"fence", "list", "stop:exec", "mark:exec", "record:disposed"}) {
		t.Fatalf("recovery order: %v", steps)
	}
	if event := (<-events).(RecoveryEvent); event.Status != rundomain.RecoveryDisposed || event.Run.RunID != "run-old" {
		t.Fatalf("recovery event: %+v", event)
	}
}

func TestRunRecoveryReportsExecStopFailureWithoutMarkingStopped(t *testing.T) {
	trace := &recoveryTestTrace{}
	stopErr := errors.New("Docker unavailable")
	handle := rundomain.ExecutionHandle{RunID: "run-old", ProjectID: "project", ContainerID: "container", ExecID: "exec", Phase: rundomain.PhaseReason}
	control := &recoveryTestControl{trace: trace, recovered: []rundomain.RunID{"run-old"}, handles: []rundomain.ExecutionHandle{handle}}
	runtime := &recoveryTestRuntime{trace: trace, stopErr: stopErr}
	events := make(chan CoordinatorEvent, 2)
	recovery, err := NewRunRecovery(control, runtime,
		rundomain.DispatcherLease{InstanceID: "dispatcher", FencingToken: 8}, events)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Recover(t.Context(), "dispatcher startup"); !errors.Is(err, stopErr) {
		t.Fatalf("recovery error: %v", err)
	}
	if len(control.marked) != 0 || len(control.records) != 1 || control.records[0].Status != rundomain.RecoveryDisposeFailed {
		t.Fatalf("marked=%+v records=%+v", control.marked, control.records)
	}
}

func TestRunRecoveryWithoutActiveExecsCompletesIdempotently(t *testing.T) {
	trace := &recoveryTestTrace{}
	control := &recoveryTestControl{trace: trace, recovered: []rundomain.RunID{"run-gone"}}
	runtime := &recoveryTestRuntime{trace: trace}
	events := make(chan CoordinatorEvent, 2)
	recovery, err := NewRunRecovery(control, runtime,
		rundomain.DispatcherLease{InstanceID: "dispatcher", FencingToken: 9}, events)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Recover(t.Context(), "dispatcher startup"); err != nil {
		t.Fatal(err)
	}
	if len(runtime.stopped) != 0 || len(control.records) != 1 || control.records[0].Status != rundomain.RecoveryDisposed {
		t.Fatalf("stopped=%+v records=%+v", runtime.stopped, control.records)
	}
}

func TestRunRecoveryKeepsCleanupPendingWhenStopAcknowledgementFails(t *testing.T) {
	trace := &recoveryTestTrace{}
	markErr := errors.New("stop acknowledgement unavailable")
	handle := rundomain.ExecutionHandle{RunID: "run-old", ProjectID: "project", ContainerID: "container", ExecID: "exec", Phase: rundomain.PhaseReason}
	control := &recoveryTestControl{trace: trace, recovered: []rundomain.RunID{"run-old"}, handles: []rundomain.ExecutionHandle{handle}, markErr: markErr}
	runtime := &recoveryTestRuntime{trace: trace}
	recovery, err := NewRunRecovery(control, runtime, rundomain.DispatcherLease{InstanceID: "dispatcher", FencingToken: 8}, make(chan CoordinatorEvent, 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Recover(t.Context(), "dispatcher startup"); !errors.Is(err, markErr) {
		t.Fatalf("recovery error: %v", err)
	}
	if len(control.records) != 1 || control.records[0].Status != rundomain.RecoveryDisposeFailed {
		t.Fatalf("unacknowledged cleanup marked disposed: %+v", control.records)
	}
}
