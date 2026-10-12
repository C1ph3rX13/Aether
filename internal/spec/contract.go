package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	SchemaLegacyV1 = "aether.execution-spec/legacy-v1"
	SchemaV2       = "aether.execution-spec/v2"

	InputIdentityV1       = "aether.input-hash/v1"
	LegacyInputIdentityV1 = "aether.input-hash/legacy-rendered-v0"
	PromptTemplateV1      = "aether.prompt-template-hash/v1"
	LegacyPromptV1        = "aether.rendered-prompt-hash/legacy-v1"
)

const (
	maximumPhaseSeconds              = 24 * 60 * 60
	maximumRunSeconds                = 4*maximumPhaseSeconds + 4*60
	maximumResultFinalizationSeconds = 30
)

// ResultFinalizationPhaseSeconds is the bounded extra same-Run phase used to
// re-serialize an established outcome after an eligible capture or protocol
// failure without resuming business work.
func ResultFinalizationPhaseSeconds(phaseSeconds int) int {
	if phaseSeconds <= 0 {
		return 0
	}
	if phaseSeconds > maximumResultFinalizationSeconds {
		return maximumResultFinalizationSeconds
	}
	return phaseSeconds
}

// Projection is the query evidence derived from one authoritative document.
type Projection struct {
	InputGeneration     int
	InputRevision       int64
	InputEvidence       Identity
	PromptEvidence      Identity
	WorkerName          string
	ConfigRevision      string
	ImageReference      string
	ResultSchema        string
	DocumentTarget      *string
	MaxProposedIntents  int
	FallbackPhaseSecond int
	RecoveryPhaseSecond int
}

type Identity struct {
	Schema string `json:"schema"`
	SHA256 string `json:"sha256"`
}

type inputV2 struct {
	Generation     *int      `json:"generation"`
	Revision       *int64    `json:"revision"`
	Identity       *Identity `json:"identity"`
	TargetIntentID *string   `json:"target_intent_id,omitempty"`
}

type inputLegacy struct {
	Generation     *int      `json:"generation"`
	Revision       *int64    `json:"revision"`
	Identity       *Identity `json:"identity"`
	Hash           *string   `json:"hash"`
	TargetIntentID *string   `json:"target_intent_id,omitempty"`
}

type workerDocument struct {
	Name             *string         `json:"name"`
	Adapter          *string         `json:"adapter"`
	Model            json.RawMessage `json:"model,omitempty"`
	Capabilities     json.RawMessage `json:"capabilities,omitempty"`
	SecretReferences json.RawMessage `json:"secret_references,omitempty"`
}

type secretReference struct {
	EnvironmentVariable *string `json:"environment_variable"`
	Provider            *string `json:"provider"`
	Key                 *string `json:"key"`
}

type imageDocument struct {
	RequestedRef *string         `json:"requested_ref"`
	ImageID      json.RawMessage `json:"image_id,omitempty"`
	RepoDigest   json.RawMessage `json:"repo_digest,omitempty"`
}

type timeoutDocument struct {
	RunSeconds           *int `json:"run_seconds"`
	PhaseSeconds         *int `json:"phase_seconds"`
	FallbackPhaseSeconds *int `json:"fallback_phase_seconds,omitempty"`
	RecoveryPhaseSeconds *int `json:"recovery_phase_seconds,omitempty"`
	GraceSeconds         *int `json:"grace_seconds"`
}

type v2Document struct {
	Schema             *string          `json:"schema"`
	ConfigRevision     *string          `json:"config_revision"`
	Prompt             *Identity        `json:"prompt"`
	Input              *inputV2         `json:"input"`
	Worker             *workerDocument  `json:"worker"`
	Image              *imageDocument   `json:"image"`
	Timeouts           *timeoutDocument `json:"timeouts"`
	ResultSchema       *string          `json:"result_schema"`
	MaxProposedIntents *int             `json:"max_proposed_intents,omitempty"`
}

type legacyDocument struct {
	Schema             json.RawMessage  `json:"schema,omitempty"`
	ConfigRevision     *string          `json:"config_revision"`
	PromptHash         *string          `json:"prompt_hash"`
	PromptTemplateHash *string          `json:"prompt_template_hash"`
	Input              *inputLegacy     `json:"input"`
	Worker             *workerDocument  `json:"worker"`
	Image              *imageDocument   `json:"image"`
	Timeouts           *timeoutDocument `json:"timeouts"`
	ResultSchema       *string          `json:"result_schema"`
	MaxProposedIntents *int             `json:"max_proposed_intents,omitempty"`
}

