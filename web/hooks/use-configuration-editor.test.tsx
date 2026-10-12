import {
  act,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import Link from "next/link"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import {
  useConfigurationEditor,
  type ConfigurationFormAdapter,
} from "@/hooks/use-configuration-editor"
import { configuration } from "@/test-support/configuration"

const { push } = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
function adapter(): ConfigurationFormAdapter<{ limit: string }> {
  return {
    read: (read) => {
      const form = { limit: String(read.draft.runtime.max_workers) }
      return { form, baselineForm: form }
    },
    validate: () => [],
    buildSave: (read, form) => ({
      expected_draft_version: read.draft_version,
      draft: {
        ...read.draft,
        runtime: { ...read.draft.runtime, max_workers: Number(form.limit) },
      },
    }),
  }
}
function wrapper({ children }: { children: ReactNode }) {
  return (
    <NavigationGuardProvider>
      <Link href="/projects">Projects</Link>
      {children}
    </NavigationGuardProvider>
  )
}
function traverse() {
  return Object.assign(new Event("navigate", { cancelable: true }), {
    navigationType: "traverse",
    destination: {
      sameDocument: true,
      key: "earlier",
      url: "http://localhost/projects",
    },
  })
}
function unload() {
  const event = new Event("beforeunload", { cancelable: true })
  window.dispatchEvent(event)
  return event.defaultPrevented
}
beforeEach(() => {
  push.mockReset()
  vi.stubGlobal(
    "navigation",
    Object.assign(new EventTarget(), { traverseTo: vi.fn() })
  )
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => json(configuration()))
  )
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it("cancels superseded and unmounted reads and only adopts the latest response", async () => {
  const first = deferred<Response>(),
    second = deferred<Response>()
  const signals: AbortSignal[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init) => {
      signals.push(init.signal)
      return signals.length === 1 ? first.promise : second.promise
    })
  )
  const fields = adapter()
  const { result, unmount } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(signals).toHaveLength(1))
  let reload!: ReturnType<typeof result.current.load>
  act(() => {
    reload = result.current.load()
  })
  expect(signals[0].aborted).toBe(true)
  const latest = configuration()
  latest.draft_version = 20
  latest.draft.runtime.max_workers = 25
  await act(async () => {
    second.resolve(json(latest))
    await reload
  })
  await act(async () => {
    first.resolve(json(configuration()))
    await first.promise
  })
  expect(result.current.form?.limit).toBe("25")
  expect(result.current.baseline?.draft_version).toBe(20)
  unmount()
  expect(signals[1].aborted).toBe(true)
})

it("preserves conflicted edits through failed reload and only replaces the baseline after successful reload", async () => {
  const latest = configuration()
  latest.draft_version = 20
  latest.draft.runtime.max_workers = 25
  let reads = 0
  const fetch = vi.fn(async (_url, init) => {
    if (init?.method === "PUT")
      return json({ code: "conflict", current_draft_version: 20 }, 409)
    reads++
    return reads === 1
      ? json(configuration())
      : reads === 2
        ? json({}, 503)
        : json(latest)
  })
  vi.stubGlobal("fetch", fetch)
  const fields = adapter()
  const { result } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => result.current.change(() => ({ limit: "17" })))
  await act(async () => {
    expect(await result.current.save()).toBe(false)
    await result.current.load()
  })
  expect(result.current.loadFailed).toBe(true)
  expect(result.current.form?.limit).toBe("17")
  expect(result.current.baseline?.draft_version).toBe(11)
  expect(result.current.conflict).toBe(true)
  await act(async () => {
    expect(await result.current.save()).toBe(false)
  })
  expect(
    fetch.mock.calls.filter(([, init]) => init?.method === "PUT")
  ).toHaveLength(1)
  await act(async () => {
    await result.current.load()
  })
  expect(result.current.form?.limit).toBe("25")
  expect(result.current.baseline?.draft_version).toBe(20)
  expect(result.current.conflict).toBe(false)
  act(() => {
    result.current.change(() => ({ limit: "99" }))
  })
  act(() => result.current.discard())
  expect(result.current.form?.limit).toBe("25")
  expect(reads).toBe(3)
})

it("isolates page-local forms and submits complete drafts with each page's captured CAS version", async () => {
  let server = configuration()
  const submitted: ReturnType<ReturnType<typeof adapter>["buildSave"]>[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method !== "PUT") return json(server)
      const request = JSON.parse(String(init.body))
      submitted.push(request)
      if (request.expected_draft_version !== server.draft_version)
        return json({ code: "conflict" }, 409)
      server = {
        ...server,
        draft: request.draft,
        draft_version: server.draft_version + 1,
      }
      return json(server)
    })
  )
  const fields = adapter()
  const first = renderHook(() => useConfigurationEditor(fields), { wrapper })
  const second = renderHook(() => useConfigurationEditor(fields), { wrapper })
  await waitFor(() =>
    expect(first.result.current.loading || second.result.current.loading).toBe(
      false
    )
  )
  act(() => {
    first.result.current.change(() => ({ limit: "17" }))
    second.result.current.change(() => ({ limit: "19" }))
  })
  await act(async () => {
    expect(await first.result.current.save()).toBe(true)
  })
  expect(second.result.current.form?.limit).toBe("19")
  expect(second.result.current.baseline?.draft_version).toBe(11)
  await act(async () => {
    expect(await second.result.current.save()).toBe(false)
  })
  expect(second.result.current.conflict).toBe(true)
  expect(submitted.map((request) => request.expected_draft_version)).toEqual([
    11, 11,
  ])
  expect(submitted.map((request) => request.draft.runtime.max_workers)).toEqual(
    [17, 19]
  )
  expect(submitted[0].draft.tasks).toEqual(configuration().draft.tasks)
  expect(submitted[0].draft.workers).toEqual(configuration().draft.workers)
})

