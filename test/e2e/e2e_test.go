package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/controlplane"
)

func TestOperatorBlackboardLifecycle(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	assertStatus(t, doJSON(t, client, http.MethodGet, server.URL+"/api/v1/settings", nil), http.StatusNotFound)

	var detail projectDetailWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects", map[string]any{
		"title": "Test Project", "origin": "Starting point", "goal": "Reach the goal", "bootstrap_enabled": false,
	}), http.StatusCreated, &detail)
	projectID := detail.Project.ID
	if detail.Project.Revision != 1 || len(detail.Facts) != 2 {
		t.Fatalf("created Blackboard: %+v", detail)
	}

	var hint hintReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/hints", map[string]any{
		"content": "Look at port 8080", "expected_revision": 1,
	}), http.StatusCreated, &hint)
	if hint.Hint.Creator != "test-operator" || hint.ProjectRevision != 2 {
		t.Fatalf("Hint receipt: %+v", hint)
	}

	var impact impactWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/impact", map[string]any{
		"action": "complete_project", "expected_revision": 2,
	}), http.StatusOK, &impact)
	if impact.ProjectRevision != 2 {
		t.Fatalf("impact preview: %+v", impact)
	}

	var paused lifecycleReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/pause", map[string]any{
		"expected_revision": 2,
	}), http.StatusOK, &paused)
	if paused.Project.Status != "paused" || paused.ProjectRevision != 3 || paused.Project.Revision != 3 {
		t.Fatalf("paused Project: %+v", paused)
	}

	var pausedHint hintReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/hints", map[string]any{
		"content": "Feedback while paused", "expected_revision": 3,
	}), http.StatusCreated, &pausedHint)
	if pausedHint.ProjectRevision != 4 {
		t.Fatalf("paused Hint receipt: %+v", pausedHint)
	}

	var resumed lifecycleReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/resume", map[string]any{
		"expected_revision": 4,
	}), http.StatusOK, &resumed)
	if resumed.Project.Status != "active" || resumed.ProjectRevision != 5 || resumed.Project.Revision != 5 {
		t.Fatalf("resumed Project: %+v", resumed)
	}

	var completed completeReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/complete", map[string]any{
		"from": []string{"origin"}, "description": "Goal achieved", "expected_revision": 5,
	}), http.StatusOK, &completed)
	if completed.Project.Status != "completed" || completed.Project.Revision != 6 || completed.Completion.Actor != "test-operator" {
		t.Fatalf("completion receipt: %+v", completed)
	}

	assertStatus(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/hints", map[string]any{
		"content": "late feedback", "expected_revision": 6,
	}), http.StatusConflict)

	var reopened reopenReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/reopen", map[string]any{
		"feedback": "New evidence requires another pass", "expected_revision": 6,
	}), http.StatusOK, &reopened)
	if reopened.Project.Status != "active" || reopened.Project.Generation != 2 || reopened.Project.Revision != 7 {
		t.Fatalf("reopened Project: %+v", reopened)
	}

	var activity activityWire
	decodeResponse(t, doJSON(t, client, http.MethodGet, server.URL+"/api/v1/projects/"+projectID+"/activity?view=all", nil), http.StatusOK, &activity)
	if len(activity.Runs) != 0 {
		t.Fatalf("new Project activity contains Runs: %+v", activity.Runs)
	}

	var revisionTwo projectDetailWire
	decodeResponse(t, doJSON(t, client, http.MethodGet, server.URL+"/api/v1/projects/"+projectID+"/history/2/blackboard", nil), http.StatusOK, &revisionTwo)
	if revisionTwo.Project.Revision != 2 || len(revisionTwo.Hints) != 1 || revisionTwo.Project.Status != "active" {
		t.Fatalf("frozen revision 2: %+v", revisionTwo)
	}

	var renamed renameReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/rename", map[string]any{
		"title": "Renamed Project", "expected_revision": 7,
	}), http.StatusOK, &renamed)
	if renamed.ProjectRevision != 8 || renamed.Project.Title != "Renamed Project" {
		t.Fatalf("rename receipt: %+v", renamed)
	}
	var stale map[string]any
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/rename", map[string]any{
		"title": "Stale", "expected_revision": 7,
	}), http.StatusConflict, &stale)
	if stale["current_revision"] != float64(8) {
		t.Fatalf("stale command response: %+v", stale)
	}

	assertStatus(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/delete", map[string]any{
		"expected_title": "Test Project", "expected_revision": 8,
	}), http.StatusConflict)
	var deleted deleteReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects/"+projectID+"/actions/delete", map[string]any{
		"expected_title": "Renamed Project", "expected_revision": 8,
	}), http.StatusOK, &deleted)
	if deleted.ProjectID != projectID || deleted.CleanupJobID == "" {
		t.Fatalf("delete receipt: %+v", deleted)
	}
	assertStatus(t, doJSON(t, client, http.MethodGet, server.URL+"/api/v1/projects/"+projectID, nil), http.StatusNotFound)
}

