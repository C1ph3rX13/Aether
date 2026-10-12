package run

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInputIdentityV1GoldenVectors(t *testing.T) {
	tests := []struct {
		name  string
		input interface{ InputIdentityV1() (InputIdentity, error) }
		want  InputIdentity
	}{
		{
			name: "Blackboard Unicode statuses and ordered sources",
			input: BlackboardInput{
				Generation: 2, Revision: 7, TargetIntentID: "意图-1",
				Project: InputProject{Title: "研究 Ω", Origin: "起点", Goal: "目标", BootstrapEnabled: true},
				Facts: []InputFact{
					{ID: "f2", Description: "旧事实", Status: "retracted", Supersedes: "f0", ReplacementFactID: "f3"},
					{ID: "f1", Description: "现状", Status: "active"},
				},
				Intents: []InputIntent{
					{ID: "i3", From: []string{"f1"}, Description: "已取消", Creator: "agent-γ", Status: "cancelled"},
					{ID: "i2", From: []string{"f2", "f1"}, Description: "待探索", Creator: "agent-β", Status: "open"},
					{ID: "i1", From: []string{}, To: "f2", Description: "已完成", Creator: "agent-α", Status: "concluded"},
				},
				Hints: []InputHint{
					{ID: "h2", Content: "旧提示", Creator: "操作员", CreatedAt: "2026-08-03T12:00:00Z", Status: "withdrawn"},
					{ID: "h1", Content: "优先验证", Creator: "操作员", Status: "active"},
				},
			},
			want: InputIdentity{Schema: InputIdentitySchemaV1, SHA256: "a14cb777651243035e108a4784e98cbf2fd3315e20b3cc092a4c525080acbecf"},
		},
		{
			name: "Blackboard absent target and empty collections",
			input: BlackboardInput{
				Generation: 1,
				Project:    InputProject{Title: "Empty", Origin: "start", Goal: "finish"},
			},
			want: InputIdentity{Schema: InputIdentitySchemaV1, SHA256: "a232d865aa4eb433fc3f784f369a1217c93bc25854b43612835aefdcca36c084"},
		},
		{
			name: "Bootstrap Unicode and empty hints",
			input: BootstrapInput{
				Generation: 3, Revision: 11, TargetIntentID: "启动-1", Origin: "源", Goal: "终点",
			},
			want: InputIdentity{Schema: InputIdentitySchemaV1, SHA256: "6ed9d5da7a918149dff95871c8fdedf1c0a3a0c764af699a358a766eb1bc24a8"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.input.InputIdentityV1()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Input Identity = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestInputIdentityRequiresExactV1SchemaAndLowercaseSHA256(t *testing.T) {
	valid := InputIdentity{Schema: InputIdentitySchemaV1, SHA256: strings.Repeat("a", 64)}
	if err := valid.ValidateV1(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []InputIdentity{
		{},
		{Schema: "aether.input-hash/v2", SHA256: valid.SHA256},
		{Schema: InputIdentitySchemaV1, SHA256: strings.Repeat("A", 64)},
		{Schema: InputIdentitySchemaV1, SHA256: strings.Repeat("a", 63)},
	} {
		if err := invalid.ValidateV1(); err == nil {
			t.Fatalf("accepted invalid Input Identity: %+v", invalid)
		}
	}
}

func TestInputSnapshotJSONUsesSchemaBearingIdentityAndReadsLegacyEvidence(t *testing.T) {
	snapshot := InputSnapshot{
		Generation: 2, Revision: 7,
		Identity: InputIdentity{Schema: InputIdentitySchemaV1, SHA256: strings.Repeat("a", 64)},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"generation":2,"revision":7,"identity":{"schema":"aether.input-hash/v1","sha256":"` + strings.Repeat("a", 64) + `"}}`
	if string(data) != want {
		t.Fatalf("Input Snapshot JSON = %s, want %s", data, want)
	}

	legacyJSON := `{"generation":1,"revision":3,"hash":"` + strings.Repeat("b", 64) + `"}`
	var legacy InputSnapshot
	if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Identity != (InputIdentity{Schema: LegacyRenderedInputIdentitySchema, SHA256: strings.Repeat("b", 64)}) {
		t.Fatalf("legacy Input Identity = %+v", legacy.Identity)
	}
}

func TestInputIdentityNormalizesOrderingWhileRenderedHashTracksExactBytes(t *testing.T) {
	first := BlackboardInput{
		Generation: 1, Revision: 4,
		Project: InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts: []InputFact{
			{ID: "f2", Description: "second"},
			{ID: "f1", Description: "first"},
		},
		Intents: []InputIntent{{
			ID: "i1", From: []string{"f2", "f1"}, Description: "inspect", Creator: "reasoner",
		}},
	}
	second := first
	second.Facts = []InputFact{first.Facts[1], first.Facts[0]}
	second.Intents = []InputIntent{{
		ID: "i1", From: []string{"f1", "f2"}, Description: "inspect", Creator: "reasoner",
	}}
	firstIdentity, err := first.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity, err := second.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	if firstIdentity != secondIdentity {
		t.Fatalf("projection ordering changed Input Identity: first=%+v second=%+v", firstIdentity, secondIdentity)
	}

	rendered, _, err := first.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	formatted := append(append([]byte(nil), rendered...), '\n')
	if RenderedInputHashFor(rendered) == RenderedInputHashFor(formatted) {
		t.Fatal("rendered byte change did not change Rendered Input Hash")
	}
	if identity, err := first.InputIdentityV1(); err != nil || identity != firstIdentity {
		t.Fatalf("rendering format changed structured identity: identity=%+v error=%v", identity, err)
	}
}

func TestInputIdentityDiscriminatesBlackboardAndBootstrapKinds(t *testing.T) {
	blackboard, err := (BlackboardInput{
		Generation: 1, Revision: 2, TargetIntentID: "i1",
		Project: InputProject{Title: "Bootstrap", Origin: "origin", Goal: "goal", BootstrapEnabled: true},
	}).InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := (BootstrapInput{
		Generation: 1, Revision: 2, TargetIntentID: "i1", Origin: "origin", Goal: "goal",
	}).InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	if blackboard == bootstrap {
		t.Fatalf("Blackboard and Bootstrap identities collided: %+v", blackboard)
	}
}

func TestStructuredInputsMatchOnlyTheirExactInputSnapshot(t *testing.T) {
	target := "i1"
	blackboard := BlackboardInput{
		Generation: 2, Revision: 7, TargetIntentID: target,
		Project: InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
	}
	identity, err := blackboard.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := InputSnapshot{
		Generation: 2, Revision: 7, Identity: identity, TargetIntentID: &target,
	}
	if !blackboard.MatchesInputSnapshot(snapshot) {
		t.Fatal("exact Blackboard Input Snapshot did not match")
	}

	changed := snapshot
	changed.Revision++
	if blackboard.MatchesInputSnapshot(changed) {
		t.Fatal("Blackboard input matched a different Revision")
	}
	changed = snapshot
	changed.Identity.SHA256 = strings.Repeat("f", 64)
	if blackboard.MatchesInputSnapshot(changed) {
		t.Fatal("Blackboard input matched a different identity")
	}
	changed = snapshot
	changed.TargetIntentID = nil
	if blackboard.MatchesInputSnapshot(changed) {
		t.Fatal("targeted Blackboard input matched an absent target")
	}

	bootstrap := BootstrapInput{
		Generation: 2, Revision: 7, TargetIntentID: target, Origin: "origin", Goal: "goal",
	}
	bootstrapIdentity, err := bootstrap.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.MatchesInputSnapshot(InputSnapshot{
		Generation: 2, Revision: 7, Identity: bootstrapIdentity, TargetIntentID: &target,
	}) {
		t.Fatal("exact Bootstrap Input Snapshot did not match")
	}
}
