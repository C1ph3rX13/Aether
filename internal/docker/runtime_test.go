package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type fakeRunDaemon struct {
	server *httptest.Server

	mu                  sync.Mutex
	containers          map[string]*fakeDaemonContainer
	createBody          dockertypes.CreateRequest
	createCount         int
	inputCount          int
	inputs              map[string][]byte
	inputTargets        map[string]string
	inputPaths          map[string]string
	inputModes          map[string]int64
	fileArchive         []byte
	fileArchives        map[string][]byte
	fileArchivePaths    []string
	blockFileArchive    bool
	fileArchiveReady    chan struct{}
	fileArchiveReadyOne sync.Once
	fileArchiveGo       chan struct{}
	fileArchiveGoOne    sync.Once
	execCount           int
	execStartCount      int
	execRunning         map[string]bool
	execPIDFiles        map[string]string
	execRelease         map[string]chan struct{}
	execReleased        map[string]bool
	execPIDHidden       int
	controlExecs        map[string]*fakeControlExec
	controlCount        int
	signalCommands      [][]string
	execCommand         []string
	execEnv             []string
	execTargets         []string
	execEnvs            [][]string
	stopIDs             []string
	startIDs            []string
	killIDs             []string
	removeIDs           []string
	removeVolumes       []bool
	removeAttempts      int
	listLimits          []int
	listBefore          []string
	requestPaths        []string
	blockExec           bool
	holdAttachAfterStop bool
	failExec            bool
	failExecInspect     bool
	failInput           bool
	failStartupPath     string
	failStart           bool
	failRemove          bool
	missingImage        bool
	blockCreate         bool
	createReady         chan struct{}
	createOnce          sync.Once
	createGo            chan struct{}
	createGoOnce        sync.Once
	execStdout          string
	execStderr          string
	execChunk           int
	execWriteDone       chan error
}

type fakeDaemonContainer struct {
	id      string
	name    string
	state   string
	labels  map[string]string
	running bool
}

type fakeControlExec struct {
	command  []string
	running  bool
	exitCode int
}

func newFakeRunDaemon(t *testing.T, blockExec bool) *fakeRunDaemon {
	t.Helper()
	daemon := &fakeRunDaemon{
		containers: make(map[string]*fakeDaemonContainer), blockExec: blockExec,
		inputs: make(map[string][]byte), inputTargets: make(map[string]string),
		inputPaths: make(map[string]string), inputModes: make(map[string]int64),
		execRunning: make(map[string]bool), execPIDFiles: make(map[string]string),
		execRelease: make(map[string]chan struct{}), execReleased: make(map[string]bool),
		controlExecs: make(map[string]*fakeControlExec),
		createReady:  make(chan struct{}), createGo: make(chan struct{}),
		fileArchiveReady: make(chan struct{}), fileArchiveGo: make(chan struct{}),
		execStdout: "phase complete\n",
	}
	daemon.server = httptest.NewServer(http.HandlerFunc(daemon.serveHTTP))
	t.Cleanup(func() {
		daemon.release()
		daemon.releaseCreation()
		daemon.releaseFileArchive()
		daemon.server.Close()
	})
	return daemon
}

