import type {
  CaptureFailureReasonWire,
  CaptureDiagnosticWire,
  ExecutionRunWire,
  ExecutionStatusWire,
  ExecutionTaskWire,
  ResultEvidenceSourceWire,
  WorkspaceExecutionEvidenceWire,
} from "@/modules/execution-evidence/wire"

export type WorkspaceExecutionTask = ExecutionTaskWire
export type WorkspaceExecutionStatus = ExecutionStatusWire
export type CaptureFailureReason = CaptureFailureReasonWire
export type CaptureDiagnostic = CaptureDiagnosticWire
export type ResultEvidenceSource = ResultEvidenceSourceWire

export interface WorkspaceExecutionRun {
  runId: string
  task: WorkspaceExecutionTask
  status: WorkspaceExecutionStatus
  attempt: number
  worker: string
  startedAt: string
  outcomeKind?: string
  errorCode?: string
  captureFailureReason?: CaptureFailureReason
  captureDiagnostic?: CaptureDiagnostic
  resultEvidenceSource?: ResultEvidenceSource
}

export interface WorkspaceExecutionEvidence {
  projectId: string
  activeReason?: WorkspaceExecutionRun
  selectedIntentRun?: WorkspaceExecutionRun
}

export interface IntentExecutionEvidenceState {
  run?: WorkspaceExecutionRun
  isLoading: boolean
  error?: unknown
}

export function executionEvidenceFromWire(
  evidence: WorkspaceExecutionEvidenceWire
): WorkspaceExecutionEvidence {
  return {
    projectId: evidence.project_id,
    ...(evidence.active_reason
      ? { activeReason: executionRunFromWire(evidence.active_reason) }
      : {}),
    ...(evidence.selected_intent_run
      ? {
          selectedIntentRun: executionRunFromWire(evidence.selected_intent_run),
        }
      : {}),
  }
}

function executionRunFromWire(run: ExecutionRunWire): WorkspaceExecutionRun {
  return {
    runId: run.run_id,
    task: run.task,
    status: run.status,
    attempt: run.attempt,
    worker: run.worker,
    startedAt: run.started_at,
    ...(run.outcome_kind ? { outcomeKind: run.outcome_kind } : {}),
    ...(run.error_code ? { errorCode: run.error_code } : {}),
    ...(run.capture_failure_reason
      ? { captureFailureReason: run.capture_failure_reason }
      : {}),
    ...(run.capture_diagnostic
      ? { captureDiagnostic: run.capture_diagnostic }
      : {}),
    ...(run.result_evidence_source
      ? { resultEvidenceSource: run.result_evidence_source }
      : {}),
  }
}
