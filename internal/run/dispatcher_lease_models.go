package run

import "time"

type DispatcherLeaseRequest struct {
	InstanceID string
	TTL        time.Duration
}

func (request DispatcherLeaseRequest) Validate() error {
	if !bounded(request.InstanceID, 256) || request.TTL < 5*time.Second || request.TTL > MaximumLeaseExtension {
		return newError(ErrorInvalidArgument, "Dispatcher Lease request is invalid")
	}
	return nil
}

type DispatcherLease struct {
	InstanceID   string       `json:"instance_id"`
	FencingToken FencingToken `json:"fencing_token"`
	ExpiresAt    time.Time    `json:"expires_at"`
}