// ValidateAndProject validates the selected document version and returns only
// the non-sensitive fields that the database is allowed to index.
func ValidateAndProject(schema string, data []byte, task string, rowTarget *string) (Projection, error) {
	if !json.Valid(data) {
		return Projection{}, fmt.Errorf("Execution Spec is not valid JSON")
	}
	if err := ValidateNoDuplicateFields(data); err != nil {
		return Projection{}, err
	}
	var projection Projection
	var err error
	switch schema {
	case SchemaLegacyV1:
		if err := validateLegacyFieldNames(data); err != nil {
			return Projection{}, err
		}
		projection, err = validateLegacy(data)
	case SchemaV2:
		if err := validateV2FieldNames(data); err != nil {
			return Projection{}, err
		}
		projection, err = validateV2(data)
	default:
		return Projection{}, fmt.Errorf("unsupported Execution Spec schema %q", schema)
	}
	if err != nil {
		return Projection{}, err
	}
	if err := validateTaskIdentity(task, rowTarget, projection, schema == SchemaV2); err != nil {
		return Projection{}, err
	}
	return projection, nil
}

func validateV2FieldNames(data []byte) error {
	root, err := validateObjectFields(data, "Execution Spec", true,
		"schema", "config_revision", "prompt", "input", "worker", "image",
		"timeouts", "result_schema", "max_proposed_intents",
	)
	if err != nil {
		return err
	}
	if raw := root["prompt"]; len(raw) != 0 {
		if _, err := validateObjectFields(raw, "Execution Spec Prompt", true, "schema", "sha256"); err != nil {
			return err
		}
	}
	if raw := root["input"]; len(raw) != 0 {
		input, err := validateObjectFields(raw, "Execution Spec Input", true,
			"generation", "revision", "identity", "target_intent_id",
		)
		if err != nil {
			return err
		}
		if identity := input["identity"]; len(identity) != 0 {
			if _, err := validateObjectFields(identity, "Execution Spec Input identity", true, "schema", "sha256"); err != nil {
				return err
			}
		}
	}
	return validateSharedFieldNames(root, true)
}

func validateLegacyFieldNames(data []byte) error {
	root, err := validateObjectFields(data, "legacy Execution Spec", false,
		"schema", "config_revision", "prompt_hash", "prompt_template_hash", "input",
		"worker", "image", "timeouts", "result_schema", "max_proposed_intents",
	)
	if err != nil {
		return err
	}
	if raw := root["input"]; len(raw) != 0 {
		input, err := validateObjectFields(raw, "legacy Execution Spec Input", false,
			"generation", "revision", "identity", "hash", "target_intent_id",
		)
		if err != nil {
			return err
		}
		if identity := input["identity"]; len(identity) != 0 {
			if _, err := validateObjectFields(identity, "legacy Execution Spec Input identity", false, "schema", "sha256"); err != nil {
				return err
			}
		}
	}
	return validateSharedFieldNames(root, false)
}

func validateSharedFieldNames(root map[string]json.RawMessage, rejectUnknown bool) error {
	if raw := root["worker"]; len(raw) != 0 {
		worker, err := validateObjectFields(raw, "Execution Spec Worker", rejectUnknown,
			"name", "adapter", "model", "capabilities", "secret_references",
		)
		if err != nil {
			return err
		}
		if references := worker["secret_references"]; len(references) != 0 {
			var values []json.RawMessage
			if err := json.Unmarshal(references, &values); err != nil {
				return fmt.Errorf("Execution Spec Secret references must be an array")
			}
			for _, reference := range values {
				if _, err := validateObjectFields(reference, "Execution Spec Secret reference", rejectUnknown,
					"environment_variable", "provider", "key",
				); err != nil {
					return err
				}
			}
		}
	}
	if raw := root["image"]; len(raw) != 0 {
		if _, err := validateObjectFields(raw, "Execution Spec Image", rejectUnknown,
			"requested_ref", "image_id", "repo_digest",
		); err != nil {
			return err
		}
	}
	if raw := root["timeouts"]; len(raw) != 0 {
		if _, err := validateObjectFields(raw, "Execution Spec timeouts", rejectUnknown,
			"run_seconds", "phase_seconds", "fallback_phase_seconds", "recovery_phase_seconds", "grace_seconds",
		); err != nil {
			return err
		}
	}
	return nil
}

