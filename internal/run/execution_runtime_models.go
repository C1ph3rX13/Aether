package run

import (
	"context"
	"strings"
	"time"
)

const (
	maximumEnvironmentBytes = 256 * 1024
	MaximumStopGrace        = time.Minute
)

// Runtime is the RunExecutor-owned boundary to an execution environment.
type Runtime interface {
	Launch(context.Context, LaunchSpec) (Process, error)
}

// ProcessOutputCapture is the optional, Adapter-aware output boundary. Runtime
// owns stream delivery and process lifecycle; the capture implementation owns
// how much of each stream is retained and what stdout represents to the
// Adapter. Implementations must accept and discard bytes after their retention
// limit so the Runtime can keep draining the attach stream.
type ProcessOutputCapture interface {
	WriteStdout([]byte)
	WriteStderr([]byte)
	Result() ProcessOutputCaptureResult
}

type ProcessOutputCaptureResult struct {
	Stdout     string
	Overflowed bool
}

// TrackedRuntime persists the Project container immediately after Docker
// creates or adopts it, then persists each Exec before Docker starts it.
type TrackedRuntime interface {
	LaunchTracked(context.Context, LaunchSpec, ExecutionHandleRegistration) (Process, error)
}

// Process represents one Agent phase running as a Docker Exec.
type Process interface {
	Wait(context.Context) (ProcessOutput, error)
	Stop(context.Context, time.Duration) error
}

type ProcessOutput struct {
	Stdout                   string
	Stderr                   string
	Exited                   bool
	ExitCode                 int
	TimedOut                 bool
	Cancelled                bool
	ErrorCode                ProcessErrorCode
	StoppedProcessFileReader StoppedProcessFileReader
}

// StoppedProcessFileReader is a one-shot capability for a finite set of exact
// files owned by a Process whose Runtime has confirmed it stopped.
type StoppedProcessFileReader interface {
	ReadFiles(context.Context, []string) map[string]StoppedProcessFile
}

// StoppedProcessFile is the independent result of one exact stopped-process
// archive read. A missing or malformed file never hides the result for another
// requested path.
type StoppedProcessFile struct {
	Bytes []byte
	Err   error
}

type ProcessErrorCode string

const (
	ProcessErrorOutputTooLarge ProcessErrorCode = "output_too_large"
	ProcessErrorStreamRead     ProcessErrorCode = "process_stream_error"
	ProcessErrorContainerState ProcessErrorCode = "process_container_state_error"
)

// Ownership identifies the Aether deployment and Dispatcher instance used by
// Runtime implementations and test doubles.
type Ownership struct {
	DeploymentID string
	DispatcherID string
}

type LaunchSpec struct {
	RunID          RunID
	ProjectID      string
	Task           TaskType
	Phase          ExecutionPhase
	FencingToken   FencingToken
	ConfigRevision string
	Image          string
	Command        []string
	Environment    map[string]string
	OutputCapture  ProcessOutputCapture
	RenderedInput  *RenderedInput
	ProtocolFile   *ProtocolFile
	PhaseTimeout   time.Duration
	StopGrace      time.Duration
}

type RenderedInput struct {
	Path  string
	Bytes []byte
	Hash  RenderedInputHash
}

type ExecutionPhase string

const (
	PhaseReason                    ExecutionPhase = "reason"
	PhaseReasonFinalization        ExecutionPhase = "reason_finalization"
	PhaseExplore                   ExecutionPhase = "explore_execute"
	PhaseExploreConclude           ExecutionPhase = "explore_conclude"
	PhaseExploreRecovery           ExecutionPhase = "explore_recovery"
	PhaseExploreConcludeRecovery   ExecutionPhase = "explore_conclude_recovery"
	PhaseBootstrap                 ExecutionPhase = "bootstrap"
	PhaseBootstrapConclude         ExecutionPhase = "bootstrap_conclude"
	PhaseBootstrapRecovery         ExecutionPhase = "bootstrap_recovery"
	PhaseBootstrapConcludeRecovery ExecutionPhase = "bootstrap_conclude_recovery"
)

func (phase ExecutionPhase) valid() bool {
	switch phase {
	case PhaseReason, PhaseReasonFinalization, PhaseExplore, PhaseExploreConclude, PhaseExploreRecovery,
		PhaseExploreConcludeRecovery, PhaseBootstrap, PhaseBootstrapConclude, PhaseBootstrapRecovery,
		PhaseBootstrapConcludeRecovery:
		return true
	default:
		return false
	}
}

