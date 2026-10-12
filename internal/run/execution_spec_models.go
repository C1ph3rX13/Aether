package run

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/C1ph3rX13/Aether/internal/spec"
)

const (
	ExecutionSpecSchemaLegacyV1          = spec.SchemaLegacyV1
	ExecutionSpecSchemaV2                = spec.SchemaV2
	PromptTemplateIdentitySchemaV1       = spec.PromptTemplateV1
	RenderedPromptIdentitySchemaLegacyV1 = spec.LegacyPromptV1
	RenderedInputIdentitySchemaLegacyV1  = spec.LegacyInputIdentityV1
)

// ExecutionSpecSnapshot is the immutable, non-sensitive evidence used to launch a Run.
// Resolved secret values and arbitrary environment maps intentionally have no field here.
type ExecutionSpecSnapshot struct {
	Schema               string          `json:"-"`
	PromptEvidenceSchema string          `json:"-"`
	ConfigRevision       string          `json:"config_revision"`
	PromptTemplateHash   string          `json:"prompt_template_hash"`
	Input                InputSnapshot   `json:"input"`
	Worker               WorkerSnapshot  `json:"worker"`
	Image                ImageSnapshot   `json:"image"`
	Timeouts             TimeoutSnapshot `json:"timeouts"`
	ResultSchema         string          `json:"result_schema"`
	MaxProposedIntents   int             `json:"max_proposed_intents,omitempty"`
}

type PromptIdentity struct {
	Schema string `json:"schema"`
	SHA256 string `json:"sha256"`
}

func (identity PromptIdentity) Validate() error {
	if identity.Schema != PromptTemplateIdentitySchemaV1 && identity.Schema != RenderedPromptIdentitySchemaLegacyV1 {
		return newError(ErrorInvalidArgument, "Prompt identity schema is invalid")
	}
	if !lowercaseSHA256Hex(identity.SHA256) {
		return newError(ErrorInvalidArgument, "Prompt identity SHA-256 digest is invalid")
	}
	return nil
}

type executionSpecV2Document struct {
	Schema             string          `json:"schema"`
	ConfigRevision     string          `json:"config_revision"`
	Prompt             PromptIdentity  `json:"prompt"`
	Input              InputSnapshot   `json:"input"`
	Worker             WorkerSnapshot  `json:"worker"`
	Image              ImageSnapshot   `json:"image"`
	Timeouts           TimeoutSnapshot `json:"timeouts"`
	ResultSchema       string          `json:"result_schema"`
	MaxProposedIntents int             `json:"max_proposed_intents,omitempty"`
}

func (s ExecutionSpecSnapshot) MarshalJSON() ([]byte, error) {
	normalized, err := s.normalized()
	if err != nil {
		return nil, err
	}
	return json.Marshal(executionSpecV2Document{
		Schema: normalized.Schema, ConfigRevision: normalized.ConfigRevision,
		Prompt: PromptIdentity{Schema: PromptTemplateIdentitySchemaV1, SHA256: normalized.PromptTemplateHash},
		Input:  normalized.Input, Worker: normalized.Worker, Image: normalized.Image,
		Timeouts: normalized.Timeouts, ResultSchema: normalized.ResultSchema,
		MaxProposedIntents: normalized.MaxProposedIntents,
	})
}

