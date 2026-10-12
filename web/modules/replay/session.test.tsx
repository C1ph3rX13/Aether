import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { useReplaySession } from "./session"
import { projectDetailFromWire } from "@/modules/workspace/model"

const snapshot = (revision: number) => ({
  project: {
    id: "proj_test",
    title: "Replay",
    status: "active" as const,
    generation: 1,
    revision,
    bootstrap_enabled: false,
    created_at: "2026-01-01T00:00:00Z",
  },
  facts: [
    {
      id: "origin",
      description: `Frame ${revision}`,
      status: "active" as const,
    },
  ],
  intents: [],
  hints: [],
  completion_records: [],
})
function mount() {
  let latest = 3
  let failRevision = 0
  let failLatest = false
  const reads: string[] = []
  vi.stubGlobal("fetch", async (url: string) => {
    reads.push(url)
    const frame = Number(/history\/(\d+)/.exec(url)?.[1])
    if (
      (frame && frame === failRevision) ||
      (!frame && !url.includes("/log") && failLatest)
    )
      return Response.json({}, { status: 503 })
    if (url.includes("/log")) {
      const before = Number(
        new URL(url, "http://localhost").searchParams.get("before_revision")
      )
      return Response.json({
        project_id: "proj_test",
        available_from_revision: 1,
        available_from_occurred_at: "2026-01-01T00:00:00Z",
        current_revision: latest,
        current_revision_occurred_at: "2026-01-01T00:00:00Z",
        revisions: Array.from(
          { length: Math.min(before - 1, latest) },
          (_, index) => ({
            revision: index + 1,
            generation: 1,
            occurred_at: "2026-01-01T00:00:00Z",
            source: { kind: "operator", name: "Operator" },
            events: [],
          })
        ).reverse(),
      })
    }
    return Response.json(snapshot(frame || latest))
  })
  const hook = renderHook(() =>
    useReplaySession("proj_test", projectDetailFromWire(snapshot(3)))
  )
  return {
    ...hook,
    reads,
    update: (revision: number) => {
      latest = revision
    },
    failFrame: (revision: number) => {
      failRevision = revision
    },
    failCurrent: (value: boolean) => {
      failLatest = value
    },
  }
}
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it("freezes authoritative frames and Log while live Revisions accumulate separately", async () => {
  const hook = mount()
  await act(() => hook.result.current.start())
  expect(hook.result.current.display?.facts[0].description).toBe("Frame 1")
  expect(hook.result.current.state).toMatchObject({
    fromRevision: 1,
    toRevision: 3,
    playing: true,
  })
  act(() => hook.result.current.actions.selectFrame(1))
  hook.update(5)
  act(() => hook.result.current.noteRevision(5))
  expect(hook.result.current.display?.project.revision).toBe(2)
  expect(hook.result.current.state).toMatchObject({
    playing: false,
    pendingCount: 2,
    toRevision: 3,
  })
  expect(
    hook.result.current.log?.revisions.map((group) => group.revision)
  ).toEqual([1, 2, 3])
  expect(hook.reads.filter((url) => url.includes("history/"))).toHaveLength(3)
  hook.unmount()
})

it("starts at the authoritative current Revision when the retained live projection is stale", async () => {
  const hook = mount()
  // The retained Workspace is R3; an unseen server update already reached R5.
  hook.update(5)
  await act(() => hook.result.current.start())
  expect(hook.result.current.state).toMatchObject({
    toRevision: 5,
    pendingCount: 0,
  })
  expect(hook.result.current.state.frameRevisions).toEqual([1, 2, 3, 4, 5])
})

it("retains cached reading on a failed start read and captures current data on explicit recovery", async () => {
  const hook = mount()
  hook.failCurrent(true)
  await act(() => hook.result.current.start())
  expect(hook.result.current.error).toContain("Retry Replay")
  expect(hook.result.current.display?.project.revision).toBe(3)
  hook.update(5)
  hook.failCurrent(false)
  await act(() => hook.result.current.start())
  expect(hook.result.current.state).toMatchObject({
    toRevision: 5,
    playing: true,
  })
})

