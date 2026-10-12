package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

const (
	changeStreamSubscriberBuffer = 10
	defaultReplayInterval        = time.Second
	defaultKeepaliveInterval     = 15 * time.Second
)

const (
	eventReplayRequired  = "replay_required"
	eventSyncComplete    = "sync_complete"
	eventActivityChanged = "activity_changed"
	eventProjectDeleted  = "project_deleted"
)

type sseEvent struct {
	Type      string `json:"type"`
	ProjectID string `json:"project_id"`
	Revision  int64  `json:"revision,omitempty"`
	Data      any    `json:"data,omitempty"`
}

type notificationKind uint8

const (
	notificationRevisionChanged notificationKind = iota
	notificationActivityChanged
	notificationProjectDeleted
)

type changeNotification struct {
	kind      notificationKind
	projectID string
	revision  int64
}

// ChangeStream owns best-effort transport notifications. Revision-bearing
// Blackboard changes remain authoritative in Project History and are replayed
// through projectChangeReader before being written to a client.
type ChangeStream struct {
	mu          sync.Mutex
	subscribers map[string]map[chan changeNotification]struct{}

	replayInterval    time.Duration
	keepaliveInterval time.Duration
}

func NewChangeStream() *ChangeStream {
	return &ChangeStream{
		subscribers:       make(map[string]map[chan changeNotification]struct{}),
		replayInterval:    defaultReplayInterval,
		keepaliveInterval: defaultKeepaliveInterval,
	}
}

// PublishChange satisfies Project's committed change notification seam.
func (stream *ChangeStream) PublishChange(change project.BlackboardChange) {
	stream.publish(changeNotification{
		kind: notificationRevisionChanged, projectID: change.ProjectID, revision: change.Revision,
	})
}

// PublishProjectChange satisfies Run's committed Project change notification seam.
func (stream *ChangeStream) PublishProjectChange(change rundomain.ProjectChange) {
	stream.publish(changeNotification{
		kind: notificationRevisionChanged, projectID: change.ProjectID, revision: change.Revision,
	})
}

// PublishActivityChanged satisfies Run's best-effort activity notification seam.
func (stream *ChangeStream) PublishActivityChanged(projectID string) {
	stream.publish(changeNotification{kind: notificationActivityChanged, projectID: projectID})
}

// PublishProjectDeleted emits the terminal event that cannot be replayed from
// Project History after the Project aggregate has been removed.
func (stream *ChangeStream) PublishProjectDeleted(projectID string, revision int64) {
	stream.publish(changeNotification{
		kind: notificationProjectDeleted, projectID: projectID, revision: revision,
	})
}

func (stream *ChangeStream) subscribe(projectID string) chan changeNotification {
	channel := make(chan changeNotification, changeStreamSubscriberBuffer)
	stream.mu.Lock()
	if stream.subscribers[projectID] == nil {
		stream.subscribers[projectID] = make(map[chan changeNotification]struct{})
	}
	stream.subscribers[projectID][channel] = struct{}{}
	stream.mu.Unlock()
	return channel
}

func (stream *ChangeStream) unsubscribe(projectID string, channel chan changeNotification) {
	stream.mu.Lock()
	delete(stream.subscribers[projectID], channel)
	if len(stream.subscribers[projectID]) == 0 {
		delete(stream.subscribers, projectID)
	}
	stream.mu.Unlock()
}

func (stream *ChangeStream) publish(notification changeNotification) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for channel := range stream.subscribers[notification.projectID] {
		select {
		case channel <- notification:
		default:
			// Notifications only wake a subscriber. Revision replay remains the
			// authoritative recovery path for a slow consumer.
		}
	}
}

type projectChangeReader interface {
	ListChanges(context.Context, string, int64) (project.ChangeReplay, error)
}

func RegisterEvents(
	group *gin.RouterGroup,
	stream *ChangeStream,
	changes projectChangeReader,
) {
	group.GET("/projects/:id/events", stream.handler(changes))
}

