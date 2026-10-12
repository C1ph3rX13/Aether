package project

type Hint struct {
	ID               string  `json:"id" yaml:"id"`
	Content          string  `json:"content" yaml:"content"`
	Creator          string  `json:"creator" yaml:"creator"`
	CreatedAt        string  `json:"created_at" yaml:"created_at"`
	Generation       int     `json:"generation" yaml:"generation"`
	WithdrawnAt      *string `json:"withdrawn_at,omitempty" yaml:"withdrawn_at,omitempty"`
	WithdrawnBy      *string `json:"withdrawn_by,omitempty" yaml:"withdrawn_by,omitempty"`
	WithdrawalReason *string `json:"withdrawal_reason,omitempty" yaml:"withdrawal_reason,omitempty"`
	Status           string  `json:"status" yaml:"status"`
}

func (h Hint) IsActive() bool {
	return h.WithdrawnAt == nil
}
