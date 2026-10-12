import { fetchStream } from "@/lib/api"
import { readAppliedSettings } from "@/lib/applied-settings"

export type ProjectAvailability =
  | "checking"
  | "configuration-required"
  | "ready"
  | "dispatcher-unavailable"
  | "database-unavailable"
  | "unreadable"
  | "transport-failure"

async function readReadiness(
  signal?: AbortSignal
): Promise<ProjectAvailability> {
  const response = await fetchStream("/ready", {
    signal,
    headers: { Accept: "application/json" },
  })
  const result = await response.json()
  if (response.status === 200 && result?.status === "ready") return "ready"
  if (response.status !== 503 || result?.status !== "not_ready")
    return "unreadable"
  switch (result.reason) {
    case "configuration_required":
      return "configuration-required"
    case "dispatcher_unavailable":
      return "dispatcher-unavailable"
    case "database_unavailable":
      return "database-unavailable"
    default:
      return "unreadable"
  }
}

export async function readProjectAvailability(signal?: AbortSignal): Promise<{
  state: ProjectAvailability
  missingTasks: string[]
}> {
  const result = (state: ProjectAvailability, missingTasks: string[] = []) => ({
    state,
    missingTasks,
  })
  const [active, readiness] = await Promise.allSettled([
    readAppliedSettings(signal),
    readReadiness(signal),
  ])
  if (
    readiness.status === "fulfilled" &&
    readiness.value === "database-unavailable"
  )
    return result("database-unavailable")
  if (active.status === "rejected" || readiness.status === "rejected") {
    const failures = [active, readiness].filter(
      (result) => result.status === "rejected"
    )
    return result(
      failures.some((failure) => failure.reason instanceof TypeError)
        ? "transport-failure"
        : "unreadable"
    )
  }
  if (readiness.value === "unreadable") return result("unreadable")
  if (active.value === null) {
    return readiness.value === "configuration-required"
      ? result("configuration-required", ["bootstrap", "reason", "explore"])
      : result("unreadable")
  }
  const coverage = new Set(
    active.value.workers.flatMap((worker) => worker.task_types)
  )
  const missingTasks = ["bootstrap", "reason", "explore"].filter(
    (task) => !coverage.has(task)
  )
  if (missingTasks.length) return result("configuration-required", missingTasks)
  return result(
    readiness.value === "configuration-required"
      ? "unreadable"
      : readiness.value
  )
}
