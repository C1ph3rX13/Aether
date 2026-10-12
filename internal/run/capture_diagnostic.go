package run

// CaptureDiagnostic is a bounded, non-sensitive explanation of a result-file
// failure observed by the Worker Adapter. It never contains output,
// Prompt, Session, or parser error text.
type CaptureDiagnostic string

const (
	CaptureDiagnosticResultFileMissing  CaptureDiagnostic = "result_file_missing"
	CaptureDiagnosticResultFileInvalid  CaptureDiagnostic = "result_file_invalid"
	CaptureDiagnosticResultFileTooLarge CaptureDiagnostic = "result_file_too_large"
)

func (diagnostic CaptureDiagnostic) Valid() bool {
	switch diagnostic {
	case "", CaptureDiagnosticResultFileMissing, CaptureDiagnosticResultFileInvalid,
		CaptureDiagnosticResultFileTooLarge:
		return true
	default:
		return false
	}
}
