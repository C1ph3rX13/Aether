import { act, renderHook, waitFor } from "@testing-library/react"
import { SWRConfig } from "swr"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { useWorkspaceSession } from "./session"

afterEach(() => vi.unstubAllGlobals())

it("keeps inspection independent of eligible Completion Evidence and prunes refreshed retractions", async () => {
  let retracted = false
  vi.stubGlobal(
    "EventSource",
    class {
      close() {}
    }
  )
  vi.stubGlobal(
    "fetch",
    async (input: string) =>
      new Response(
        JSON.stringify(
          String(input).includes("execution-evidence")
            ? { project_id: "proj_test" }
            : String(input).includes("/log")
              ? {
                  project_id: "proj_test",
                  available_from_revision: 1,
                  current_revision: 2,
                  available_from_occurred_at: "2026-01-01T00:00:00Z",
                  current_revision_occurred_at: "2026-01-01T00:00:00Z",
                  revisions: [2, 1].map((revision) => ({
                    revision,
                    generation: 1,
                    occurred_at: "2026-01-01T00:00:00Z",
                    source: { kind: "operator", name: "Operator" },
                    events: [],
                  })),
                }
              : {
                  project: {
                    id: "proj_test",
                    title: "Selection",
                    status: "active",
                    generation: 1,
                    revision: String(input).includes("/history/")
                      ? Number(/history\/(\d+)/.exec(String(input))?.[1])
                      : retracted
                        ? 2
                        : 1,
                    bootstrap_enabled: false,
                    created_at: "2026-01-01T00:00:00Z",
                  },
                  facts: [
                    { id: "origin", status: "active", description: "Origin" },
                    { id: "goal", status: "active", description: "Goal" },
                    {
                      id: "f001",
                      status: retracted ? "retracted" : "active",
                      description: "Evidence",
                    },
                    {
                      id: "f002",
                      status: "retracted",
                      description: "Old evidence",
                    },
                  ],
                  intents: [],
                  hints: [],
                  completion_records: [],
                }
        )
      )
  )
  const hook = renderHook(() => useWorkspaceSession("proj_test"), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <SWRConfig
        value={{ provider: () => new Map(), shouldRetryOnError: false }}
      >
        {children}
      </SWRConfig>
    ),
  })
  await waitFor(() => expect(hook.result.current.status).toBe("ready"))
  act(() =>
    hook.result.current.actions.selectCompletionEvidence([
      "origin",
      "goal",
      "f001",
      "f001",
      "f002",
      "missing",
    ])
  )
  expect(hook.result.current.view?.completionFactIds).toEqual([
    "origin",
    "f001",
  ])
  act(() => hook.result.current.actions.inspect({ type: "fact", id: "goal" }))
  expect(hook.result.current.view?.selection?.type).toBe("fact")
  expect(hook.result.current.view?.completionFactIds).toEqual([
    "origin",
    "f001",
  ])
  retracted = true
  await act(() => hook.result.current.actions.retry())
  await waitFor(() =>
    expect(hook.result.current.view?.completionFactIds).toEqual(["origin"])
  )
  expect(hook.result.current.evidenceNotice).toContain(
    "1 selected Fact is no longer eligible"
  )
  expect(hook.result.current.view?.selection).toMatchObject({
    type: "fact",
    fact: { id: "goal" },
  })
  await act(() => hook.result.current.actions.startReplay())
  expect(hook.result.current.view?.completionFactIds).toEqual([])
  expect(hook.result.current.view?.freshness).toBe("replay")
  expect(hook.result.current.availability.addHint).toBe(false)
  act(() => hook.result.current.actions.replay.selectFrame(1))
  expect(hook.result.current.view?.detail.project.revision).toBe(2)
  expect(hook.result.current.view?.mode).toBe("replay")
  await expect(
    hook.result.current.commands.createHint("Historical write", 2)
  ).rejects.toMatchObject({ code: "projection_not_live" })
  act(() => hook.result.current.actions.selectCompletionEvidence(["origin"]))
  expect(hook.result.current.view?.completionFactIds).toEqual([])
  await act(() => hook.result.current.actions.replay.exit())
  expect(hook.result.current.view?.completionFactIds).toEqual([])
  expect(hook.result.current.view?.freshness).not.toBe("live")
})