func (d *fakeRunDaemon) serveHTTP(w http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	d.mu.Lock()
	d.requestPaths = append(d.requestPaths, request.Method+" "+path)
	d.mu.Unlock()
	switch {
	case (request.Method == http.MethodHead || request.Method == http.MethodGet) && path == "/_ping":
		w.Header().Set("API-Version", "1.51")
		w.WriteHeader(http.StatusOK)
		if request.Method == http.MethodGet {
			_, _ = io.WriteString(w, "OK")
		}

	case request.Method == http.MethodPost && strings.HasSuffix(path, "/containers/create"):
		var body dockertypes.CreateRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		d.createCount++
		id := fmt.Sprintf("container-%d", d.createCount)
		d.createBody = body
		missingImage := d.missingImage
		if !missingImage {
			d.containers[id] = &fakeDaemonContainer{
				id: id, name: request.URL.Query().Get("name"), state: "created", labels: body.Config.Labels,
			}
		}
		blockCreate := d.blockCreate
		d.mu.Unlock()
		if missingImage {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such image: agent:missing"}`)
			return
		}
		if blockCreate {
			d.createOnce.Do(func() { close(d.createReady) })
			<-d.createGo
		}
		writeJSON(w, dockertypes.CreateResponse{ID: id})

	case request.Method == http.MethodPost && strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/start"):
		id := pathSegmentBefore(path, "start")
		d.mu.Lock()
		if d.failStart {
			d.mu.Unlock()
			http.Error(w, `{"message":"start unavailable"}`, http.StatusInternalServerError)
			return
		}
		if container := d.containers[id]; container != nil {
			container.running = true
			container.state = "running"
		}
		d.startIDs = append(d.startIDs, id)
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case request.Method == http.MethodPost && strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/exec"):
		var body dockertypes.ExecCreateRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		if d.failExec {
			d.mu.Unlock()
			http.Error(w, `{"message":"exec unavailable"}`, http.StatusInternalServerError)
			return
		}
		if isSignalCommand(body.Cmd) {
			d.controlCount++
			execID := fmt.Sprintf("control-exec-%d", d.controlCount)
			d.controlExecs[execID] = &fakeControlExec{command: append([]string(nil), body.Cmd...)}
			d.signalCommands = append(d.signalCommands, append([]string(nil), body.Cmd...))
			d.mu.Unlock()
			writeJSON(w, dockertypes.ExecCreateResponse{ID: execID})
			return
		}
		d.execCount++
		execID := fmt.Sprintf("exec-%d", d.execCount)
		d.execRunning[execID] = false
		d.execPIDFiles[execID] = body.Cmd[4]
		d.execRelease[execID] = make(chan struct{})
		d.execCommand = append([]string(nil), body.Cmd...)
		d.execEnv = append([]string(nil), body.Env...)
		d.execTargets = append(d.execTargets, pathSegmentBefore(path, "exec"))
		d.execEnvs = append(d.execEnvs, append([]string(nil), body.Env...))
		d.mu.Unlock()
		writeJSON(w, dockertypes.ExecCreateResponse{ID: execID})

	case request.Method == http.MethodPost && strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/start"):
		execID := pathSegmentBefore(path, "start")
		d.mu.Lock()
		if control := d.controlExecs[execID]; control != nil {
			control.running = false
			target := d.execForPIDFile(control.command[4])
			if target != "" {
				d.execRunning[target] = false
				if !d.holdAttachAfterStop {
					d.releaseExecLocked(target)
				}
			}
			d.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		d.execStartCount++
		d.execRunning[execID] = d.blockExec
		d.mu.Unlock()
		// A real Docker daemon consumes ExecStart options before hijacking the
		// connection. Leaving request bytes unread can close the fake TCP socket
		// with a reset and discard the final multiplexed stderr frames.
		_, _ = io.Copy(io.Discard, request.Body)
		d.serveExecAttach(w, execID)

	case request.Method == http.MethodPut && strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/archive"):
		d.mu.Lock()
		failInput := d.failInput
		d.mu.Unlock()
		if failInput {
			http.Error(w, `{"message":"input unavailable"}`, http.StatusInternalServerError)
			return
		}
		containerID := pathSegmentBefore(path, "archive")
		archive := tar.NewReader(request.Body)
		var file *tar.Header
		var payload []byte
		for {
			header, err := archive.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if header.Typeflag != tar.TypeReg {
				continue
			}
			if file != nil {
				http.Error(w, "multiple archive files", http.StatusBadRequest)
				return
			}
			copy := *header
			file = &copy
			payload, err = io.ReadAll(archive)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if file == nil {
			http.Error(w, "archive file missing", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		if d.failStartupPath == pathpkg.Join(request.URL.Query().Get("path"), file.Name) {
			d.mu.Unlock()
			http.Error(w, `{"message":"protocol file unavailable"}`, http.StatusInternalServerError)
			return
		}
		d.inputCount++
		key := fmt.Sprintf("archive-%d", d.inputCount)
		d.inputTargets[key] = containerID
		d.inputPaths[key] = pathpkg.Join(request.URL.Query().Get("path"), file.Name)
		d.inputModes[key] = file.Mode
		d.inputs[key] = payload
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case request.Method == http.MethodGet && strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/archive"):
		d.mu.Lock()
		d.fileArchivePaths = append(d.fileArchivePaths, request.URL.Query().Get("path"))
		payload := append([]byte(nil), d.fileArchive...)
		missing := false
		if d.fileArchives != nil {
			archive, ok := d.fileArchives[request.URL.Query().Get("path")]
			payload, missing = append([]byte(nil), archive...), !ok
		}
		blocked := d.blockFileArchive
		d.mu.Unlock()
		if missing {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		if blocked {
			d.fileArchiveReadyOne.Do(func() { close(d.fileArchiveReady) })
			select {
			case <-request.Context().Done():
				return
			case <-d.fileArchiveGo:
			}
		}
		stat, _ := json.Marshal(dockertypes.PathStat{
			Name: pathpkg.Base(request.URL.Query().Get("path")), Size: 1, Mode: 0o600,
		})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)

	case request.Method == http.MethodGet && strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
		execID := pathSegmentBefore(path, "json")
		d.mu.Lock()
		if control := d.controlExecs[execID]; control != nil {
			exitCode := control.exitCode
			inspection := dockertypes.ExecInspectResponse{Running: control.running, ExitCode: &exitCode}
			d.mu.Unlock()
			writeJSON(w, inspection)
			return
		}
		running := d.execRunning[execID]
		failInspect := d.failExecInspect
		pid := 4242
		if running && d.execPIDHidden > 0 {
			d.execPIDHidden--
			pid = 0
		}
		d.mu.Unlock()
		if failInspect {
			http.Error(w, `{"message":"inspect unavailable"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, dockertypes.ExecInspectResponse{Running: running, ExitCode: new(int), Pid: pid})

	case request.Method == http.MethodGet && strings.Contains(path, "/containers/") &&
		strings.HasSuffix(path, "/json") && !strings.HasSuffix(path, "/containers/json"):
		id := pathSegmentBefore(path, "json")
		d.mu.Lock()
		container := d.containerByIDOrName(id)
		d.mu.Unlock()
		if container == nil {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"Id": container.id, "Config": map[string]any{"Labels": container.labels},
			"State": map[string]any{"Status": container.state, "Running": container.running},
		})

	case request.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
		var filters client.Filters
		if err := json.Unmarshal([]byte(request.URL.Query().Get("filters")), &filters); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var before string
		for id, enabled := range filters["before"] {
			if enabled {
				before = id
			}
		}
		d.mu.Lock()
		limit, limitErr := strconv.Atoi(request.URL.Query().Get("limit"))
		if limitErr == nil {
			d.listLimits = append(d.listLimits, limit)
		}
		d.listBefore = append(d.listBefore, before)
		containers := make([]dockertypes.Summary, 0, len(d.containers))
		for _, container := range d.containers {
			if !matchesListFilters(request, container.labels) {
				continue
			}
			containers = append(containers, dockertypes.Summary{
				ID: container.id, State: dockertypes.ContainerState(container.state), Labels: container.labels,
			})
		}
		sort.Slice(containers, func(i, j int) bool { return containers[i].ID > containers[j].ID })
		if before != "" {
			start := len(containers)
			for index := range containers {
				if containers[index].ID == before {
					start = index + 1
					break
				}
			}
			containers = containers[start:]
		}
		if limitErr == nil && limit > 0 && len(containers) > limit {
			containers = containers[:limit]
		}
		d.mu.Unlock()
		writeJSON(w, containers)

	case request.Method == http.MethodPost && strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/stop"):
		id := pathSegmentBefore(path, "stop")
		d.mu.Lock()
		d.stopIDs = append(d.stopIDs, id)
		if container := d.containers[id]; container != nil {
			container.running = false
			container.state = "exited"
		}
		d.mu.Unlock()
		d.release()
		w.WriteHeader(http.StatusNoContent)

	case request.Method == http.MethodPost && strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/kill"):
		id := pathSegmentBefore(path, "kill")
		d.mu.Lock()
		d.killIDs = append(d.killIDs, id)
		d.mu.Unlock()
		d.release()
		w.WriteHeader(http.StatusNoContent)

	case request.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
		id := path[strings.LastIndex(path, "/")+1:]
		d.mu.Lock()
		d.removeAttempts++
		d.removeVolumes = append(d.removeVolumes, request.URL.Query().Get("v") == "1")
		if d.failRemove {
			d.mu.Unlock()
			http.Error(w, `{"message":"remove unavailable"}`, http.StatusInternalServerError)
			return
		}
		d.removeIDs = append(d.removeIDs, id)
		delete(d.containers, id)
		d.mu.Unlock()
		d.release()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.NotFound(w, request)
	}
}

type trackingRegistration struct {
	daemon         *fakeRunDaemon
	container      rundomain.ProjectContainerHandle
	handle         rundomain.ExecutionHandle
	containerReady bool
	started        bool
	stopped        bool
	containerErr   error
	err            error
}

func (registration *trackingRegistration) ContainerReady(_ context.Context, handle rundomain.ProjectContainerHandle) error {
	registration.daemon.mu.Lock()
	execs := registration.daemon.execCount
	registration.daemon.mu.Unlock()
	if execs != 0 {
		return fmt.Errorf("Exec prepared before Project container registration")
	}
	registration.container = handle
	registration.containerReady = true
	return registration.containerErr
}

func (registration *trackingRegistration) Started(_ context.Context, handle rundomain.ExecutionHandle) error {
	registration.daemon.mu.Lock()
	starts := registration.daemon.execStartCount
	registration.daemon.mu.Unlock()
	if starts != 0 {
		return fmt.Errorf("Exec started before handle registration")
	}
	registration.handle = handle
	registration.started = true
	return registration.err
}

func (registration *trackingRegistration) Stopped(_ context.Context, _ rundomain.ExecutionHandle) error {
	registration.stopped = true
	return nil
}

func TestTrackedLaunchRecordsExecBeforeStart(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	registration := &trackingRegistration{daemon: daemon}
	process, err := runtime.LaunchTracked(t.Context(), dockerLaunchSpec("tracked-run"), registration)
	if err != nil {
		t.Fatal(err)
	}
	if !registration.started || registration.handle.RunID != "tracked-run" || registration.handle.ProjectID != "project" ||
		registration.handle.ContainerID != "container-1" || registration.handle.ExecID != "exec-1" || registration.handle.StartedAt.IsZero() {
		t.Fatalf("registration: %+v", registration)
	}
	daemon.mu.Lock()
	starts := daemon.execStartCount
	daemon.mu.Unlock()
	if starts != 1 {
		t.Fatalf("Exec starts=%d, want 1", starts)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !registration.stopped {
		t.Fatal("completed Exec handle was not marked stopped")
	}
}

func TestTrackedLaunchDoesNotStartExecWhenRegistrationFails(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	registration := &trackingRegistration{daemon: daemon, err: errors.New("ledger unavailable")}
	if _, err := runtime.LaunchTracked(t.Context(), dockerLaunchSpec("untracked-run"), registration); err == nil {
		t.Fatal("expected registration failure")
	}
	daemon.mu.Lock()
	starts := daemon.execStartCount
	daemon.mu.Unlock()
	if !registration.started || starts != 0 {
		t.Fatalf("registration=%+v Exec starts=%d", registration, starts)
	}
}

func TestTrackedLaunchRecordsProjectContainerBeforeInputFailure(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	daemon.failInput = true
	runtime := newTestRuntime(t, daemon)
	registration := &trackingRegistration{daemon: daemon}
	if _, err := runtime.LaunchTracked(t.Context(), dockerLaunchSpec("input-failure-run"), registration); err == nil {
		t.Fatal("expected Blackboard input failure")
	}
	if !registration.containerReady || registration.container.ProjectID != "project" ||
		registration.container.ContainerID != "container-1" || registration.started {
		t.Fatalf("registration after input failure: %+v", registration)
	}
	daemon.mu.Lock()
	execs := daemon.execCount
	daemon.mu.Unlock()
	if execs != 0 {
		t.Fatalf("Execs prepared after input failure = %d, want 0", execs)
	}
}

func TestTrackedLaunchPreparesProtocolFileBeforeExec(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("write-fails-%t", fail), func(t *testing.T) {
			daemon := newFakeRunDaemon(t, false)
			runtime := newTestRuntime(t, daemon)
			spec := dockerLaunchSpec("protocol-file-run")
			spec.ProtocolFile = &rundomain.ProtocolFile{InvocationID: "12345678-abcd-4abc-8def-123456789abc", Filename: "output-schema.json", Bytes: []byte(`{"type":"object"}`)}
			if fail {
				daemon.failStartupPath = spec.ProtocolFile.Path()
			}
			registration := &trackingRegistration{daemon: daemon}
			process, err := runtime.LaunchTracked(t.Context(), spec, registration)
			if (err != nil) != fail {
				t.Fatalf("protocol preparation failure=%t: %v", fail, err)
			}
			if process != nil {
				if _, err := process.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			daemon.mu.Lock()
			defer daemon.mu.Unlock()
			if !registration.containerReady || !bytes.Equal(daemon.inputs["archive-1"], spec.RenderedInput.Bytes) || daemon.inputPaths["archive-1"] != spec.RenderedInput.Path || !spec.RenderedInput.Hash.Matches(daemon.inputs["archive-1"]) {
				t.Fatal("protocol preparation changed the Blackboard input or container registration")
			}
			if fail {
				if daemon.execCount != 0 || daemon.execStartCount != 0 || registration.started {
					t.Fatal("failed protocol preparation created or started Exec")
				}
			} else if !bytes.Equal(daemon.inputs["archive-2"], spec.ProtocolFile.Bytes) || daemon.inputPaths["archive-2"] != spec.ProtocolFile.Path() || daemon.execStartCount != 1 {
				t.Fatal("Exec did not receive its declared protocol file")
			}
		})
	}
}

func TestTrackedLaunchRemovesNewContainerWhenContainerRegistrationFails(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	registration := &trackingRegistration{daemon: daemon, containerErr: errors.New("project was deleted")}

	if _, err := runtime.LaunchTracked(t.Context(), dockerLaunchSpec("deleted-project-run"), registration); err == nil {
		t.Fatal("expected Project container registration failure")
	}

	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if !registration.containerReady || registration.started {
		t.Fatalf("registration after failure: %+v", registration)
	}
	if len(daemon.removeIDs) != 1 || daemon.removeIDs[0] != "container-1" {
		t.Fatalf("removed containers = %v, want [container-1]", daemon.removeIDs)
	}
	if daemon.execCount != 0 || len(daemon.containers) != 0 {
		t.Fatalf("Execs=%d containers=%v after failed registration", daemon.execCount, daemon.containers)
	}
}

func TestTrackedLaunchRecordsProjectContainerBeforeStartFailure(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	daemon.failStart = true
	runtime := newTestRuntime(t, daemon)
	registration := &trackingRegistration{daemon: daemon}
	if _, err := runtime.LaunchTracked(t.Context(), dockerLaunchSpec("start-failure-run"), registration); err == nil {
		t.Fatal("expected Project container start failure")
	}
	if !registration.containerReady || registration.container.ContainerID != "container-1" || registration.started {
		t.Fatalf("registration after start failure: %+v", registration)
	}
}

func (d *fakeRunDaemon) serveExecAttach(writer http.ResponseWriter, execID string) {
	connection, readWriter, err := writer.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer connection.Close()
	_, _ = fmt.Fprint(readWriter, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	_ = readWriter.Flush()
	d.mu.Lock()
	stdout, stderr, chunk, writeDone := d.execStdout, d.execStderr, d.execChunk, d.execWriteDone
	d.mu.Unlock()
	writeErr := writeExecOutput(readWriter, stdcopy.Stdout, stdout, chunk)
	if writeErr == nil {
		writeErr = writeExecOutput(readWriter, stdcopy.Stderr, stderr, chunk)
	}
	if writeErr == nil {
		writeErr = readWriter.Flush()
	}
	if writeErr != nil {
		if writeDone != nil {
			writeDone <- writeErr
		}
		return
	}
	if d.blockExec {
		d.mu.Lock()
		release := d.execRelease[execID]
		d.mu.Unlock()
		<-release
	}
	if writeDone != nil {
		writeDone <- nil
	}
}

func writeExecOutput(writer io.Writer, stream stdcopy.StdType, output string, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = len(output)
	}
	for offset := 0; offset < len(output); offset += chunkSize {
		end := offset + chunkSize
		if end > len(output) {
			end = len(output)
		}
		frame := make([]byte, dockerStreamHeaderBytes+end-offset)
		frame[0] = byte(stream)
		binary.BigEndian.PutUint32(frame[4:], uint32(end-offset))
		copy(frame[dockerStreamHeaderBytes:], output[offset:end])
		if _, err := io.Copy(writer, bytes.NewReader(frame)); err != nil {
			return err
		}
	}
	return nil
}

func (d *fakeRunDaemon) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for execID := range d.execRelease {
		d.releaseExecLocked(execID)
	}
}

func (d *fakeRunDaemon) releaseExecLocked(execID string) {
	if !d.execReleased[execID] {
		d.execRunning[execID] = false
		close(d.execRelease[execID])
		d.execReleased[execID] = true
	}
}

func (d *fakeRunDaemon) execForPIDFile(pidFile string) string {
	for execID, candidate := range d.execPIDFiles {
		if candidate == pidFile {
			return execID
		}
	}
	return ""
}

func (d *fakeRunDaemon) releaseCreation() { d.createGoOnce.Do(func() { close(d.createGo) }) }

func (d *fakeRunDaemon) releaseFileArchive() {
	d.fileArchiveGoOne.Do(func() { close(d.fileArchiveGo) })
}

func (d *fakeRunDaemon) containerByIDOrName(value string) *fakeDaemonContainer {
	if container := d.containers[value]; container != nil {
		return container
	}
	for _, container := range d.containers {
		if container.name == value {
			return container
		}
	}
	return nil
}

func (d *fakeRunDaemon) client(t *testing.T) *client.Client {
	t.Helper()
	serverURL, err := url.Parse(d.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(
		client.WithHost("tcp://"+serverURL.Host), client.WithHTTPClient(d.server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestProjectRuntimeNegotiatesDaemonAPIVersion(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	defer runtime.Close()
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("negotiated-run"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	paths := append([]string(nil), daemon.requestPaths...)
	daemon.mu.Unlock()
	if len(paths) < 2 || paths[0] != "HEAD /_ping" {
		t.Fatalf("requests did not start with API negotiation: %v", paths)
	}
	for _, request := range paths[1:] {
		_, path, _ := strings.Cut(request, " ")
		if !strings.HasPrefix(path, "/v1.51/") {
			t.Fatalf("request used a different API version from the daemon: %s", request)
		}
	}
}

func TestProjectRuntimeRejectsRenderedInputMismatchBeforeContainerMutation(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	defer runtime.Close()
	spec := dockerLaunchSpec("mismatched-rendered-input")
	spec.RenderedInput.Hash = rundomain.RenderedInputHash(strings.Repeat("0", 64))
	if _, err := runtime.Launch(t.Context(), spec); err == nil {
		t.Fatal("expected rendered input mismatch")
	}
	daemon.mu.Lock()
	createCount, inputCount, execCount := daemon.createCount, daemon.inputCount, daemon.execCount
	daemon.mu.Unlock()
	if createCount != 0 || inputCount != 0 || execCount != 0 {
		t.Fatalf("mismatched rendered input mutated Docker: create=%d input=%d exec=%d", createCount, inputCount, execCount)
	}
}

func TestProjectRuntimeDoesNotProvisionImageAndReportsMissingImageAtContainerCreation(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	daemon.missingImage = true
	runtime, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project:      ProjectConfig{Image: "agent:missing", NetworkMode: "host", CompletedAction: "remove"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	daemon.mu.Lock()
	requestsBeforeLaunch := append([]string(nil), daemon.requestPaths...)
	daemon.mu.Unlock()
	if len(requestsBeforeLaunch) != 0 {
		t.Fatalf("Runtime construction accessed Docker: %v", requestsBeforeLaunch)
	}

	_, err = runtime.Launch(t.Context(), dockerLaunchSpec("missing-image-run"))
	if err == nil || !strings.Contains(err.Error(), "create Project container project") ||
		!strings.Contains(err.Error(), "No such image: agent:missing") {
		t.Fatalf("missing image error=%v", err)
	}
	daemon.mu.Lock()
	requestPaths := append([]string(nil), daemon.requestPaths...)
	createCount, inputCount, execCount := daemon.createCount, daemon.inputCount, daemon.execCount
	daemon.mu.Unlock()
	for _, requestPath := range requestPaths {
		if strings.Contains(requestPath, "/images/") {
			t.Fatalf("Runtime pulled or inspected its configured image: %v", requestPaths)
		}
	}
	if createCount != 1 || inputCount != 0 || execCount != 0 {
		t.Fatalf("missing image path: create=%d input=%d exec=%d", createCount, inputCount, execCount)
	}
}

func TestProjectRuntimeSharesContainerAcrossRunsAndReconcilesLifecycle(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project: ProjectConfig{
			Image: "agent:latest", NetworkMode: "host", CapAdd: []string{"SYS_ADMIN"}, CompletedAction: "remove",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"project-run-1", "project-run-2"} {
		spec := dockerLaunchSpec(runID)
		spec.Phase = rundomain.PhaseReason
		spec.RenderedInput.Path = rundomain.BlackboardPath(spec.RunID, spec.Phase)
		process, err := runtime.Launch(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := process.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := runtime.CompleteRun(t.Context(), spec.RunID); err != nil {
			t.Fatal(err)
		}
	}
	daemon.mu.Lock()
	createBody := daemon.createBody
	createCount, inputCount, execCount := daemon.createCount, daemon.inputCount, daemon.execCount
	execCommand := append([]string(nil), daemon.execCommand...)
	stopCount, removeCount := len(daemon.stopIDs), len(daemon.removeIDs)
	inputTargets := make(map[string]string, len(daemon.inputTargets))
	for execID, target := range daemon.inputTargets {
		inputTargets[execID] = target
	}
	inputPaths := make(map[string]string, len(daemon.inputPaths))
	for archiveID, inputPath := range daemon.inputPaths {
		inputPaths[archiveID] = inputPath
	}
	inputModes := make(map[string]int64, len(daemon.inputModes))
	for archiveID, mode := range daemon.inputModes {
		inputModes[archiveID] = mode
	}
	daemon.mu.Unlock()
	if createCount != 1 || inputCount != 2 || execCount != 2 {
		t.Fatalf("create=%d input=%d exec=%d", createCount, inputCount, execCount)
	}
	wantCommand, _ := projectExecCommand(dockerLaunchSpec("project-run-2"))
	if strings.Join(execCommand, "\x00") != strings.Join(wantCommand, "\x00") {
		t.Fatalf("Project Exec command: %v", execCommand)
	}
	if stopCount != 0 || removeCount != 0 {
		t.Fatalf("Run completion touched shared container: stop=%d remove=%d", stopCount, removeCount)
	}
	if createBody.Config.Image != "agent:latest" || createBody.Config.User != "" || createBody.Config.WorkingDir != "" ||
		createBody.HostConfig.Init == nil || !*createBody.HostConfig.Init ||
		createBody.HostConfig.NetworkMode != "host" || createBody.HostConfig.ReadonlyRootfs ||
		len(createBody.HostConfig.CapAdd) != 1 || createBody.HostConfig.CapAdd[0] != "CAP_SYS_ADMIN" ||
		createBody.HostConfig.Resources.Memory != 0 || createBody.HostConfig.Resources.NanoCPUs != 0 ||
		createBody.HostConfig.Resources.PidsLimit != nil || len(createBody.HostConfig.Tmpfs) != 0 {
		t.Fatalf("Project container freedom contract: config=%+v host=%+v", createBody.Config, createBody.HostConfig)
	}
	for _, target := range inputTargets {
		if target != "container-1" {
			t.Fatalf("input writer target=%q, want shared container-1", target)
		}
	}
	if len(inputPaths) != 2 {
		t.Fatalf("Blackboard archive paths: %v", inputPaths)
	}
	seenPaths := make(map[string]bool, len(inputPaths))
	for archiveID, inputPath := range inputPaths {
		if !rundomain.ValidBlackboardInputPath(inputPath) || inputModes[archiveID] != 0o644 {
			t.Fatalf("Blackboard archive %s path=%q mode=%o", archiveID, inputPath, inputModes[archiveID])
		}
		seenPaths[inputPath] = true
	}
	if len(seenPaths) != 2 {
		t.Fatalf("Run inputs reused one container path: %v", inputPaths)
	}

	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{"project": "paused"}); err != nil {
		t.Fatal(err)
	}
	waitForDaemon(t, daemon, func(d *fakeRunDaemon) bool { return len(d.stopIDs) == 1 },
		"paused Project container was not stopped asynchronously")

	resumed := dockerLaunchSpec("project-run-resumed")
	resumed.Phase = rundomain.PhaseReason
	resumed.RenderedInput.Path = rundomain.BlackboardPath(resumed.RunID, resumed.Phase)
	process, err := runtime.Launch(t.Context(), resumed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	createCount, startCount := daemon.createCount, len(daemon.startIDs)
	daemon.mu.Unlock()
	if createCount != 1 || startCount != 2 {
		t.Fatalf("pause/resume created=%d started=%d, want one reused container started twice", createCount, startCount)
	}

	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{"project": "completed"}); err != nil {
		t.Fatal(err)
	}
	waitForDaemon(t, daemon, func(d *fakeRunDaemon) bool { return len(d.removeIDs) == 1 },
		"completed Project container was not removed asynchronously")

	deleted := dockerLaunchSpec("deleted-project-run")
	deleted.ProjectID = "deleted-project"
	deleted.RenderedInput.Path = rundomain.BlackboardPath(deleted.RunID, deleted.Phase)
	process, err = runtime.Launch(t.Context(), deleted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{}); err != nil {
		t.Fatal(err)
	}
	waitForDaemon(t, daemon, func(d *fakeRunDaemon) bool { return len(d.removeIDs) == 2 },
		"deleted Project container was not removed asynchronously")
	daemon.mu.Lock()
	removeVolumes := append([]bool(nil), daemon.removeVolumes...)
	daemon.mu.Unlock()
	for _, removeVolume := range removeVolumes {
		if removeVolume {
			t.Fatalf("Project container removal deleted anonymous volumes: %v", removeVolumes)
		}
	}
}

func TestProjectRuntimeBatchesOnlyCurrentDeploymentInventory(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	for index := 0; index < rundomain.MaximumProjectRuntimeStatusBatch+1; index++ {
		containerID := fmt.Sprintf("managed-%03d", index)
		projectID := fmt.Sprintf("project-%03d", index)
		daemon.containers[containerID] = &fakeDaemonContainer{
			id: containerID, state: "running", running: true,
			labels: map[string]string{
				projectLabelManaged: "true", projectLabelDeployment: "deployment", projectLabelID: projectID,
			},
		}
	}
	daemon.containers["other-deployment"] = &fakeDaemonContainer{
		id: "other-deployment", state: "running", running: true,
		labels: map[string]string{
			projectLabelManaged: "true", projectLabelDeployment: "other", projectLabelID: "unrelated-project",
		},
	}
	runtime := newTestRuntime(t, daemon)
	defer runtime.Close()

	var lookupMu sync.Mutex
	var batchSizes []int
	observed := make(map[string]bool)
	done := make(chan struct{})
	var doneOnce sync.Once
	if err := runtime.ReconcileProjectInventory(t.Context(), func(_ context.Context, projectIDs []string) ([]rundomain.ProjectRuntimeStatusResult, error) {
		lookupMu.Lock()
		batchSizes = append(batchSizes, len(projectIDs))
		for _, projectID := range projectIDs {
			observed[projectID] = true
		}
		if len(observed) == rundomain.MaximumProjectRuntimeStatusBatch+1 {
			doneOnce.Do(func() { close(done) })
		}
		lookupMu.Unlock()
		results := make([]rundomain.ProjectRuntimeStatusResult, len(projectIDs))
		for index, projectID := range projectIDs {
			results[index] = rundomain.ProjectRuntimeStatusResult{ProjectID: projectID, Status: rundomain.ProjectRuntimeActive}
		}
		return results, nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Project inventory lookup did not complete")
	}
	lookupMu.Lock()
	if len(batchSizes) != 2 || batchSizes[0] > rundomain.MaximumProjectRuntimeStatusBatch ||
		batchSizes[1] > rundomain.MaximumProjectRuntimeStatusBatch || observed["unrelated-project"] {
		t.Fatalf("inventory batches=%v observed unrelated=%v", batchSizes, observed["unrelated-project"])
	}
	lookupMu.Unlock()
	daemon.mu.Lock()
	createCount, stopCount, removeCount := daemon.createCount, len(daemon.stopIDs), len(daemon.removeIDs)
	listLimits, listBefore := append([]int(nil), daemon.listLimits...), append([]string(nil), daemon.listBefore...)
	daemon.mu.Unlock()
	if createCount != 0 || stopCount != 0 || removeCount != 0 {
		t.Fatalf("active inventory mutated containers: create=%d stop=%d remove=%d", createCount, stopCount, removeCount)
	}
	if len(listLimits) != 2 || listLimits[0] != rundomain.MaximumProjectRuntimeStatusBatch ||
		listLimits[1] != rundomain.MaximumProjectRuntimeStatusBatch || len(listBefore) != 2 ||
		listBefore[0] != "" || listBefore[1] == "" {
		t.Fatalf("Docker inventory pagination: limits=%v before=%v", listLimits, listBefore)
	}
}

func TestProjectRuntimeMissingInventoryAndCleanupJobAreIdempotent(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	defer runtime.Close()
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("duplicate-cleanup-run"))
	if err != nil {
		t.Fatal(err)
	}
	handle := process.(rundomain.ProcessHandle).Handle()
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{}); err != nil {
		t.Fatal(err)
	}
	waitForDaemon(t, daemon, func(d *fakeRunDaemon) bool { return len(d.removeIDs) == 1 },
		"missing Project inventory did not remove its container")
	if err := runtime.CleanupProject(t.Context(), rundomain.ProjectCleanupJob{
		ProjectID: "project", ContainerID: handle.ContainerID,
	}); err != nil {
		t.Fatalf("cleanup job was not idempotent after inventory removal: %v", err)
	}
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * projectReconcileRetryDelay)
	daemon.mu.Lock()
	removeCount := len(daemon.removeIDs)
	daemon.mu.Unlock()
	if removeCount != 1 {
		t.Fatalf("duplicate cleanup removed containers %d times", removeCount)
	}
}

func TestProjectRuntimeAdoptsExistingContainerWithoutReconcilingConfigDrift(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	first, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher-1", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project: ProjectConfig{
			Image: "agent:old", NetworkMode: "bridge", CapAdd: []string{"NET_RAW"}, CompletedAction: "stop",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := first.Launch(t.Context(), dockerLaunchSpec("first-dispatcher-run"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	second, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher-2", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project: ProjectConfig{
			Image: "agent:new", NetworkMode: "host", CapAdd: []string{"SYS_ADMIN"}, CompletedAction: "remove",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err = second.Launch(t.Context(), dockerLaunchSpec("second-dispatcher-run"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	createCount := daemon.createCount
	execTargets := append([]string(nil), daemon.execTargets...)
	createdImage := daemon.createBody.Config.Image
	daemon.mu.Unlock()
	if createCount != 1 || len(execTargets) != 2 || execTargets[0] != execTargets[1] || createdImage != "agent:old" {
		t.Fatalf("config drift reuse: creates=%d targets=%v image=%q", createCount, execTargets, createdImage)
	}
}

func TestProjectRuntimeIsolatesSameProjectIDAcrossDeployments(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	newRuntime := func(deploymentID string) *Runtime {
		runtime, err := NewRuntime(daemon.client(t), Options{
			DeploymentID: deploymentID, DispatcherID: "dispatcher", DisposeGrace: time.Second,
			OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
			Project:      ProjectConfig{Image: "agent:latest", NetworkMode: "host", CompletedAction: "stop"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}

	launch := func(runtime *Runtime, runID string) rundomain.ProcessHandle {
		process, err := runtime.Launch(t.Context(), dockerLaunchSpec(runID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := process.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		return process.(rundomain.ProcessHandle)
	}

	first := launch(newRuntime("deployment-one"), "deployment-one-run")
	second := launch(newRuntime("deployment-two"), "deployment-two-run")
	if first.Handle().ContainerID == second.Handle().ContainerID {
		t.Fatalf("deployments shared Project container %s", first.Handle().ContainerID)
	}
}

func TestProjectRuntimeStopsCompletedContainerWhenConfigured(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("completed-stop-run"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{"project": "completed"}); err != nil {
		t.Fatal(err)
	}
	waitForDaemon(t, daemon, func(d *fakeRunDaemon) bool {
		return len(d.stopIDs) == 1 && len(d.removeIDs) == 0
	}, "completed Project container was not stopped")
}

func TestProjectCleanupUsesPersistedContainerWithoutActiveHandles(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	registration := &trackingRegistration{daemon: daemon}
	process, err := runtime.LaunchTracked(t.Context(), dockerLaunchSpec("cleanup-evidence-run"), registration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CleanupProject(t.Context(), rundomain.ProjectCleanupJob{
		ProjectID: "project", ContainerID: registration.handle.ContainerID,
	}); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	removed := append([]string(nil), daemon.removeIDs...)
	daemon.mu.Unlock()
	if len(removed) != 1 || removed[0] != registration.handle.ContainerID {
		t.Fatalf("removed containers = %v, want [%s]", removed, registration.handle.ContainerID)
	}
}

func TestProjectRuntimeRetriesFailedLifecycleReconciliation(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project:      ProjectConfig{Image: "agent:latest", NetworkMode: "bridge", CompletedAction: "remove"},
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("retry-cleanup-run"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	daemon.failRemove = true
	daemon.mu.Unlock()
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{"project": "completed"}); err != nil {
		t.Fatal(err)
	}
	waitForDaemon(t, daemon, func(d *fakeRunDaemon) bool { return d.removeAttempts >= 1 },
		"Project cleanup was not attempted")
	daemon.mu.Lock()
	daemon.failRemove = false
	daemon.mu.Unlock()
	waitForDaemonWithin(t, daemon, 2500*time.Millisecond,
		func(d *fakeRunDaemon) bool { return len(d.removeIDs) == 1 && d.removeAttempts >= 2 },
		"Project cleanup was not retried")
}

func TestProjectRuntimeBoundsFailedLifecycleReconciliation(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project:      ProjectConfig{Image: "agent:latest", NetworkMode: "bridge", CompletedAction: "remove"},
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("bounded-cleanup-run"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	daemon.failRemove = true
	daemon.mu.Unlock()
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{"project": "completed"}); err != nil {
		t.Fatal(err)
	}
	waitForDaemonWithin(t, daemon, 2*time.Second, func(d *fakeRunDaemon) bool {
		runtime.reconcileMu.Lock()
		defer runtime.reconcileMu.Unlock()
		return d.removeAttempts == projectReconcileMaxAttempts && !runtime.reconcileRunning
	}, "Project cleanup did not stop after its bounded retry budget")
	time.Sleep(2 * projectReconcileRetryDelay)
	daemon.mu.Lock()
	attempts := daemon.removeAttempts
	daemon.mu.Unlock()
	if attempts != projectReconcileMaxAttempts {
		t.Fatalf("Project cleanup attempts=%d, want %d", attempts, projectReconcileMaxAttempts)
	}
}

func TestProjectRuntimeRejectsReconciliationAfterClose(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	runtime := newTestRuntime(t, daemon)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{"project": "paused"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcile after Close error=%v, want context.Canceled", err)
	}
}

func TestProjectRuntimeRejectsMissingInventoryLookup(t *testing.T) {
	runtime := newTestRuntime(t, newFakeRunDaemon(t, false))
	defer runtime.Close()
	if err := runtime.ReconcileProjectInventory(t.Context(), nil); err == nil {
		t.Fatal("missing Project runtime status lookup was accepted")
	}
}

func TestProjectProcessCancellationStopsOnlyItsExec(t *testing.T) {
	daemon := newFakeRunDaemon(t, true)
	runtime := newTestRuntime(t, daemon)
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("cancelled-project-exec"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err := process.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Cancelled {
		t.Fatalf("cancelled Project Exec output: %+v", output)
	}
	daemon.mu.Lock()
	signals := append([][]string(nil), daemon.signalCommands...)
	stopCount, removeCount := len(daemon.stopIDs), len(daemon.removeIDs)
	daemon.mu.Unlock()
	if len(signals) == 0 || signals[0][len(signals[0])-1] != "TERM" {
		t.Fatalf("Project Exec signal commands: %v", signals)
	}
	if stopCount != 0 || removeCount != 0 {
		t.Fatalf("Exec cancellation touched shared container: stop=%d remove=%d", stopCount, removeCount)
	}
}

func TestProjectProcessCancellationClosesAttachAfterExecStops(t *testing.T) {
	daemon := newFakeRunDaemon(t, true)
	daemon.holdAttachAfterStop = true
	runtime := newTestRuntime(t, daemon)
	spec := dockerLaunchSpec("cancelled-held-attach")
	spec.StopGrace = time.Second
	spec.OutputCapture = &recordingOutputCapture{}
	process, err := runtime.Launch(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("cancel held attach: %v", err)
	}
	if !output.Exited || !output.Cancelled {
		t.Fatalf("cancelled held attach output: %+v", output)
	}
	daemon.mu.Lock()
	running := daemon.execRunning["exec-1"]
	daemon.mu.Unlock()
	if running {
		t.Fatal("cancelled Exec was still running after attach closed")
	}
}

func TestProjectProcessCancellationDoesNotStopSiblingExec(t *testing.T) {
	daemon := newFakeRunDaemon(t, true)
	runtime := newTestRuntime(t, daemon)
	first, err := runtime.Launch(t.Context(), dockerLaunchSpec("cancelled-sibling-a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Launch(t.Context(), dockerLaunchSpec("cancelled-sibling-b"))
	if err != nil {
		t.Fatal(err)
	}

	firstContext, cancelFirst := context.WithCancel(t.Context())
	cancelFirst()
	if output, err := first.Wait(firstContext); err != nil || !output.Exited || !output.Cancelled {
		t.Fatalf("cancel first sibling output=%+v err=%v", output, err)
	}
	daemon.mu.Lock()
	firstRunning := daemon.execRunning["exec-1"]
	secondRunning := daemon.execRunning["exec-2"]
	containerRunning := daemon.containers["container-1"].running
	daemon.mu.Unlock()
	if firstRunning || !secondRunning || !containerRunning {
		t.Fatalf("sibling cancellation state: first=%t second=%t container=%t", firstRunning, secondRunning, containerRunning)
	}

	secondContext, cancelSecond := context.WithCancel(t.Context())
	cancelSecond()
	if output, err := second.Wait(secondContext); err != nil || !output.Exited || !output.Cancelled {
		t.Fatalf("cancel second sibling output=%+v err=%v", output, err)
	}
}

func TestProjectProcessCancellationWaitsForDockerToPublishExecPID(t *testing.T) {
	daemon := newFakeRunDaemon(t, true)
	daemon.mu.Lock()
	daemon.execPIDHidden = 1
	daemon.mu.Unlock()
	runtime := newTestRuntime(t, daemon)
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("cancelled-before-pid-visible"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err := process.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Cancelled {
		t.Fatalf("cancelled Project Exec output: %+v", output)
	}
	daemon.mu.Lock()
	signals := append([][]string(nil), daemon.signalCommands...)
	daemon.mu.Unlock()
	if len(signals) == 0 || signals[0][len(signals[0])-1] != "TERM" {
		t.Fatalf("Project Exec signal commands: %v", signals)
	}
}

func TestRuntimeBoundsEachStreamAndDrainsAttach(t *testing.T) {
	const chunkSize = 16 << 10
	limit := MinOutputLimitBytes
	stdout := strings.Repeat("o", int(limit*3))
	stderr := strings.Repeat("e", int(limit*2))
	daemon := newFakeRunDaemon(t, false)
	daemon.mu.Lock()
	daemon.execStdout = stdout
	daemon.execStderr = stderr
	daemon.execChunk = chunkSize
	daemon.execWriteDone = make(chan error, 1)
	writeDone := daemon.execWriteDone
	daemon.mu.Unlock()

	process, err := newTestRuntime(t, daemon).Launch(context.Background(), dockerLaunchSpec("run-output-limit"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output.ErrorCode != rundomain.ProcessErrorOutputTooLarge || output.ExitCode != 0 {
		t.Fatalf("process output: %+v", output)
	}
	if len(output.Stdout) != int(limit) || output.Stdout != stdout[:limit] {
		t.Fatalf("stdout retained %d bytes, want bounded prefix of %d", len(output.Stdout), limit)
	}
	if len(output.Stderr) != int(limit) || output.Stderr != stderr[:limit] {
		t.Fatalf("stderr retained %d bytes, want bounded prefix of %d", len(output.Stderr), limit)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("Docker attach producer was not drained: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Docker attach producer remained blocked after output reached its limit")
	}
}

func TestRuntimeDeliversVerboseStdoutToAdapterCaptureWithoutRetainingTransport(t *testing.T) {
	const chunkSize = 16 << 10
	stdout := strings.Repeat("irrelevant-jsonl-event ", int(DefaultOutputLimitBytes/int64(len("irrelevant-jsonl-event ")))*2) + "final"
	daemon := newFakeRunDaemon(t, false)
	daemon.mu.Lock()
	daemon.execStdout = stdout
	daemon.execChunk = chunkSize
	daemon.execWriteDone = make(chan error, 1)
	writeDone := daemon.execWriteDone
	daemon.mu.Unlock()

	recording := &recordingOutputCapture{result: "final"}
	runtime, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher", DisposeGrace: time.Second,
		OutputLimits: DefaultOutputLimits(),
		Project:      ProjectConfig{Image: "agent:latest", NetworkMode: "bridge", CompletedAction: "stop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	spec := dockerLaunchSpec("streaming-capture-run")
	spec.OutputCapture = recording
	process, err := runtime.Launch(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(t.Context())
	if err != nil || !output.Exited || output.ExitCode != 0 || output.Stdout != "final" || output.ErrorCode != "" {
		t.Fatalf("streaming output=%+v err=%v", output, err)
	}
	if recording.stdoutBytes != len(stdout) {
		t.Fatalf("adapter capture received %d stdout bytes, want %d", recording.stdoutBytes, len(stdout))
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("Docker attach producer was not drained: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Docker attach producer remained blocked after Adapter discarded transport output")
	}
}

func TestStoppedExecFileEvidenceUsesFixedLimits(t *testing.T) {
	if stoppedProcessFileMaximumBytes != 8<<20 {
		t.Fatalf("stopped file limit=%d, want 8 MiB", stoppedProcessFileMaximumBytes)
	}
	if stoppedProcessFileReadTimeout != 5*time.Second {
		t.Fatalf("stopped file timeout=%s, want 5s", stoppedProcessFileReadTimeout)
	}
}

func TestStoppedExecFileEvidenceUsesOneExactArchiveRequest(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	daemon.fileArchive = stoppedFileArchive(t, stoppedArchiveEntry{
		name: "session.jsonl", kind: tar.TypeReg, content: []byte("session evidence"),
	})
	runtime := newTestRuntime(t, daemon)
	process, err := runtime.Launch(t.Context(), dockerLaunchSpec("stopped-file-exact"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if output.StoppedProcessFileReader == nil {
		t.Fatal("confirmed stopped Exec omitted file evidence capability")
	}

	const requestedPath = "/tmp/private/session.jsonl"
	daemon.mu.Lock()
	execsBeforeRead := daemon.execCount
	daemon.mu.Unlock()
	content, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), requestedPath)
	if err != nil || string(content) != "session evidence" {
		t.Fatalf("stopped file content=%q err=%v", content, err)
	}
	daemon.mu.Lock()
	paths := append([]string(nil), daemon.fileArchivePaths...)
	execsAfterRead := daemon.execCount
	daemon.mu.Unlock()
	if len(paths) != 1 || paths[0] != requestedPath {
		t.Fatalf("Docker archive paths=%v, want exact %q", paths, requestedPath)
	}
	if execsAfterRead != execsBeforeRead {
		t.Fatalf("stopped file read launched helper Exec: before=%d after=%d", execsBeforeRead, execsAfterRead)
	}
	if _, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), requestedPath); err == nil {
		t.Fatal("stopped file reader allowed reuse")
	}
	daemon.mu.Lock()
	requestCount := len(daemon.fileArchivePaths)
	daemon.mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("one-shot reader made %d Docker archive requests", requestCount)
	}
}

func TestStoppedExecFileEvidenceReadsTwoIndependentTargetsOnce(t *testing.T) {
	const sessionPath = "/tmp/private/session.jsonl"
	const resultPath = "/tmp/private/result.toml"
	for _, failedPath := range []string{"", sessionPath, resultPath} {
		t.Run("missing="+failedPath, func(t *testing.T) {
			daemon := newFakeRunDaemon(t, false)
			daemon.fileArchives = map[string][]byte{
				sessionPath: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg, content: []byte("session evidence")}),
				resultPath:  stoppedFileArchive(t, stoppedArchiveEntry{name: "result.toml", kind: tar.TypeReg, content: []byte("[outcome]\nkind='no_change'\n")}),
			}
			delete(daemon.fileArchives, failedPath)
			runtime := newTestRuntime(t, daemon)
			process, err := runtime.Launch(t.Context(), dockerLaunchSpec("two-stopped-files"))
			if err != nil {
				t.Fatal(err)
			}
			output, err := process.Wait(t.Context())
			if err != nil || output.StoppedProcessFileReader == nil {
				t.Fatalf("stopped process omitted reader: %v", err)
			}
			daemon.mu.Lock()
			execsBefore := daemon.execCount
			daemon.mu.Unlock()
			targets := []string{sessionPath, resultPath}
			results := output.StoppedProcessFileReader.ReadFiles(t.Context(), targets)
			for target, want := range map[string]string{sessionPath: "session evidence", resultPath: "[outcome]\nkind='no_change'\n"} {
				file, ok := results[target]
				if !ok {
					t.Fatalf("missing independent result for %s", target)
				}
				if target == failedPath {
					if file.Err == nil || len(file.Bytes) != 0 {
						t.Fatalf("missing file returned usable content: %+v", file)
					}
				} else if file.Err != nil || string(file.Bytes) != want {
					t.Fatalf("other file was affected: target=%s file=%+v", target, file)
				}
			}
			for target, file := range output.StoppedProcessFileReader.ReadFiles(t.Context(), targets) {
				if file.Err == nil || len(file.Bytes) != 0 {
					t.Fatalf("reader allowed reuse for %s", target)
				}
			}
			daemon.mu.Lock()
			paths := append([]string(nil), daemon.fileArchivePaths...)
			execsAfter := daemon.execCount
			daemon.mu.Unlock()
			if len(paths) != 2 || paths[0] != sessionPath || paths[1] != resultPath || execsAfter != execsBefore {
				t.Fatalf("read must copy exact targets once without helper exec: paths=%v execs=%d/%d", paths, execsBefore, execsAfter)
			}
		})
	}
}

func TestStoppedExecFileEvidenceRequiresConfirmedStop(t *testing.T) {
	daemon := newFakeRunDaemon(t, true)
	runtime := newTestRuntime(t, daemon)
	processValue, err := runtime.Launch(t.Context(), dockerLaunchSpec("stopped-file-sequencing"))
	if err != nil {
		t.Fatal(err)
	}
	process := processValue.(*process)
	if output := process.result(); output.StoppedProcessFileReader != nil {
		t.Fatal("running Exec exposed file evidence capability")
	}
	daemon.release()
	output, err := process.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !output.Exited || output.StoppedProcessFileReader == nil {
		t.Fatalf("stopped Exec output=%+v", output)
	}

	unconfirmedDaemon := newFakeRunDaemon(t, false)
	unconfirmedDaemon.failExecInspect = true
	unconfirmedRuntime := newTestRuntime(t, unconfirmedDaemon)
	unconfirmed, err := unconfirmedRuntime.Launch(t.Context(), dockerLaunchSpec("unconfirmed-stopped-file"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = unconfirmed.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if output.Exited || output.StoppedProcessFileReader != nil {
		t.Fatalf("unconfirmed Exec stop exposed file evidence: %+v", output)
	}
}

func TestStoppedExecFileEvidenceRejectsInvalidPathsWithoutDockerRequest(t *testing.T) {
	invalidPaths := []string{"", "relative/session.jsonl", "/", "/tmp/../session.jsonl", "/tmp/session.jsonl/", "//tmp/session.jsonl", "/tmp/session\x00.jsonl"}
	for _, requestedPath := range invalidPaths {
		t.Run(fmt.Sprintf("path-%x", sha256.Sum256([]byte(requestedPath))), func(t *testing.T) {
			daemon := newFakeRunDaemon(t, false)
			reader := newStoppedProcessFileReader(daemon.client(t), "container")
			if _, err := readSingleStoppedTestFile(reader, t.Context(), requestedPath); err == nil {
				t.Fatalf("invalid path %q was accepted", requestedPath)
			}
			daemon.mu.Lock()
			requests := len(daemon.fileArchivePaths)
			daemon.mu.Unlock()
			if requests != 0 {
				t.Fatalf("invalid path %q reached Docker", requestedPath)
			}
			if _, err := readSingleStoppedTestFile(reader, t.Context(), "/tmp/session.jsonl"); err == nil {
				t.Fatal("invalid path attempt did not consume reader")
			}
		})
	}
}

func TestStoppedExecFileEvidenceRespectsRemainingContext(t *testing.T) {
	daemon := newFakeRunDaemon(t, false)
	daemon.blockFileArchive = true
	reader := newStoppedProcessFileReader(daemon.client(t), "container")
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := readSingleStoppedTestFile(reader, ctx, "/tmp/session.jsonl"); err == nil {
		t.Fatal("timed out archive request succeeded")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("archive request ignored remaining context: %s", elapsed)
	}
	daemon.mu.Lock()
	requests := len(daemon.fileArchivePaths)
	daemon.mu.Unlock()
	if requests != 1 {
		t.Fatalf("timed out reader attempts=%d, want 1", requests)
	}
	if _, err := readSingleStoppedTestFile(reader, t.Context(), "/tmp/session.jsonl"); err == nil {
		t.Fatal("timed out reader allowed reuse")
	}
}

func TestStoppedExecFileEvidenceRejectsUnsafeArchives(t *testing.T) {
	maximum := bytesOf('m', int(stoppedProcessFileMaximumBytes))
	tests := []struct {
		name    string
		archive []byte
		valid   bool
	}{
		{name: "maximum regular file", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg, content: maximum}), valid: true},
		{name: "empty file", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg})},
		{name: "oversized file", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg, content: append(maximum, 'x')})},
		{name: "wrong basename", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "other.jsonl", kind: tar.TypeReg, content: []byte("x")})},
		{name: "nested member", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "dir/session.jsonl", kind: tar.TypeReg, content: []byte("x")})},
		{name: "traversal member", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "../session.jsonl", kind: tar.TypeReg, content: []byte("x")})},
		{name: "absolute member", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "/session.jsonl", kind: tar.TypeReg, content: []byte("x")})},
		{name: "directory", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeDir})},
		{name: "symbolic link", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeSymlink, link: "target"})},
		{name: "hard link", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeLink, link: "target"})},
		{name: "extra member", archive: stoppedFileArchive(t,
			stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg, content: []byte("x")},
			stoppedArchiveEntry{name: "extra", kind: tar.TypeReg, content: []byte("y")})},
		{name: "malformed", archive: []byte("not a tar archive")},
		{name: "truncated", archive: stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg, content: []byte("truncated")})[:517]},
		{name: "nonzero trailing data", archive: append(stoppedFileArchive(t, stoppedArchiveEntry{name: "session.jsonl", kind: tar.TypeReg, content: []byte("x")}), 'x')},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			daemon := newFakeRunDaemon(t, false)
			daemon.fileArchive = test.archive
			reader := newStoppedProcessFileReader(daemon.client(t), "container")
			content, err := readSingleStoppedTestFile(reader, t.Context(), "/tmp/session.jsonl")
			if test.valid {
				if err != nil || len(content) != len(maximum) {
					t.Fatalf("valid archive content=%d err=%v", len(content), err)
				}
				return
			}
			if err == nil || len(content) != 0 {
				t.Fatalf("unsafe archive accepted: content=%d err=%v", len(content), err)
			}
		})
	}
}

type stoppedArchiveEntry struct {
	name    string
	kind    byte
	content []byte
	link    string
}

func stoppedFileArchive(t *testing.T, entries ...stoppedArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Typeflag: entry.kind, Mode: 0o600,
			Size: int64(len(entry.content)), Linkname: entry.link,
		}
		if entry.kind == tar.TypeDir || entry.kind == tar.TypeSymlink || entry.kind == tar.TypeLink {
			header.Size = 0
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.content) > 0 {
			if _, err := archive.Write(entry.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func bytesOf(value byte, count int) []byte {
	return bytes.Repeat([]byte{value}, count)
}

func TestNewExecProcessRejectsInvalidOutputLimits(t *testing.T) {
	_, err := newExecProcess(nil, "container", "/tmp/aether-test.pid", []string{"agent"}, nil, OutputLimits{
		StdoutBytes: MinOutputLimitBytes - 1,
		StderrBytes: MinOutputLimitBytes,
	}, nil)
	if err == nil {
		t.Fatal("expected invalid stdout output limit error")
	}
}

func TestPhaseTimedOutClassifiesForcedTimeoutWithoutMisclassifyingStops(t *testing.T) {
	deadline := 30 * time.Second
	if !phaseTimedOut(false, 137, false, deadline, deadline) {
		t.Fatal("expected timeout-k forced exit to be timed out")
	}
	if phaseTimedOut(false, 137, false, deadline-time.Millisecond, deadline) {
		t.Fatal("early exit 137 must not be inferred as a timeout")
	}
	if phaseTimedOut(false, 124, false, time.Second, deadline) {
		t.Fatal("early non-zero exit 124 must not be inferred as a timeout")
	}
	if !phaseTimedOut(false, 124, false, deadline, deadline) {
		t.Fatal("deadline exit 124 must be classified as a timeout")
	}
	if phaseTimedOut(false, 137, true, deadline, deadline) {
		t.Fatal("explicit Stop must not be inferred as a timeout")
	}
}

func TestProjectRuntimeDockerIntegration(t *testing.T) {
	image := dockerIntegrationImage(t)
	deploymentID := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	dockerAPI, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dockerAPI, Options{
		DeploymentID: deploymentID, DispatcherID: "integration-test", DisposeGrace: time.Second,
		OutputLimits: DefaultOutputLimits(),
		Project:      ProjectConfig{Image: image, NetworkMode: "bridge", CompletedAction: "remove"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.reconcileProjects(cleanupContext, func(_ context.Context, projectIDs []string) ([]rundomain.ProjectRuntimeStatusResult, error) {
			results := make([]rundomain.ProjectRuntimeStatusResult, len(projectIDs))
			for index, projectID := range projectIDs {
				results[index] = rundomain.ProjectRuntimeStatusResult{ProjectID: projectID, Status: rundomain.ProjectRuntimeMissing}
			}
			return results, nil
		})
	}()

	projectID := "integration-project-" + deploymentID
	evidencePath := "/tmp/aether-stopped-evidence-" + deploymentID + ".jsonl"
	const evidenceContent = "stopped process evidence"
	spec := dockerLaunchSpec("integration-run")
	spec.ProjectID = projectID
	spec.ProtocolFile = &rundomain.ProtocolFile{InvocationID: "12345678-abcd-4abc-8def-123456789abc", Filename: "output-schema.json", Bytes: []byte(`{"type":"object","properties":{"outcome":{"type":"object"}},"required":["outcome"],"additionalProperties":false}`)}
	schemaHash := sha256.Sum256(spec.ProtocolFile.Bytes)
	spec.Command = []string{
		"/bin/sh", "-c",
		`test -r "$1" && test -r "$4" && printf '%s  %s\n%s  %s\n' "$5" "$1" "$6" "$4" | sha256sum --check --status && printf '%s' "$3" > "$2" && printf integration`,
		"aether-test", spec.RenderedInput.Path, evidencePath, evidenceContent, spec.ProtocolFile.Path(), string(spec.RenderedInput.Hash), fmt.Sprintf("%x", schemaHash),
	}
	process, err := runtime.Launch(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	firstHandle := process.(rundomain.ProcessHandle).Handle()
	output, err := process.Wait(t.Context())
	if err != nil || !output.Exited || output.Stdout != "integration" || output.ExitCode != 0 {
		t.Fatalf("integration output=%+v err=%v", output, err)
	}
	if output.StoppedProcessFileReader == nil {
		t.Fatal("real Docker stopped Exec omitted file evidence capability")
	}
	files := output.StoppedProcessFileReader.ReadFiles(t.Context(), []string{spec.ProtocolFile.Path(), evidencePath})
	if file := files[evidencePath]; file.Err != nil || string(file.Bytes) != evidenceContent {
		t.Fatalf("real Docker stopped file evidence=%+v", file)
	}
	if file := files[spec.ProtocolFile.Path()]; file.Err != nil || !bytes.Equal(file.Bytes, spec.ProtocolFile.Bytes) {
		t.Fatalf("real Docker stopped schema=%+v", file)
	}
	if _, err := readSingleStoppedTestFile(output.StoppedProcessFileReader, t.Context(), evidencePath); err == nil {
		t.Fatal("real Docker stopped file evidence was reusable")
	}
	missingSpec := dockerLaunchSpec("integration-missing-result")
	missingSpec.ProjectID = projectID
	missingSpec.Command = []string{"/bin/sh", "-c", ":"}
	missingProcess, err := runtime.Launch(t.Context(), missingSpec)
	if err != nil {
		t.Fatal(err)
	}
	missingOutput, err := missingProcess.Wait(t.Context())
	if err != nil || !missingOutput.Exited || missingOutput.ExitCode != 0 || missingOutput.StoppedProcessFileReader == nil {
		t.Fatalf("real Docker missing-result fixture failed: output=%+v err=%v", missingOutput, err)
	}
	missingPath := evidencePath + ".missing.toml"
	files = missingOutput.StoppedProcessFileReader.ReadFiles(t.Context(), []string{missingPath, evidencePath})
	if file := files[missingPath]; file.Err == nil || len(file.Bytes) != 0 {
		t.Fatalf("real Docker missing result was accepted: %+v", file)
	}
	if file := files[evidencePath]; file.Err != nil || string(file.Bytes) != evidenceContent {
		t.Fatalf("real Docker missing result blocked Session evidence: %+v", file)
	}

	failedSpec := dockerLaunchSpec("integration-schema-failure")
	failedSpec.ProjectID = projectID
	failedSpec.ProtocolFile = &rundomain.ProtocolFile{InvocationID: "12345678-abcd-4abc-8def-123456789abd", Filename: "output-schema.json", Bytes: spec.ProtocolFile.Bytes}
	marker := "/tmp/aether-unexpected-exec-" + deploymentID
	failedSpec.Command = []string{"/bin/sh", "-c", `touch "$1"`, "aether-test", marker}
	// The daemon owns prepared schema directories. Use a root fixture Exec to
	// create a directory where the subsequent regular-file copy must fail.
	setup, err := dockerAPI.ExecCreate(t.Context(), firstHandle.ContainerID, client.ExecCreateOptions{
		User: "root", Cmd: []string{"mkdir", "-p", failedSpec.ProtocolFile.Path()}, AttachStderr: true, AttachStdout: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := dockerAPI.ExecAttach(t.Context(), setup.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(io.Discard, attached.Reader)
	attached.Close()
	setupInspection, err := dockerAPI.ExecInspect(t.Context(), setup.ID, client.ExecInspectOptions{})
	if copyErr != nil || err != nil || setupInspection.Running || setupInspection.ExitCode != 0 {
		t.Fatalf("schema conflict setup: inspection=%+v copyErr=%v err=%v", setupInspection, copyErr, err)
	}
	if process, err := runtime.Launch(t.Context(), failedSpec); err == nil || process != nil {
		t.Fatalf("protocol write failure did not fail Launch: process=%v err=%v", process, err)
	}
	checkSpec := dockerLaunchSpec("integration-schema-failure-check")
	checkSpec.ProjectID = projectID
	checkSpec.Command = []string{"/bin/sh", "-c", `test ! -e "$1" && test ! -e "$2" && printf '%s  %s\n' "$3" "$4" | sha256sum --check --status`, "aether-test", marker, projectExecPIDFile(failedSpec.RunID, failedSpec.Phase), string(failedSpec.RenderedInput.Hash), failedSpec.RenderedInput.Path}
	checkProcess, err := runtime.Launch(t.Context(), checkSpec)
	if err != nil {
		t.Fatal(err)
	}
	checkOutput, err := checkProcess.Wait(t.Context())
	if err != nil || checkOutput.ExitCode != 0 {
		t.Fatalf("failed schema write started Exec or changed input: output=%+v err=%v", checkOutput, err)
	}

	for index := 0; index < 20; index++ {
		zombieSpec := dockerLaunchSpec(fmt.Sprintf("integration-zombie-%d", index))
		zombieSpec.ProjectID = projectID
		zombieSpec.Command = []string{"/bin/sh", "-c", "sleep 0.05 & exit 0"}
		zombieSpec.PhaseTimeout = 2 * time.Second
		process, err := runtime.Launch(t.Context(), zombieSpec)
		if err != nil {
			t.Fatal(err)
		}
		zombieOutput, err := process.Wait(t.Context())
		if err != nil || !zombieOutput.Exited || zombieOutput.ExitCode != 0 {
			t.Fatalf("zombie producer output=%+v err=%v", zombieOutput, err)
		}
	}
	time.Sleep(250 * time.Millisecond)
	zombieCheck := dockerLaunchSpec("integration-zombie-check")
	zombieCheck.ProjectID = projectID
	zombieCheck.Command = []string{"/bin/sh", "-c", `count=$(ps -eo stat= | awk '$1 ~ /^Z/ { count++ } END { print count + 0 }'); printf '%s' "$count"; test "$count" -eq 0`}
	zombieCheckProcess, err := runtime.Launch(t.Context(), zombieCheck)
	if err != nil {
		t.Fatal(err)
	}
	zombieCheckOutput, err := zombieCheckProcess.Wait(t.Context())
	if err != nil || !zombieCheckOutput.Exited || zombieCheckOutput.ExitCode != 0 || zombieCheckOutput.Stdout != "0" {
		t.Fatalf("Project container did not reap Exec children: output=%+v err=%v", zombieCheckOutput, err)
	}

	backgroundPIDPath := "/tmp/aether-background-" + deploymentID + ".pid"
	timeoutSpec := dockerLaunchSpec("integration-timeout")
	timeoutSpec.ProjectID = projectID
	timeoutSpec.Command = []string{"/bin/sh", "-c", `trap "" TERM; sleep 30 & child=$!; printf '%s\n' "$child" > "$1"; wait`, "aether-test", backgroundPIDPath}
	timeoutSpec.PhaseTimeout = time.Second
	timeoutSpec.StopGrace = time.Second
	timeoutExecPIDPath := projectExecPIDFile(timeoutSpec.RunID, timeoutSpec.Phase)
	process, err = runtime.Launch(t.Context(), timeoutSpec)
	if err != nil {
		t.Fatal(err)
	}
	output, err = process.Wait(t.Context())
	if err != nil || !output.Exited || !output.TimedOut || output.Cancelled {
		t.Fatalf("timeout output=%+v err=%v", output, err)
	}

	cleanupSpec := dockerLaunchSpec("integration-timeout-cleanup")
	cleanupSpec.ProjectID = projectID
	cleanupSpec.Command = []string{"/bin/sh", "-c", `test -s "$1" || exit 21; test -s "$2" || exit 22; pid=$(cat "$2"); child_state=$(ps -o state= -p "$pid" 2>/dev/null | tr -d ' '); case "$child_state" in ""|Z) ;; *) exit 23 ;; esac; printf cleaned`, "aether-test", timeoutExecPIDPath, backgroundPIDPath}
	cleanupProcess, err := runtime.Launch(t.Context(), cleanupSpec)
	if err != nil {
		t.Fatal(err)
	}
	cleanupOutput, err := cleanupProcess.Wait(t.Context())
	if err != nil || !cleanupOutput.Exited || cleanupOutput.Stdout != "cleaned" || cleanupOutput.ExitCode != 0 {
		t.Fatalf("background cleanup output=%+v err=%v", cleanupOutput, err)
	}

	cancelSpec := dockerLaunchSpec("integration-cancel")
	cancelSpec.ProjectID = projectID
	cancelSpec.Command = []string{"/bin/sh", "-c", "sleep 30"}
	process, err = runtime.Launch(t.Context(), cancelSpec)
	if err != nil {
		t.Fatal(err)
	}
	siblingSpec := dockerLaunchSpec("integration-sibling")
	siblingSpec.ProjectID = projectID
	siblingSpec.Command = []string{"/bin/sh", "-c", "sleep 2; printf sibling"}
	siblingProcess, err := runtime.Launch(t.Context(), siblingSpec)
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithCancel(t.Context())
	cancelWait()
	output, err = process.Wait(waitContext)
	if err != nil || !output.Exited || !output.Cancelled {
		t.Fatalf("cancel output=%+v err=%v", output, err)
	}
	siblingOutput, err := siblingProcess.Wait(t.Context())
	if err != nil || !siblingOutput.Exited || siblingOutput.Stdout != "sibling" || siblingOutput.ExitCode != 0 {
		t.Fatalf("sibling output=%+v err=%v", siblingOutput, err)
	}
	containerInspection, err := runtime.client.ContainerInspect(t.Context(), firstHandle.ContainerID, client.ContainerInspectOptions{})
	if err != nil || containerInspection.Container.State == nil || !containerInspection.Container.State.Running {
		t.Fatalf("sibling cancellation stopped Project container: inspection=%+v err=%v", containerInspection.Container.State, err)
	}

	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{projectID: "paused"}); err != nil {
		t.Fatal(err)
	}
	waitForContainerState(t, runtime, firstHandle.ContainerID, false)
	resumedSpec := dockerLaunchSpec("integration-resumed")
	resumedSpec.ProjectID = projectID
	process, err = runtime.Launch(t.Context(), resumedSpec)
	if err != nil {
		t.Fatal(err)
	}
	resumedHandle := process.(rundomain.ProcessHandle).Handle()
	if resumedHandle.ContainerID != firstHandle.ContainerID {
		t.Fatalf("resume replaced Project container: before=%s after=%s", firstHandle.ContainerID, resumedHandle.ContainerID)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := queueProjectStatuses(t, runtime, map[string]rundomain.ProjectRuntimeStatus{projectID: "completed"}); err != nil {
		t.Fatal(err)
	}
	waitForContainerRemoval(t, runtime, firstHandle.ContainerID)
}

func waitForContainerState(t *testing.T, runtime *Runtime, containerID string, running bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		inspection, err := runtime.client.ContainerInspect(t.Context(), containerID, client.ContainerInspectOptions{})
		if err == nil && inspection.Container.State != nil && inspection.Container.State.Running == running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("container %s running=%t: inspect=%+v err=%v", containerID, running, inspection.Container.State, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForContainerRemoval(t *testing.T, runtime *Runtime, containerID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := runtime.client.ContainerInspect(t.Context(), containerID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("container %s was not removed: %v", containerID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newTestRuntime(t *testing.T, daemon *fakeRunDaemon) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(daemon.client(t), Options{
		DeploymentID: "deployment", DispatcherID: "dispatcher", DisposeGrace: time.Second,
		OutputLimits: OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes},
		Project:      ProjectConfig{Image: "agent:latest", NetworkMode: "bridge", CompletedAction: "stop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func dockerLaunchSpec(runID string) rundomain.LaunchSpec {
	yaml := []byte("schema: aether.blackboard/v1\ngeneration: 1\nrevision: 1\n")
	digest := sha256.Sum256(yaml)
	id := rundomain.RunID(runID)
	return rundomain.LaunchSpec{
		RunID: id, ProjectID: "project", Task: rundomain.TaskReason, Phase: rundomain.PhaseReason,
		FencingToken: 7, ConfigRevision: "config", Image: "aether:test",
		Command: []string{"agent", "run"}, Environment: map[string]string{"SECRET": "resolved-value"},
		RenderedInput: &rundomain.RenderedInput{Path: rundomain.BlackboardPath(id, rundomain.PhaseReason), Bytes: yaml, Hash: rundomain.RenderedInputHash(fmt.Sprintf("%x", digest[:]))},
		PhaseTimeout:  30 * time.Second, StopGrace: 2 * time.Second,
	}
}

func pathSegmentBefore(path, suffix string) string {
	trimmed := strings.TrimSuffix(path, "/"+suffix)
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func queueProjectStatuses(t *testing.T, runtime *Runtime, statuses map[string]rundomain.ProjectRuntimeStatus) error {
	t.Helper()
	return runtime.ReconcileProjectInventory(t.Context(), func(_ context.Context, projectIDs []string) ([]rundomain.ProjectRuntimeStatusResult, error) {
		results := make([]rundomain.ProjectRuntimeStatusResult, len(projectIDs))
		for index, projectID := range projectIDs {
			status, exists := statuses[projectID]
			if !exists {
				status = rundomain.ProjectRuntimeMissing
			}
			results[index] = rundomain.ProjectRuntimeStatusResult{ProjectID: projectID, Status: status}
		}
		return results, nil
	})
}

func isSignalCommand(command []string) bool {
	return len(command) == 6 && command[0] == "/bin/sh" && command[1] == "-c" &&
		command[3] == "aether-signal" && (command[5] == "TERM" || command[5] == "KILL")
}

func waitForDaemon(t *testing.T, daemon *fakeRunDaemon, condition func(*fakeRunDaemon) bool, message string) {
	t.Helper()
	waitForDaemonWithin(t, daemon, time.Second, condition, message)
}

func waitForDaemonWithin(
	t *testing.T,
	daemon *fakeRunDaemon,
	timeout time.Duration,
	condition func(*fakeRunDaemon) bool,
	message string,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		daemon.mu.Lock()
		done := condition(daemon)
		daemon.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func matchesListFilters(request *http.Request, labels map[string]string) bool {
	encoded := request.URL.Query().Get("filters")
	if encoded == "" {
		return true
	}
	var requested map[string]map[string]bool
	if err := json.Unmarshal([]byte(encoded), &requested); err != nil {
		return false
	}
	for selector, enabled := range requested["label"] {
		if !enabled {
			continue
		}
		key, value, hasValue := strings.Cut(selector, "=")
		actual, exists := labels[key]
		if !exists || (hasValue && actual != value) {
			return false
		}
	}
	return true
}

func readSingleStoppedTestFile(reader rundomain.StoppedProcessFileReader, ctx context.Context, requestedPath string) ([]byte, error) {
	result := reader.ReadFiles(ctx, []string{requestedPath})[requestedPath]
	return result.Bytes, result.Err
}
