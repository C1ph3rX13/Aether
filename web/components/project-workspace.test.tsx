import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { SWRConfig } from "swr"
import { afterEach, expect, it, vi } from "vitest"
import { ProjectWorkspace } from "@/components/project-workspace"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import type { ProjectDetailWire } from "@/modules/workspace/wire"

const route = vi.hoisted(() => ({
  pathname: "/projects/proj_test",
}))
vi.mock("next/navigation", () => ({
  usePathname: () => route.pathname,
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock("@/modules/workspace/graph/view", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/modules/workspace/graph/view")>()
  const { graphRenderer } = await import("@/test-support/workspace-renderer")
  return { ...actual, WorkspaceGraph: graphRenderer(actual.GraphRecordCard) }
})
class Stream {
  static current: Stream
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  constructor() {
    Stream.current = this
  }
  close() {}
}
const description =
  "完整的记录说明 — épreuve — 検証\n" + "Long evidence content. ".repeat(100)
const detail: ProjectDetailWire = {
  project: {
    id: "proj_test",
    title: "Workspace acceptance",
    status: "paused",
    bootstrap_enabled: true,
    generation: 2,
    revision: 18,
    created_at: "2026-01-01T00:00:00Z",
  },
  facts: [
    { id: "origin", description: "Origin evidence", status: "active" },
    { id: "goal", description: "Goal evidence", status: "active" },
    ...Array.from({ length: 12 }, (_, index) => ({
      id: `f${String(index + 1).padStart(3, "0")}`,
      description: index === 11 ? description : `Fact ${index + 1}`,
      status: "active" as const,
    })),
  ],
  intents: [
    {
      id: "i001",
      kind: "bootstrap",
      status: "concluded",
      from: ["origin"],
      to: "f012",
      description: "Inspect bootstrap result",
      creator: "worker",
      created_at: "2026-01-02T00:00:00Z",
      concluded_at: "2026-01-02T01:00:00Z",
    },
  ],
  hints: [
    {
      id: "h001",
      content: "Operator guidance",
      creator: "Operator",
      status: "active",
      generation: 2,
      created_at: "2026-01-02T00:00:00Z",
    },
  ],
  completion_records: [],
}
afterEach(() => {
  vi.unstubAllGlobals()
  route.pathname = "/projects/proj_test"
})
function mount(
  options: {
    projectFailure?: number
    evidenceFailure?: boolean
    active?: boolean
    completed?: boolean
  } = {}
) {
  const controls = {
    projectFailure: options.projectFailure ?? 0,
    evidenceFailure: options.evidenceFailure ?? false,
    detail: structuredClone(detail),
    commands: [] as { path: string; body: Record<string, unknown> }[],
    respond: (async () => Response.json({}, { status: 503 })) as (
      path: string,
      body: Record<string, unknown>
    ) => Promise<Response>,
  }
  if (options.active) controls.detail.project.status = "active"
  if (options.completed) controls.detail.project.status = "completed"
  vi.stubGlobal("EventSource", Stream)
  vi.stubGlobal("navigation", new EventTarget())
  vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
    const path = String(input)
    if (path.endsWith("/actions/impact"))
      return Response.json({
        project_revision: controls.detail.project.revision,
        affected_run_count: 0,
      })
    if (path.endsWith("/deployment"))
      return Response.json({
        operator: { name: "Operator" },
        build: {},
        capabilities: {},
      })
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>
      controls.commands.push({ path, body })
      return controls.respond(path, body)
    }
    if (path.includes("execution-evidence") && controls.evidenceFailure)
      return new Response("{}", { status: 503 })
    if (
      !path.includes("execution-evidence") &&
      !path.includes("/log") &&
      controls.projectFailure
    )
      return new Response("{}", { status: controls.projectFailure })
    const response = path.includes("execution-evidence")
      ? {
          project_id: "proj_test",
          active_reason: {
            run_id: "reason-1",
            task: "reason",
            status: "active",
            attempt: 2,
            worker: "reason-worker",
            started_at: "2026-01-02T00:00:00Z",
          },
          selected_intent_run: path.includes("intent_id")
            ? {
                run_id: "run-1",
                task: "bootstrap",
                status: "committed",
                attempt: 1,
                worker: "acceptance-worker",
                started_at: "2026-01-02T00:00:00Z",
                outcome_kind: "conclude_intent",
              }
            : undefined,
        }
      : path.includes("/log")
        ? {
            project_id: "proj_test",
            available_from_revision: 1,
            available_from_occurred_at: "2026-01-01T00:00:00Z",
            current_revision: 18,
            current_revision_occurred_at: "2026-01-02T00:00:00Z",
            revisions: [
              {
                revision: 18,
                generation: 2,
                occurred_at: "2026-01-02T00:00:00Z",
                source: { kind: "operator", name: "Operator" },
                events: [
                  {
                    event_id: "ev-18",
                    kind: "intent_concluded",
                    intent_id: "i001",
                    description: "Inspect bootstrap result",
                    source_fact_ids: ["origin"],
                    produced_fact: { id: "f012", description },
                    focus_target: { type: "intent", id: "i001" },
                  },
                ],
              },
            ],
          }
        : controls.detail
    return new Response(JSON.stringify(response), {
      headers: { "Content-Type": "application/json" },
    })
  })
  render(
    <SWRConfig value={{ provider: () => new Map(), shouldRetryOnError: false }}>
      <NavigationGuardProvider>
        <ProjectWorkspace />
      </NavigationGuardProvider>
    </SWRConfig>
  )
  return controls
}
async function confirmLive(revision = 18) {
  await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
  act(() =>
    Stream.current.onmessage?.({
      data: JSON.stringify({
        type: "sync_complete",
        project_id: "proj_test",
        data: { current_revision: revision },
      }),
    })
  )
  await screen.findByLabelText("Data Freshness: Live")
}

