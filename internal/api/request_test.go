package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type requestCommands struct {
	ProjectCommands
	calls    int
	revision int64
	err      error
}

func (commands *requestCommands) Create(context.Context, project.CreateProjectRequest) (project.ProjectDetail, error) {
	commands.calls++
	return project.ProjectDetail{}, commands.err
}

func (commands *requestCommands) UpdateTitle(_ context.Context, _ string, request project.UpdateProjectTitleRequest) (project.RenameProjectReceipt, error) {
	commands.calls++
	commands.revision = request.ExpectedRevision
	return project.RenameProjectReceipt{}, nil
}

type requestRuns struct {
	RunControl
	calls int
	err   error
}

func (runs *requestRuns) AcquireDispatcherLease(context.Context, rundomain.DispatcherLeaseRequest) (rundomain.DispatcherLease, error) {
	runs.calls++
	return rundomain.DispatcherLease{}, runs.err
}

func (runs *requestRuns) StartRun(context.Context, rundomain.StartRequest) (*rundomain.AdmittedRun, error) {
	runs.calls++
	return &rundomain.AdmittedRun{}, runs.err
}

type requestSecrets struct {
	calls int
}

func (secrets *requestSecrets) ResolveRunSecrets(context.Context, rundomain.RunSecretRequest) (*rundomain.ResolvedSecrets, error) {
	secrets.calls++
	return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "not authorized"}
}

func (secrets *requestSecrets) ResolveRevisionSecrets(context.Context, string, string, rundomain.DispatcherLease) (*rundomain.ResolvedSecrets, error) {
	secrets.calls++
	return nil, &rundomain.DomainError{Code: rundomain.ErrorForbidden, Detail: "not authorized"}
}

type requestConfiguration struct {
	Configuration
	calls int
	err   error
}

func (configuration *requestConfiguration) SaveDraft(context.Context, config.DynamicDraftSaveRequest) (*config.ConfigurationRead, error) {
	configuration.calls++
	return &config.ConfigurationRead{}, configuration.err
}

func TestWriteRoutesValidateCompleteJSONBeforeCallingModules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []struct {
		name, path, body string
		status           int
	}{
		{"Project creation", "/projects", `{"title":"Aether","origin":"start","goal":"finish"}`, http.StatusCreated},
		{"Project command", "/projects/p1/actions/rename", `{"title":"renamed","expected_revision":0}`, http.StatusOK},
		{"Run Control", "/dispatcher-leases/actions/acquire", `{"instance_id":"d1","ttl_seconds":60}`, http.StatusOK},
		{"Run start", "/runs/actions/start", `{"project_id":"p1","task":"reason"}`, http.StatusCreated},
		{"Run Secret", "/runs/r1/actions/resolve-secrets", `{"run_id":"r1","fencing_token":1,"dispatcher_id":"d1","dispatcher_fencing_token":1}`, http.StatusForbidden},
		{"Revision Secret", "/configuration/revisions/c1/workers/w1/actions/resolve-secrets", `{"instance_id":"d1","fencing_token":1}`, http.StatusForbidden},
		{"Configuration", "/configuration", `{"expected_draft_version":1,"draft":{}}`, http.StatusOK},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, test := range []struct {
				name, body string
				status     int
			}{
				{"valid", route.body, route.status},
				{"trailing whitespace", route.body + " \n\t", route.status},
				{"empty", "", http.StatusBadRequest},
				{"null", "null", http.StatusBadRequest},
				{"array", "[]", http.StatusBadRequest},
				{"incomplete", route.body[:len(route.body)-1], http.StatusBadRequest},
				{"second object", route.body + `{}`, http.StatusBadRequest},
				{"trailing garbage", route.body + ` garbage`, http.StatusBadRequest},
				{"unknown field", route.body[:len(route.body)-1] + `,"obsolete":true}`, http.StatusBadRequest},
				{"oversized trailing whitespace", route.body + strings.Repeat(" ", int(maximumRequestBodyBytes)), http.StatusRequestEntityTooLarge},
			} {
				t.Run(test.name, func(t *testing.T) {
					commands := &requestCommands{}
					runs := &requestRuns{}
					secrets := &requestSecrets{}
					configuration := &requestConfiguration{}
					router := gin.New()
					group := router.Group("/api/v1")
					RegisterProjects(group, nil, nil, commands, nil)
					RegisterRuns(group, runs)
					RegisterRunSecrets(group, secrets)
					RegisterRevisionSecrets(group, secrets)
					RegisterConfiguration(group, configuration)
					method := http.MethodPost
					if route.name == "Configuration" {
						method = http.MethodPut
					}
					response := serveJSON(router, method, "/api/v1"+route.path, test.body)
					if response.Code != test.status {
						t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
					}
					calls := commands.calls + runs.calls + secrets.calls + configuration.calls
					wantCalls := 0
					if test.name == "valid" || test.name == "trailing whitespace" {
						wantCalls = 1
					} else {
						var failure struct{ Code string }
						if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Code != "invalid_argument" {
							t.Fatalf("failure=%s err=%v", response.Body.String(), err)
						}
					}
					if calls != wantCalls {
						t.Fatalf("module calls=%d want=%d", calls, wantCalls)
					}
				})
			}
		})
	}
}

