package run

import "time"

const (
	MaximumLeaseExtension       = time.Hour
	ProcessRecoveryCapability   = "process_recovery"
	maximumRecoveryPhaseSeconds = 24 * 60 * 60
)

type ProcessRecoveryMode string

const (
	ProcessRecoveryExplorePrimary    ProcessRecoveryMode = "explore_primary"
	ProcessRecoveryExploreConclude   ProcessRecoveryMode = "explore_conclude"
	ProcessRecoveryBootstrapPrimary  ProcessRecoveryMode = "bootstrap_primary"
	ProcessRecoveryBootstrapConclude ProcessRecoveryMode = "bootstrap_conclude"
)

type BeginProcessRecoveryRequest struct {
	RunID                  RunID               `json:"run_id"`
	FencingToken           FencingToken        `json:"fencing_token"`
	DispatcherID           string              `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken        `json:"dispatcher_fencing_token"`
	FailedPhase            ExecutionPhase      `json:"failed_phase"`
	Mode                   ProcessRecoveryMode `json:"mode"`
}

type ProcessRecoveryReservation struct {
	RunID        RunID               `json:"run_id"`
	FencingToken FencingToken        `json:"fencing_token"`
	Mode         ProcessRecoveryMode `json:"mode"`
	Phase        ExecutionPhase      `json:"phase"`
	ReservedAt   time.Time           `json:"reserved_at"`
	PhaseTimeout time.Duration       `json:"phase_timeout"`
}

func (request BeginProcessRecoveryRequest) Validate() error {
	if !bounded(string(request.RunID), 256) || request.FencingToken <= 0 ||
		!bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "process recovery request is invalid")
	}
	switch request.Mode {
	case ProcessRecoveryExplorePrimary:
		if request.FailedPhase != PhaseExplore {
			return newError(ErrorInvalidArgument, "Explore process recovery requires an Explore phase")
		}
	case ProcessRecoveryExploreConclude:
		if request.FailedPhase != PhaseExploreConclude {
			return newError(ErrorInvalidArgument, "Explore conclude process recovery requires an Explore conclude phase")
		}
	case ProcessRecoveryBootstrapPrimary:
		if request.FailedPhase != PhaseBootstrap {
			return newError(ErrorInvalidArgument, "Bootstrap process recovery requires a Bootstrap phase")
		}
	case ProcessRecoveryBootstrapConclude:
		if request.FailedPhase != PhaseBootstrapConclude {
			return newError(ErrorInvalidArgument, "Bootstrap conclude process recovery requires a Bootstrap conclude phase")
		}
	default:
		return newError(ErrorInvalidArgument, "process recovery request is invalid")
	}
	return nil
}

func processRecoveryPhase(mode ProcessRecoveryMode) ExecutionPhase {
	switch mode {
	case ProcessRecoveryExplorePrimary:
		return PhaseExploreRecovery
	case ProcessRecoveryExploreConclude:
		return PhaseExploreConcludeRecovery
	case ProcessRecoveryBootstrapPrimary:
		return PhaseBootstrapRecovery
	case ProcessRecoveryBootstrapConclude:
		return PhaseBootstrapConcludeRecovery
	default:
		return ""
	}
}

func (reservation ProcessRecoveryReservation) Validate() error {
	phase := processRecoveryPhase(reservation.Mode)
	if !bounded(string(reservation.RunID), 256) || reservation.FencingToken <= 0 ||
		reservation.ReservedAt.IsZero() || reservation.PhaseTimeout <= 0 ||
		reservation.PhaseTimeout > time.Duration(maximumRecoveryPhaseSeconds)*time.Second ||
		phase == "" || reservation.Phase != phase {
		return newError(ErrorInvalidArgument, "process recovery reservation is invalid")
	}
	return nil
}

func (snapshot WorkerSnapshot) SupportsProcessRecovery() bool {
	for _, capability := range snapshot.Capabilities {
		if capability == ProcessRecoveryCapability {
			return true
		}
	}
	return false
}

type RenewRunRequest struct {
	RunID          RunID         `json:"run_id"`
	FencingToken   FencingToken  `json:"fencing_token"`
	DispatcherID   string        `json:"dispatcher_id"`
	LeaseExtension time.Duration `json:"lease_extension"`
}

func (request RenewRunRequest) Validate() error {
	if request.RunID == "" || request.FencingToken <= 0 || request.DispatcherID == "" ||
		request.LeaseExtension <= 0 || request.LeaseExtension > MaximumLeaseExtension {
		return newError(ErrorInvalidArgument, "run lease renewal is invalid")
	}
	return nil
}

type RecoveryOptions struct {
	DispatcherID           string
	DispatcherFencingToken FencingToken
	Reason                 string
}

func (options RecoveryOptions) Validate() error {
	if !bounded(options.DispatcherID, 256) || options.DispatcherFencingToken <= 0 ||
		options.Reason == "" || len(options.Reason) > 256 {
		return newError(ErrorInvalidArgument, "run recovery options are invalid")
	}
	return nil
}

type RecoveryStatus string

const (
	RecoveryObserved      RecoveryStatus = "observed"
	RecoveryDisposed      RecoveryStatus = "disposed"
	RecoveryInterrupted   RecoveryStatus = "interrupted"
	RecoveryDisposeFailed RecoveryStatus = "dispose_failed"
)

type RecoveryRecord struct {
	RunID                  RunID          `json:"run_id"`
	DispatcherID           string         `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken   `json:"dispatcher_fencing_token"`
	Status                 RecoveryStatus `json:"status"`
	Reason                 string         `json:"reason"`
}

func (record RecoveryRecord) Validate() error {
	if !bounded(string(record.RunID), 256) || !bounded(record.DispatcherID, 256) ||
		record.DispatcherFencingToken <= 0 || record.Reason == "" || len(record.Reason) > 256 {
		return newError(ErrorInvalidArgument, "run recovery record is invalid")
	}
	switch record.Status {
	case RecoveryObserved, RecoveryDisposed, RecoveryInterrupted, RecoveryDisposeFailed:
		return nil
	default:
		return newError(ErrorInvalidArgument, "run recovery status is invalid")
	}
}
