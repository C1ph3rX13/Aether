import { ApiError } from "@/lib/api"
import type { WorkspaceMode } from "@/modules/workspace/contract"
import type { ConsoleFreshnessStatus } from "@/modules/workspace/live-sync"
import type { ProjectMeta } from "@/modules/workspace/model"
import {
  resolveProjectCommandAvailability,
  type ProjectCommandAvailability,
} from "@/modules/workspace/command-availability"

export interface ProjectCommandAuthority {
  project: ProjectMeta | null
  freshness: ConsoleFreshnessStatus
  mode: WorkspaceMode
  markCommandCommitted: (revision?: number) => void
}

export interface ProjectCommandReceipt {
  project_revision: number
}

export async function executeProjectCommand<T extends ProjectCommandReceipt>(
  authority: ProjectCommandAuthority,
  action: keyof ProjectCommandAvailability,
  command: () => Promise<T>
): Promise<T> {
  if (
    !authority.project ||
    authority.freshness !== "live" ||
    authority.mode !== "live"
  ) {
    throw new ApiError(
      "Project command unavailable: the projection is not live",
      409,
      { code: "projection_not_live" }
    )
  }
  if (
    !resolveProjectCommandAvailability(
      authority.project.status,
      authority.freshness,
      false
    )[action]
  ) {
    throw new Error(
      "This action is not available for the current Project status. Review the refreshed Project before retrying."
    )
  }
  const result = await command()
  const revision = result?.project_revision
  if (
    typeof revision !== "number" ||
    !Number.isSafeInteger(revision) ||
    revision < 1
  ) {
    throw new Error(
      "The Project action returned an unreadable Revision. Refresh Project before retrying."
    )
  }
  authority.markCommandCommitted(revision)
  return result
}
