package project

import "testing"

func TestValidateCreateProjectRequest(t *testing.T) {
	validated, domainErr := ValidateCreateProjectRequest(CreateProjectRequest{
		Title: "  Project  ", Origin: "  origin  ", Goal: "  goal  ",
		Hints: []CreateHintInline{{Content: "  inspect TLS  "}},
	})
	if domainErr != nil {
		t.Fatal(domainErr)
	}
	if validated.Title != "Project" || validated.Origin != "origin" || validated.Goal != "goal" ||
		len(validated.Hints) != 1 || validated.Hints[0].Content != "inspect TLS" {
		t.Fatalf("validated request: %+v", validated)
	}

	tests := []struct {
		name string
		req  CreateProjectRequest
	}{
		{name: "title", req: CreateProjectRequest{Origin: "origin", Goal: "goal"}},
		{name: "origin", req: CreateProjectRequest{Title: "project", Goal: "goal"}},
		{name: "goal", req: CreateProjectRequest{Title: "project", Origin: "origin"}},
		{name: "hint content", req: CreateProjectRequest{Title: "project", Origin: "origin", Goal: "goal", Hints: []CreateHintInline{{}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, domainErr := ValidateCreateProjectRequest(test.req); domainErr == nil || domainErr.Code != ErrorInvalidArgument {
				t.Fatalf("expected invalid argument, got %v", domainErr)
			}
		})
	}
}

func TestValidateCompleteRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CompleteRequest
		wantErr bool
		errMsg  string
	}{
		{"valid", CompleteRequest{From: []string{"f001"}, Description: "Goal achieved", ExpectedRevision: 1}, false, ""},
		{"revision zero", CompleteRequest{From: []string{"f001"}, Description: "Done"}, false, ""},
		{"negative revision", CompleteRequest{From: []string{"f001"}, Description: "Done", ExpectedRevision: -1}, true, "expected_revision must be a non-negative integer"},
		{"empty description", CompleteRequest{From: []string{"f001"}, Description: "  ", ExpectedRevision: 1}, true, "description is required"},
		{"empty from", CompleteRequest{Description: "Done", ExpectedRevision: 1}, true, "from is required and must not be empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, appErr := ValidateCompleteRequest(test.req)
			if (appErr != nil) != test.wantErr {
				t.Errorf("error = %v, wantErr %v", appErr, test.wantErr)
			}
			if appErr != nil && appErr.Detail != test.errMsg {
				t.Errorf("error detail = %q, want %q", appErr.Detail, test.errMsg)
			}
		})
	}
}

func TestValidateCreateHintRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateHintRequest
		wantErr bool
		errMsg  string
	}{
		{"valid", CreateHintRequest{Content: "Check port 8080", ExpectedRevision: 1}, false, ""},
		{"trimmed", CreateHintRequest{Content: "  Check port 8080  ", ExpectedRevision: 1}, false, ""},
		{"empty content", CreateHintRequest{Content: "  ", ExpectedRevision: 1}, true, "content is required"},
		{"revision zero", CreateHintRequest{Content: "Check port"}, false, ""},
		{"negative revision", CreateHintRequest{Content: "Check port", ExpectedRevision: -1}, true, "expected_revision must be a non-negative integer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, appErr := ValidateCreateHintRequest(test.req)
			if (appErr != nil) != test.wantErr {
				t.Errorf("error = %v, wantErr %v", appErr, test.wantErr)
			}
			if appErr != nil && appErr.Detail != test.errMsg {
				t.Errorf("error detail = %q, want %q", appErr.Detail, test.errMsg)
			}
		})
	}
}
