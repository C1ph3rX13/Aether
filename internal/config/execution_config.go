package config

import (
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

const (
	DefaultOutputLimitBytes int64 = 1 << 20
	MinOutputLimitBytes     int64 = 64 << 10
	MaxOutputLimitBytes     int64 = 8 << 20
)

type CompletedAction string

const (
	CompletedActionStop   CompletedAction = "stop"
	CompletedActionRemove CompletedAction = "remove"
)

type ContainerConfig struct {
	Image            string
	NetworkMode      string
	CapAdd           []string
	CompletedAction  CompletedAction
	StdoutLimitBytes int64
	StderrLimitBytes int64
}

type RuntimeConfig struct {
	MaxWorkers         int
	MaxRunningProjects int
	MaxProjectWorkers  int
	Interval           int
	HealthcheckTimeout int
	WorkerHealthcheck  string
	PromptGroup        string
}

// DispatchConfig is the immutable execution view of one Config Revision.
type DispatchConfig struct {
	ConfigRevision    string
	Runtime           RuntimeConfig
	Container         ContainerConfig
	Workers           []worker.WorkerConfig
	ExecutionPolicies map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy
}
