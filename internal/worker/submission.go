package worker

import (
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// submissionProtocol owns both invocation-scoped submission preparation and
// the static materials used for Catalog and Prompt Template identity.
type submissionProtocol interface {
	prepareSubmission(AgentRequest, ResultContract) (submissionMaterial, error)
	applySubmission(DriverResult, submissionMaterial) (DriverResult, error)
	submissionIdentity(...rundomain.TaskType) submissionIdentity
}

// submissionMaterial is prepared once, before rendering the prompt. Applying
// it to a command must use the same schema and file, without preparing again.
type submissionMaterial struct {
	instructions string
	schema       string
	protocolFile *rundomain.ProtocolFile
}

// submissionIdentity excludes invocation paths, sessions and dynamic limits.
type submissionIdentity struct {
	Contract  string           `json:"contract"`
	Templates map[Phase]string `json:"templates,omitempty"`
}

const mockOutcomeSubmissionContract = `Mock Adapter submission protocol: aether.outcome/v1. Return exactly one JSON object with an "outcome" object matching the invocation JSON Schema below. The outcome kind determines its fields. Do not include Aether RunResult protocol/schema fields, Run or Invocation identity, a result hash, Markdown fences, comments, or explanatory text. Use only the fields allowed by the selected kind and preserve field presence; do not hide invalid fields by omitting them.`

const claudeStructuredOutputContract = `Claude Code submission protocol: return exactly one structured output object with an "outcome" property matching the native JSON Schema supplied by Aether for this invocation. Do not put the result in response text, Markdown fences, markers, logs, or explanatory prose. Do not include Aether RunResult protocol/schema fields, Run or Invocation identity, or a result hash.`

const codexStructuredOutputContract = `Codex submission protocol: aether.outcome/v1. Submit one final JSON object with an "outcome" property matching the native JSON Schema supplied by Aether through --output-schema for this invocation. The outcome kind determines its required and allowed fields. Do not include Aether RunResult protocol/schema fields, Run or Invocation identity, or a result hash.`

func (d *MockDriver) prepareSubmission(_ AgentRequest, contract ResultContract) (submissionMaterial, error) {
	schema, err := structuredOutputSchema(contract)
	if err != nil {
		return submissionMaterial{}, &CaptureError{Code: CaptureBoundaryError, Detail: "Mock outcome schema could not be encoded"}
	}
	return submissionMaterial{
		instructions: mockOutcomeSubmissionContract + "\n\nJSON Schema for this invocation:\n" + string(schema),
	}, nil
}

func (d *MockDriver) applySubmission(result DriverResult, _ submissionMaterial) (DriverResult, error) {
	return result, nil
}

func (d *MockDriver) submissionIdentity(...rundomain.TaskType) submissionIdentity {
	return submissionIdentity{Contract: mockOutcomeSubmissionContract}
}

func (d *ClaudeCodeDriver) prepareSubmission(_ AgentRequest, contract ResultContract) (submissionMaterial, error) {
	schema, err := structuredOutputSchema(contract)
	if err != nil {
		return submissionMaterial{}, &CaptureError{Code: CaptureBoundaryError, Detail: "native structured output schema could not be encoded"}
	}
	return submissionMaterial{instructions: claudeStructuredOutputContract, schema: string(schema)}, nil
}

func (d *ClaudeCodeDriver) applySubmission(result DriverResult, material submissionMaterial) (DriverResult, error) {
	result.Argv = d.addStructuredOutput(result.Argv, material.schema)
	if len(result.Argv) == 0 {
		return DriverResult{}, &CaptureError{Code: CaptureBoundaryError, Detail: "native structured output command is invalid"}
	}
	return result, nil
}

func (d *ClaudeCodeDriver) submissionIdentity(...rundomain.TaskType) submissionIdentity {
	return submissionIdentity{Contract: claudeStructuredOutputContract}
}

func (d *CodexDriver) prepareSubmission(request AgentRequest, contract ResultContract) (submissionMaterial, error) {
	schema, err := structuredOutputSchema(contract)
	if err != nil {
		return submissionMaterial{}, &CaptureError{Code: CaptureBoundaryError, Detail: "native structured output schema could not be encoded"}
	}
	file := &rundomain.ProtocolFile{InvocationID: request.InvocationID, Filename: "output-schema.json", Bytes: schema}
	if err := file.Validate(); err != nil {
		return submissionMaterial{}, &CaptureError{Code: CaptureBoundaryError, Detail: "native structured output Invocation identity is invalid"}
	}
	return submissionMaterial{
		instructions: codexStructuredOutputContract + "\nSchema file for this invocation: " + file.Path() + ".",
		protocolFile: file,
	}, nil
}

func (d *CodexDriver) applySubmission(result DriverResult, material submissionMaterial) (DriverResult, error) {
	result.Argv = d.addSchemaFile(result.Argv, material.protocolFile.Path())
	if len(result.Argv) == 0 {
		return DriverResult{}, &CaptureError{Code: CaptureBoundaryError, Detail: "native structured output command is invalid"}
	}
	result.ProtocolFile = material.protocolFile
	return result, nil
}

func (d *CodexDriver) submissionIdentity(...rundomain.TaskType) submissionIdentity {
	return submissionIdentity{Contract: codexStructuredOutputContract}
}

func (d *PiDriver) prepareSubmission(request AgentRequest, contract ResultContract) (submissionMaterial, error) {
	file := piResultProtocolFile(request.InvocationID, contract)
	if err := file.Validate(); err != nil {
		return submissionMaterial{}, &CaptureError{Code: CaptureBoundaryError, Detail: "Pi result file Invocation identity is invalid"}
	}
	return submissionMaterial{instructions: piTomlSubmissionContract(*file, contract), protocolFile: file}, nil
}

func (d *PiDriver) applySubmission(result DriverResult, material submissionMaterial) (DriverResult, error) {
	result.ProtocolFile = material.protocolFile
	return result, nil
}

func (d *PiDriver) submissionIdentity(tasks ...rundomain.TaskType) submissionIdentity {
	identity := submissionIdentity{Contract: piResultOutputContract}
	for _, task := range tasks {
		for _, rule := range resultPhaseRules {
			if rule.task != task {
				continue
			}
			if identity.Templates == nil {
				identity.Templates = make(map[Phase]string)
			}
			identity.Templates[rule.phase] = string(piResultTemplate(task, rule.outcomes))
		}
	}
	return identity
}
