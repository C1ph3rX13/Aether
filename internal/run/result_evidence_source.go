package run

// ResultEvidenceSource identifies the evidence channel that supplied an
// accepted, normalized RunResult. It is empty for technical terminal results.
type ResultEvidenceSource string

const (
	ResultEvidenceSourceFile ResultEvidenceSource = "file"
)

func (source ResultEvidenceSource) Valid() bool {
	switch source {
	case "", ResultEvidenceSourceFile:
		return true
	default:
		return false
	}
}
