export type ExecutionTaskWire = "reason" | "explore" | "bootstrap"

export type ExecutionStatusWire =
  | "active"
  | "committed"
  | "declined"
  | "stale"
  | "failed"
  | "cancelled"
  | "interrupted"

export type CaptureFailureReasonWire =
  | "timed_out"
  | "no_candidate"
  | "multiple_candidates"
  | "invalid_candidate"
  | "candidate_too_large"
  | "boundary_error"

export type CaptureDiagnosticWire =
  "result_file_missing" | "result_file_invalid" | "result_file_too_large"

export type ResultEvidenceSourceWire = "file"

export interface ExecutionRunWire {
  run_id: string
  task: ExecutionTaskWire
  status: ExecutionStatusWire
  attempt: number
  worker: string
  started_at: string
  outcome_kind?: string
  error_code?: string
  capture_failure_reason?: CaptureFailureReasonWire
  capture_diagnostic?: CaptureDiagnosticWire
  result_evidence_source?: ResultEvidenceSourceWire
}

export interface WorkspaceExecutionEvidenceWire {
  project_id: string
  active_reason?: ExecutionRunWire
  selected_intent_run?: ExecutionRunWire
}
