package api

import (
	"math"
	"testing"
)

func TestDispatcherLeaseTTLRejectsOverflowingSeconds(t *testing.T) {
	if _, err := dispatcherLeaseTTL(math.MaxInt64); err == nil {
		t.Fatal("overflowing lease TTL was accepted")
	}
	if ttl, err := dispatcherLeaseTTL(60); err != nil || ttl.Seconds() != 60 {
		t.Fatalf("valid lease TTL: ttl=%s err=%v", ttl, err)
	}
}
