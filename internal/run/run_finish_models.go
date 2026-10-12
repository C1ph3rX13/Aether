package run

import (
	"encoding/json"
	"time"
)

type FinishRequest struct {
	RunID                RunID                `json:"run_id"`
	FencingToken         FencingToken         `json:"fencing_token"`
	Candidate            json.RawMessage      `json:"candidate,omitempty"`
	CaptureDiagnostic    CaptureDiagnostic    `json:"capture_diagnostic,omitempty"`
	ResultEvidenceSource ResultEvidenceSource `json:"result_evidence_source,omitempty"`
	Termination          *RunTermination      `json:"termination,omitempty"`
}

type RunTermination struct {
	Status               Status               `json:"status"`
	Code                 ErrorCode            `json:"code,omitempty"`
	CaptureFailureReason CaptureFailureReason `json:"capture_failure_reason,omitempty"`
}

type FinishReceipt struct {
	RunID                RunID                `json:"run_id"`
	Status               Status               `json:"status"`
	Code                 ErrorCode            `json:"code,omitempty"`
	CaptureFailureReason CaptureFailureReason `json:"capture_failure_reason,omitempty"`
	CaptureDiagnostic    CaptureDiagnostic    `json:"capture_diagnostic,omitempty"`
	ResultEvidenceSource ResultEvidenceSource `json:"result_evidence_source,omitempty"`
	OutcomeKind          OutcomeKind          `json:"outcome_kind"`
	ResultHash           string               `json:"result_hash"`
	ProjectRevision      int64                `json:"project_revision"`
	CreatedFactIDs       []string             `json:"created_fact_ids,omitempty"`
	CreatedIntentIDs     []string             `json:"created_intent_ids,omitempty"`
	CompletionIntentID   string               `json:"completion_intent_id,omitempty"`
	FinishedAt           time.Time            `json:"finished_at"`
	Replayed             bool                 `json:"replayed,omitempty"`
}
