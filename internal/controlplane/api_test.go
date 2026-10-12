package controlplane

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectdomain "github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestAPIV1UsesCanonicalProjectCommandsAndStableErrors(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "v1", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var project projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &project)

	paused := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects/"+project.Project.ID+"/actions/pause", map[string]any{"expected_revision": project.Project.Revision})
	if paused.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", paused.Code, paused.Body.String())
	}
	canonical := requestV1JSON(t, router, http.MethodGet, "/api/v1/projects/"+project.Project.ID, nil)
	decodeV1Response(t, canonical.Body.Bytes(), &project)
	if project.Project.Status != projectdomain.ProjectStatusPaused {
		t.Fatalf("v1 status=%q, want paused", project.Project.Status)
	}
	legacyRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.Project.ID, nil)
	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, legacyRequest)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("unversioned API status=%d, want 404", legacy.Code)
	}

	invalid := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects/"+project.Project.ID+"/actions/rename", map[string]string{})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid command status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	var failure struct {
		Code   rundomain.ErrorCode `json:"code"`
		Detail string              `json:"detail"`
	}
	decodeV1Response(t, invalid.Body.Bytes(), &failure)
	if failure.Code != rundomain.ErrorInvalidArgument || !strings.Contains(failure.Detail, "expected_revision") {
		t.Fatalf("stable v1 error: %+v", failure)
	}
	nullRevision := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects/"+project.Project.ID+"/actions/rename", map[string]any{
		"title": "invalid", "expected_revision": nil,
	})
	if nullRevision.Code != http.StatusBadRequest {
		t.Fatalf("null expected_revision status=%d body=%s", nullRevision.Code, nullRevision.Body.String())
	}

	conn, err := application.db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO projects (id, title, status, bootstrap_enabled, created_at, generation, blackboard_revision)
		VALUES ('legacy-zero', 'Legacy zero', 'active', 0, '2026-01-01T00:00:00Z', 1, 0);
		INSERT INTO facts (id, project_id, description) VALUES ('origin', 'legacy-zero', 'origin');
		INSERT INTO facts (id, project_id, description) VALUES ('goal', 'legacy-zero', 'goal');
	`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	if err := projectdomain.InitializeHistory(t.Context(), application.db); err != nil {
		t.Fatal(err)
	}
	zeroRevision := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects/legacy-zero/actions/rename", map[string]any{
		"title": "Legacy first command", "expected_revision": 0,
	})
	if zeroRevision.Code != http.StatusOK {
		t.Fatalf("revision-zero command status=%d body=%s", zeroRevision.Code, zeroRevision.Body.String())
	}
	var zeroReceipt projectdomain.RenameProjectReceipt
	decodeV1Response(t, zeroRevision.Body.Bytes(), &zeroReceipt)
	if zeroReceipt.Project.Revision != 1 || zeroReceipt.Project.Title != "Legacy first command" {
		t.Fatalf("revision-zero command receipt: %+v", zeroReceipt)
	}

	oversized := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": strings.Repeat("x", 6*1024*1024), "origin": "origin", "goal": "goal",
	})
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized command status=%d body=%s", oversized.Code, oversized.Body.String())
	}
	decodeV1Response(t, oversized.Body.Bytes(), &failure)
	if failure.Code != rundomain.ErrorInvalidArgument {
		t.Fatalf("oversized command error: %+v", failure)
	}

	oversizedMutation := requestV1JSON(t, router, http.MethodPost,
		"/api/v1/projects/"+project.Project.ID+"/actions/rename", map[string]any{
			"title": strings.Repeat("x", 6*1024*1024), "expected_revision": project.Project.Revision,
		})
	if oversizedMutation.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized revision command status=%d body=%s", oversizedMutation.Code, oversizedMutation.Body.String())
	}
}

func TestAPIV1RejectsInvalidActivityLimit(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "activity limit", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)

	response := requestV1JSON(t, router, http.MethodGet,
		"/api/v1/projects/"+detail.Project.ID+"/activity?limit=abc", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid Activity limit status=%d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Code rundomain.ErrorCode `json:"code"`
	}
	decodeV1Response(t, response.Body.Bytes(), &failure)
	if failure.Code != rundomain.ErrorInvalidArgument {
		t.Fatalf("invalid Activity limit error=%+v", failure)
	}
}

func TestAPIV1ProjectCatalogUsesProjectReadModelContract(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	empty := requestV1JSON(t, router, http.MethodGet, "/api/v1/projects", nil)
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty Catalog status=%d body=%s", empty.Code, empty.Body.String())
	}

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "Catalog contract", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)

	response := requestV1JSON(t, router, http.MethodGet, "/api/v1/projects", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("Catalog status=%d body=%s", response.Code, response.Body.String())
	}
	var entries []map[string]any
	decodeV1Response(t, response.Body.Bytes(), &entries)
	if len(entries) != 1 {
		t.Fatalf("Catalog entries=%d body=%s", len(entries), response.Body.String())
	}
	entry := entries[0]
	for _, retired := range []string{"id", "active_fact_count", "open_intent_count", "active_hint_count", "activity"} {
		if _, exists := entry[retired]; exists {
			t.Fatalf("Catalog retained retired field %q: %s", retired, response.Body.String())
		}
	}
	projectValue, ok := entry["project"].(map[string]any)
	if !ok || projectValue["id"] != detail.Project.ID || projectValue["title"] != "Catalog contract" {
		t.Fatalf("Catalog project metadata: %#v", entry["project"])
	}
	blackboard, ok := entry["blackboard"].(map[string]any)
	if !ok {
		t.Fatalf("Catalog blackboard: %#v", entry["blackboard"])
	}
	assertCatalogCount := func(name string, total, current float64) {
		t.Helper()
		counts, ok := blackboard[name].(map[string]any)
		if !ok || counts["total"] != total || counts[map[string]string{"facts": "active", "intents": "open", "hints": "active"}[name]] != current {
			t.Fatalf("Catalog %s counts: %#v", name, blackboard[name])
		}
	}
	assertCatalogCount("facts", 2, 2)
	assertCatalogCount("intents", 0, 0)
	assertCatalogCount("hints", 0, 0)
	execution, ok := entry["execution"].(map[string]any)
	if !ok || len(execution) != 1 || execution["status"] != "idle" {
		t.Fatalf("Catalog execution: %#v", entry["execution"])
	}
}

func TestAPIV1WorkspaceExecutionEvidenceUsesProjectReadModelContract(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "Evidence contract", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)

	response := requestV1JSON(t, router, http.MethodGet,
		"/api/v1/projects/"+detail.Project.ID+"/execution-evidence?intent_id=i001", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("Workspace Execution Evidence status=%d body=%s", response.Code, response.Body.String())
	}
	var evidence map[string]any
	decodeV1Response(t, response.Body.Bytes(), &evidence)
	if evidence["project_id"] != detail.Project.ID || evidence["active_reason"] != nil || evidence["selected_intent_run"] != nil {
		t.Fatalf("Workspace Execution Evidence: %#v", evidence)
	}

	missing := requestV1JSON(t, router, http.MethodGet,
		"/api/v1/projects/proj_missing/execution-evidence", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing Workspace Execution Evidence status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestAPIV1ProjectYAMLExportPreservesCanonicalFieldNames(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "YAML contract", "origin": "origin", "goal": "goal", "bootstrap_enabled": true,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)

	exported := requestV1JSON(t, router, http.MethodGet, "/api/v1/projects/"+detail.Project.ID+"/export?format=yaml", nil)
	if exported.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", exported.Code, exported.Body.String())
	}
	body := exported.Body.String()
	for _, canonical := range []string{"bootstrap_enabled:", "created_at:", "completion_records:"} {
		if !strings.Contains(body, canonical) {
			t.Fatalf("YAML export omitted canonical field %q:\n%s", canonical, body)
		}
	}
	for _, collapsed := range []string{"bootstrapenabled:", "createdat:", "completionrecords:"} {
		if strings.Contains(body, collapsed) {
			t.Fatalf("YAML export retained collapsed field %q:\n%s", collapsed, body)
		}
	}
}

func TestAPIV1ProjectLogUsesRevisionGroupedProjectContract(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "Log contract", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)
	projectID := detail.Project.ID

	renamed := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects/"+projectID+"/actions/rename", map[string]any{
		"title": "Renamed Log contract", "expected_revision": 1,
	})
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}

	type logRevision struct {
		Revision   int64                    `json:"revision"`
		ChangeKind projectdomain.ChangeKind `json:"change_kind"`
		Source     struct {
			Kind  string  `json:"kind"`
			Name  string  `json:"name"`
			RunID *string `json:"run_id"`
		} `json:"source"`
		Events []map[string]any `json:"events"`
	}
	type logPage struct {
		ProjectID                 string        `json:"project_id"`
		AvailableFromRevision     int64         `json:"available_from_revision"`
		AvailableFromOccurredAt   string        `json:"available_from_occurred_at"`
		CurrentRevision           int64         `json:"current_revision"`
		CurrentRevisionOccurredAt string        `json:"current_revision_occurred_at"`
		Revisions                 []logRevision `json:"revisions"`
		NextBeforeRevision        *int64        `json:"next_before_revision"`
	}

	latest := requestV1JSON(t, router, http.MethodGet, "/api/v1/projects/"+projectID+"/log?limit=1", nil)
	if latest.Code != http.StatusOK || strings.Contains(latest.Body.String(), `"activity"`) {
		t.Fatalf("latest Log status=%d body=%s", latest.Code, latest.Body.String())
	}
	var latestPage logPage
	decodeV1Response(t, latest.Body.Bytes(), &latestPage)
	if latestPage.ProjectID != projectID || latestPage.AvailableFromRevision != 1 ||
		latestPage.AvailableFromOccurredAt == "" || latestPage.CurrentRevision != 2 ||
		latestPage.CurrentRevisionOccurredAt == "" || latestPage.NextBeforeRevision == nil ||
		*latestPage.NextBeforeRevision != 2 || len(latestPage.Revisions) != 1 {
		t.Fatalf("latest Log page: %+v", latestPage)
	}
	latestGroup := latestPage.Revisions[0]
	if latestGroup.Revision != 2 || latestGroup.ChangeKind != projectdomain.ChangeProjectRenamed ||
		latestGroup.Source.Kind != "operator" || latestGroup.Source.Name != "test-operator" || latestGroup.Source.RunID != nil ||
		len(latestGroup.Events) != 1 || latestGroup.Events[0]["kind"] != "project_renamed" ||
		latestGroup.Events[0]["previous_title"] != "Log contract" || latestGroup.Events[0]["title"] != "Renamed Log contract" ||
		latestGroup.Events[0]["event_id"] != projectID+":r2:project_renamed:"+projectID {
		t.Fatalf("latest Log revision: %+v", latestGroup)
	}

	older := requestV1JSON(t, router, http.MethodGet,
		"/api/v1/projects/"+projectID+"/log?before_revision=2&limit=1", nil)
	if older.Code != http.StatusOK {
		t.Fatalf("older Log status=%d body=%s", older.Code, older.Body.String())
	}
	var olderPage logPage
	decodeV1Response(t, older.Body.Bytes(), &olderPage)
	if olderPage.AvailableFromRevision != latestPage.AvailableFromRevision ||
		olderPage.CurrentRevision != latestPage.CurrentRevision || olderPage.NextBeforeRevision != nil ||
		len(olderPage.Revisions) != 1 || olderPage.Revisions[0].Revision != 1 ||
		len(olderPage.Revisions[0].Events) != 1 || olderPage.Revisions[0].Events[0]["kind"] != "project_created" {
		t.Fatalf("older Log page: %+v", olderPage)
	}

	for _, path := range []string{
		"/api/v1/projects/" + projectID + "/log?before_revision=invalid",
		"/api/v1/projects/" + projectID + "/log?limit=invalid",
		"/api/v1/projects/" + projectID + "/log?limit=101",
	} {
		response := requestV1JSON(t, router, http.MethodGet, path, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid Log query %q status=%d body=%s", path, response.Code, response.Body.String())
		}
		var failure struct {
			Code rundomain.ErrorCode `json:"code"`
		}
		decodeV1Response(t, response.Body.Bytes(), &failure)
		if failure.Code != rundomain.ErrorInvalidArgument {
			t.Fatalf("invalid Log query %q error=%+v", path, failure)
		}
	}

	missing := requestV1JSON(t, router, http.MethodGet, "/api/v1/projects/proj_missing/log", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing Log status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestAPIV1RemovesLegacyHistoryRoutesAndKeepsCurrentHistoryReads(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "History contract", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)
	basePath := "/api/v1/projects/" + detail.Project.ID

	for _, path := range []string{basePath + "/history", basePath + "/history/1"} {
		response := requestV1JSON(t, router, http.MethodGet, path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired History route %q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}

	logResponse := requestV1JSON(t, router, http.MethodGet, basePath+"/log", nil)
	if logResponse.Code != http.StatusOK {
		t.Fatalf("Project Log status=%d body=%s", logResponse.Code, logResponse.Body.String())
	}
	snapshotResponse := requestV1JSON(t, router, http.MethodGet, basePath+"/history/1/blackboard", nil)
	if snapshotResponse.Code != http.StatusOK {
		t.Fatalf("Revision Snapshot status=%d body=%s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	var snapshot projectdomain.ProjectDetail
	decodeV1Response(t, snapshotResponse.Body.Bytes(), &snapshot)
	if snapshot.Project.ID != detail.Project.ID || snapshot.Project.Revision != 1 {
		t.Fatalf("Revision Snapshot: %+v", snapshot.Project)
	}
}

func TestAPIV1ProjectEventStreamReplaysFromRevisionCursor(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	serverHTTP := httptest.NewServer(application.Handler())
	defer serverHTTP.Close()

	created := requestHTTPJSON(t, serverHTTP.Client(), http.MethodPost, serverHTTP.URL+"/api/v1/projects", map[string]any{
		"title": "events", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	defer created.Body.Close()
	var project projectdomain.ProjectDetail
	if err := json.NewDecoder(created.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		serverHTTP.URL+"/api/v1/projects/"+project.Project.ID+"/events?after_revision=0", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := serverHTTP.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE response: status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.Contains(line, `"type":"sync_complete"`) {
			break
		}
	}
	cancel()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "id: 1") || !strings.Contains(joined, `"revision":1`) ||
		!strings.Contains(joined, `"project_id":"`+project.Project.ID+`"`) ||
		!strings.Contains(joined, `"type":"sync_complete"`) ||
		!strings.Contains(joined, `"current_revision":1`) {
		t.Fatalf("revision replay event: %s", joined)
	}
}

func TestAPIV1ProjectEventStreamRejectsFutureRevisionCursor(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	router := application.Handler()

	created := requestV1JSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "future events", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail projectdomain.ProjectDetail
	decodeV1Response(t, created.Body.Bytes(), &detail)

	response := requestV1JSON(t, router, http.MethodGet,
		"/api/v1/projects/"+detail.Project.ID+"/events?after_revision=2", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("future SSE cursor status=%d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Code rundomain.ErrorCode `json:"code"`
	}
	decodeV1Response(t, response.Body.Bytes(), &failure)
	if failure.Code != rundomain.ErrorInvalidArgument {
		t.Fatalf("future SSE cursor error=%+v", failure)
	}
}

func TestAPIV1ProjectEventStreamTerminatesOnCommittedDeletion(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	serverHTTP := httptest.NewServer(application.Handler())
	defer serverHTTP.Close()

	created := requestHTTPJSON(t, serverHTTP.Client(), http.MethodPost, serverHTTP.URL+"/api/v1/projects", map[string]any{
		"title": "delete events", "origin": "origin", "goal": "goal", "bootstrap_enabled": false,
	})
	defer created.Body.Close()
	var detail projectdomain.ProjectDetail
	if err := json.NewDecoder(created.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		serverHTTP.URL+"/api/v1/projects/"+detail.Project.ID+"/events?after_revision=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := serverHTTP.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	type streamEvent struct {
		Type      string `json:"type"`
		ProjectID string `json:"project_id"`
		Revision  int64  `json:"revision,omitempty"`
	}
	events := make(chan streamEvent, 4)
	scanErrors := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event streamEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				scanErrors <- err
				return
			}
			events <- event
			if event.Type == "project_deleted" {
				return
			}
		}
		scanErrors <- scanner.Err()
	}()

	select {
	case event := <-events:
		if event.Type != "sync_complete" {
			t.Fatalf("initial SSE event = %+v, want sync_complete", event)
		}
	case err := <-scanErrors:
		t.Fatalf("read initial SSE event: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial SSE sync")
	}

	deleted := requestHTTPJSON(t, serverHTTP.Client(), http.MethodPost,
		serverHTTP.URL+"/api/v1/projects/"+detail.Project.ID+"/actions/delete", map[string]any{
			"expected_title": "delete events", "expected_revision": 1,
		})
	defer deleted.Body.Close()
	if deleted.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(deleted.Body)
		t.Fatalf("delete status=%d body=%s", deleted.StatusCode, payload)
	}

	select {
	case event := <-events:
		if event.Type != "project_deleted" || event.ProjectID != detail.Project.ID || event.Revision != 1 {
			t.Fatalf("deletion SSE event = %+v", event)
		}
	case err := <-scanErrors:
		t.Fatalf("read deletion SSE event: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Project deletion event")
	}
}

func requestV1JSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requestHTTPJSON(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeV1Response(t *testing.T, payload []byte, destination any) {
	t.Helper()
	if err := json.Unmarshal(payload, destination); err != nil {
		t.Fatalf("decode response %s: %v", payload, err)
	}
}
