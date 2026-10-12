package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

const (
	maximumPromptSize = 256 * 1024
	maximumInlineSize = 64 * 1024
)

type Phase string

const (
	PhaseReason                    Phase = "reason"
	PhaseReasonFinalization        Phase = "reason_finalization"
	PhaseExplore                   Phase = "explore_execute"
	PhaseBootstrap                 Phase = "bootstrap"
	PhaseExploreConclude           Phase = "explore_conclude"
	PhaseBootstrapConclude         Phase = "bootstrap_conclude"
	PhaseExploreRecovery           Phase = "explore_recovery"
	PhaseExploreConcludeRecovery   Phase = "explore_conclude_recovery"
	PhaseBootstrapRecovery         Phase = "bootstrap_recovery"
	PhaseBootstrapConcludeRecovery Phase = "bootstrap_conclude_recovery"
)

type InputKind string

const (
	InputBlackboardFile  InputKind = "blackboard_file"
	InputBootstrapInline InputKind = "bootstrap_inline"
)

type InputReference struct {
	Kind   InputKind
	Path   string
	Inline string
}

type AgentRequest struct {
	InvocationID       string
	Task               rundomain.TaskType
	Phase              Phase
	Prompt             string
	Input              InputReference
	MaxProposedIntents int
}

type ConcludeRequest struct {
	AgentRequest
	Session *Session
}

type ProcessRecoveryRequest struct {
	AgentRequest
	Session *Session
}

// ProcessRecoveryAdapter is an optional adapter capability. It is deliberately
// separate from AgentAdapter and ConcludeAdapter so stateless or unsupported
// CLIs do not receive a fake recovery method.
type ProcessRecoveryAdapter interface {
	PrepareRecovery(ProcessRecoveryRequest) (Invocation, error)
}

// Session is intentionally opaque to Run and Runtime; only the Adapter can interpret it.
type Session struct {
	value     string
	header    piSessionHeader
	hasHeader bool
	cursor    string
	hasCursor bool
}

type Invocation struct {
	argv           []string
	env            map[string]string
	phase          Phase
	input          InputReference
	prompt         string
	resultContract ResultContract
	protocolFile   *rundomain.ProtocolFile
	sessionDir     string
	session        *Session
	capture        streamingCapture
}

func (invocation Invocation) Argv() []string { return append([]string(nil), invocation.argv...) }

func (invocation Invocation) Env() map[string]string {
	result := make(map[string]string, len(invocation.env))
	for key, value := range invocation.env {
		result[key] = value
	}
	return result
}

func (invocation Invocation) Phase() Phase { return invocation.phase }

func (invocation Invocation) Input() InputReference { return invocation.input }

func (invocation Invocation) ResultContract() ResultContract { return invocation.resultContract }

func (invocation Invocation) ProtocolFile() *rundomain.ProtocolFile {
	if invocation.protocolFile == nil {
		return nil
	}
	file := *invocation.protocolFile
	file.Bytes = append([]byte(nil), file.Bytes...)
	return &file
}

func (invocation Invocation) Session() *Session {
	if invocation.session == nil {
		return nil
	}
	copy := *invocation.session
	return &copy
}

type ProcessOutput = rundomain.ProcessOutput

type Diagnostics struct {
	StdoutSummary string
	StderrSummary string
}

type CaptureResult struct {
	Candidate            json.RawMessage
	ResultHash           string
	Session              *Session
	Diagnostics          Diagnostics
	CaptureFailureReason rundomain.CaptureFailureReason
	CaptureDiagnostic    rundomain.CaptureDiagnostic
	ResultEvidenceSource rundomain.ResultEvidenceSource
}

type CaptureErrorCode string

