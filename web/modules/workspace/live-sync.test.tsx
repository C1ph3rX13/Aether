import { act, renderHook, waitFor } from "@testing-library/react"
import { SWRConfig } from "swr"
import type { ReactNode } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { useProjectWorkspace } from "./api"
import { useProjectLiveSync } from "./live-sync"

class Stream {
  static current: Stream
  onmessage: ((event: { data: string }) => void) | null = null
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  close = vi.fn()
  constructor(public url: string) {
    Stream.current = this
  }
  send(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) })
  }
}
function wrapper({ children }: { children: ReactNode }) {
  return (
    <SWRConfig value={{ provider: () => new Map(), shouldRetryOnError: false }}>
      {children}
    </SWRConfig>
  )
}
function mount() {
  let revision = 8
  let failed = false
  vi.stubGlobal("EventSource", Stream)
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      failed
        ? Response.json({}, { status: 503 })
        : new Response(
            JSON.stringify({
              project: {
                id: "proj one",
                title: "Launch",
                status: "active",
                revision,
                generation: 1,
                bootstrap_enabled: false,
                created_at: "2026-01-01T00:00:00Z",
              },
              facts: [],
              intents: [],
              hints: [],
              completion_records: [],
            })
          )
    )
  )
  const hook = renderHook(
    () => {
      const query = useProjectWorkspace("proj one")
      return useProjectLiveSync(
        "proj one",
        query.data?.project.revision ?? null
      )
    },
    { wrapper }
  )
  return {
    ...hook,
    setRevision: (value: number) => {
      revision = value
    },
    failReads: (value: boolean) => {
      failed = value
    },
  }
}
const synced = (revision = 8) => ({
  type: "sync_complete",
  project_id: "proj one",
  data: { current_revision: revision },
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("current workspace live synchronization", () => {
  it("waits for synchronization and refreshes an encoded Project before becoming Live", async () => {
    const hook = mount()
    await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
    expect(Stream.current.url).toBe(
      "/api/v1/projects/proj%20one/events?after_revision=8"
    )
    act(() => Stream.current.onopen?.())
    expect(hook.result.current.status).toBe("connecting")
    act(() => Stream.current.send({ ...synced(), project_id: "another" }))
    expect(hook.result.current.syncComplete).toBe(false)
    hook.setRevision(9)
    act(() => Stream.current.send(synced(9)))
    await waitFor(() =>
      expect(hook.result.current).toMatchObject({
        status: "live",
        projectionRevision: 9,
        streamRevision: 9,
      })
    )
    const stream = Stream.current
    hook.unmount()
    expect(stream.close).toHaveBeenCalled()
  })
  it("marks malformed events stale and requires a fresh synchronization after reconnecting", async () => {
    const hook = mount()
    await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
    act(() => Stream.current.send(synced()))
    await waitFor(() => expect(hook.result.current.status).toBe("live"))
    vi.useFakeTimers()
    const previous = Stream.current
    act(() => previous.onmessage?.({ data: "malformed" }))
    expect(hook.result.current.status).toBe("stale")
    expect(previous.close).toHaveBeenCalled()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(Stream.current).not.toBe(previous)
    expect(hook.result.current.status).toBe("reconnecting")
    act(() => Stream.current.send(synced()))
    expect(hook.result.current.status).toBe("live")
  })
  it("recovers a Revision gap through an authoritative read and requires confirmation on the new stream", async () => {
    const hook = mount()
    await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
    act(() => Stream.current.send(synced()))
    await waitFor(() => expect(hook.result.current.status).toBe("live"))
    const previous = Stream.current
    hook.setRevision(10)
    act(() =>
      previous.send({
        type: "hint_created",
        project_id: "proj one",
        revision: 10,
      })
    )
    expect(hook.result.current.status).not.toBe("live")
    await waitFor(() => expect(Stream.current).not.toBe(previous))
    expect(previous.close).toHaveBeenCalled()
    expect(Stream.current.url).toBe(
      "/api/v1/projects/proj%20one/events?after_revision=10"
    )
    expect(hook.result.current).toMatchObject({
      projectionRevision: 10,
      syncComplete: false,
    })
    act(() => Stream.current.onopen?.())
    expect(hook.result.current.status).not.toBe("live")
    act(() => Stream.current.send(synced(10)))
    await waitFor(() => expect(hook.result.current.status).toBe("live"))
  })
  it.each([
    null,
    [],
    {},
    {
      type: "sync_complete",
      project_id: "proj one",
      data: { current_revision: "9" },
    },
  ])(
    "keeps malformed synchronization evidence read-only until canonical recovery: %j",
    async (evidence) => {
      const hook = mount()
      await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
      act(() => Stream.current.send(synced()))
      await waitFor(() => expect(hook.result.current.status).toBe("live"))
      const previous = Stream.current
      act(() => previous.send(evidence))
      expect(hook.result.current.status).toBe("stale")
      // Late events from a discarded connection cannot restore eligibility.
      act(() => previous.send(synced()))
      expect(hook.result.current.status).toBe("stale")
      hook.setRevision(9)
      await act(() => hook.result.current.refresh())
      expect(Stream.current).not.toBe(previous)
      expect(hook.result.current.status).not.toBe("live")
      act(() => Stream.current.send(synced(9)))
      await waitFor(() => expect(hook.result.current.status).toBe("live"))
    }
  )
  it("retains read-only projection evidence after a replay-required read fails and recovers explicitly", async () => {
    const hook = mount()
    await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
    act(() => Stream.current.send(synced()))
    await waitFor(() => expect(hook.result.current.status).toBe("live"))
    hook.setRevision(10)
    hook.failReads(true)
    act(() =>
      Stream.current.send({
        type: "replay_required",
        project_id: "proj one",
        data: { current_revision: 10 },
      })
    )
    await waitFor(() => expect(hook.result.current.status).toBe("stale"))
    expect(hook.result.current.projectionRevision).toBe(8)
    hook.failReads(false)
    await act(() => hook.result.current.refresh())
    expect(hook.result.current.projectionRevision).toBe(10)
    expect(hook.result.current.syncComplete).toBe(false)
    act(() => Stream.current.send(synced(10)))
    await waitFor(() => expect(hook.result.current.status).toBe("live"))
  })
})
