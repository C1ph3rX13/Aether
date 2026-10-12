import { afterEach, describe, expect, it, vi } from "vitest"
import { loadReplayWindow } from "./api"

function snapshot(revision: number) {
  return new Response(
    JSON.stringify({
      project: { id: "proj-1", revision },
      facts: [{ id: "origin", description: `Evidence at R${revision}` }],
      intents: [],
      hints: [],
    })
  )
}

function pendingSnapshots() {
  const pending = new Map<
    number,
    { signal: AbortSignal; resolve: (response: Response) => void }
  >()
  const fetcher = vi.fn(
    (url: string, init: RequestInit) =>
      new Promise<Response>((resolve, reject) => {
        const revision = Number(/\/history\/(\d+)\//.exec(url)?.[1])
        const signal = init.signal!
        pending.set(revision, { signal, resolve })
        signal.addEventListener("abort", () => reject(signal.reason), {
          once: true,
        })
      })
  )
  vi.stubGlobal("fetch", fetcher)
  return { pending, fetcher }
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
describe("Replay history requests", () => {
  it("bounds parallel reads and keeps the frozen frames ordered when responses arrive out of order", async () => {
    const { pending, fetcher } = pendingSnapshots()
    const request = loadReplayWindow("proj-1", 8, 13)
    expect([...pending.keys()]).toEqual([8, 9, 10, 11])
    pending.get(11)!.resolve(snapshot(11))
    await vi.waitFor(() => expect(pending.has(12)).toBe(true))
    pending.get(12)!.resolve(snapshot(12))
    await vi.waitFor(() => expect(pending.has(13)).toBe(true))
    for (const revision of [13, 10, 9, 8])
      pending.get(revision)!.resolve(snapshot(revision))
    const frames = await request
    expect(frames.map((frame) => frame.revision)).toEqual([
      8, 9, 10, 11, 12, 13,
    ])
    expect(frames.map((frame) => frame.project.facts[0].description)).toEqual(
      [8, 9, 10, 11, 12, 13].map((revision) => `Evidence at R${revision}`)
    )
    expect(fetcher).toHaveBeenCalledTimes(6)
  })

  it.each([1, 2])(
    "reads a small window of %s frames exactly once",
    async (count) => {
      const { pending, fetcher } = pendingSnapshots()
      const request = loadReplayWindow("proj-1", 4, 3 + count)
      expect(pending.size).toBe(count)
      for (const [revision, read] of pending) read.resolve(snapshot(revision))
      expect((await request).map((frame) => frame.revision)).toEqual(
        Array.from({ length: count }, (_, index) => 4 + index)
      )
      expect(fetcher).toHaveBeenCalledTimes(count)
    }
  )

  it("cancels every in-flight read and leaves queued revisions unfetched", async () => {
    const { pending, fetcher } = pendingSnapshots()
    const controller = new AbortController()
    const request = loadReplayWindow("proj-1", 1, 10, controller.signal)
    const failure = expect(request).rejects.toMatchObject({
      name: "AbortError",
    })
    expect(pending.size).toBe(4)
    controller.abort()
    await failure
    expect([...pending.values()].every((read) => read.signal.aborted)).toBe(
      true
    )
    expect(fetcher).toHaveBeenCalledTimes(4)
  })

  it.each(["HTTP error", "Revision mismatch", "timeout"] as const)(
    "aborts sibling reads and preserves the first %s failure",
    async (kind) => {
      const deadline = new AbortController()
      vi.spyOn(AbortSignal, "timeout").mockReturnValueOnce(deadline.signal)
      const { pending, fetcher } = pendingSnapshots()
      const request = loadReplayWindow("proj-1", 1, 10)
      const failure = expect(request).rejects.toThrow(
        kind === "HTTP error"
          ? "Snapshot unavailable"
          : kind === "Revision mismatch"
            ? "Revision mismatch at Revision 1"
            : "Check your connection and retry"
      )
      if (kind === "HTTP error")
        pending.get(1)!.resolve(
          new Response(JSON.stringify({ detail: "Snapshot unavailable" }), {
            status: 503,
          })
        )
      else if (kind === "Revision mismatch")
        pending.get(1)!.resolve(snapshot(9))
      else deadline.abort(new DOMException("Read timed out", "TimeoutError"))
      await failure
      expect([...pending.values()].every((read) => read.signal.aborted)).toBe(
        true
      )
      expect(fetcher).toHaveBeenCalledTimes(4)
    }
  )

  it.each([
    [0, 3],
    [3, 2],
    [1.5, 3],
  ])("rejects invalid windows %s–%s before fetching", async (from, to) => {
    const fetcher = vi.fn()
    vi.stubGlobal("fetch", fetcher)
    await expect(loadReplayWindow("proj-1", from, to)).rejects.toThrow(
      "invalid"
    )
    expect(fetcher).not.toHaveBeenCalled()
  })
  it("stops before the first request if cancellation already happened", async () => {
    const controller = new AbortController()
    controller.abort()
    const fetcher = vi.fn()
    vi.stubGlobal("fetch", fetcher)
    await expect(
      loadReplayWindow("proj-1", 1, 3, controller.signal)
    ).rejects.toMatchObject({ name: "AbortError" })
    expect(fetcher).not.toHaveBeenCalled()
  })
  it("rejects a snapshot belonging to a different revision", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              project: { revision: 9 },
              facts: [],
              intents: [],
              hints: [],
            })
          )
      )
    )
    await expect(loadReplayWindow("proj-1", 1, 3)).rejects.toThrow(
      "Revision mismatch"
    )
  })
})
