package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	"github.com/gin-gonic/gin"
)

type changeReaderFunc func(context.Context, string, int64) (project.ChangeReplay, error)

func (reader changeReaderFunc) ListChanges(
	ctx context.Context,
	projectID string,
	afterRevision int64,
) (project.ChangeReplay, error) {
	return reader(ctx, projectID, afterRevision)
}

type wireEvent struct {
	Type      string         `json:"type"`
	ProjectID string         `json:"project_id"`
	Revision  int64          `json:"revision,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

type receivedEvent struct {
	id    string
	event wireEvent
}

func TestChangeStreamPreservesCursorReplayWireContract(t *testing.T) {
	cursor := make(chan int64, 1)
	reader := changeReaderFunc(func(_ context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		cursor <- afterRevision
		entityID := "fact-2"
		return project.ChangeReplay{
			ProjectID:       projectID,
			AfterRevision:   afterRevision,
			CurrentRevision: 2,
			Changes: []project.BlackboardChange{{
				ProjectID: projectID, Revision: 2, Kind: project.ChangeFactSuperseded,
				EntityType: "fact", EntityID: &entityID,
				Data: json.RawMessage(`{"reason":"corrected"}`),
			}},
		}, nil
	})
	stream := NewChangeStream()
	server := newChangeStreamServer(t, stream, reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		server.URL+"/api/v1/projects/project-1/events?after_revision=0",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "1")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	select {
	case got := <-cursor:
		if got != 1 {
			t.Fatalf("replay cursor = %d, want Last-Event-ID 1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("change reader did not receive the replay cursor")
	}

	events, readErrors := scanSSE(response.Body)
	change := receiveEvent(t, events, readErrors)
	if change.id != "2" || change.event.Type != string(project.ChangeFactSuperseded) ||
		change.event.ProjectID != "project-1" || change.event.Revision != 2 ||
		change.event.Data["fact_id"] != "fact-2" || change.event.Data["reason"] != "corrected" {
		t.Fatalf("change event = %+v", change)
	}
	syncComplete := receiveEvent(t, events, readErrors)
	if syncComplete.id != "" || syncComplete.event.Type != "sync_complete" ||
		syncComplete.event.Data["current_revision"] != float64(2) {
		t.Fatalf("sync-complete event = %+v", syncComplete)
	}
}

func TestChangeStreamPreservesReplayRequiredWireContract(t *testing.T) {
	reader := changeReaderFunc(func(_ context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		return project.ChangeReplay{
			ProjectID: projectID, AfterRevision: afterRevision,
			CurrentRevision: 9, ReplayRequired: true,
		}, nil
	})
	stream := NewChangeStream()
	server := newChangeStreamServer(t, stream, reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := openChangeStream(t, ctx, server, "/api/v1/projects/project-1/events?after_revision=2")
	defer response.Body.Close()
	events, readErrors := scanSSE(response.Body)

	replayRequired := receiveEvent(t, events, readErrors)
	if replayRequired.id != "" || replayRequired.event.Type != "replay_required" ||
		replayRequired.event.Data["current_revision"] != float64(9) {
		t.Fatalf("replay-required event = %+v", replayRequired)
	}
	syncComplete := receiveEvent(t, events, readErrors)
	if syncComplete.event.Type != "sync_complete" || syncComplete.event.Data["current_revision"] != float64(9) {
		t.Fatalf("sync-complete event = %+v", syncComplete)
	}
}

func TestChangeStreamPreservesEmptyAndNullChangeDataWireContract(t *testing.T) {
	reader := changeReaderFunc(func(_ context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		return project.ChangeReplay{
			ProjectID: projectID, AfterRevision: afterRevision, CurrentRevision: 2,
			Changes: []project.BlackboardChange{
				{ProjectID: projectID, Revision: 1, Kind: project.ChangeProjectCreated, Data: json.RawMessage(`{}`)},
				{ProjectID: projectID, Revision: 2, Kind: project.ChangeProjectRenamed, Data: json.RawMessage(`null`)},
			},
		}, nil
	})
	stream := NewChangeStream()
	server := newChangeStreamServer(t, stream, reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := openChangeStream(t, ctx, server, "/api/v1/projects/project-1/events?after_revision=0")
	defer response.Body.Close()
	lines, readErrors := scanSSELines(response.Body)

	var payloads []map[string]json.RawMessage
	for len(payloads) < 2 {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("SSE stream ended before both changes were received")
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, payload)
		case err := <-readErrors:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for persisted change events")
		}
	}
	if got := string(payloads[0]["data"]); got != "{}" {
		t.Fatalf("empty object data = %s, want {}", got)
	}
	if got := string(payloads[1]["data"]); got != "null" {
		t.Fatalf("null data = %s, want null", got)
	}
}

func TestChangeStreamWritesKeepalives(t *testing.T) {
	reader := changeReaderFunc(func(_ context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		return project.ChangeReplay{
			ProjectID: projectID, AfterRevision: afterRevision, CurrentRevision: 0,
		}, nil
	})
	stream := NewChangeStream()
	stream.replayInterval = time.Hour
	stream.keepaliveInterval = 5 * time.Millisecond
	server := newChangeStreamServer(t, stream, reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := openChangeStream(t, ctx, server, "/api/v1/projects/project-1/events")
	defer response.Body.Close()
	lines, readErrors := scanSSELines(response.Body)

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("SSE stream ended before a keepalive was received")
			}
			if line == ": keepalive" {
				return
			}
		case err := <-readErrors:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for SSE keepalive")
		}
	}
}

func TestChangeStreamMapsActivityAndDeletionNotifications(t *testing.T) {
	reader := changeReaderFunc(func(_ context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		return project.ChangeReplay{
			ProjectID: projectID, AfterRevision: afterRevision, CurrentRevision: 1,
		}, nil
	})
	stream := NewChangeStream()
	server := newChangeStreamServer(t, stream, reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := openChangeStream(t, ctx, server, "/api/v1/projects/project-1/events?after_revision=1")
	defer response.Body.Close()
	events, readErrors := scanSSE(response.Body)
	if initial := receiveEvent(t, events, readErrors); initial.event.Type != "sync_complete" {
		t.Fatalf("initial event = %+v", initial)
	}

	stream.PublishActivityChanged("project-1")
	activity := receiveEvent(t, events, readErrors)
	if activity.id != "" || activity.event.Type != "activity_changed" || activity.event.Revision != 0 {
		t.Fatalf("activity event = %+v", activity)
	}

	stream.PublishProjectDeleted("project-1", 1)
	deleted := receiveEvent(t, events, readErrors)
	if deleted.id != "" || deleted.event.Type != "project_deleted" || deleted.event.Revision != 1 {
		t.Fatalf("project-deleted event = %+v", deleted)
	}
}

func TestChangeStreamSlowSubscriberCatchesDroppedWakeupByRevisionReplay(t *testing.T) {
	initialReplayStarted := make(chan struct{})
	releaseInitialReplay := make(chan struct{})
	var calls atomic.Int64
	entityID := "fact-1"
	change := project.BlackboardChange{
		ProjectID: "project-1", Revision: 1, Kind: project.ChangeProjectCreated,
		EntityType: "fact", EntityID: &entityID,
		Data: json.RawMessage(`{"title":"created"}`),
	}
	reader := changeReaderFunc(func(ctx context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		if calls.Add(1) == 1 {
			close(initialReplayStarted)
			select {
			case <-releaseInitialReplay:
				return project.ChangeReplay{
					ProjectID: projectID, AfterRevision: afterRevision, CurrentRevision: 0,
				}, nil
			case <-ctx.Done():
				return project.ChangeReplay{}, ctx.Err()
			}
		}
		replay := project.ChangeReplay{
			ProjectID: projectID, AfterRevision: afterRevision, CurrentRevision: 1,
		}
		if afterRevision < 1 {
			replay.Changes = []project.BlackboardChange{change}
		}
		return replay, nil
	})
	stream := NewChangeStream()
	stream.replayInterval = 5 * time.Millisecond
	server := newChangeStreamServer(t, stream, reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	responseResult := make(chan *http.Response, 1)
	requestError := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(
			ctx, http.MethodGet, server.URL+"/api/v1/projects/project-1/events?after_revision=0", nil,
		)
		if err != nil {
			requestError <- err
			return
		}
		response, err := server.Client().Do(request)
		if err != nil {
			requestError <- err
			return
		}
		responseResult <- response
	}()

	select {
	case <-initialReplayStarted:
	case err := <-requestError:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("initial replay did not start")
	}
	for range changeStreamSubscriberBuffer {
		stream.PublishActivityChanged("project-1")
	}
	// The subscriber queue is full, so this best-effort revision wakeup is
	// intentionally dropped. The periodic replay must still observe revision 1.
	stream.PublishChange(change)
	close(releaseInitialReplay)

	var response *http.Response
	select {
	case response = <-responseResult:
	case err := <-requestError:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("SSE request did not receive response headers")
	}
	defer response.Body.Close()
	events, readErrors := scanSSE(response.Body)
	for {
		received := receiveEvent(t, events, readErrors)
		if received.event.Revision != 1 {
			continue
		}
		if received.id != "1" || received.event.Type != string(project.ChangeProjectCreated) ||
			received.event.Data["fact_id"] != "fact-1" {
			t.Fatalf("replayed change event = %+v", received)
		}
		break
	}
	if calls.Load() < 2 {
		t.Fatalf("change reader calls = %d, want periodic replay after initial query", calls.Load())
	}
}

func TestChangeStreamConcurrentPublishAndRequestCancellation(t *testing.T) {
	reader := changeReaderFunc(func(_ context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		return project.ChangeReplay{
			ProjectID: projectID, AfterRevision: afterRevision, CurrentRevision: 0,
		}, nil
	})
	stream := NewChangeStream()
	server := newChangeStreamServer(t, stream, reader)

	const subscriberCount = 24
	start := make(chan struct{})
	stopPublishing := make(chan struct{})
	var publishers sync.WaitGroup
	for publisherID := range 8 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			<-start
			for revision := 0; ; revision++ {
				select {
				case <-stopPublishing:
					return
				default:
					stream.PublishActivityChanged("project-" + strconv.Itoa((publisherID+revision)%subscriberCount))
					runtime.Gosched()
				}
			}
		}()
	}

	clientErrors := make(chan error, subscriberCount)
	var clients sync.WaitGroup
	for index := range subscriberCount {
		clients.Add(1)
		go func() {
			defer clients.Done()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			<-start
			request, err := http.NewRequestWithContext(
				ctx,
				http.MethodGet,
				server.URL+"/api/v1/projects/project-"+strconv.Itoa(index)+"/events?after_revision=0",
				nil,
			)
			if err != nil {
				clientErrors <- err
				return
			}
			response, err := server.Client().Do(request)
			if err != nil {
				clientErrors <- err
				return
			}
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"sync_complete"`) {
					cancel()
					return
				}
			}
			if err := scanner.Err(); err != nil && ctx.Err() == nil {
				clientErrors <- err
				return
			}
			clientErrors <- io.ErrUnexpectedEOF
		}()
	}
	close(start)
	clientsDone := make(chan struct{})
	go func() {
		clients.Wait()
		close(clientsDone)
	}()
	select {
	case <-clientsDone:
	case err := <-clientErrors:
		close(stopPublishing)
		publishers.Wait()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		close(stopPublishing)
		publishers.Wait()
		t.Fatal("concurrent SSE subscriptions did not complete cancellation")
	}
	close(stopPublishing)
	publishers.Wait()
	select {
	case err := <-clientErrors:
		t.Fatal(err)
	default:
	}
}