func (s *ExecutionSpecSnapshot) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("Execution Spec destination is nil")
	}
	if err := spec.ValidateNoDuplicateFields(data); err != nil {
		return fmt.Errorf("validate Execution Spec JSON: %w", err)
	}
	var envelope struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Schema == "" {
		if _, err := spec.ValidateAndProject(
			ExecutionSpecSchemaLegacyV1, data, "", nil,
		); err != nil {
			return fmt.Errorf("validate legacy Execution Spec: %w", err)
		}
		legacy, err := decodeLegacyExecutionSpec(data)
		if err != nil {
			return err
		}
		*s = legacy
		return nil
	}
	if envelope.Schema != ExecutionSpecSchemaV2 {
		return fmt.Errorf("unsupported Execution Spec schema %q", envelope.Schema)
	}
	if _, err := spec.ValidateAndProject(ExecutionSpecSchemaV2, data, "", nil); err != nil {
		return fmt.Errorf("validate v2 Execution Spec: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document executionSpecV2Document
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if document.Prompt.Schema != PromptTemplateIdentitySchemaV1 || document.Prompt.Validate() != nil {
		return fmt.Errorf("v2 Execution Spec Prompt identity is invalid")
	}
	decoded := ExecutionSpecSnapshot{
		Schema: document.Schema, PromptEvidenceSchema: document.Prompt.Schema,
		ConfigRevision:     document.ConfigRevision,
		PromptTemplateHash: document.Prompt.SHA256, Input: document.Input,
		Worker: document.Worker, Image: document.Image, Timeouts: document.Timeouts,
		ResultSchema: document.ResultSchema, MaxProposedIntents: document.MaxProposedIntents,
	}
	normalized, err := decoded.normalized()
	if err != nil {
		return err
	}
	*s = normalized
	return nil
}

func decodeLegacyExecutionSpec(data []byte) (ExecutionSpecSnapshot, error) {
	var document struct {
		ConfigRevision     string          `json:"config_revision"`
		PromptHash         string          `json:"prompt_hash"`
		PromptTemplateHash string          `json:"prompt_template_hash"`
		Input              InputSnapshot   `json:"input"`
		Worker             WorkerSnapshot  `json:"worker"`
		Image              ImageSnapshot   `json:"image"`
		Timeouts           TimeoutSnapshot `json:"timeouts"`
		ResultSchema       string          `json:"result_schema"`
		MaxProposedIntents int             `json:"max_proposed_intents,omitempty"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return ExecutionSpecSnapshot{}, err
	}
	if document.PromptHash != "" && document.PromptTemplateHash != "" &&
		document.PromptHash != document.PromptTemplateHash {
		return ExecutionSpecSnapshot{}, fmt.Errorf("legacy Execution Spec Prompt hashes disagree")
	}
	promptHash := document.PromptTemplateHash
	promptSchema := PromptTemplateIdentitySchemaV1
	if promptHash == "" {
		promptHash = document.PromptHash
		promptSchema = RenderedPromptIdentitySchemaLegacyV1
	}
	var legacyInput struct {
		Hash string `json:"hash"`
	}
	var inputContainer struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(data, &inputContainer); err != nil {
		return ExecutionSpecSnapshot{}, err
	}
	if err := json.Unmarshal(inputContainer.Input, &legacyInput); err != nil {
		return ExecutionSpecSnapshot{}, err
	}
	if legacyInput.Hash != "" {
		if document.Input.Identity.SHA256 != "" && document.Input.Identity.SHA256 != legacyInput.Hash {
			return ExecutionSpecSnapshot{}, fmt.Errorf("legacy Execution Spec Input hashes disagree")
		}
		if document.Input.Identity.SHA256 == "" {
			document.Input.Identity = InputIdentity{
				Schema: RenderedInputIdentitySchemaLegacyV1, SHA256: legacyInput.Hash,
			}
		}
	}
	if !sha256Hex(promptHash) || !sha256Hex(document.Input.Identity.SHA256) {
		return ExecutionSpecSnapshot{}, fmt.Errorf("legacy Execution Spec evidence is invalid")
	}
	return ExecutionSpecSnapshot{
		Schema: ExecutionSpecSchemaLegacyV1, PromptEvidenceSchema: promptSchema,
		ConfigRevision:     document.ConfigRevision,
		PromptTemplateHash: promptHash, Input: document.Input, Worker: document.Worker,
		Image: document.Image, Timeouts: document.Timeouts, ResultSchema: document.ResultSchema,
		MaxProposedIntents: document.MaxProposedIntents,
	}, nil
}

// AdmissionSpecSnapshot is Dispatcher-owned immutable execution evidence.
// Project input identity is deliberately absent and is added by Run Admission.
type AdmissionSpecSnapshot struct {
	ConfigRevision     string          `json:"config_revision"`
	PromptTemplateHash string          `json:"prompt_template_hash"`
	Worker             WorkerSnapshot  `json:"worker"`
	Image              ImageSnapshot   `json:"image"`
	Timeouts           TimeoutSnapshot `json:"timeouts"`
	ResultSchema       string          `json:"result_schema"`
	MaxProposedIntents int             `json:"max_proposed_intents,omitempty"`
}

type InputSnapshot struct {
	Generation     int           `json:"generation"`
	Revision       int64         `json:"revision"`
	Identity       InputIdentity `json:"identity"`
	TargetIntentID *string       `json:"target_intent_id,omitempty"`
}

type WorkerSnapshot struct {
	Name             string            `json:"name"`
	Adapter          string            `json:"adapter"`
	Model            string            `json:"model,omitempty"`
	Capabilities     []string          `json:"capabilities,omitempty"`
	SecretReferences []SecretReference `json:"secret_references,omitempty"`
}

type SecretReference struct {
	EnvironmentVariable string `json:"environment_variable"`
	Provider            string `json:"provider"`
	Key                 string `json:"key"`
}

type ImageSnapshot struct {
	RequestedRef string `json:"requested_ref"`
	ImageID      string `json:"image_id,omitempty"`
	RepoDigest   string `json:"repo_digest,omitempty"`
}

type TimeoutSnapshot struct {
	RunSeconds           int `json:"run_seconds"`
	PhaseSeconds         int `json:"phase_seconds"`
	FallbackPhaseSeconds int `json:"fallback_phase_seconds,omitempty"`
	RecoveryPhaseSeconds int `json:"recovery_phase_seconds,omitempty"`
	GraceSeconds         int `json:"grace_seconds"`
}

const (
	maximumPhaseTimeoutSeconds = 24 * 60 * 60
	maximumRunTimeoutSeconds   = 4*maximumPhaseTimeoutSeconds + 4*int(MaximumStopGrace/time.Second)
)

func (s ExecutionSpecSnapshot) normalized() (ExecutionSpecSnapshot, error) {
	if s.Schema != ExecutionSpecSchemaV2 {
		return ExecutionSpecSnapshot{}, newError(ErrorInvalidArgument, "only v2 Execution Specs may execute")
	}
	if s.PromptEvidenceSchema == "" {
		s.PromptEvidenceSchema = PromptTemplateIdentitySchemaV1
	}
	if s.PromptEvidenceSchema != PromptTemplateIdentitySchemaV1 {
		return ExecutionSpecSnapshot{}, newError(ErrorInvalidArgument, "v2 Prompt evidence schema is invalid")
	}
	if s.Input.Identity.ValidateV1() != nil {
		return ExecutionSpecSnapshot{}, newError(ErrorInvalidArgument, "config revision and SHA-256 input/prompt hashes are required")
	}
	if s.Input.Generation <= 0 || s.Input.Revision < 0 {
		return ExecutionSpecSnapshot{}, newError(ErrorInvalidArgument, "input generation or revision is invalid")
	}
	configuration, err := (AdmissionSpecSnapshot{
		ConfigRevision: s.ConfigRevision, PromptTemplateHash: s.PromptTemplateHash,
		Worker: s.Worker, Image: s.Image, Timeouts: s.Timeouts,
		ResultSchema: s.ResultSchema, MaxProposedIntents: s.MaxProposedIntents,
	}).normalized()
	if err != nil {
		return ExecutionSpecSnapshot{}, err
	}
	s.ConfigRevision = configuration.ConfigRevision
	s.PromptTemplateHash = configuration.PromptTemplateHash
	s.Worker = configuration.Worker
	s.Image = configuration.Image
	s.Timeouts = configuration.Timeouts
	s.ResultSchema = configuration.ResultSchema
	s.MaxProposedIntents = configuration.MaxProposedIntents
	return s, nil
}

func (s ExecutionSpecSnapshot) PromptEvidence() PromptIdentity {
	schema := s.PromptEvidenceSchema
	if schema == "" && s.Schema != ExecutionSpecSchemaLegacyV1 {
		schema = PromptTemplateIdentitySchemaV1
	}
	return PromptIdentity{Schema: schema, SHA256: s.PromptTemplateHash}
}

func (s AdmissionSpecSnapshot) normalized() (AdmissionSpecSnapshot, error) {
	if !bounded(s.ConfigRevision, 256) || !lowercaseSHA256Hex(s.PromptTemplateHash) {
		return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "config revision and SHA-256 prompt template hash are required")
	}
	if !bounded(s.Worker.Name, 256) || !bounded(s.Worker.Adapter, 128) || len(s.Worker.Model) > 256 {
		return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "worker snapshot is invalid")
	}
	if !bounded(s.Image.RequestedRef, 512) || len(s.Image.ImageID) > 512 || len(s.Image.RepoDigest) > 512 {
		return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "image snapshot is invalid")
	}
	if err := s.Timeouts.validate(); err != nil {
		return AdmissionSpecSnapshot{}, err
	}
	if !bounded(s.ResultSchema, 256) {
		return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "result schema is required")
	}
	if len(s.Worker.Capabilities) > 64 || len(s.Worker.SecretReferences) > 64 {
		return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "worker snapshot exceeds its item limit")
	}

	capabilities := append([]string(nil), s.Worker.Capabilities...)
	for _, capability := range capabilities {
		if !bounded(capability, 128) {
			return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "worker capability is invalid")
		}
	}
	sort.Strings(capabilities)
	if duplicateStrings(capabilities) {
		return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "worker capabilities must be unique")
	}
	s.Worker.Capabilities = capabilities

	references := append([]SecretReference(nil), s.Worker.SecretReferences...)
	for _, reference := range references {
		if !bounded(reference.EnvironmentVariable, 128) || !bounded(reference.Provider, 64) || !bounded(reference.Key, 512) {
			return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "secret reference is invalid")
		}
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].EnvironmentVariable != references[j].EnvironmentVariable {
			return references[i].EnvironmentVariable < references[j].EnvironmentVariable
		}
		if references[i].Provider != references[j].Provider {
			return references[i].Provider < references[j].Provider
		}
		return references[i].Key < references[j].Key
	})
	for index := 1; index < len(references); index++ {
		if references[index-1] == references[index] || references[index-1].EnvironmentVariable == references[index].EnvironmentVariable {
			return AdmissionSpecSnapshot{}, newError(ErrorInvalidArgument, "secret environment variables must be unique")
		}
	}
	s.Worker.SecretReferences = references
	return s, nil
}

func bounded(value string, maximum int) bool {
	return value != "" && len(value) <= maximum
}

func sha256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func duplicateStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return true
		}
	}
	return false
}

func (timeouts TimeoutSnapshot) validate() error {
	minimumRunSeconds := timeouts.PhaseSeconds + timeouts.GraceSeconds
	if timeouts.FallbackPhaseSeconds > 0 {
		minimumRunSeconds += timeouts.FallbackPhaseSeconds + timeouts.GraceSeconds
		minimumRunSeconds += spec.ResultFinalizationPhaseSeconds(timeouts.FallbackPhaseSeconds) + timeouts.GraceSeconds
	} else {
		minimumRunSeconds += spec.ResultFinalizationPhaseSeconds(timeouts.PhaseSeconds) + timeouts.GraceSeconds
	}
	if timeouts.RecoveryPhaseSeconds > 0 {
		minimumRunSeconds += timeouts.RecoveryPhaseSeconds + timeouts.GraceSeconds
	}
	if timeouts.RunSeconds <= 0 || timeouts.RunSeconds > maximumRunTimeoutSeconds ||
		timeouts.PhaseSeconds <= 0 || timeouts.PhaseSeconds > maximumPhaseTimeoutSeconds ||
		timeouts.FallbackPhaseSeconds < 0 || timeouts.FallbackPhaseSeconds > maximumPhaseTimeoutSeconds ||
		timeouts.RecoveryPhaseSeconds < 0 || timeouts.RecoveryPhaseSeconds > maximumRecoveryPhaseSeconds ||
		timeouts.GraceSeconds <= 0 || timeouts.RunSeconds < minimumRunSeconds ||
		timeouts.GraceSeconds > timeouts.PhaseSeconds ||
		timeouts.FallbackPhaseSeconds > 0 && timeouts.GraceSeconds > timeouts.FallbackPhaseSeconds ||
		timeouts.RecoveryPhaseSeconds > 0 && timeouts.GraceSeconds > timeouts.RecoveryPhaseSeconds {
		return newError(ErrorInvalidArgument, "timeout snapshot is invalid")
	}
	return nil
}
