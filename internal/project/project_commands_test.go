package project

import (
	"encoding/json"
	"testing"
)

func TestCreateProjectRecordsOneOperatorOwnedFullAfterImage(t *testing.T) {
	service, _ := newRenameTestService(t)
	detail, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Definition", Origin: "known origin", Goal: "target state",
		BootstrapEnabled: true,
		Hints:            []CreateHintInline{{Content: "inspect the boundary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 1 || len(detail.Hints) != 1 || detail.Hints[0].Creator != "deployment-operator" {
		t.Fatalf("created Project = %+v", detail)
	}
	if len(detail.Intents) != 1 || detail.Intents[0].Kind != IntentKindBootstrap || detail.Intents[0].Status != IntentStatusOpen {
		t.Fatalf("bootstrap business projection = %+v", detail.Intents)
	}

	replay, err := service.ListChanges(t.Context(), detail.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if replay.CurrentRevision != 1 || len(replay.Changes) != 1 || replay.Changes[0].Kind != ChangeProjectCreated {
		t.Fatalf("creation history = %+v", replay)
	}
	var payload struct {
		SchemaVersion int           `json:"schema_version"`
		Source        ChangeSource  `json:"source"`
		Before        any           `json:"before"`
		After         ProjectDetail `json:"after"`
	}
	if err := json.Unmarshal(replay.Changes[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != ChangeSchemaVersion || payload.Source.Name != "deployment-operator" ||
		payload.Before != nil || payload.After.Project.Revision != 1 ||
		len(payload.After.Facts) != 2 || len(payload.After.Intents) != 1 || len(payload.After.Hints) != 1 {
		t.Fatalf("creation after-image = %+v", payload)
	}
}
