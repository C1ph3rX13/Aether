package run

import "github.com/C1ph3rX13/Aether/internal/spec"

func shouldFinalizeCapture(reason CaptureFailureReason) bool {
	return reason == CaptureFailureNoCandidate || reason == CaptureFailureInvalidCandidate
}

func shouldFinalizeReasonCapture(captured CapturedResult) bool {
	if shouldFinalizeCapture(captured.CaptureFailureReason) {
		return true
	}
	switch captured.CaptureDiagnostic {
	case CaptureDiagnosticResultFileMissing, CaptureDiagnosticResultFileInvalid, CaptureDiagnosticResultFileTooLarge:
		return true
	default:
		return false
	}
}

func resultFinalizationSeconds(phaseSeconds int) int {
	return spec.ResultFinalizationPhaseSeconds(phaseSeconds)
}
