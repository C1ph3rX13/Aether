package run

import (
	"hash/fnv"
	"strconv"
	"time"
)

const MaximumRetryAttempts = 100

// RetryPolicy is the technical retry budget attached to a Work Key. It is
// persisted with the first attempt so a config reload cannot change a chain.
type RetryPolicy struct {
	MaxAttempts     int           `json:"max_attempts"`
	InitialBackoff  time.Duration `json:"initial_backoff"`
	MaxBackoff      time.Duration `json:"max_backoff"`
	Multiplier      float64       `json:"multiplier"`
	DeclineCooldown time.Duration `json:"decline_cooldown"`
}

type RetryState string

const (
	RetryEligible  RetryState = "eligible"
	RetryBackoff   RetryState = "backoff"
	RetryExhausted RetryState = "exhausted"
	RetryClosed    RetryState = "closed"
)

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, InitialBackoff: 30 * time.Second, MaxBackoff: 5 * time.Minute, Multiplier: 2, DeclineCooldown: 5 * time.Second}
}

func (policy RetryPolicy) Normalized() (RetryPolicy, error) {
	defaults := DefaultRetryPolicy()
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = defaults.MaxAttempts
	}
	if policy.InitialBackoff == 0 {
		policy.InitialBackoff = defaults.InitialBackoff
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = defaults.MaxBackoff
	}
	if policy.Multiplier == 0 {
		policy.Multiplier = defaults.Multiplier
	}
	if policy.DeclineCooldown == 0 {
		policy.DeclineCooldown = defaults.DeclineCooldown
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > MaximumRetryAttempts || policy.InitialBackoff < time.Millisecond || policy.InitialBackoff > 24*time.Hour ||
		policy.MaxBackoff < policy.InitialBackoff || policy.MaxBackoff > 7*24*time.Hour || policy.Multiplier < 1 || policy.Multiplier > 10 ||
		policy.DeclineCooldown < time.Millisecond || policy.DeclineCooldown > 24*time.Hour {
		return RetryPolicy{}, newError(ErrorInvalidArgument, "retry policy is invalid")
	}
	return policy, nil
}

// Backoff returns the bounded deterministic base delay for an attempt. The
// scheduling layer may add a bounded jitter when presenting candidates.
func (policy RetryPolicy) Backoff(attempt int64) time.Duration {
	if attempt <= 1 {
		return policy.InitialBackoff
	}
	delay := float64(policy.InitialBackoff)
	for index := int64(1); index < attempt; index++ {
		delay *= policy.Multiplier
		if delay >= float64(policy.MaxBackoff) {
			return policy.MaxBackoff
		}
	}
	return time.Duration(delay)
}

// BackoffFor adds stable +/-10% jitter so independent Work Keys do not become
// eligible in one retry wave. Stability keeps restart behavior auditable.
func (policy RetryPolicy) BackoffFor(workKey WorkKey, attempt int64) time.Duration {
	base := policy.Backoff(attempt)
	spread := base / 10
	if spread < time.Millisecond {
		return base
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(workKey))
	_, _ = hash.Write([]byte{':'})
	_, _ = hash.Write([]byte(strconv.FormatInt(attempt, 10)))
	width := uint64(spread*2 + 1)
	offset := time.Duration(hash.Sum64()%width) - spread
	delay := base + offset
	if delay < time.Millisecond {
		return time.Millisecond
	}
	if delay > policy.MaxBackoff {
		return policy.MaxBackoff
	}
	return delay
}
