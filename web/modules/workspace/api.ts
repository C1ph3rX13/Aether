import useSWR from "swr"
import { getJSON } from "@/lib/api"
import {
  projectDetailFromWire,
  type ProjectDetail,
} from "@/modules/workspace/model"
import type { ProjectDetailWire } from "@/modules/workspace/wire"

async function getProjectDetail(
  path: string,
  signal?: AbortSignal
): Promise<ProjectDetail> {
  return projectDetailFromWire(await getJSON<ProjectDetailWire>(path, signal))
}

export function getProjectWorkspace(
  projectId: string,
  signal?: AbortSignal
): Promise<ProjectDetail> {
  return getProjectDetail(`/projects/${encodeURIComponent(projectId)}`, signal)
}

export function useProjectWorkspace(projectId: string | null) {
  return useSWR<ProjectDetail>(
    projectId ? `/projects/${encodeURIComponent(projectId)}` : null,
    getProjectDetail
  )
}
