package run_test

import (
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestExecutionHandleRecordingChecksRunAndDispatcherFencingIndependently(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "execution handle", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	handle := rundomain.ExecutionHandle{
		RunID: started.Run.ID, ProjectID: started.Run.ProjectID, Phase: rundomain.PhaseReason,
		ContainerID: "container-1", ExecID: "exec-1", StartedAt: time.Now().UTC(),
	}
	request := rundomain.ExecutionHandleRecordRequest{
		Handle: handle, RunFencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}

	wrongDispatcher := request
	wrongDispatcher.DispatcherFencingToken++
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		return runService.RecordExecutionHandle(t.Context(), wrongDispatcher)
	})
	wrongRun := request
	wrongRun.RunFencingToken++
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		return runService.RecordExecutionHandle(t.Context(), wrongRun)
	})
	if err := runService.RecordExecutionHandle(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := runService.RecordExecutionHandle(t.Context(), request); err != nil {
		t.Fatalf("idempotent record: %v", err)
	}
	listRequest := rundomain.ExecutionHandleListRequest{
		RunIDs: []rundomain.RunID{started.Run.ID}, DispatcherID: started.Run.DispatcherID,
		DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}
	wrongList := listRequest
	wrongList.DispatcherFencingToken++
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, listErr := runService.ListExecutionHandles(t.Context(), wrongList)
		return listErr
	})
	handles, err := runService.ListExecutionHandles(t.Context(), listRequest)
	if err != nil || len(handles) != 1 || handles[0].ExecID != handle.ExecID {
		t.Fatalf("handles=%+v err=%v", handles, err)
	}
	stopRequest := rundomain.ExecutionHandleStopRequest{
		Handle: handle, DispatcherID: started.Run.DispatcherID,
		DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}
	wrongStop := stopRequest
	wrongStop.DispatcherFencingToken++
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		return runService.MarkExecutionHandleStopped(t.Context(), wrongStop)
	})
	if err := runService.MarkExecutionHandleStopped(t.Context(), stopRequest); err != nil {
		t.Fatal(err)
	}
	handles, err = runService.ListExecutionHandles(t.Context(), listRequest)
	if err != nil || len(handles) != 0 {
		t.Fatalf("stopped handles=%+v err=%v", handles, err)
	}
}

func TestRunContinuationAuthorizationRechecksCurrentRunAndProjectState(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "conclude authorization", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	request := rundomain.RunContinuationRequest{
		RunID: started.Run.ID, ProjectID: started.Run.ProjectID,
		FencingToken: started.Run.FencingToken, DispatcherID: started.Run.DispatcherID,
		DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}
	if err := runService.AuthorizeRunContinuation(t.Context(), request); err != nil {
		t.Fatalf("authorize active Run: %v", err)
	}
	stale := request
	stale.DispatcherFencingToken++
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		return runService.AuthorizeRunContinuation(t.Context(), stale)
	})
	if _, err := projectService.PauseProject(t.Context(), project.Project.ID, ProjectLifecycleRequest{ExpectedRevision: project.Project.Revision}); err != nil {
		t.Fatal(err)
	}
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		return runService.AuthorizeRunContinuation(t.Context(), request)
	})
}