const (
	CaptureNoCandidate        CaptureErrorCode = "no_candidate"
	CaptureMultipleCandidates CaptureErrorCode = "multiple_candidates"
	CaptureBoundaryError      CaptureErrorCode = "candidate_boundary_error"
	CaptureInvalidCandidate   CaptureErrorCode = "invalid_candidate"
	CaptureCandidateTooLarge  CaptureErrorCode = "candidate_too_large"
	CaptureProcessFailed      CaptureErrorCode = "process_failed"
	CaptureOutputTooLarge     CaptureErrorCode = "output_too_large"
	CaptureCancelled          CaptureErrorCode = "cancelled"
	CaptureTimedOut           CaptureErrorCode = "timed_out"
)

type CaptureError struct {
	Code   CaptureErrorCode
	Detail string
}

func (e *CaptureError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

type AgentAdapter interface {
	TypeName() string
	Prepare(request AgentRequest) (Invocation, error)
	ObserveSession(context.Context, Invocation, ProcessOutput) *Session
	CanContinueSession(*Session) bool
	Capture(context.Context, Invocation, ProcessOutput) (CaptureResult, error)
}

type ConcludeAdapter interface {
	PrepareConclude(request ConcludeRequest) (Invocation, error)
}

type configuredAdapter struct {
	definition AdapterDefinition
	worker     WorkerConfig
}

type configuredConcludeAdapter struct{ *configuredAdapter }
type configuredRecoveryAdapter struct{ *configuredAdapter }

func NewAgentAdapter(config WorkerConfig) (AgentAdapter, error) {
	definition, err := LookupAdapterDefinition(string(config.Type))
	if err != nil {
		return nil, err
	}
	base := &configuredAdapter{
		definition: definition, worker: cloneWorkerConfig(config),
	}
	if definition.driver.hasConclude() {
		return &configuredConcludeAdapter{configuredAdapter: base}, nil
	}
	if definition.driver.hasRecovery() {
		return &configuredRecoveryAdapter{configuredAdapter: base}, nil
	}
	return base, nil
}

func (a *configuredAdapter) TypeName() string { return string(a.definition.Name()) }

func (a *configuredAdapter) Prepare(request AgentRequest) (Invocation, error) {
	if err := validateAgentRequest(request, preparationPrimary); err != nil {
		return Invocation{}, err
	}
	contract, err := resultContractForRequest(request)
	if err != nil {
		return Invocation{}, &CaptureError{Code: CaptureBoundaryError, Detail: err.Error()}
	}
	material, err := a.definition.submission.prepareSubmission(request, contract)
	if err != nil {
		return Invocation{}, err
	}
	prompt, err := promptForRequestWithResultOutputContract(request, material.instructions)
	if err != nil {
		return Invocation{}, err
	}
	worker := a.workerForRequest(request)
	var session *string
	if a.definition.driver.prepareSession != nil {
		session = a.definition.driver.prepareSession()
	}
	result := a.definition.driver.buildExecute(worker, prompt, session)
	result, err = a.definition.submission.applySubmission(result, material)
	if err != nil {
		return Invocation{}, err
	}
	return newInvocation(result, worker.Env, request, prompt, contract, a.sessionDirectory(result.Argv)), nil
}

func (a *configuredRecoveryAdapter) PrepareRecovery(request ProcessRecoveryRequest) (Invocation, error) {
	return a.prepareRecovery(request)
}

func (a *configuredConcludeAdapter) PrepareRecovery(request ProcessRecoveryRequest) (Invocation, error) {
	return a.prepareRecovery(request)
}

func (a *configuredAdapter) prepareRecovery(request ProcessRecoveryRequest) (Invocation, error) {
	if err := a.validateContinuationSession(request.Session, "process recovery"); err != nil {
		return Invocation{}, err
	}
	if err := validateAgentRequest(request.AgentRequest, preparationRecovery); err != nil {
		return Invocation{}, err
	}
	contract, err := resultContractForRequest(request.AgentRequest)
	if err != nil {
		return Invocation{}, &CaptureError{Code: CaptureBoundaryError, Detail: err.Error()}
	}
	material, err := a.definition.submission.prepareSubmission(request.AgentRequest, contract)
	if err != nil {
		return Invocation{}, err
	}
	prompt, err := promptForRequestWithResultOutputContract(request.AgentRequest, material.instructions)
	if err != nil {
		return Invocation{}, err
	}
	worker := a.workerForRequest(request.AgentRequest)
	if a.definition.driver.buildRecovery == nil {
		return Invocation{}, &CaptureError{Code: CaptureBoundaryError, Detail: "process recovery is unsupported by this Adapter"}
	}
	argv := a.definition.driver.buildRecovery(worker, prompt, request.Session.value, request.Phase)
	if len(argv) == 0 {
		return Invocation{}, &CaptureError{Code: CaptureBoundaryError, Detail: "process recovery is unsupported by this Adapter"}
	}
	result, err := a.definition.submission.applySubmission(DriverResult{Argv: argv, Session: &request.Session.value}, material)
	if err != nil {
		return Invocation{}, err
	}
	return Invocation{argv: result.Argv, env: cloneEnv(worker.Env), phase: request.Phase,
		input: request.Input, prompt: prompt, resultContract: contract,
		protocolFile: result.ProtocolFile,
		sessionDir:   a.sessionDirectory(result.Argv), session: request.Session}, nil
}

func (a *configuredConcludeAdapter) PrepareConclude(request ConcludeRequest) (Invocation, error) {
	if err := validateAgentRequest(request.AgentRequest, preparationConclude); err != nil {
		return Invocation{}, err
	}
	if err := a.validateContinuationSession(request.Session, "conclude"); err != nil {
		return Invocation{}, err
	}
	contract, err := resultContractForRequest(request.AgentRequest)
	if err != nil {
		return Invocation{}, &CaptureError{Code: CaptureBoundaryError, Detail: err.Error()}
	}
	material, err := a.definition.submission.prepareSubmission(request.AgentRequest, contract)
	if err != nil {
		return Invocation{}, err
	}
	prompt, err := promptForRequestWithResultOutputContract(request.AgentRequest, material.instructions)
	if err != nil {
		return Invocation{}, err
	}
	worker := a.workerForRequest(request.AgentRequest)
	argv := a.definition.driver.buildConclude(worker, prompt, request.Session.value)
	result, err := a.definition.submission.applySubmission(DriverResult{Argv: argv, Session: &request.Session.value}, material)
	if err != nil {
		return Invocation{}, err
	}
	return Invocation{
		argv: result.Argv, env: cloneEnv(worker.Env), phase: request.Phase,
		input: request.Input, prompt: prompt, resultContract: contract,
		protocolFile: result.ProtocolFile,
		sessionDir:   a.sessionDirectory(result.Argv), session: request.Session,
	}, nil
}

func (a *configuredAdapter) validateContinuationSession(session *Session, operation string) error {
	if session == nil || !a.CanContinueSession(session) {
		return &CaptureError{Code: CaptureBoundaryError, Detail: operation + " requires a verified adapter Session"}
	}
	return nil
}

func (a *configuredAdapter) CanContinueSession(session *Session) bool {
	if session == nil {
		return false
	}
	return a.definition.driver.resultEvidence.CanContinueSession(session)
}

func (a *configuredAdapter) NewOutputCapture(invocation Invocation) streamingCapture {
	return a.definition.driver.resultEvidence.NewOutputCapture(invocation.session)
}

func (a *configuredAdapter) workerForRequest(request AgentRequest) WorkerConfig {
	worker := cloneWorkerConfig(a.worker)
	if a.definition.decorateInvocation != nil {
		a.definition.decorateInvocation(worker.Env, request.Phase)
	}
	return worker
}

func (a *configuredAdapter) ObserveSession(ctx context.Context, invocation Invocation, output ProcessOutput) *Session {
	return a.definition.driver.resultEvidence.ObserveSession(ctx, invocation, output)
}

func (a *configuredAdapter) Capture(ctx context.Context, invocation Invocation, output ProcessOutput) (CaptureResult, error) {
	return a.definition.driver.resultEvidence.Capture(ctx, invocation, output)
}

func (a *configuredConcludeAdapter) Capture(ctx context.Context, invocation Invocation, output ProcessOutput) (CaptureResult, error) {
	return a.definition.driver.resultEvidence.Capture(ctx, invocation, output)
}

func (a *configuredAdapter) sessionDirectory(argv []string) string {
	return a.definition.driver.resultEvidence.SessionDirectory(argv)
}

func newInvocation(result DriverResult, env map[string]string, request AgentRequest, prompt string, contract ResultContract, sessionDir string) Invocation {
	var session *Session
	if result.Session != nil {
		session = &Session{value: *result.Session}
	}
	return Invocation{
		argv: result.Argv, env: cloneEnv(env), phase: request.Phase,
		input: request.Input, prompt: prompt, resultContract: contract,
		protocolFile: result.ProtocolFile,
		sessionDir:   sessionDir, session: session,
	}
}

func validateAgentRequest(request AgentRequest, preparation invocationPreparation) error {
	if strings.TrimSpace(request.Prompt) == "" {
		return &CaptureError{Code: CaptureBoundaryError, Detail: "prompt is empty or exceeds its limit"}
	}
	if request.Task != rundomain.TaskReason && request.Task != rundomain.TaskExplore && request.Task != rundomain.TaskBootstrap {
		return &CaptureError{Code: CaptureBoundaryError, Detail: "unknown task type"}
	}
	rule, ok := resultRuleFor(request.Task, request.Phase)
	if !ok {
		return &CaptureError{Code: CaptureBoundaryError, Detail: "task and phase do not match"}
	}
	if rule.preparation != preparation {
		return &CaptureError{Code: CaptureBoundaryError, Detail: "phase does not match the invocation preparation"}
	}
	switch request.Input.Kind {
	case InputBlackboardFile:
		if request.Task == rundomain.TaskBootstrap || !rundomain.ValidBlackboardInputPath(request.Input.Path) || request.Input.Inline != "" {
			return &CaptureError{Code: CaptureBoundaryError, Detail: "Reason/Explore require an isolated Blackboard input path"}
		}
	case InputBootstrapInline:
		if request.Task != rundomain.TaskBootstrap || request.Input.Path != "" ||
			strings.TrimSpace(request.Input.Inline) == "" || len(request.Input.Inline) > maximumInlineSize {
			return &CaptureError{Code: CaptureBoundaryError, Detail: "Bootstrap requires inline input"}
		}
	default:
		return &CaptureError{Code: CaptureBoundaryError, Detail: "input kind is invalid"}
	}
	return nil
}

func promptForRequestWithResultOutputContract(
	request AgentRequest,
	resultOutputContract string,
) (string, error) {
	var input string
	switch request.Input.Kind {
	case InputBlackboardFile:
		input = "Read the immutable Blackboard input for this Run from " + request.Input.Path + "."
	case InputBootstrapInline:
		input = "Use only this Bootstrap input:\n----- AETHER BOOTSTRAP INPUT BEGIN -----\n" +
			request.Input.Inline + "\n----- AETHER BOOTSTRAP INPUT END -----"
	}
	prompt := strings.TrimRight(request.Prompt, "\n") + "\n\n" + input +
		"\n\n" + resultOutputContract + "\n"
	if len(prompt) > maximumPromptSize {
		return "", &CaptureError{Code: CaptureBoundaryError, Detail: "prepared prompt exceeds its limit"}
	}
	return prompt, nil
}

func cloneWorkerConfig(config WorkerConfig) WorkerConfig {
	config.Env = cloneEnv(config.Env)
	config.TaskTypes = append([]TaskType(nil), config.TaskTypes...)
	config.Capabilities = append([]string(nil), config.Capabilities...)
	return config
}

func cloneEnv(env map[string]string) map[string]string {
	result := make(map[string]string, len(env))
	for key, value := range env {
		result[key] = value
	}
	return result
}

func cloneIntMap(values map[string]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
