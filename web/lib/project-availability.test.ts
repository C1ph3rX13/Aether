import { afterEach, expect, it, vi } from "vitest"
import { readProjectAvailability } from "@/lib/project-availability"
import { configuration } from "@/test-support/configuration"

afterEach(() => vi.unstubAllGlobals())

it.each([
  [[], ["bootstrap", "reason", "explore"]],
  [["reason"], ["bootstrap", "explore"]],
])(
  "blocks creation for incomplete coverage even while execution is ready",
  async (tasks, missing) => {
    const draft = configuration().draft
    draft.workers = tasks.length
      ? [{ ...draft.workers[0], task_types: tasks }]
      : []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) =>
        Response.json(
          path.endsWith("/ready")
            ? { status: "ready" }
            : { configuration: draft }
        )
      )
    )
    expect(await readProjectAvailability()).toEqual({
      state: "configuration-required",
      missingTasks: missing,
    })
  }
)

it("restores creation from the current saved coverage without requiring execution online", async () => {
  const draft = configuration().draft
  draft.workers[0].task_types = ["reason"]
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      path.endsWith("/ready")
        ? Response.json(
            { status: "not_ready", reason: "dispatcher_unavailable" },
            { status: 503 }
          )
        : Response.json({ configuration: draft })
    )
  )
  expect((await readProjectAvailability()).missingTasks).toEqual([
    "bootstrap",
    "explore",
  ])
  draft.workers[0].task_types = ["bootstrap", "reason", "explore"]
  expect(await readProjectAvailability()).toEqual({
    state: "dispatcher-unavailable",
    missingTasks: [],
  })
})
