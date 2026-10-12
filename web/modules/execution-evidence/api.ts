import useSWR from "swr"
import { getJSON } from "@/lib/api"
import {
  executionEvidenceFromWire,
  type WorkspaceExecutionEvidence,
} from "@/modules/execution-evidence/model"
import type { WorkspaceExecutionEvidenceWire } from "@/modules/execution-evidence/wire"

function executionEvidencePath(
  projectId: string,
  selectedIntentId: string | null
): string {
  const query = new URLSearchParams()
  if (selectedIntentId) query.set("intent_id", selectedIntentId)
  const suffix = query.size > 0 ? `?${query}` : ""
  return `/projects/${encodeURIComponent(projectId)}/execution-evidence${suffix}`
}

async function getExecutionEvidence(
  path: string
): Promise<WorkspaceExecutionEvidence> {
  return executionEvidenceFromWire(
    await getJSON<WorkspaceExecutionEvidenceWire>(path)
  )
}

export function getWorkspaceExecutionEvidence(
  projectId: string,
  selectedIntentId: string | null
): Promise<WorkspaceExecutionEvidence> {
  return getExecutionEvidence(
    executionEvidencePath(projectId, selectedIntentId)
  )
}

export function useWorkspaceExecutionEvidence(
  projectId: string | null,
  selectedIntentId: string | null
) {
  const key = projectId
    ? executionEvidencePath(projectId, selectedIntentId)
    : null
  return useSWR<WorkspaceExecutionEvidence>(key, getExecutionEvidence)
}
