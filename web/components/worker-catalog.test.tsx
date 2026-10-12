import {
  act,
  fireEvent,
  render as renderUI,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import { WorkerCatalog } from "@/components/worker-catalog"
import { configuration } from "@/test-support/configuration"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
function render(ui: React.ReactNode) {
  vi.stubGlobal("navigation", new EventTarget())
  return renderUI(ui, { wrapper: NavigationGuardProvider })
}
afterEach(() => vi.unstubAllGlobals())

async function openDelete(user: ReturnType<typeof userEvent.setup>) {
  const trigger = await screen.findByRole("button", {
    name: "View worker-a configuration",
  })
  await user.click(trigger)
  await user.click(
    await screen.findByRole("button", { name: "Delete worker-a" })
  )
  return trigger
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status })

it("distinguishes loading and failed reads from an empty catalog, then recovers saved configuration", async () => {
  const user = userEvent.setup()
  const read = configuration()
  read.draft.workers[0].provider_name = "Controlled Provider"
  read.validation_problems = [
    { path: "workers[0].agent_env.CODEX_BASE_URL", message: "required" },
  ]
  let failed = true
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      failed ? json({ detail: "private-credential" }, 503) : json(read)
    )
  )
  render(<WorkerCatalog agentType="codex" />)
  expect(screen.getByRole("status", { name: "Loading Workers" })).toBeVisible()
  await screen.findByText("Unable to load Workers")
  expect(screen.queryByText("No Codex Workers")).not.toBeInTheDocument()
  expect(screen.queryByText("private-credential")).not.toBeInTheDocument()
  failed = false
  await user.click(screen.getByRole("button", { name: "Retry" }))
  await screen.findByRole("heading", { name: "worker-a" })
  expect(screen.queryByText(/Controlled Provider/)).not.toBeInTheDocument()
  expect(
    within(screen.getByRole("listitem", { name: "Worker worker-a" })).getByText(
      read.draft.workers[0].agent_env.CODEX_MODEL
    )
  ).toBeVisible()
  expect(screen.getByText("Setup required")).toBeVisible()
  expect(screen.getByText("Reason")).toBeVisible()
})

it("requires confirmation, sends the captured Draft version, and uses the authoritative deletion response", async () => {
  const user = userEvent.setup()
  const initial = configuration()
  const deleted: unknown[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      if (init?.method === "DELETE") {
        deleted.push({ url, body: JSON.parse(init.body) })
        return json({
          ...initial,
          draft_version: 12,
          draft: {
            ...initial.draft,
            workers: [
              { ...initial.draft.workers[0], name: "authoritative-worker" },
            ],
          },
        })
      }
      return json(initial)
    })
  )
  render(<WorkerCatalog agentType="codex" />)
  const trigger = await openDelete(user)
  const dialog = screen.getByRole("dialog", { name: "Delete worker-a?" })
  expect(
    within(dialog).getByText(/Existing Projects keep their bound configuration/)
  ).toBeVisible()
  expect(deleted).toEqual([])
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
  expect(trigger).toHaveFocus()
  await openDelete(user)
  await user.click(screen.getByRole("button", { name: "Delete Worker" }))
  await screen.findByRole("heading", { name: "authoritative-worker" })
  expect(
    screen.queryByRole("heading", { name: "worker-a" })
  ).not.toBeInTheDocument()
  expect(deleted).toEqual([
    {
      url: "/api/v1/configuration/workers/worker-a",
      body: { expected_draft_version: 11 },
    },
  ])
  expect(screen.getByRole("button", { name: "Refresh" })).toHaveFocus()
})

it("keeps Agent Types independent and uses full-Draft validation indices for saved readiness", async () => {
  const read = configuration()
  const worker = read.draft.workers[0]
  read.draft.workers = [
    {
      ...worker,
      name: "codex-ready",
      secrets: { OPENAI_API_KEY: { configured: true, mask: "********" } },
    },
    { ...worker, name: "pi-setup", agent_type: "pi", task_types: [] },
    {
      ...worker,
      name: "claude-ready",
      agent_type: "claude_code",
      provider_name: "Anthropic",
    },
  ]
  read.validation_problems = [
    { path: "workers[1].task_types", message: "required" },
    { path: "runtime.max_workers", message: "invalid" },
  ]
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => json(read))
  )
  const view = render(<WorkerCatalog agentType="pi" />)
  await screen.findByRole("heading", { name: "pi-setup" })
  expect(screen.getByText("Setup required")).toBeVisible()
  expect(screen.getByText("No tasks selected")).toBeVisible()
  expect(screen.queryByText("codex-ready")).not.toBeInTheDocument()
  view.rerender(<WorkerCatalog agentType="claude_code" />)
  expect(screen.getByText("Configured")).toBeVisible()
  expect(screen.queryByText(/Anthropic/)).not.toBeInTheDocument()
  await userEvent
    .setup()
    .click(
      screen.getByRole("button", { name: "View claude-ready configuration" })
    )
  expect(
    within(
      screen.getByRole("dialog", { name: "Worker configuration" })
    ).getByText("Anthropic")
  ).toBeVisible()
  await userEvent.setup().keyboard("{Escape}")
  view.rerender(<WorkerCatalog agentType="codex" />)
  expect(screen.getByText("Configured")).toBeVisible()
  expect(screen.queryByText("********")).not.toBeInTheDocument()
  read.draft.workers = []
  await userEvent.setup().click(screen.getByRole("button", { name: "Refresh" }))
  await screen.findByText("No Codex Workers")
})