func validateObjectFields(
	raw []byte,
	name string,
	rejectUnknown bool,
	canonicalFields ...string,
) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	for actual := range fields {
		canonical := ""
		for _, candidate := range canonicalFields {
			if strings.EqualFold(actual, candidate) {
				canonical = candidate
				break
			}
		}
		if canonical == "" {
			if rejectUnknown {
				return nil, fmt.Errorf("%s contains unknown field %q", name, actual)
			}
			continue
		}
		if actual != canonical {
			return nil, fmt.Errorf("%s field %q must use canonical spelling %q", name, actual, canonical)
		}
	}
	return fields, nil
}

func validateV2(data []byte) (Projection, error) {
	var document v2Document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Projection{}, fmt.Errorf("decode v2 Execution Spec: %w", err)
	}
	if document.Schema == nil || *document.Schema != SchemaV2 {
		return Projection{}, fmt.Errorf("v2 document schema is missing or inconsistent")
	}
	if document.Prompt == nil || !validCanonicalIdentity(*document.Prompt, PromptTemplateV1) {
		return Projection{}, fmt.Errorf("v2 Prompt evidence is invalid")
	}
	if document.Input == nil || document.Input.Generation == nil || *document.Input.Generation <= 0 ||
		document.Input.Revision == nil || *document.Input.Revision < 0 || document.Input.Identity == nil ||
		!validCanonicalIdentity(*document.Input.Identity, InputIdentityV1) {
		return Projection{}, fmt.Errorf("v2 Input evidence is invalid")
	}
	projection, err := validateShared(
		document.ConfigRevision, document.Worker, document.Image, document.Timeouts,
		document.ResultSchema, document.MaxProposedIntents,
	)
	if err != nil {
		return Projection{}, err
	}
	projection.InputGeneration = *document.Input.Generation
	projection.InputRevision = *document.Input.Revision
	projection.InputEvidence = *document.Input.Identity
	projection.PromptEvidence = *document.Prompt
	projection.DocumentTarget = document.Input.TargetIntentID
	return projection, nil
}

func validateLegacy(data []byte) (Projection, error) {
	var document legacyDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return Projection{}, fmt.Errorf("decode legacy Execution Spec: %w", err)
	}
	if len(document.Schema) != 0 {
		return Projection{}, fmt.Errorf("legacy Execution Spec contains an explicit schema")
	}
	if document.Input == nil || document.Input.Generation == nil || *document.Input.Generation <= 0 ||
		document.Input.Revision == nil || *document.Input.Revision < 0 {
		return Projection{}, fmt.Errorf("legacy Input coordinates are invalid")
	}
	var inputIdentity Identity
	switch {
	case document.Input.Identity != nil && document.Input.Hash != nil:
		return Projection{}, fmt.Errorf("legacy Input contains competing identities")
	case document.Input.Identity != nil:
		inputIdentity = *document.Input.Identity
		if !validLegacyIdentity(inputIdentity, InputIdentityV1, LegacyInputIdentityV1) {
			return Projection{}, fmt.Errorf("legacy structured Input evidence is invalid")
		}
	case document.Input.Hash != nil:
		inputIdentity = Identity{Schema: LegacyInputIdentityV1, SHA256: *document.Input.Hash}
		if !validLegacyIdentity(inputIdentity, LegacyInputIdentityV1) {
			return Projection{}, fmt.Errorf("legacy rendered Input evidence is invalid")
		}
	default:
		return Projection{}, fmt.Errorf("legacy Input evidence is missing")
	}
	var promptIdentity Identity
	switch {
	case document.PromptTemplateHash != nil:
		if document.PromptHash != nil && *document.PromptHash != *document.PromptTemplateHash {
			return Projection{}, fmt.Errorf("legacy Prompt evidence disagrees")
		}
		promptIdentity = Identity{Schema: PromptTemplateV1, SHA256: *document.PromptTemplateHash}
	case document.PromptHash != nil:
		promptIdentity = Identity{Schema: LegacyPromptV1, SHA256: *document.PromptHash}
	default:
		return Projection{}, fmt.Errorf("legacy Prompt evidence is missing")
	}
	if !validLegacyIdentity(promptIdentity, PromptTemplateV1, LegacyPromptV1) {
		return Projection{}, fmt.Errorf("legacy Prompt evidence is invalid")
	}
	projection, err := validateShared(
		document.ConfigRevision, document.Worker, document.Image, document.Timeouts,
		document.ResultSchema, document.MaxProposedIntents,
	)
	if err != nil {
		return Projection{}, err
	}
	projection.InputGeneration = *document.Input.Generation
	projection.InputRevision = *document.Input.Revision
	projection.InputEvidence = inputIdentity
	projection.PromptEvidence = promptIdentity
	projection.DocumentTarget = document.Input.TargetIntentID
	return projection, nil
}

