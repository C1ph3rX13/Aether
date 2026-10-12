package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const InputIdentitySchemaV1 = "aether.input-hash/v1"

const LegacyRenderedInputIdentitySchema = "aether.input-hash/legacy-rendered-v0"

type InputIdentity struct {
	Schema string `json:"schema"`
	SHA256 string `json:"sha256"`
}

func (identity InputIdentity) ValidateV1() error {
	if identity.Schema != InputIdentitySchemaV1 || !lowercaseSHA256Hex(identity.SHA256) {
		return newError(ErrorInvalidArgument, "versioned input identity is invalid")
	}
	return nil
}

func (snapshot *InputSnapshot) UnmarshalJSON(data []byte) error {
	var encoded struct {
		Generation     int           `json:"generation"`
		Revision       int64         `json:"revision"`
		Identity       InputIdentity `json:"identity"`
		Hash           string        `json:"hash"`
		TargetIntentID *string       `json:"target_intent_id"`
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	if encoded.Identity != (InputIdentity{}) && encoded.Hash != "" {
		return fmt.Errorf("input snapshot contains both versioned and legacy identities")
	}
	identity := encoded.Identity
	if encoded.Hash != "" {
		if !sha256Hex(encoded.Hash) {
			return fmt.Errorf("legacy input snapshot hash is invalid")
		}
		identity = InputIdentity{Schema: LegacyRenderedInputIdentitySchema, SHA256: encoded.Hash}
	}
	*snapshot = InputSnapshot{
		Generation: encoded.Generation, Revision: encoded.Revision,
		Identity: identity, TargetIntentID: encoded.TargetIntentID,
	}
	return nil
}

func lowercaseSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

type inputIdentityV1Project struct {
	Title            string `json:"title"`
	Origin           string `json:"origin"`
	Goal             string `json:"goal"`
	BootstrapEnabled bool   `json:"bootstrap_enabled"`
}

type inputIdentityV1BootstrapProject struct {
	Origin string `json:"origin"`
	Goal   string `json:"goal"`
}

type inputIdentityV1Fact struct {
	ID                string `json:"id"`
	Description       string `json:"description"`
	Status            string `json:"status"`
	Supersedes        string `json:"supersedes"`
	ReplacementFactID string `json:"replacement_fact_id"`
}

type inputIdentityV1Intent struct {
	ID          string   `json:"id"`
	From        []string `json:"from"`
	To          string   `json:"to"`
	Description string   `json:"description"`
	Creator     string   `json:"creator"`
	Status      string   `json:"status"`
}

type inputIdentityV1Hint struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Creator   string `json:"creator"`
	CreatedAt string `json:"created_at"`
	Status    string `json:"status"`
}

type blackboardInputIdentityV1 struct {
	Kind           string                  `json:"kind"`
	InputSchema    string                  `json:"input_schema"`
	Generation     int                     `json:"generation"`
	Revision       int64                   `json:"revision"`
	TargetIntentID string                  `json:"target_intent_id"`
	Project        inputIdentityV1Project  `json:"project"`
	Facts          []inputIdentityV1Fact   `json:"facts"`
	Intents        []inputIdentityV1Intent `json:"intents"`
	Hints          []inputIdentityV1Hint   `json:"hints"`
}

type bootstrapInputIdentityV1 struct {
	Kind           string                          `json:"kind"`
	InputSchema    string                          `json:"input_schema"`
	Generation     int                             `json:"generation"`
	Revision       int64                           `json:"revision"`
	TargetIntentID string                          `json:"target_intent_id"`
	Project        inputIdentityV1BootstrapProject `json:"project"`
	Hints          []inputIdentityV1Hint           `json:"hints"`
}

type inputIdentityCanonicalV1 interface {
	blackboardInputIdentityV1 | bootstrapInputIdentityV1
}

