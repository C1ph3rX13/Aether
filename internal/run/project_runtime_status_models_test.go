package run

import "testing"

func TestProjectRuntimeStatusRequestRequiresBoundedUniqueProjects(t *testing.T) {
	valid := ProjectRuntimeStatusRequest{
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
		ProjectIDs: []string{"project-a", "project-b"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Project runtime status request: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ProjectRuntimeStatusRequest)
	}{
		{name: "empty batch", mutate: func(request *ProjectRuntimeStatusRequest) { request.ProjectIDs = nil }},
		{name: "too many projects", mutate: func(request *ProjectRuntimeStatusRequest) {
			request.ProjectIDs = make([]string, MaximumProjectRuntimeStatusBatch+1)
			for index := range request.ProjectIDs {
				request.ProjectIDs[index] = stringOfByte('p', 250) + string(rune(index))
			}
		}},
		{name: "duplicate project", mutate: func(request *ProjectRuntimeStatusRequest) {
			request.ProjectIDs = []string{"project-a", "project-a"}
		}},
		{name: "missing dispatcher", mutate: func(request *ProjectRuntimeStatusRequest) { request.DispatcherID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatalf("invalid request accepted: %+v", request)
			}
		})
	}
}

func TestProjectRuntimeStatusResultsRequireExactRequestedOrder(t *testing.T) {
	requested := []string{"active", "missing"}
	valid := []ProjectRuntimeStatusResult{
		{ProjectID: "active", Status: ProjectRuntimeActive},
		{ProjectID: "missing", Status: ProjectRuntimeMissing},
	}
	if err := ValidateProjectRuntimeStatusResults(requested, valid); err != nil {
		t.Fatalf("valid Project runtime status results: %v", err)
	}
	if err := ValidateProjectRuntimeStatusResults(requested, valid[:1]); err == nil {
		t.Fatal("partial Project runtime status results were accepted")
	}
	invalid := append([]ProjectRuntimeStatusResult(nil), valid...)
	invalid[1].Status = "archived"
	if err := ValidateProjectRuntimeStatusResults(requested, invalid); err == nil {
		t.Fatal("unknown Project runtime status was accepted")
	}
}