func TestChangeStreamForeignProjectTrafficCannotDropRevisionWakeup(t *testing.T) {
	initialReplayStarted := make(chan struct{})
	releaseInitialReplay := make(chan struct{})
	var calls atomic.Int64
	change := project.BlackboardChange{
		ProjectID: "project-1", Revision: 1, Kind: project.ChangeProjectCreated,
		Data: json.RawMessage(`{"title":"created"}`),
	}
	reader := changeReaderFunc(func(ctx context.Context, projectID string, afterRevision int64) (project.ChangeReplay, error) {
		if calls.Add(1) == 1 {
			close(initialReplayStarted)
			select {
			case <-releaseInitialReplay:
				return project.ChangeReplay{ProjectID: projectID}, nil
			case <-ctx.Done():
				return project.ChangeReplay{}, ctx.Err()
			}
		}
		return project.ChangeReplay{ProjectID: projectID, CurrentRevision: 1, Changes: []project.BlackboardChange{change}}, nil
	})
	stream := NewChangeStream()
	// Only a relevant notification can wake this subscriber during the test.
	stream.replayInterval = time.Hour
	server := newChangeStreamServer(t, stream, reader)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		response *http.Response
		err      error
	}
	responses := make(chan result, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/projects/project-1/events", nil)
		if err != nil {
			responses <- result{err: err}
			return
		}
		response, err := server.Client().Do(request)
		responses <- result{response: response, err: err}
	}()
	select {
	case <-initialReplayStarted:
	case <-time.After(time.Second):
		t.Fatal("initial replay did not start")
	}
	// The first History read is blocked, so a global subscriber queue would
	// fill with these unrelated events and drop the actual Project wakeup.
	for range changeStreamSubscriberBuffer {
		stream.PublishActivityChanged("project-2")
	}
	stream.PublishChange(change)
	close(releaseInitialReplay)
	var response *http.Response
	select {
	case received := <-responses:
		if received.err != nil {
			t.Fatal(received.err)
		}
		response = received.response
	case <-time.After(time.Second):
		t.Fatal("SSE response did not arrive")
	}
	defer response.Body.Close()
	events, readErrors := scanSSE(response.Body)
	if synced := receiveEvent(t, events, readErrors); synced.event.Type != eventSyncComplete {
		t.Fatalf("initial event=%+v", synced)
	}
	received := receiveEvent(t, events, readErrors)
	if received.id != "1" || received.event.ProjectID != "project-1" || received.event.Revision != 1 {
		t.Fatalf("Project revision event=%+v", received)
	}
}

func newChangeStreamServer(t *testing.T, stream *ChangeStream, changes projectChangeReader) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterEvents(router.Group("/api/v1"), stream, changes)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func openChangeStream(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	path string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status = %d, body = %s", response.StatusCode, payload)
	}
	return response
}

func scanSSE(body io.Reader) (<-chan receivedEvent, <-chan error) {
	events := make(chan receivedEvent, 256)
	errors := make(chan error, 1)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(body)
		var id string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				var event wireEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					errors <- err
					return
				}
				events <- receivedEvent{id: id, event: event}
				id = ""
			}
		}
		if err := scanner.Err(); err != nil {
			errors <- err
		}
	}()
	return events, errors
}

func scanSSELines(body io.Reader) (<-chan string, <-chan error) {
	lines := make(chan string, 32)
	errors := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			errors <- err
		}
	}()
	return lines, errors
}

func receiveEvent(
	t *testing.T,
	events <-chan receivedEvent,
	errors <-chan error,
) receivedEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			select {
			case err := <-errors:
				t.Fatalf("SSE stream ended: %v", err)
			default:
				t.Fatal("SSE stream ended")
			}
		}
		return event
	case err := <-errors:
		t.Fatalf("read SSE event: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
	return receivedEvent{}
}
