package project

type Intent struct {
	ID                 string       `json:"id" yaml:"id"`
	Kind               IntentKind   `json:"kind" yaml:"kind"`
	Status             IntentStatus `json:"status" yaml:"status"`
	From               []string     `json:"from" yaml:"from"`
	To                 *string      `json:"to,omitempty" yaml:"to,omitempty"`
	Description        string       `json:"description" yaml:"description"`
	Creator            string       `json:"creator" yaml:"creator"`
	CreatedAt          string       `json:"created_at" yaml:"created_at"`
	ConcludedAt        *string      `json:"concluded_at,omitempty" yaml:"concluded_at,omitempty"`
	CancelledAt        *string      `json:"cancelled_at,omitempty" yaml:"cancelled_at,omitempty"`
	CancelledBy        *string      `json:"cancelled_by,omitempty" yaml:"cancelled_by,omitempty"`
	CancellationReason *string      `json:"cancellation_reason,omitempty" yaml:"cancellation_reason,omitempty"`
}

type IntentKind string

const (
	IntentKindBootstrap  IntentKind = "bootstrap"
	IntentKindExplore    IntentKind = "explore"
	IntentKindCompletion IntentKind = "completion"
)

type IntentStatus string

const (
	IntentStatusOpen      IntentStatus = "open"
	IntentStatusConcluded IntentStatus = "concluded"
	IntentStatusCancelled IntentStatus = "cancelled"
)

func (i Intent) IsOpen() bool {
	if i.Status != "" {
		return i.Status == IntentStatusOpen
	}
	return i.To == nil && i.CancelledAt == nil
}
