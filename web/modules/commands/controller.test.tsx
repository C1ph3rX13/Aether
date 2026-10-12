import { act, renderHook } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { useProjectCommands } from "./controller"
import type { ProjectMeta } from "@/modules/workspace/model"

afterEach(() => vi.unstubAllGlobals())

it.each([false, true])(
  "serializes Project writes and releases the guard after failure=%s",
  async (fails) => {
    let finish!: (response: Response) => void
    const fetch = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve
        })
    )
    vi.stubGlobal("fetch", fetch)
    const markCommandCommitted = vi.fn()
    const project: ProjectMeta = {
      id: "proj-1",
      title: "Plan",
      status: "active",
      revision: 3,
      generation: 1,
      bootstrapEnabled: false,
      createdAt: "2026-01-01T00:00:00Z",
    }
    const { result, rerender } = renderHook(
      ({ project }) =>
        useProjectCommands("proj-1", {
          project,
          freshness: "live",
          mode: "live",
          markCommandCommitted,
        }),
      { initialProps: { project } }
    )
    let first!: Promise<unknown>
    await act(async () => {
      first = result.current.updateProjectStatus("paused", 3)
      const failure = first.catch(() => undefined)
      await expect(result.current.createHint("Do not race", 3)).rejects.toThrow(
        "already in progress"
      )
      void failure
    })
    expect(fetch).toHaveBeenCalledOnce()
    expect(result.current.pending).toBe(true)
    await act(async () => {
      finish(
        new Response(
          JSON.stringify(
            fails
              ? { detail: "Unavailable" }
              : {
                  project: {
                    id: "proj-1",
                    title: "Plan",
                    status: "paused",
                    revision: 4,
                  },
                  project_revision: 4,
                }
          ),
          { status: fails ? 503 : 200 }
        )
      )
      await first.catch(() => undefined)
    })
    expect(result.current.pending).toBe(false)
    expect(markCommandCommitted).toHaveBeenCalledTimes(fails ? 0 : 1)
    rerender({ project: { ...project, status: "paused", revision: 4 } })
    fetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          project: {
            id: "proj-1",
            title: "Plan",
            status: "active",
            revision: 5,
          },
          project_revision: 5,
        })
      )
    )
    await act(async () => {
      await result.current.updateProjectStatus("active", 4)
    })
    expect(fetch).toHaveBeenCalledTimes(2)
  }
)