func (input BlackboardInput) InputIdentityV1() (InputIdentity, error) {
	normalized, err := input.Normalize()
	if err != nil {
		return InputIdentity{}, err
	}
	canonical := blackboardInputIdentityV1{
		Kind: "blackboard", InputSchema: normalized.Schema,
		Generation: normalized.Generation, Revision: normalized.Revision,
		TargetIntentID: normalized.TargetIntentID,
		Project: inputIdentityV1Project{
			Title: normalized.Project.Title, Origin: normalized.Project.Origin,
			Goal: normalized.Project.Goal, BootstrapEnabled: normalized.Project.BootstrapEnabled,
		},
		Facts:   make([]inputIdentityV1Fact, len(normalized.Facts)),
		Intents: make([]inputIdentityV1Intent, len(normalized.Intents)),
		Hints:   canonicalInputIdentityV1Hints(normalized.Hints),
	}
	for index, fact := range normalized.Facts {
		canonical.Facts[index] = inputIdentityV1Fact{
			ID: fact.ID, Description: fact.Description, Status: fact.Status,
			Supersedes: fact.Supersedes, ReplacementFactID: fact.ReplacementFactID,
		}
	}
	for index, intent := range normalized.Intents {
		canonical.Intents[index] = inputIdentityV1Intent{
			ID: intent.ID, From: append([]string{}, intent.From...), To: intent.To,
			Description: intent.Description, Creator: intent.Creator, Status: intent.Status,
		}
	}
	return inputIdentityV1(canonical)
}

func (input BootstrapInput) InputIdentityV1() (InputIdentity, error) {
	normalized, err := input.Normalize()
	if err != nil {
		return InputIdentity{}, err
	}
	canonical := bootstrapInputIdentityV1{
		Kind: "bootstrap", InputSchema: normalized.Schema,
		Generation: normalized.Generation, Revision: normalized.Revision,
		TargetIntentID: normalized.TargetIntentID,
		Project:        inputIdentityV1BootstrapProject{Origin: normalized.Origin, Goal: normalized.Goal},
		Hints:          canonicalInputIdentityV1Hints(normalized.Hints),
	}
	return inputIdentityV1(canonical)
}

func (input BlackboardInput) MatchesInputSnapshot(snapshot InputSnapshot) bool {
	identity, err := input.InputIdentityV1()
	return err == nil && matchesInputSnapshot(
		snapshot, identity, input.Generation, input.Revision, input.TargetIntentID,
	)
}

func (input BootstrapInput) MatchesInputSnapshot(snapshot InputSnapshot) bool {
	identity, err := input.InputIdentityV1()
	return err == nil && matchesInputSnapshot(
		snapshot, identity, input.Generation, input.Revision, input.TargetIntentID,
	)
}

func canonicalInputIdentityV1Hints(hints []InputHint) []inputIdentityV1Hint {
	canonical := make([]inputIdentityV1Hint, len(hints))
	for index, hint := range hints {
		canonical[index] = inputIdentityV1Hint{
			ID: hint.ID, Content: hint.Content, Creator: hint.Creator,
			CreatedAt: hint.CreatedAt, Status: hint.Status,
		}
	}
	return canonical
}

func matchesInputSnapshot(
	snapshot InputSnapshot,
	identity InputIdentity,
	generation int,
	revision int64,
	targetIntentID string,
) bool {
	if snapshot.Identity != identity || snapshot.Generation != generation || snapshot.Revision != revision {
		return false
	}
	if targetIntentID == "" {
		return snapshot.TargetIntentID == nil
	}
	return snapshot.TargetIntentID != nil && *snapshot.TargetIntentID == targetIntentID
}

func inputIdentityV1[T inputIdentityCanonicalV1](canonical T) (InputIdentity, error) {
	data, err := json.Marshal(canonical)
	if err != nil {
		return InputIdentity{}, fmt.Errorf("marshal canonical input identity: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(InputIdentitySchemaV1))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(data)
	return InputIdentity{Schema: InputIdentitySchemaV1, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}