func validateShared(
	configRevision *string,
	worker *workerDocument,
	image *imageDocument,
	timeouts *timeoutDocument,
	resultSchema *string,
	maxProposedIntents *int,
) (Projection, error) {
	if !requiredBounded(configRevision, 256) || !requiredBounded(resultSchema, 256) {
		return Projection{}, fmt.Errorf("Execution Spec revision or result schema is invalid")
	}
	if worker == nil || !requiredBounded(worker.Name, 256) || !requiredBounded(worker.Adapter, 128) {
		return Projection{}, fmt.Errorf("Execution Spec Worker is invalid")
	}
	if _, err := optionalString(worker.Model, 256); err != nil {
		return Projection{}, fmt.Errorf("Execution Spec Worker model: %w", err)
	}
	if err := validateCapabilities(worker.Capabilities); err != nil {
		return Projection{}, err
	}
	if err := validateSecretReferences(worker.SecretReferences); err != nil {
		return Projection{}, err
	}
	if image == nil || !requiredBounded(image.RequestedRef, 512) {
		return Projection{}, fmt.Errorf("Execution Spec Image is invalid")
	}
	if _, err := optionalString(image.ImageID, 512); err != nil {
		return Projection{}, fmt.Errorf("Execution Spec Image ID: %w", err)
	}
	if _, err := optionalString(image.RepoDigest, 512); err != nil {
		return Projection{}, fmt.Errorf("Execution Spec Image digest: %w", err)
	}
	fallback, recovery, err := validateTimeouts(timeouts)
	if err != nil {
		return Projection{}, err
	}
	maximum := 0
	if maxProposedIntents != nil {
		maximum = *maxProposedIntents
		if maximum < 0 || maximum > 64 {
			return Projection{}, fmt.Errorf("Execution Spec proposed Intent limit is invalid")
		}
	}
	return Projection{
		WorkerName: *worker.Name, ConfigRevision: *configRevision,
		ImageReference: *image.RequestedRef, ResultSchema: *resultSchema,
		MaxProposedIntents: maximum, FallbackPhaseSecond: fallback, RecoveryPhaseSecond: recovery,
	}, nil
}

func validateTimeouts(timeouts *timeoutDocument) (int, int, error) {
	if timeouts == nil || timeouts.RunSeconds == nil || timeouts.PhaseSeconds == nil ||
		timeouts.GraceSeconds == nil {
		return 0, 0, fmt.Errorf("Execution Spec timeouts are incomplete")
	}
	fallback := 0
	if timeouts.FallbackPhaseSeconds != nil {
		fallback = *timeouts.FallbackPhaseSeconds
	}
	recovery := 0
	if timeouts.RecoveryPhaseSeconds != nil {
		recovery = *timeouts.RecoveryPhaseSeconds
	}
	runSeconds, phaseSeconds, graceSeconds := *timeouts.RunSeconds, *timeouts.PhaseSeconds, *timeouts.GraceSeconds
	minimumRunSeconds := phaseSeconds + graceSeconds
	if fallback > 0 {
		minimumRunSeconds += fallback + graceSeconds
	}
	if recovery > 0 {
		minimumRunSeconds += recovery + graceSeconds
	}
	if runSeconds <= 0 || runSeconds > maximumRunSeconds || phaseSeconds <= 0 ||
		phaseSeconds > maximumPhaseSeconds || fallback < 0 || fallback > maximumPhaseSeconds ||
		recovery < 0 || recovery > maximumPhaseSeconds || graceSeconds <= 0 ||
		graceSeconds > phaseSeconds || runSeconds < minimumRunSeconds ||
		fallback > 0 && graceSeconds > fallback || recovery > 0 && graceSeconds > recovery {
		return 0, 0, fmt.Errorf("Execution Spec timeouts are invalid")
	}
	return fallback, recovery, nil
}

