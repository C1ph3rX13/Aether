package run

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestValidateLaunchSpecRejectsMismatchedInputAndBootstrapFile(t *testing.T) {
	spec := validLaunchSpec("run-1", "project-1")
	spec.RenderedInput.Hash = RenderedInputHash(fmt.Sprintf("%064x", 1))
	if err := ValidateLaunchSpec(spec); err == nil {
		t.Fatal("expected mismatched Blackboard input hash rejection")
	}
	spec = validLaunchSpec("run-1", "project-1")
	spec.Task = TaskBootstrap
	if err := ValidateLaunchSpec(spec); err == nil {
		t.Fatal("expected Bootstrap file input rejection")
	}
}

func TestValidateLaunchSpecRequiresBootstrapRenderedInputIntegrity(t *testing.T) {
	spec := validLaunchSpec("run-bootstrap", "project-1")
	spec.Task = TaskBootstrap
	spec.Phase = PhaseBootstrap
	spec.RenderedInput = nil
	if err := ValidateLaunchSpec(spec); err == nil {
		t.Fatal("expected Bootstrap without rendered input integrity to be rejected")
	}
	inline := []byte("schema: aether.bootstrap/v1\ngeneration: 1\nrevision: 1\n")
	spec.RenderedInput = &RenderedInput{Bytes: inline, Hash: RenderedInputHashFor(inline)}
	if err := ValidateLaunchSpec(spec); err != nil {
		t.Fatalf("valid Bootstrap rendered input: %v", err)
	}
	spec.RenderedInput.Bytes = append(spec.RenderedInput.Bytes, '\n')
	if err := ValidateLaunchSpec(spec); err == nil {
		t.Fatal("expected changed Bootstrap bytes to be rejected")
	}
}

func TestValidateLaunchSpecRejectsOversizedEnvironment(t *testing.T) {
	spec := validLaunchSpec("run-1", "project-1")
	spec.Environment = map[string]string{"SECRET": strings.Repeat("x", maximumEnvironmentBytes)}
	if err := ValidateLaunchSpec(spec); err == nil {
		t.Fatal("expected oversized environment rejection")
	}
}

func TestValidateLaunchSpecRejectsUnsupportedStopGrace(t *testing.T) {
	spec := validLaunchSpec("run-1", "project-1")
	spec.PhaseTimeout = 2 * MaximumStopGrace
	spec.StopGrace = MaximumStopGrace + time.Second
	if err := ValidateLaunchSpec(spec); err == nil {
		t.Fatal("expected unsupported stop grace rejection")
	}
}

func validLaunchSpec(runID, projectID string) LaunchSpec {
	yaml := []byte("schema: aether.blackboard/v1\ngeneration: 1\nrevision: 1\n")
	digest := sha256.Sum256(yaml)
	id := RunID(runID)
	return LaunchSpec{
		RunID: id, ProjectID: projectID, Task: TaskReason, Phase: PhaseReason, FencingToken: 1,
		ConfigRevision: "config-1", Image: "aether:test",
		Command:       []string{"agent", "run"},
		Environment:   map[string]string{"TOKEN": "secret"},
		RenderedInput: &RenderedInput{Path: BlackboardPath(id, PhaseReason), Bytes: yaml, Hash: RenderedInputHash(fmt.Sprintf("%x", digest[:]))},
		PhaseTimeout:  time.Minute, StopGrace: 5 * time.Second,
	}
}
