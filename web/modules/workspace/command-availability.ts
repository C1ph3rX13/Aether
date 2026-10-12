import type { ConsoleFreshnessStatus } from "@/modules/workspace/live-sync"
import type { ProjectMeta } from "@/modules/workspace/model"

export interface ProjectCommandAvailability {
  renameProject: boolean
  pauseProject: boolean
  resumeProject: boolean
  reopenProject: boolean
  deleteProject: boolean
  addHint: boolean
  completeProject: boolean
  retryExhaustedWork: boolean
}

export function resolveProjectCommandAvailability(
  status: ProjectMeta["status"],
  freshness: ConsoleFreshnessStatus,
  commandPending: boolean
): ProjectCommandAvailability {
  const writable = freshness === "live" && !commandPending
  const active = writable && status === "active"

  return {
    renameProject: writable,
    pauseProject: active,
    resumeProject: writable && status === "paused",
    reopenProject: writable && status === "completed",
    deleteProject: writable,
    addHint: writable && status !== "completed",
    completeProject: active,
    retryExhaustedWork: active,
  }
}
