package project

import (
	"encoding/json"
	"testing"
)

func TestFactJSONRoundtrip(t *testing.T) {
	fact := Fact{ID: "f001", Description: "target is 10.10.10.1"}
	data, err := json.Marshal(fact)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Fact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "f001" || got.Description != "target is 10.10.10.1" {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
}

func TestIntentJSONWithNullFields(t *testing.T) {
	intent := Intent{
		ID:          "i001",
		From:        []string{"origin"},
		To:          nil,
		Description: "scan target",
		Creator:     "dispatcher.bootstrap",
		CreatedAt:   "2026-07-04T12:00:00Z",
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Verify null fields are omitted
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if _, ok := raw["to"]; ok {
		t.Error("null To should be omitted")
	}
	if _, ok := raw["worker"]; ok {
		t.Error("legacy worker field should not be serialized")
	}
}

func TestCreateProjectRequestValidation(t *testing.T) {
	req := CreateProjectRequest{
		Title:            "test project",
		Origin:           "target IP is 10.10.10.1",
		Goal:             "get root shell",
		BootstrapEnabled: true,
	}
	if req.Title == "" || req.Origin == "" || req.Goal == "" {
		t.Error("required fields should not be empty")
	}
}
