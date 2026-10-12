package worker

import rundomain "github.com/C1ph3rX13/Aether/internal/run"

type WorkerType string

const (
	WorkerTypeClaudeCode WorkerType = "claudecode"
	WorkerTypeCodex      WorkerType = "codex"
	WorkerTypePi         WorkerType = "pi"
	WorkerTypeMock       WorkerType = "mock"
)

type TaskType = rundomain.TaskType

const ProcessRecoveryCapability = rundomain.ProcessRecoveryCapability

const (
	TaskTypeBootstrap TaskType = rundomain.TaskBootstrap
	TaskTypeReason    TaskType = rundomain.TaskReason
	TaskTypeExplore   TaskType = rundomain.TaskExplore
)

// WorkerConfig is the immutable execution representation of one Worker.
// SecretVersionIDs maps Agent environment slots to immutable Secret Versions.
type WorkerConfig struct {
	Name             string
	Type             WorkerType
	TaskTypes        []TaskType
	MaxRunning       int
	Priority         int
	Capabilities     []string
	Env              map[string]string
	SecretVersionIDs map[string]string
}