it("holds confirmation while deleting and prevents duplicate or contradictory in-flight actions", async () => {
  const user = userEvent.setup()
  const initial = configuration()
  let finish!: (response: Response) => void
  let deletions = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method !== "DELETE") return json(initial)
      deletions++
      return new Promise<Response>((resolve) => {
        finish = resolve
      })
    })
  )
  render(<WorkerCatalog agentType="codex" />)
  await openDelete(user)
  const confirm = screen.getByRole("button", { name: "Delete Worker" })
  fireEvent.click(confirm)
  fireEvent.click(confirm)
  expect(screen.getByRole("button", { name: "Deleting…" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled()
  expect(
    screen.getByRole("button", { name: "Refresh", hidden: true })
  ).toBeDisabled()
  await user.keyboard("{Escape}")
  expect(screen.getByRole("dialog")).toBeVisible()
  expect(deletions).toBe(1)
  await act(async () =>
    finish(
      json({
        ...initial,
        draft_version: 12,
        draft: { ...initial.draft, workers: [] },
      })
    )
  )
  await screen.findByText("No Codex Workers")
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
})

it("preserves a conflicted deletion through failed reload and requires confirmation with the latest version", async () => {
  const user = userEvent.setup()
  const initial = configuration()
  const versions: number[] = []
  let readFails = false
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method === "DELETE") {
        versions.push(JSON.parse(init.body).expected_draft_version)
        if (versions.length === 1)
          return json({ code: "conflict", detail: "private-secret" }, 409)
        return json({
          ...initial,
          draft_version: 13,
          draft: { ...initial.draft, workers: [] },
        })
      }
      return readFails
        ? json({}, 503)
        : json({ ...initial, draft_version: versions.length ? 12 : 11 })
    })
  )
  render(<WorkerCatalog agentType="codex" />)
  await openDelete(user)
  await user.click(screen.getByRole("button", { name: "Delete Worker" }))
  await screen.findByText(/Worker settings changed elsewhere/)
  expect(screen.getByRole("button", { name: "Delete Worker" })).toBeDisabled()
  readFails = true
  await user.click(screen.getByRole("button", { name: "Reload list" }))
  await screen.findByText("Could not reload Workers. Retry Reload list.")
  expect(screen.getByRole("dialog")).toBeVisible()
  expect(screen.queryByText("No Codex Workers")).not.toBeInTheDocument()
  expect(screen.queryByText("private-secret")).not.toBeInTheDocument()
  expect(versions).toEqual([11])
  readFails = false
  await user.click(screen.getByRole("button", { name: "Reload list" }))
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  await openDelete(user)
  await user.click(screen.getByRole("button", { name: "Delete Worker" }))
  await screen.findByText("No Codex Workers")
  expect(versions).toEqual([11, 12])
})

it.each([400, 401, 503, "network"])(
  "keeps a failed deletion recoverable without exposing response diagnostics (%s)",
  async (status) => {
    const user = userEvent.setup()
    const initial = configuration()
    let failing = true
    const versions: number[] = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "DELETE") return json(initial)
        versions.push(JSON.parse(init.body).expected_draft_version)
        if (failing) {
          if (status === "network") throw new Error("private-credential")
          return json(
            {
              code: status === 400 ? "validation_failed" : "provider_error",
              detail: "private-credential",
              validation_problems: [
                { path: "workers", message: "private-credential" },
              ],
            },
            Number(status)
          )
        }
        return json({
          ...initial,
          draft_version: 12,
          draft: { ...initial.draft, workers: [] },
        })
      })
    )
    render(<WorkerCatalog agentType="codex" />)
    await openDelete(user)
    await user.click(screen.getByRole("button", { name: "Delete Worker" }))
    await within(screen.getByRole("dialog")).findByRole("alert")
    expect(screen.getByRole("dialog")).not.toHaveTextContent(
      "private-credential"
    )
    expect(screen.getByRole("button", { name: "Delete Worker" })).toBeEnabled()
    expect(versions).toEqual([11])
    failing = false
    await user.click(screen.getByRole("button", { name: "Delete Worker" }))
    await screen.findByText("No Codex Workers")
    expect(versions).toEqual([11, 11])
  }
)
