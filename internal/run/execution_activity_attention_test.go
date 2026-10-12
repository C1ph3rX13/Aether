package run

import (
	"testing"
)

func TestActivityAttentionClassification(t *testing.T) {
	tests := []struct {
		name       string
		status     Status
		retryState RetryState
		want       AttentionKind
	}{
		{name: "none", status: StatusActive, retryState: RetryEligible, want: AttentionNone},
		{name: "failed", status: StatusFailed, retryState: RetryEligible, want: AttentionFailed},
		{name: "interrupted", status: StatusInterrupted, retryState: RetryEligible, want: AttentionInterrupted},
		{name: "backoff", status: StatusCancelled, retryState: RetryBackoff, want: AttentionRetryBackoff},
		{name: "exhausted takes precedence over failure", status: StatusFailed, retryState: RetryExhausted, want: AttentionRetryExhausted},
		{name: "failure takes precedence over backoff", status: StatusFailed, retryState: RetryBackoff, want: AttentionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := activityAttention(test.status, test.retryState); got != test.want {
				t.Fatalf("activityAttention(%q, %q) = %q, want %q", test.status, test.retryState, got, test.want)
			}
		})
	}
}