it("preserves the last frozen frame on both current-read and Snapshot restart failures", async () => {
  const hook = mount()
  await act(() => hook.result.current.start())
  act(() => hook.result.current.actions.selectFrame(2))
  hook.update(5)
  act(() => hook.result.current.noteRevision(5))
  hook.failCurrent(true)
  await act(() => hook.result.current.actions.restart())
  expect(hook.result.current.display?.project.revision).toBe(3)
  expect(hook.result.current.error).toContain("frozen view remains readable")
  hook.failCurrent(false)
  hook.failFrame(4)
  await act(() => hook.result.current.actions.restart())
  expect(hook.result.current.display?.project.revision).toBe(3)
  expect(hook.result.current.state).toMatchObject({
    toRevision: 3,
    pendingCount: 2,
    playing: false,
  })
  expect(hook.result.current.log?.currentRevision).toBe(3)
  hook.failFrame(0)
  await act(() => hook.result.current.actions.restart())
  expect(hook.result.current.error).toBeNull()
  expect(hook.result.current.state).toMatchObject({
    fromRevision: 1,
    toRevision: 5,
    pendingCount: 0,
  })
  expect(hook.result.current.display?.project.revision).toBe(1)
})

it("retries a failed initial Snapshot against the original frozen boundary", async () => {
  const hook = mount()
  hook.failFrame(2)
  await act(() => hook.result.current.start())
  expect(hook.result.current.display?.project.revision).toBe(3)
  expect(hook.result.current.error).toContain("Retry Replay")
  hook.update(5)
  act(() => hook.result.current.noteRevision(5))
  hook.failFrame(0)
  await act(() => hook.result.current.start())
  expect(hook.result.current.state).toMatchObject({
    toRevision: 3,
    pendingCount: 2,
  })
})

it("manual selection pauses playback, the final frame stays historical, and Play starts again", async () => {
  const hook = mount()
  await act(() => hook.result.current.start())
  act(() => hook.result.current.actions.togglePlayback())
  vi.useFakeTimers()
  act(() => hook.result.current.actions.togglePlayback())
  await act(() => vi.advanceTimersByTimeAsync(1100))
  expect(hook.result.current.display?.project.revision).toBe(2)
  act(() => hook.result.current.actions.previous())
  await act(() => vi.advanceTimersByTimeAsync(5000))
  expect(hook.result.current.display?.project.revision).toBe(1)
  act(() => hook.result.current.actions.selectFrame(2))
  expect(hook.result.current.state).toMatchObject({
    active: true,
    playing: false,
  })
  act(() => hook.result.current.actions.togglePlayback())
  expect(hook.result.current.display?.project.revision).toBe(1)
  await act(() => vi.advanceTimersByTimeAsync(1100))
  await act(() => vi.advanceTimersByTimeAsync(1100))
  expect(hook.result.current.state).toMatchObject({
    active: true,
    playing: false,
    frameIndex: 2,
  })
})

it("late reads after Exit or a newer Replay cannot replace the active window", async () => {
  const hook = mount()
  await act(() => hook.result.current.start())
  hook.update(4)
  const realFetch = fetch
  let release!: () => void
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    if (url.includes("/history/4/")) await held
    return realFetch(url, init)
  })
  let old!: Promise<void>
  act(() => {
    old = hook.result.current.actions.restart()
  })
  await waitFor(() => expect(hook.result.current.state.restarting).toBe(true))
  act(() => hook.result.current.actions.exit())
  vi.stubGlobal("fetch", realFetch)
  await act(() => hook.result.current.start())
  await act(async () => {
    release()
    await old
  })
  expect(hook.result.current.state).toMatchObject({
    active: true,
    toRevision: 4,
  })
  expect(hook.result.current.display?.project.revision).toBe(1)
})
