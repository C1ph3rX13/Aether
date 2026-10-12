package run

// CaptureFailureReason is a bounded diagnostic for a Result Capture failure.
// It is intentionally separate from the terminal capture_failed error code.
type CaptureFailureReason string

const (
	CaptureFailureTimedOut           CaptureFailureReason = "timed_out"
	CaptureFailureNoCandidate        CaptureFailureReason = "no_candidate"
	CaptureFailureMultipleCandidates CaptureFailureReason = "multiple_candidates"
	CaptureFailureInvalidCandidate   CaptureFailureReason = "invalid_candidate"
	CaptureFailureCandidateTooLarge  CaptureFailureReason = "candidate_too_large"
	CaptureFailureBoundaryError      CaptureFailureReason = "boundary_error"
)

func (reason CaptureFailureReason) Valid() bool {
	switch reason {
	case "", CaptureFailureTimedOut, CaptureFailureNoCandidate,
		CaptureFailureMultipleCandidates, CaptureFailureInvalidCandidate,
		CaptureFailureCandidateTooLarge, CaptureFailureBoundaryError:
		return true
	default:
		return false
	}
}