it("serializes writes and freezes reload, edits and discard while saving", async () => {
  const response = deferred<Response>()
  const fetch = vi.fn(async (_url, init) =>
    init?.method === "PUT" ? response.promise : json(configuration())
  )
  vi.stubGlobal("fetch", fetch)
  const fields = adapter()
  const { result } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => result.current.change(() => ({ limit: "17" })))
  let saving!: Promise<boolean>
  await act(async () => {
    saving = result.current.save()
    expect(await result.current.save()).toBe(false)
    await result.current.load()
    result.current.change(() => ({ limit: "99" }))
    result.current.discard()
  })
  expect(result.current.saving).toBe(true)
  expect(result.current.form?.limit).toBe("17")
  expect(fetch).toHaveBeenCalledTimes(2)
  await act(async () => {
    const saved = configuration()
    saved.draft_version = 12
    saved.draft.runtime.max_workers = 17
    response.resolve(json(saved))
    expect(await saving).toBe(true)
  })
  expect(result.current.saved).toBe(true)
  expect(result.current.dirty).toBe(false)
})

it("does not project a save response or report success after its page unmounts", async () => {
  const response = deferred<Response>()
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) =>
      init?.method === "PUT" ? response.promise : json(configuration())
    )
  )
  const fields = adapter(),
    read = vi.spyOn(fields, "read")
  const { result, unmount } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => result.current.change(() => ({ limit: "17" })))
  let saving!: Promise<boolean>
  act(() => {
    saving = result.current.save()
  })
  unmount()
  response.resolve(json(configuration()))
  expect(await saving).toBe(false)
  expect(read).toHaveBeenCalledTimes(1)
})

it("allows history navigation and unload during a clean read", async () => {
  const response = deferred<Response>()
  vi.stubGlobal(
    "fetch",
    vi.fn(() => response.promise)
  )
  const fields = adapter()
  const { result } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  const event = traverse()
  window.navigation.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(false)
  expect(unload()).toBe(false)
  await act(async () => {
    response.resolve(json(configuration()))
    await response.promise
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
})

it("handles Cancel, failed Save, successful Save-and-leave and Discard through one guard", async () => {
  const user = userEvent.setup()
  let fail = true
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method !== "PUT") return json(configuration())
      return fail
        ? json({}, 503)
        : json({
            ...configuration(),
            draft: JSON.parse(String(init.body)).draft,
            draft_version: 12,
          })
    })
  )
  const fields = adapter()
  const { result } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => result.current.change(() => ({ limit: "17" })))
  expect(unload()).toBe(true)
  await user.click(screen.getByRole("link", { name: "Projects" }))
  await user.click(screen.getByRole("button", { name: "Cancel" }))
  expect(push).not.toHaveBeenCalled()
  await user.click(screen.getByRole("link", { name: "Projects" }))
  await user.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(result.current.form?.limit).toBe("17")
  expect(push).not.toHaveBeenCalled()
  fail = false
  await user.click(screen.getByRole("link", { name: "Projects" }))
  await user.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(push).toHaveBeenCalledWith("/projects"))
  act(() => result.current.change(() => ({ limit: "99" })))
  await user.click(screen.getByRole("link", { name: "Projects" }))
  await user.click(screen.getByRole("button", { name: "Discard" }))
  expect(result.current.form?.limit).toBe("17")
  expect(unload()).toBe(false)
})

it("preserves history entries through Cancel and resumes the original destination after Discard", async () => {
  const user = userEvent.setup()
  const fields = adapter()
  const { result } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => result.current.change(() => ({ limit: "17" })))
  const first = traverse()
  act(() => {
    window.navigation.dispatchEvent(first)
  })
  expect(first.defaultPrevented).toBe(true)
  await user.click(screen.getByRole("button", { name: "Cancel" }))
  expect(window.navigation.traverseTo).not.toHaveBeenCalled()
  const second = traverse()
  act(() => {
    window.navigation.dispatchEvent(second)
  })
  await user.click(screen.getByRole("button", { name: "Discard" }))
  expect(window.navigation.traverseTo).toHaveBeenCalledWith("earlier")
  expect(push).not.toHaveBeenCalled()
  expect(result.current.dirty).toBe(false)
})

it("cancels a dirty page's pending reload when Discard-and-leave is chosen", async () => {
  const user = userEvent.setup()
  const response = deferred<Response>()
  let reads = 0
  let reloadSignal!: AbortSignal
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      reads++
      if (reads === 1) return json(configuration())
      reloadSignal = init.signal
      return response.promise
    })
  )
  const fields = adapter()
  const { result } = renderHook(() => useConfigurationEditor(fields), {
    wrapper,
  })
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => result.current.change(() => ({ limit: "17" })))
  let reload!: ReturnType<typeof result.current.load>
  act(() => {
    reload = result.current.load()
  })
  await user.click(screen.getByRole("link", { name: "Projects" }))
  await user.click(screen.getByRole("button", { name: "Discard" }))
  expect(reloadSignal.aborted).toBe(true)
  expect(push).toHaveBeenCalledWith("/projects")
  const latest = configuration()
  latest.draft_version = 25
  await act(async () => {
    response.resolve(json(latest))
    await reload
  })
  expect(result.current.baseline?.draft_version).toBe(11)
  expect(result.current.form?.limit).toBe("9")
})
