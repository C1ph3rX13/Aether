import { useCallback, useMemo, useRef, useState } from "react"
import {
  deleteProject,
  renameProject,
  reopenProject,
  setProjectStatus,
} from "@/lib/project-catalog"
import { completeProject, createHint, retryExhaustedWork } from "./api"
import type { ProjectCommandAvailability } from "@/modules/workspace/command-availability"
import {
  executeProjectCommand,
  type ProjectCommandAuthority,
  type ProjectCommandReceipt,
} from "./gateway"

/** One pending write across the entire Project Workspace. */
export function useProjectCommands(
  projectId: string,
  authority: ProjectCommandAuthority
) {
  const inFlight = useRef(false)
  const [pending, setPending] = useState(false)
  const execute = useCallback(
    async <T extends ProjectCommandReceipt>(
      action: keyof ProjectCommandAvailability,
      command: () => Promise<T>
    ): Promise<T> => {
      if (inFlight.current)
        throw new Error(
          "A Project action is already in progress. Wait for it to finish before retrying."
        )
      inFlight.current = true
      setPending(true)
      try {
        return await executeProjectCommand(authority, action, command)
      } finally {
        inFlight.current = false
        setPending(false)
      }
    },
    [authority]
  )
  return useMemo(
    () => ({
      pending,
      retryExhaustedWork: (requestId: string, revision: number) =>
        execute("retryExhaustedWork", () =>
          retryExhaustedWork(projectId, requestId, revision)
        ),
      completeProject: (
        from: string[],
        description: string,
        revision: number
      ) =>
        execute("completeProject", () =>
          completeProject(projectId, from, description, revision)
        ),
      createHint: (content: string, revision: number) =>
        execute("addHint", () => createHint(projectId, content, revision)),
      deleteProject: (title: string, revision: number) =>
        execute("deleteProject", () =>
          deleteProject(projectId, title, revision)
        ),
      reopenProject: (feedback: string, revision: number) =>
        execute("reopenProject", () =>
          reopenProject(projectId, feedback, revision)
        ),
      updateProjectStatus: (status: "active" | "paused", revision: number) =>
        execute(status === "paused" ? "pauseProject" : "resumeProject", () =>
          setProjectStatus(projectId, status, revision)
        ),
      updateProjectTitle: (title: string, revision: number) =>
        execute("renameProject", () =>
          renameProject(projectId, title, revision)
        ),
    }),
    [execute, pending, projectId]
  )
}
