package controlplane

import "github.com/C1ph3rX13/Aether/internal/version"

// DeploymentMetadata is the public, read-only deployment identity contract.
type DeploymentMetadata struct {
	Operator     OperatorIdentity       `json:"operator"`
	Build        BuildInfo              `json:"build"`
	Capabilities DeploymentCapabilities `json:"capabilities"`
}

type OperatorIdentity struct {
	Name string `json:"name"`
}

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

type DeploymentCapabilities struct {
	BlackboardHistory bool `json:"blackboard_history"`
	ExecutionActivity bool `json:"execution_activity"`
}

func newDeploymentMetadata(operatorName string) DeploymentMetadata {
	return DeploymentMetadata{
		Operator: OperatorIdentity{Name: operatorName},
		Build: BuildInfo{
			Version: version.Version,
			Commit:  version.Commit,
			BuiltAt: version.BuiltAt,
		},
		Capabilities: DeploymentCapabilities{
			BlackboardHistory: true,
			ExecutionActivity: true,
		},
	}
}