it("explicitly retries exhausted work and reuses the request after a lost acknowledgement", async () => {
  const controls = mount({ active: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  await user.click(
    await screen.findByRole("menuitem", { name: "Retry exhausted work" })
  )
  const dialog = await screen.findByRole("alertdialog", {
    name: "Retry exhausted work",
  })
  expect(controls.commands).toHaveLength(0)
  controls.respond = async () => {
    throw new TypeError("Lost acknowledgement")
  }
  await user.click(
    within(dialog).getByRole("button", { name: "Retry exhausted work" })
  )
  await within(dialog).findByRole("alert")
  expect(controls.commands).toHaveLength(1)
  const original = controls.commands[0]
  expect(original.path).toBe(
    "/api/v1/projects/proj_test/actions/retry-exhausted"
  )
  expect(original.body).toEqual({
    request_id: expect.any(String),
    expected_revision: 18,
  })
  expect(original.body.request_id).toMatch(/^[0-9a-f-]{36}$/)
  controls.respond = async () =>
    Response.json({
      request_id: original.body.request_id,
      project_revision: 18,
      run_ids: ["run-exhausted"],
      replayed: true,
    })
  await user.click(
    within(dialog).getByRole("button", { name: "Retry exhausted work" })
  )
  await waitFor(() =>
    expect(
      screen.queryByRole("alertdialog", { name: "Retry exhausted work" })
    ).toBeNull()
  )
  expect(controls.commands).toHaveLength(2)
  expect(controls.commands[1]).toEqual(original)
  expect(await screen.findByText("Retry requested for 1 task")).toBeVisible()
})

it("keeps Reopen focused on its reason and offers refresh only while recovering non-live data", async () => {
  const controls = mount({ completed: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(screen.getByRole("button", { name: "Reopen" }))
  const dialog = await screen.findByRole("dialog", { name: "Reopen Project" })
  const reason = within(dialog).getByLabelText("Reason for reopening")
  await user.type(reason, "Check the remaining evidence")
  expect(
    within(dialog).queryByRole("button", { name: "Refresh Project" })
  ).toBeNull()
  expect(within(dialog).getByRole("button", { name: "Reopen" })).toBeEnabled()

  act(() => Stream.current.onerror?.())
  expect(reason).toHaveValue("Check the remaining evidence")
  expect(within(dialog).getByRole("button", { name: "Reopen" })).toBeDisabled()
  await user.click(
    within(dialog).getByRole("button", { name: "Refresh Project" })
  )
  await confirmLive()
  expect(reason).toHaveValue("Check the remaining evidence")
  expect(
    within(dialog).queryByRole("button", { name: "Refresh Project" })
  ).toBeNull()
  controls.respond = async () => Response.json({ project_revision: 19 })
  await user.click(within(dialog).getByRole("button", { name: "Reopen" }))
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Reopen Project" })).toBeNull()
  )
  expect(controls.commands).toHaveLength(1)
  expect(controls.commands[0]).toEqual({
    path: "/api/v1/projects/proj_test/actions/reopen",
    body: { feedback: "Check the remaining evidence", expected_revision: 18 },
  })
})

it("shares explicit Completion Evidence between graph and inspection without selecting inspected records", async () => {
  mount({ active: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(screen.getByRole("button", { name: "Open Fact f001" }))
  const inGraph = screen.getByRole("checkbox", {
    hidden: true,
    name: "Select f001 as Completion Evidence",
  })
  const inInspection = screen.getByRole("checkbox", {
    name: "Use f001 as Completion Evidence",
  })
  expect(inGraph).not.toBeChecked()
  expect(inInspection).not.toBeChecked()
  await user.click(screen.getByText("Completion Evidence", { exact: true }))
  expect(inInspection).toBeChecked()
  expect(inGraph).toBeChecked()
  expect(
    screen.getByText("1 Facts selected as Completion Evidence")
  ).toBeVisible()
  await user.keyboard("{Escape}")
  await user.click(screen.getByRole("button", { name: "Open Fact origin" }))
  expect(
    screen.getByRole("checkbox", {
      name: "Use origin as Completion Evidence",
    })
  ).not.toBeChecked()
  expect(inGraph).toBeChecked()
  await user.keyboard("{Escape}")
  await user.click(inGraph)
  expect(
    screen.getByRole("button", { name: "Complete Project" })
  ).toBeDisabled()
  await user.click(screen.getByRole("button", { name: "Open Fact goal" }))
  expect(screen.queryByText("Completion Evidence", { exact: true })).toBeNull()
})

it("shares evidence across Graph and Records while inspection stays independent", async () => {
  mount({ active: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(
    screen.getByRole("checkbox", {
      name: "Select origin as Completion Evidence",
    })
  )
  await user.click(screen.getByRole("tab", { name: "Records" }))
  const records = screen.getByRole("tabpanel", { name: "Records" })
  expect(
    within(records).getByRole("checkbox", {
      name: "Records evidence origin",
    })
  ).toBeChecked()
  await user.click(
    within(records).getByRole("button", { name: "Open Fact f001" })
  )
  expect(
    within(records).getByRole("checkbox", {
      name: "Records evidence f001",
      hidden: true,
    })
  ).not.toBeChecked()
  expect(
    screen.getByRole("region", { name: "Record Detail" })
  ).toHaveTextContent("Fact 1")
  await user.keyboard("{Escape}")
  await user.click(
    within(records).getByRole("checkbox", { name: "Records evidence f001" })
  )
  await user.click(screen.getByRole("tab", { name: "Blackboard Graph" }))
  const graph = screen.getByRole("application", { name: "Blackboard graph" })
  for (const id of ["origin", "f001"])
    expect(
      within(graph).getByRole("checkbox", {
        name: `Select ${id} as Completion Evidence`,
      })
    ).toBeChecked()
  expect(screen.getByRole("button", { name: "Complete Project" })).toBeEnabled()
})

it("selects explicit Completion Evidence independently of graph inspection", async () => {
  mount({ active: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  const first = screen.getByRole("checkbox", {
    name: "Select f001 as Completion Evidence",
  })
  first.focus()
  await user.keyboard(" ")
  expect(first).toBeChecked()
  expect(first).toHaveFocus()
  await user.click(
    screen.getByRole("checkbox", {
      name: "Select origin as Completion Evidence",
    })
  )
  expect(
    screen.queryByRole("checkbox", {
      name: "Select goal as Completion Evidence",
    })
  ).not.toBeInTheDocument()
  await user.click(
    within(
      screen.getByRole("application", { name: "Blackboard graph" })
    ).getByRole("button", { name: "Open Intent i001" })
  )
  expect(
    screen.getByRole("region", { name: "Record Detail" })
  ).toHaveTextContent("Intent i001")
  await user.keyboard("{Escape}")
  expect(
    screen.getByText("2 Facts selected as Completion Evidence")
  ).toBeVisible()
  await user.click(screen.getByRole("button", { name: "Complete Project" }))
  const confirmation = await screen.findByRole("dialog", {
    name: "Complete Project",
  })
  expect(within(confirmation).getByText("f001", { exact: true })).toBeVisible()
  expect(
    within(confirmation).getByText("Origin evidence", { exact: true })
  ).toBeVisible()
  await user.click(within(confirmation).getByRole("button", { name: "Cancel" }))
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(screen.getByRole("button", { name: "Complete Project" })).toHaveFocus()
  expect(
    screen.getByText("2 Facts selected as Completion Evidence")
  ).toBeVisible()
})
it("opens complete details and follows graph relationships in the right Sheet", async () => {
  mount()
  const user = userEvent.setup()
  const graph = await screen.findByRole("application", {
    name: "Blackboard graph",
  })
  await user.click(
    within(graph).getByRole("button", { name: "Open Intent i001" })
  )
  const inspector = screen.getByRole("region", { name: "Record Detail" })
  await user.click(
    within(inspector).getByRole("button", {
      name: "Execution Evidence",
    })
  )
  await within(inspector).findByText("acceptance-worker")
  await user.click(
    within(inspector).getByRole("button", { name: "Open Fact f012" })
  )
  expect(inspector).toHaveTextContent("Fact f012")
  expect(
    within(inspector).getByText(description, { normalizer: (text) => text })
  ).toBeVisible()
  expect(screen.getByRole("tab", { name: "Details" })).toHaveAttribute(
    "aria-selected",
    "true"
  )
  await confirmLive()
})

it("keeps Add Hint editable with a single dialog before opening the right Sheet", async () => {
  const controls = mount({ active: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  const trigger = screen.getByRole("button", { name: "Add Hint" })
  await user.click(trigger)
  const dialog = await screen.findByRole("dialog", { name: "Add Hint" })
  expect(
    screen.queryByRole("dialog", { name: "Project inspection" })
  ).toBeNull()
  expect(screen.getAllByRole("dialog")).toHaveLength(1)
  const input = within(dialog).getByRole("textbox", { name: "Hint text" })
  await user.type(input, "Review this evidence")
  expect(input).toHaveValue("Review this evidence")
  expect(input).toHaveFocus()
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(trigger).toHaveFocus()
  await user.click(
    screen.getByRole("button", { name: "Toggle right inspection panel" })
  )
  expect(screen.getByRole("tab", { name: /^Hints/ })).toHaveAttribute(
    "aria-selected",
    "true"
  )
  expect(controls.commands).toEqual([])
})

it("serializes pending Hint writes and waits for authoritative synchronization before showing the result", async () => {
  const controls = mount({ active: true })
  const user = userEvent.setup()
  let finish!: (response: Response) => void
  controls.respond = () =>
    new Promise((resolve) => {
      finish = resolve
    })
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(screen.getByRole("button", { name: "Add Hint" }))
  const dialog = await screen.findByRole("dialog", { name: "Add Hint" })
  expect(within(dialog).getByLabelText("Hint text")).toBeRequired()
  expect(
    within(dialog).getByLabelText("Hint text")
  ).toHaveAccessibleDescription(
    "Required. Recorded under your Operator identity."
  )
  await user.type(
    within(dialog).getByLabelText("Hint text"),
    "Guide the investigation"
  )
  await user.click(within(dialog).getByRole("button", { name: "Add Hint" }))
  expect(within(dialog).getByRole("button", { name: "Adding…" })).toBeDisabled()
  expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled()
  await user.keyboard("{Escape}")
  expect(dialog).toBeVisible()
  expect(controls.commands).toEqual([
    {
      path: "/api/v1/projects/proj_test/hints",
      body: { content: "Guide the investigation", expected_revision: 18 },
    },
  ])
  const hint = {
    id: "h002",
    content: "Guide the investigation",
    creator: "Operator",
    status: "active" as const,
    generation: 2,
    created_at: "2026-01-02T00:00:00Z",
  }
  await act(async () =>
    finish(Response.json({ hint, project_revision: 19, affected_run_count: 0 }))
  )
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(screen.getByRole("button", { name: "Add Hint" })).toBeDisabled()
  expect(screen.getByLabelText("Data Freshness: Refreshing")).toBeVisible()
  await user.click(
    screen.getByRole("button", { name: "Toggle right inspection panel" })
  )
  await user.click(screen.getByRole("tab", { name: /^Hints/ }))
  expect(screen.queryByText("Guide the investigation")).not.toBeInTheDocument()
  controls.detail.project.revision = 19
  controls.detail.hints.push(hint)
  act(() =>
    Stream.current.onmessage?.({
      data: JSON.stringify({
        type: "hint_created",
        project_id: "proj_test",
        revision: 19,
      }),
    })
  )
  await screen.findByText("Guide the investigation")
  await user.keyboard("{Escape}")
  await screen.findByLabelText("Data Freshness: Live")
  expect(screen.getByRole("button", { name: "Add Hint" })).toBeEnabled()
})

it("preserves completion input and evidence after conflict, blocks reconnecting writes and prunes invalid sources on refresh", async () => {
  const controls = mount({ active: true })
  controls.respond = async () =>
    Response.json(
      { code: "stale_input", detail: "Revision changed", current_revision: 19 },
      { status: 409 }
    )
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(
    screen.getByRole("checkbox", {
      name: "Select f001 as Completion Evidence",
    })
  )
  await user.click(screen.getByRole("button", { name: "Complete Project" }))
  const dialog = await screen.findByRole("dialog", { name: "Complete Project" })
  expect(within(dialog).getByLabelText("Completion description")).toBeRequired()
  expect(
    within(dialog).getByLabelText("Completion description")
  ).toHaveAccessibleDescription(
    /Required\. Marks the Goal achieved and cancels/
  )
  await user.type(
    within(dialog).getByLabelText("Completion description"),
    "Evidence proves the Goal"
  )
  await user.click(
    within(dialog).getByRole("button", { name: "Complete Project" })
  )
  await within(dialog).findByRole("alert")
  expect(within(dialog).getByLabelText("Completion description")).toHaveValue(
    "Evidence proves the Goal"
  )
  expect(within(dialog).getByText("f001", { exact: true })).toBeVisible()
  expect(controls.commands[0].body).toEqual({
    from: ["f001"],
    description: "Evidence proves the Goal",
    expected_revision: 18,
  })
  act(() => Stream.current.onerror?.())
  expect(
    within(dialog).getByRole("button", { name: "Complete Project" })
  ).toBeDisabled()
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
  controls.detail.facts[2].status = "retracted"
  controls.detail.project.revision = 19
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  await user.click(screen.getByRole("menuitem", { name: "Refresh Project" }))
  await screen.findByText("Completion Evidence updated")
  expect(
    screen.getByRole("button", { name: "Complete Project" })
  ).toBeDisabled()
  expect(
    screen.queryByRole("checkbox", {
      name: "Select f001 as Completion Evidence",
    })
  ).not.toBeInTheDocument()
})

it("recovers initial reads, retains browsing on transient errors and replaces cached data on authoritative Not Found", async () => {
  const controls = mount({ projectFailure: 503 })
  const user = userEvent.setup()
  await screen.findByRole("heading", { name: "Project unavailable" })
  controls.projectFailure = 0
  await user.click(screen.getByRole("button", { name: "Retry Project" }))
  await screen.findByRole("application", { name: "Blackboard graph" })
  controls.projectFailure = 503
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  await user.click(screen.getByRole("menuitem", { name: "Refresh Project" }))
  await screen.findByText("Project update unavailable")
  expect(
    screen.getByRole("application", { name: "Blackboard graph" })
  ).toBeVisible()
  expect(
    screen.queryByLabelText("Data Freshness: Live")
  ).not.toBeInTheDocument()
  controls.projectFailure = 0
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  await user.click(screen.getByRole("menuitem", { name: "Refresh Project" }))
  await waitFor(() =>
    expect(
      screen.queryByText("Project update unavailable")
    ).not.toBeInTheDocument()
  )
  controls.projectFailure = 404
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  await user.click(screen.getByRole("menuitem", { name: "Refresh Project" }))
  await screen.findByRole("heading", { name: "Project Not Found" })
  expect(
    screen.queryByRole("application", { name: "Blackboard graph" })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Return to Projects" })
  ).toHaveAttribute("href", "/projects")
})

it("keeps Hint text available for correction and retry after validation and request failures", async () => {
  const controls = mount()
  const user = userEvent.setup()
  controls.respond = async () =>
    Response.json(
      { code: "validation", detail: "Hint content must describe guidance." },
      { status: 400 }
    )
  await screen.findByRole("application", { name: "Blackboard graph" })
  await confirmLive()
  await user.click(screen.getByRole("button", { name: "Add Hint" }))
  const dialog = await screen.findByRole("dialog", { name: "Add Hint" })
  await user.type(
    within(dialog).getByLabelText("Hint text"),
    "Retained guidance"
  )
  await user.click(within(dialog).getByRole("button", { name: "Add Hint" }))
  await within(dialog).findByText("Hint content must describe guidance.")
  expect(within(dialog).getByLabelText("Hint text")).toHaveValue(
    "Retained guidance"
  )
  controls.respond = async () => {
    throw new TypeError("Failed to fetch")
  }
  await user.click(within(dialog).getByRole("button", { name: "Add Hint" }))
  await within(dialog).findByText(
    "The Server could not be reached. Check the connection and retry."
  )
  expect(within(dialog).getByLabelText("Hint text")).toHaveValue(
    "Retained guidance"
  )
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(screen.getByRole("button", { name: "Add Hint" })).toHaveFocus()
})

it("retries Active Reason and selected Intent evidence without changing record lifecycle", async () => {
  const controls = mount({ evidenceFailure: true })
  const user = userEvent.setup()
  await screen.findByRole("application", { name: "Blackboard graph" })
  await user.click(screen.getByRole("button", { name: "Open Intent i001" }))
  const sheet = await screen.findByRole("region", { name: "Record Detail" })
  await user.click(
    within(sheet).getByRole("button", {
      name: "Execution Evidence",
    })
  )
  await within(sheet).findByRole("button", {
    name: "Retry Intent Execution Evidence",
  })
  expect(
    within(sheet).getByRole("button", { name: "Retry Current Active Reason" })
  ).toBeVisible()
  for (const metadata of within(sheet).getAllByText("Concluded"))
    expect(metadata).toBeVisible()
  controls.evidenceFailure = false
  await user.click(
    within(sheet).getByRole("button", {
      name: "Retry Intent Execution Evidence",
    })
  )
  await within(sheet).findByText("acceptance-worker")
  expect(within(sheet).getByText("reason-worker · Attempt 2")).toBeVisible()
  expect(within(sheet).getByText("Committed")).toBeVisible()
  for (const metadata of within(sheet).getAllByText("Concluded"))
    expect(metadata).toBeVisible()
})

it("rejects malformed Project routes before attempting a read", async () => {
  route.pathname = "/projects/__placeholder__"
  mount()
  await screen.findByRole("heading", { name: "Invalid Project" })
  expect(
    screen.getByRole("link", { name: "Return to Projects" })
  ).toHaveAttribute("href", "/projects")
})