func TestProjectCommandsRequireIntegerRevisionAndAcceptZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		body     string
		status   int
		revision int64
	}{
		{`{"title":"renamed"}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":null}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":"0"}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":0.5}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":9223372036854775808}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","EXPECTED_REVISION":7}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":7,"EXPECTED_REVISION":1}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","EXPECTED_REVISION":1,"expected_revision":7}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":7,"Expected_Revision":1}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":7,"EXPECTED_REVISION":null}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":7,"expected_reviſion":1}`, http.StatusBadRequest, 0},
		{`{"title":"renamed","expected_revision":0}`, http.StatusOK, 0},
		{`{"title":"renamed","expected_revision":7}`, http.StatusOK, 7},
	} {
		commands := &requestCommands{}
		router := gin.New()
		RegisterProjects(router.Group("/api/v1"), nil, nil, commands, nil)
		response := serveJSON(router, http.MethodPost, "/api/v1/projects/p1/actions/rename", test.body)
		wantCalls := 0
		if test.status == http.StatusOK {
			wantCalls = 1
		}
		if response.Code != test.status || commands.calls != wantCalls || commands.revision != test.revision {
			t.Errorf("body=%s status=%d calls=%d revision=%d", test.body, response.Code, commands.calls, commands.revision)
		}
		if test.status == http.StatusBadRequest {
			var failure struct{ Code string }
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Code != "invalid_argument" {
				t.Fatalf("failure=%s err=%v", response.Body.String(), err)
			}
		}
	}
}

func TestRunStartAppliesFiveMiBBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, extra := range []int{0, 1} {
		runs := &requestRuns{}
		router := gin.New()
		RegisterRuns(router.Group("/api/v1"), runs)
		body := `{"project_id":"p1","task":"reason"}`
		body += strings.Repeat(" ", 5*1024*1024-len(body)+extra)
		response := serveJSON(router, http.MethodPost, "/api/v1/runs/actions/start", body)
		if extra == 0 && (response.Code != http.StatusCreated || runs.calls != 1) {
			t.Fatalf("body at limit status=%d calls=%d", response.Code, runs.calls)
		}
		if extra == 1 && (response.Code != http.StatusRequestEntityTooLarge || runs.calls != 0) {
			t.Fatalf("body over limit status=%d calls=%d", response.Code, runs.calls)
		}
	}
}

func TestRunRouteAppliesCommandBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, extra := range []int{0, 1} {
		runs := &requestRuns{}
		router := gin.New()
		RegisterRuns(router.Group("/api/v1"), runs)
		body := `{"instance_id":"d1","ttl_seconds":60}`
		body += strings.Repeat(" ", int(runControlSmallBodyBytes)-len(body)+extra)
		response := serveJSON(router, http.MethodPost, "/api/v1/dispatcher-leases/actions/acquire", body)
		if extra == 0 && (response.Code != http.StatusOK || runs.calls != 1) {
			t.Fatalf("body at limit status=%d calls=%d", response.Code, runs.calls)
		}
		if extra == 1 && (response.Code != http.StatusRequestEntityTooLarge || runs.calls != 0) {
			t.Fatalf("body over limit status=%d calls=%d", response.Code, runs.calls)
		}
	}
}

func TestRunCommandsRetainBindingValidationAndPathIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runs := &requestRuns{}
	secrets := &requestSecrets{}
	router := gin.New()
	RegisterRuns(router.Group("/api/v1"), runs)
	RegisterRunSecrets(router.Group("/api/v1"), secrets)
	for _, test := range []struct{ path, body string }{
		{"/dispatcher-leases/actions/acquire", `{"ttl_seconds":60}`},
		{"/dispatcher-leases/actions/acquire", `{"instance_id":"d1","ttl_seconds":0}`},
		{"/runs/r1/actions/resolve-secrets", `{"run_id":"r2","fencing_token":1,"dispatcher_id":"d1","dispatcher_fencing_token":1}`},
	} {
		response := serveJSON(router, http.MethodPost, "/api/v1"+test.path, test.body)
		if response.Code != http.StatusBadRequest || runs.calls+secrets.calls != 0 {
			t.Errorf("path=%s status=%d calls=%d body=%s", test.path, response.Code, runs.calls+secrets.calls, response.Body.String())
		}
	}
}

func serveJSON(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