func (stream *ChangeStream) handler(changes projectChangeReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		projectID := c.Param("id")
		lastRevision, ok := parseSSERevisionCursor(c)
		if !ok {
			return
		}

		notifications := stream.subscribe(projectID)
		defer stream.unsubscribe(projectID, notifications)

		replay, err := changes.ListChanges(c.Request.Context(), projectID, lastRevision)
		if err != nil {
			writeError(c, err)
			return
		}

		setSSEHeaders(c.Writer)
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()

		if replay.ReplayRequired {
			if err := writeSSEEvent(c.Writer, sseEvent{
				Type: eventReplayRequired, ProjectID: projectID,
				Data: map[string]any{"current_revision": replay.CurrentRevision},
			}, false); err != nil {
				slog.Error("SSE: replay-required write error", "error", err)
				return
			}
			lastRevision = replay.CurrentRevision
		}
		for _, change := range replay.Changes {
			if err := writeSSEEvent(c.Writer, eventForChange(change), true); err != nil {
				slog.Error("SSE: replay write error", "error", err)
				return
			}
			lastRevision = change.Revision
		}
		if err := writeSSEEvent(c.Writer, sseEvent{
			Type: eventSyncComplete, ProjectID: projectID,
			Data: map[string]any{"current_revision": replay.CurrentRevision},
		}, false); err != nil {
			slog.Error("SSE: sync-complete write error", "error", err)
			return
		}
		c.Writer.Flush()

		replayTicker := time.NewTicker(stream.replayInterval)
		defer replayTicker.Stop()
		keepaliveTicker := time.NewTicker(stream.keepaliveInterval)
		defer keepaliveTicker.Stop()

		writePendingChanges := func(writer io.Writer) bool {
			replay, err := changes.ListChanges(c.Request.Context(), projectID, lastRevision)
			if err != nil {
				if project.IsProjectNotFound(err) {
					if writeErr := writeSSEEvent(writer, sseEvent{
						Type: eventProjectDeleted, ProjectID: projectID,
					}, false); writeErr != nil {
						slog.Error("SSE: project-deleted write error", "error", writeErr)
					}
					return false
				}
				slog.Error(
					"SSE: change replay query error",
					"project", projectID,
					"revision", lastRevision,
					"error", err,
				)
				return true
			}
			if replay.ReplayRequired {
				if err := writeSSEEvent(writer, sseEvent{
					Type: eventReplayRequired, ProjectID: projectID,
					Data: map[string]any{"current_revision": replay.CurrentRevision},
				}, false); err != nil {
					slog.Error("SSE: replay-required write error", "error", err)
					return false
				}
				lastRevision = replay.CurrentRevision
				return true
			}
			for _, change := range replay.Changes {
				if err := writeSSEEvent(writer, eventForChange(change), true); err != nil {
					slog.Error("SSE: change replay write error", "error", err)
					return false
				}
				lastRevision = change.Revision
			}
			return true
		}

		c.Stream(func(writer io.Writer) bool {
			select {
			case notification := <-notifications:
				switch notification.kind {
				case notificationRevisionChanged:
					return writePendingChanges(writer)
				case notificationActivityChanged:
					if err := writeSSEEvent(writer, sseEvent{
						Type: eventActivityChanged, ProjectID: projectID,
					}, false); err != nil {
						slog.Error("SSE: activity-changed write error", "error", err)
						return false
					}
					return true
				case notificationProjectDeleted:
					if err := writeSSEEvent(writer, sseEvent{
						Type: eventProjectDeleted, ProjectID: projectID, Revision: notification.revision,
					}, false); err != nil {
						slog.Error("SSE: project-deleted write error", "error", err)
					}
					return false
				default:
					return true
				}
			case <-replayTicker.C:
				return writePendingChanges(writer)
			case <-keepaliveTicker.C:
				if _, err := io.WriteString(writer, ": keepalive\n\n"); err != nil {
					slog.Error("SSE: keepalive write error", "error", err)
					return false
				}
				return true
			case <-c.Request.Context().Done():
				return false
			}
		})
	}
}

func parseSSERevisionCursor(c *gin.Context) (int64, bool) {
	cursor := c.GetHeader("Last-Event-ID")
	if cursor == "" {
		cursor = c.Query("after_revision")
	}
	if cursor == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || parsed < 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": "invalid_argument", "detail": "SSE revision cursor must be a non-negative integer",
		})
		return 0, false
	}
	return parsed, true
}

func setSSEHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("Access-Control-Allow-Origin", "*")
}

func eventForChange(change project.BlackboardChange) sseEvent {
	var data map[string]any
	if err := json.Unmarshal(change.Data, &data); err != nil {
		data = map[string]any{"change_data": change.Data}
	}
	if change.EntityID != nil && change.EntityType != "" {
		if data == nil {
			data = make(map[string]any)
		}
		key := change.EntityType + "_id"
		if _, exists := data[key]; !exists {
			data[key] = *change.EntityID
		}
	}
	return sseEvent{
		Type: string(change.Kind), ProjectID: change.ProjectID,
		Revision: change.Revision, Data: data,
	}
}

func writeSSEEvent(writer io.Writer, event sseEvent, includeRevisionID bool) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if includeRevisionID && event.Revision > 0 {
		if _, err := fmt.Fprintf(writer, "id: %d\n", event.Revision); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", data)
	return err
}
