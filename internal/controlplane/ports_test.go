package controlplane

import (
	"reflect"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestProjectExecutionPortMapsCancellationReasons(t *testing.T) {
	tests := []struct {
		reason project.CancellationReason
		code   rundomain.ErrorCode
	}{
		{project.CancellationProjectPaused, rundomain.ErrorProjectPaused},
		{project.CancellationProjectCompleted, rundomain.ErrorProjectCompleted},
		{project.CancellationBlackboardChanged, rundomain.ErrorBlackboardChanged},
		{project.CancellationFactRetracted, rundomain.ErrorFactRetracted},
		{project.CancellationIntentCancelled, rundomain.ErrorIntentCancelled},
		{project.CancellationOperatorCompleted, rundomain.ErrorOperatorCompleted},
		{project.CancellationProjectDeleted, rundomain.ErrorProjectDeleted},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			if got := mapCancellationReason(test.reason); got != test.code {
				t.Fatalf("mapped cancellation reason = %q, want %q", got, test.code)
			}
		})
	}
}

func TestProjectExecutionPortRejectsUnknownCancellationReason(t *testing.T) {
	if got := mapCancellationReason(project.CancellationReason("unknown")); got != rundomain.ErrorInvalidArgument {
		t.Fatalf("unknown cancellation reason mapped to %q, want %q", got, rundomain.ErrorInvalidArgument)
	}
}

func TestProjectExecutionPortConvertsCancellationSelectors(t *testing.T) {
	targetIntentID := "intent-1"
	sourceFactID := "fact-1"
	inputRevision := int64(7)
	selector := project.RunCancellationSelector{Clauses: []project.RunCancellationClause{
		{
			Tasks:               []project.RunTask{project.RunTaskExplore, project.RunTaskBootstrap},
			TargetIntentID:      &targetIntentID,
			SourceFactID:        &sourceFactID,
			InputRevisionAtMost: &inputRevision,
		},
	}}

	converted := toRunCancellationSelector(selector)
	want := rundomain.RunCancellationSelector{Clauses: []rundomain.RunCancellationClause{
		{
			Tasks:               []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskBootstrap},
			TargetIntentID:      &targetIntentID,
			SourceFactID:        &sourceFactID,
			InputRevisionAtMost: &inputRevision,
		},
	}}
	if !reflect.DeepEqual(converted, want) {
		t.Fatalf("converted cancellation selector = %+v, want %+v", converted, want)
	}
}
