import useSWR from "swr"
import { getJSON } from "@/lib/api"
import {
  consoleLogPageFromWire,
  type ConsoleLogPage,
} from "@/modules/log/model"
import type { ConsoleLogPage as ConsoleLogPageWire } from "@/modules/log/wire"
import {
  projectDetailFromWire,
  type ProjectDetail,
} from "@/modules/workspace/model"
import type { ProjectDetailWire } from "@/modules/workspace/wire"

function projectLogPath(
  projectId: string,
  beforeRevision?: number,
  limit?: number
) {
  const query = new URLSearchParams()
  if (beforeRevision !== undefined)
    query.set("before_revision", String(beforeRevision))
  if (limit !== undefined) query.set("limit", String(limit))
  const suffix = query.size > 0 ? `?${query}` : ""
  return `/projects/${encodeURIComponent(projectId)}/log${suffix}`
}

async function getLogPage(
  path: string,
  signal?: AbortSignal
): Promise<ConsoleLogPage> {
  return consoleLogPageFromWire(await getJSON<ConsoleLogPageWire>(path, signal))
}

export function useProjectLog(
  projectId: string | null,
  beforeRevision?: number,
  limit?: number
) {
  return useSWR<ConsoleLogPage>(
    projectId ? projectLogPath(projectId, beforeRevision, limit) : null,
    getLogPage
  )
}

export function getProjectLog(
  projectId: string,
  beforeRevision?: number,
  limit?: number,
  signal?: AbortSignal
): Promise<ConsoleLogPage> {
  return getLogPage(projectLogPath(projectId, beforeRevision, limit), signal)
}

export async function getRevisionBlackboard(
  projectId: string,
  revision: number,
  signal?: AbortSignal
): Promise<ProjectDetail> {
  return projectDetailFromWire(
    await getJSON<ProjectDetailWire>(
      `/projects/${encodeURIComponent(projectId)}/history/${revision}/blackboard`,
      signal
    )
  )
}
