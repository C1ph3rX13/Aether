package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type activityProjectionTestService struct {
	page   rundomain.ActivityPage
	detail rundomain.ActivityRunDetail
	chain  []rundomain.ActivityRun
}

func (service activityProjectionTestService) ListProjectActivity(
	context.Context, string, rundomain.ActivityQuery,
) (rundomain.ActivityPage, error) {
	return service.page, nil
}

func (service activityProjectionTestService) GetProjectActivityRunDetail(
	context.Context, string, rundomain.RunID,
) (rundomain.ActivityRunDetail, error) {
	return service.detail, nil
}

func (service activityProjectionTestService) GetProjectRetryChain(
	context.Context, string, rundomain.RunID,
) ([]rundomain.ActivityRun, error) {
	return service.chain, nil
}

func TestActivityRoutesExposeCaptureEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		run            rundomain.ActivityRun
		wantReason     rundomain.CaptureFailureReason
		wantDiagnostic rundomain.CaptureDiagnostic
		wantSource     rundomain.ResultEvidenceSource
	}{
		{
			name: "failed capture",
			run: rundomain.ActivityRun{
				ID: "run-capture", ProjectID: "project-1", Task: rundomain.TaskExplore,
				Status: rundomain.StatusFailed, Attempt: 1,
				CaptureFailureReason: rundomain.CaptureFailureInvalidCandidate,
				CaptureDiagnostic:    rundomain.CaptureDiagnosticResultFileInvalid,
			},
			wantReason:     rundomain.CaptureFailureInvalidCandidate,
			wantDiagnostic: rundomain.CaptureDiagnosticResultFileInvalid,
			wantSource:     "",
		},
		{
			name: "file candidate",
			run: rundomain.ActivityRun{
				ID: "run-repaired", ProjectID: "project-1", Task: rundomain.TaskExplore,
				Status: rundomain.StatusDeclined, Attempt: 2,
				OutcomeKind:          rundomain.OutcomeDecline,
				ResultEvidenceSource: rundomain.ResultEvidenceSourceFile,
			},
			wantDiagnostic: "",
			wantSource:     rundomain.ResultEvidenceSourceFile,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := activityProjectionTestService{
				page:   rundomain.ActivityPage{Runs: []rundomain.ActivityRun{test.run}},
				detail: rundomain.ActivityRunDetail{ActivityRun: test.run},
				chain:  []rundomain.ActivityRun{test.run},
			}
			router := gin.New()
			RegisterActivity(router.Group("/api/v1"), service)

			endpoints := []struct {
				name string
				path string
				body any
			}{
				{name: "list", path: "/api/v1/projects/project-1/activity", body: &rundomain.ActivityPage{}},
				{name: "detail", path: "/api/v1/projects/project-1/activity/" + string(test.run.ID), body: &rundomain.ActivityRunDetail{}},
				{name: "retry chain", path: "/api/v1/projects/project-1/activity/" + string(test.run.ID) + "/retry-chain", body: &[]rundomain.ActivityRun{}},
			}
			for _, endpoint := range endpoints {
				t.Run(endpoint.name, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodGet, endpoint.path, nil)
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					if response.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					if err := json.Unmarshal(response.Body.Bytes(), endpoint.body); err != nil {
						t.Fatal(err)
					}
					var got rundomain.ActivityRun
					switch body := endpoint.body.(type) {
					case *rundomain.ActivityPage:
						got = body.Runs[0]
					case *rundomain.ActivityRunDetail:
						got = body.ActivityRun
					case *[]rundomain.ActivityRun:
						got = (*body)[0]
					default:
						t.Fatalf("unsupported response type %T", endpoint.body)
					}
					if got.CaptureFailureReason != test.wantReason || got.CaptureDiagnostic != test.wantDiagnostic ||
						got.ResultEvidenceSource != test.wantSource {
						t.Fatalf("capture evidence: reason=%q diagnostic=%q source=%q", got.CaptureFailureReason, got.CaptureDiagnostic, got.ResultEvidenceSource)
					}
				})
			}
		})
	}
}