func TestBlackboardCorrectionAndHistory(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()
	var detail projectDetailWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects", map[string]any{
		"title": "Corrections", "origin": "old", "goal": "goal", "bootstrap_enabled": false,
	}), http.StatusCreated, &detail)

	var correction correctionReceiptWire
	decodeResponse(t, doJSON(t, client, http.MethodPost,
		server.URL+"/api/v1/projects/"+detail.Project.ID+"/facts/origin/actions/supersede", map[string]any{
			"description": "corrected", "reason": "bad source", "expected_revision": 1,
		}), http.StatusCreated, &correction)
	if correction.Original.Status != "retracted" || correction.Replacement.Description != "corrected" || correction.ProjectRevision != 2 {
		t.Fatalf("correction receipt: %+v", correction)
	}

	var replay changeReplayWire
	decodeResponse(t, doJSON(t, client, http.MethodGet,
		server.URL+"/api/v1/projects/"+detail.Project.ID+"/changes?after_revision=0", nil), http.StatusOK, &replay)
	if replay.CurrentRevision != 2 || len(replay.Changes) != 2 || replay.Changes[1].Kind != "fact_superseded" {
		t.Fatalf("change replay: %+v", replay)
	}

	var historical projectDetailWire
	decodeResponse(t, doJSON(t, client, http.MethodGet,
		server.URL+"/api/v1/projects/"+detail.Project.ID+"/history/1/blackboard", nil), http.StatusOK, &historical)
	var historicalOrigin *factWire
	for index := range historical.Facts {
		if historical.Facts[index].ID == "origin" {
			historicalOrigin = &historical.Facts[index]
			break
		}
	}
	if historicalOrigin == nil || historicalOrigin.Status != "active" || historicalOrigin.Description != "old" {
		t.Fatalf("revision 1 was mutated: %+v", historical.Facts)
	}

	assertStatus(t, doJSON(t, client, http.MethodPost,
		server.URL+"/api/v1/projects/"+detail.Project.ID+"/facts/goal/actions/retract", map[string]any{
			"reason": "not allowed", "expected_revision": 2,
		}), http.StatusForbidden)
}

func TestLegacyManualWorkerRoutesAreRetired(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()
	var detail projectDetailWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects", map[string]any{
		"title": "No manual workers", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	}), http.StatusCreated, &detail)

	for _, path := range []string{
		"/api/v1/projects/" + detail.Project.ID + "/intents",
		"/api/v1/projects/" + detail.Project.ID + "/intents/i001/actions/renew",
		"/api/v1/projects/" + detail.Project.ID + "/intents/i001/actions/release",
		"/api/v1/projects/" + detail.Project.ID + "/intents/i001/actions/conclude",
		"/api/v1/projects/" + detail.Project.ID + "/reason/actions/claim",
		"/api/v1/projects/" + detail.Project.ID + "/reason/actions/renew",
		"/api/v1/projects/" + detail.Project.ID + "/reason/actions/release",
	} {
		assertStatus(t, doJSON(t, client, http.MethodPost, server.URL+path, map[string]any{}), http.StatusNotFound)
	}
}