func (phase ExecutionPhase) validFor(task TaskType) bool {
	if !phase.valid() {
		return false
	}
	switch task {
	case TaskReason:
		return phase == PhaseReason || phase == PhaseReasonFinalization
	case TaskExplore:
		return phase == PhaseExplore || phase == PhaseExploreConclude ||
			phase == PhaseExploreRecovery || phase == PhaseExploreConcludeRecovery
	case TaskBootstrap:
		return phase == PhaseBootstrap || phase == PhaseBootstrapConclude || phase == PhaseBootstrapRecovery ||
			phase == PhaseBootstrapConcludeRecovery
	default:
		return false
	}
}

func ValidateLaunchSpec(spec LaunchSpec) error {
	if spec.ProtocolFile != nil {
		if err := spec.ProtocolFile.Validate(); err != nil {
			return err
		}
	}
	if !bounded(string(spec.RunID), 256) || !bounded(spec.ProjectID, 256) || !spec.Task.valid() || spec.FencingToken <= 0 {
		return newError(ErrorInvalidArgument, "Run execution ownership is invalid")
	}
	if !spec.Phase.validFor(spec.Task) {
		return newError(ErrorInvalidArgument, "Run execution phase is invalid")
	}
	if !bounded(spec.ConfigRevision, 256) || !bounded(spec.Image, 512) {
		return newError(ErrorInvalidArgument, "Run image or config revision is invalid")
	}
	if len(spec.Command) == 0 || len(spec.Command) > 256 {
		return newError(ErrorInvalidArgument, "Run invocation command is invalid")
	}
	commandBytes := 0
	for _, argument := range spec.Command {
		if argument == "" || strings.IndexByte(argument, 0) >= 0 {
			return newError(ErrorInvalidArgument, "Run invocation argument is invalid")
		}
		commandBytes += len(argument)
	}
	if commandBytes > 256*1024 || len(spec.Environment) > 256 {
		return newError(ErrorInvalidArgument, "Run invocation exceeds its size limit")
	}
	environmentBytes := 0
	for key, value := range spec.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return newError(ErrorInvalidArgument, "Run invocation environment is invalid")
		}
		environmentBytes += len(key) + len(value) + 1
		if environmentBytes > maximumEnvironmentBytes {
			return newError(ErrorInvalidArgument, "Run invocation exceeds its size limit")
		}
	}
	if spec.PhaseTimeout <= 0 || spec.StopGrace <= 0 || spec.StopGrace > MaximumStopGrace || spec.StopGrace > spec.PhaseTimeout {
		return newError(ErrorInvalidArgument, "Run phase timeout is invalid")
	}
	if spec.RenderedInput == nil || len(spec.RenderedInput.Bytes) == 0 || !spec.RenderedInput.Hash.Matches(spec.RenderedInput.Bytes) {
		return newError(ErrorInvalidArgument, "rendered input integrity is invalid")
	}
	if spec.Task == TaskBootstrap {
		if spec.Phase == PhaseBootstrapRecovery || spec.Phase == PhaseBootstrapConcludeRecovery {
			if spec.RenderedInput.Path != BlackboardPath(spec.RunID, spec.Phase) || len(spec.RenderedInput.Bytes) > MaximumBootstrapInputBytes {
				return newError(ErrorInvalidArgument, "Bootstrap recovery rendered input is invalid")
			}
			return nil
		}
		if spec.RenderedInput.Path != "" || len(spec.RenderedInput.Bytes) > MaximumBootstrapInputBytes {
			return newError(ErrorInvalidArgument, "Bootstrap rendered input is invalid")
		}
		return nil
	}
	if spec.RenderedInput.Path != BlackboardPath(spec.RunID, spec.Phase) ||
		len(spec.RenderedInput.Bytes) > MaximumBlackboardInputBytes {
		return newError(ErrorInvalidArgument, "Reason/Explore rendered input is invalid")
	}
	return nil
}

func ValidateOwnership(ownership Ownership) error {
	if !bounded(ownership.DeploymentID, 256) || len(ownership.DispatcherID) > 256 {
		return newError(ErrorInvalidArgument, "runtime ownership filter is invalid")
	}
	return nil
}
