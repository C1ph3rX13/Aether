package run

import (
	"testing"
	"time"
)

func TestRetryPolicyRejectsSubMillisecondPersistenceLoss(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.InitialBackoff = time.Nanosecond
	if _, err := policy.Normalized(); err == nil {
		t.Fatal("sub-millisecond backoff was accepted")
	}
	policy = DefaultRetryPolicy()
	policy.DeclineCooldown = time.Nanosecond
	if _, err := policy.Normalized(); err == nil {
		t.Fatal("sub-millisecond decline cooldown was accepted")
	}
}

func TestRetryBackoffJitterIsStableBoundedAndKeyed(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 3, InitialBackoff: 10 * time.Second, MaxBackoff: time.Minute,
		Multiplier: 2, DeclineCooldown: time.Second,
	}
	first := policy.BackoffFor("work-a", 1)
	if again := policy.BackoffFor("work-a", 1); again != first {
		t.Fatalf("unstable jitter: first=%s again=%s", first, again)
	}
	if first < 9*time.Second || first > 11*time.Second {
		t.Fatalf("jitter outside +/-10%%: %s", first)
	}
	if other := policy.BackoffFor("work-b", 1); other == first {
		t.Fatalf("different Work Keys received identical jitter: %s", first)
	}
}
