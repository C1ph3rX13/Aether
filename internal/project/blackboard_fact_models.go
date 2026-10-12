package project

type Fact struct {
	ID                string     `json:"id" yaml:"id"`
	Description       string     `json:"description" yaml:"description"`
	Status            FactStatus `json:"status" yaml:"status"`
	Supersedes        *string    `json:"supersedes,omitempty" yaml:"supersedes,omitempty"`
	RetractedAt       *string    `json:"retracted_at,omitempty" yaml:"retracted_at,omitempty"`
	RetractedBy       *string    `json:"retracted_by,omitempty" yaml:"retracted_by,omitempty"`
	RetractionReason  *string    `json:"retraction_reason,omitempty" yaml:"retraction_reason,omitempty"`
	ReplacementFactID *string    `json:"replacement_fact_id,omitempty" yaml:"replacement_fact_id,omitempty"`
}

type FactStatus string

const (
	FactStatusActive    FactStatus = "active"
	FactStatusRetracted FactStatus = "retracted"
)

func (f Fact) IsActive() bool {
	return f.Status != FactStatusRetracted && f.RetractedAt == nil
}