func TestProjectSSEStream(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()
	var detail projectDetailWire
	decodeResponse(t, doJSON(t, client, http.MethodPost, server.URL+"/api/v1/projects", map[string]any{
		"title": "SSE live", "origin": "origin", "goal": "goal",
	}), http.StatusCreated, &detail)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type streamEvent struct {
		Type      string         `json:"type"`
		ProjectID string         `json:"project_id"`
		Revision  int64          `json:"revision,omitempty"`
		Data      map[string]any `json:"data,omitempty"`
	}
	events := make(chan streamEvent, 1)
	synced := make(chan struct{})
	errors := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			server.URL+"/api/v1/projects/"+detail.Project.ID+"/events", nil)
		if err != nil {
			errors <- err
			return
		}
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Last-Event-ID", "1")
		response, err := server.Client().Do(request)
		if err != nil {
			errors <- err
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			errors <- fmt.Errorf("SSE status %d", response.StatusCode)
			return
		}
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data: ") {
				continue
			}
			var event streamEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
				errors <- err
				return
			}
			if event.Type == "sync_complete" {
				close(synced)
				continue
			}
			if event.Type == "hint_created" {
				events <- event
				return
			}
		}
		errors <- scanner.Err()
	}()

	select {
	case <-synced:
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial SSE sync")
	}
	var createdHint hintReceiptWire
	decodeResponse(t, doJSON(
		t,
		server.Client(),
		http.MethodPost,
		server.URL+"/api/v1/projects/"+detail.Project.ID+"/hints",
		map[string]any{"content": "live hint", "expected_revision": 1},
	), http.StatusCreated, &createdHint)
	select {
	case event := <-events:
		if event.ProjectID != detail.Project.ID || event.Revision != 2 ||
			event.Data["hint_id"] != createdHint.Hint.ID {
			t.Fatalf("SSE Project: %+v", event)
		}
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	host, err := controlplane.NewControlPlaneHost(
		filepath.Join(t.TempDir(), "aether.db"), nil,
		controlplane.ControlPlaneHostOptions{OperatorName: "test-operator", DeploymentID: "e2e-deployment"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(); err != nil {
			t.Errorf("Control Plane shutdown: %v", err)
		}
	})
	server := httptest.NewServer(host.Handler())
	t.Cleanup(server.Close)
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(provider.Close)
	var read config.ConfigurationRead
	decodeResponse(t, doJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/configuration", nil), http.StatusOK, &read)
	draft := read.RecommendedDraft
	draft.Runtime.WorkerHealthcheck = "disabled"
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "e2e-worker", AgentType: "codex", ProviderName: "fixture",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 1, Priority: 1,
		AgentEnv: map[string]string{"CODEX_BASE_URL": provider.URL, "CODEX_MODEL": "fixture"},
	}}
	decodeResponse(t, doJSON(t, server.Client(), http.MethodPut, server.URL+"/api/v1/configuration", config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: read.DraftVersion, Draft: draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"e2e-worker": {"OPENAI_API_KEY": {Value: "e2e-secret"}},
		},
	}), http.StatusOK, &read)
	decodeResponse(t, doJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/configuration/revisions/active", nil), http.StatusOK, nil)
	return server
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, wantStatus int, target any) {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s: got %d, want %d; body: %s", response.Request.Method, response.Request.URL.Path, response.StatusCode, wantStatus, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s: %v; body: %s", response.Request.URL.Path, err, data)
		}
	}
}

func assertStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode == want {
		return
	}
	data, _ := io.ReadAll(response.Body)
	t.Fatalf("%s %s: got %d, want %d; body: %s", response.Request.Method, response.Request.URL.Path, response.StatusCode, want, data)
}
