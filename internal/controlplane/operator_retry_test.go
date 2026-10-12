package controlplane

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestOperatorRetryHTTPUsesRevisionAndServerOwnedIdentity(t *testing.T) {
	application, err := NewControlPlaneTestHost(filepath.Join(t.TempDir(), "aether.db"), nil, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()
	created, err := application.module.projects.Create(t.Context(), project.CreateProjectRequest{Title: "operator retry HTTP", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	router := application.Handler()
	path := "/api/v1/projects/" + created.Project.ID + "/actions/retry-exhausted"
	for _, body := range []map[string]any{
		{"request_id": "missing-revision"},
		{"request_id": "actor-injection", "expected_revision": created.Project.Revision, "operator_name": "browser"},
		{"expected_revision": created.Project.Revision},
	} {
		response := requestV1JSON(t, router, http.MethodPost, path, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %d %s", response.Code, response.Body.String())
		}
	}
	body := map[string]any{"request_id": "retry-http", "expected_revision": created.Project.Revision}
	first := requestV1JSON(t, router, http.MethodPost, path, body)
	if first.Code != http.StatusOK {
		t.Fatalf("retry response: %d %s", first.Code, first.Body.String())
	}
	var receipt rundomain.RetryExhaustedReceipt
	decodeV1Response(t, first.Body.Bytes(), &receipt)
	if receipt.Replayed || receipt.ProjectRevision != created.Project.Revision || receipt.RunIDs == nil || len(receipt.RunIDs) != 0 {
		t.Fatalf("receipt: %+v", receipt)
	}
	if _, err := application.module.projects.PauseProject(t.Context(), created.Project.ID, project.ProjectLifecycleRequest{ExpectedRevision: created.Project.Revision}); err != nil {
		t.Fatal(err)
	}
	replay := requestV1JSON(t, router, http.MethodPost, path, body)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay after lifecycle change: %d %s", replay.Code, replay.Body.String())
	}
	decodeV1Response(t, replay.Body.Bytes(), &receipt)
	if !receipt.Replayed {
		t.Fatal("retry command replay was not recognized")
	}
	body["request_id"], body["expected_revision"] = "retry-paused", created.Project.Revision+1
	response := requestV1JSON(t, router, http.MethodPost, path, body)
	if response.Code != http.StatusConflict {
		t.Fatalf("paused retry accepted: %d %s", response.Code, response.Body.String())
	}
}
