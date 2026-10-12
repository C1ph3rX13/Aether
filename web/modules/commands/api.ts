import { postJSON } from "@/lib/api"
import type {
  CompletionRecordWire,
  HintWire,
  IntentWire,
  ProjectWire,
} from "@/modules/workspace/wire"

export function retryExhaustedWork(
  id: string,
  requestId: string,
  expectedRevision: number
) {
  return postJSON<{
    request_id: string
    project_revision: number
    run_ids: string[]
    replayed: boolean
  }>(`/projects/${encodeURIComponent(id)}/actions/retry-exhausted`, {
    request_id: requestId,
    expected_revision: expectedRevision,
  })
}

export function completeProject(
  id: string,
  from: string[],
  description: string,
  expectedRevision: number
) {
  return postJSON<{
    project: ProjectWire
    project_revision: number
    intent: IntentWire
    completion: CompletionRecordWire
    affected_run_count: number
  }>(`/projects/${encodeURIComponent(id)}/actions/complete`, {
    from,
    description,
    expected_revision: expectedRevision,
  })
}

export function createHint(
  id: string,
  content: string,
  expectedRevision: number
) {
  return postJSON<{
    hint: HintWire
    project_revision: number
    affected_run_count: number
  }>(`/projects/${encodeURIComponent(id)}/hints`, {
    content,
    expected_revision: expectedRevision,
  })
}

export function previewCompletionImpact(id: string, expectedRevision: number) {
  return postJSON<{ project_revision: number; affected_run_count: number }>(
    `/projects/${encodeURIComponent(id)}/actions/impact`,
    { action: "complete_project", expected_revision: expectedRevision }
  )
}