func validateTaskIdentity(task string, rowTarget *string, projection Projection, enforceCurrentConfiguration bool) error {
	if task == "" {
		return nil
	}
	if task != "reason" && task != "explore" && task != "bootstrap" {
		return fmt.Errorf("Execution Spec task is invalid")
	}
	if task == "reason" {
		if rowTarget != nil || projection.DocumentTarget != nil {
			return fmt.Errorf("reason Execution Spec cannot target an Intent")
		}
		if enforceCurrentConfiguration && (projection.MaxProposedIntents <= 0 || projection.FallbackPhaseSecond != 0) {
			return fmt.Errorf("reason Execution Spec task configuration is invalid")
		}
		return nil
	}
	if rowTarget == nil || *rowTarget == "" || projection.DocumentTarget == nil || *projection.DocumentTarget != *rowTarget {
		return fmt.Errorf("Execution Spec target does not match its Run")
	}
	if enforceCurrentConfiguration && (projection.MaxProposedIntents != 0 || projection.FallbackPhaseSecond <= 0) {
		return fmt.Errorf("Execution Spec task configuration is invalid")
	}
	return nil
}

func validateCapabilities(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("Execution Spec Worker capabilities must be an array")
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) > 64 {
		return fmt.Errorf("Execution Spec Worker capabilities are invalid")
	}
	sorted := append([]string(nil), values...)
	for _, value := range sorted {
		if value == "" || len(value) > 128 {
			return fmt.Errorf("Execution Spec Worker capability is invalid")
		}
	}
	sort.Strings(sorted)
	for index := 1; index < len(sorted); index++ {
		if sorted[index-1] == sorted[index] {
			return fmt.Errorf("Execution Spec Worker capabilities are not unique")
		}
	}
	return nil
}

func validateSecretReferences(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("Execution Spec Secret references must be an array")
	}
	var references []secretReference
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&references); err != nil || len(references) > 64 {
		return fmt.Errorf("Execution Spec Secret references are invalid")
	}
	environments := make([]string, 0, len(references))
	for _, reference := range references {
		if !requiredBounded(reference.EnvironmentVariable, 128) || !requiredBounded(reference.Provider, 64) ||
			!requiredBounded(reference.Key, 512) {
			return fmt.Errorf("Execution Spec Secret reference is invalid")
		}
		environments = append(environments, *reference.EnvironmentVariable)
	}
	sort.Strings(environments)
	for index := 1; index < len(environments); index++ {
		if environments[index-1] == environments[index] {
			return fmt.Errorf("Execution Spec Secret environment variables are not unique")
		}
	}
	return nil
}

func requiredBounded(value *string, maximum int) bool {
	return value != nil && *value != "" && len(*value) <= maximum
}

func optionalString(raw json.RawMessage, maximum int) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("must be a string when present")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || len(value) > maximum {
		return "", fmt.Errorf("must be a bounded string")
	}
	return value, nil
}

func validCanonicalIdentity(identity Identity, schemas ...string) bool {
	allowed := false
	for _, schema := range schemas {
		allowed = allowed || identity.Schema == schema
	}
	if !allowed || len(identity.SHA256) != 64 || strings.ToLower(identity.SHA256) != identity.SHA256 {
		return false
	}
	for _, character := range identity.SHA256 {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validLegacyIdentity(identity Identity, schemas ...string) bool {
	allowed := false
	for _, schema := range schemas {
		allowed = allowed || identity.Schema == schema
	}
	if !allowed || len(identity.SHA256) != 64 {
		return false
	}
	if identity.Schema == InputIdentityV1 || identity.Schema == PromptTemplateV1 {
		return validCanonicalIdentity(identity, identity.Schema)
	}
	for _, character := range identity.SHA256 {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}
