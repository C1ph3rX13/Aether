import { selectOption } from "@/test-support/select-option"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { ProjectWorkspace } from "./project-workspace"
import { NavigationGuardProvider } from "./navigation-guard"
import type { ProjectDetailWire } from "@/modules/workspace/wire"

vi.mock("next/navigation", () => ({
  usePathname: () => "/projects/proj_test",
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
  constructor() {
    Stream.current = this
  }
  close() {}
  send(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) })
  }
}
const time = "2026-01-01T00:00:00Z"
function snapshot(revision: number): ProjectDetailWire {
  return {
    project: {
      id: "proj_test",
      title: "Historical Workspace",
      status: "active",
      generation: 1,
      revision,
      bootstrap_enabled: true,
      created_at: time,
    },
    facts: [
      { id: "origin", description: "Origin", status: "active" },
      { id: "goal", description: "Goal", status: "active" },
      ...(revision >= 2
        ? [
            {
              id: "f001",
              description: "Bootstrap evidence",
              status:
                revision >= 3 ? ("retracted" as const) : ("active" as const),
              ...(revision >= 3
                ? {
                    retraction_reason: "Corrected evidence",
                    replacement_fact_id: "f002",
                  }
                : {}),
            },
          ]
        : []),
      ...(revision >= 3
        ? [
            {
              id: "f002",
              description: "Replacement evidence",
              status: "active" as const,
              supersedes: "f001",
            },
          ]
        : []),
    ],
    intents: [
      {
        id: "i001",
        kind: "bootstrap",
        status: revision >= 2 ? "concluded" : "open",
        from: ["origin"],
        ...(revision >= 2 ? { to: "f001" } : {}),
        description: "Bootstrap exploration",
        creator: "worker",
        created_at: time,
      },
    ],
    hints: [
      {
        id: "h001",
        content: "Initial guidance",
        status: "active",
        generation: 1,
        creator: "Operator",
        created_at: time,
      },
      ...(revision >= 4
        ? [
            {
              id: "h002",
              content: "Current guidance",
              status: "active" as const,
              generation: 1,
              creator: "Operator",
              created_at: time,
            },
          ]
        : []),
    ],
    completion_records: [],
  }
}
afterEach(() => vi.unstubAllGlobals())

it("reads historical graph, details, relationships and atomic Log while shared commands remain ineligible", async () => {
  const user = userEvent.setup()
  let currentRevision = 3
  const requests: string[] = []
  vi.stubGlobal("EventSource", Stream)
  vi.stubGlobal("navigation", new EventTarget())
  vi.stubGlobal("fetch", async (url: string) => {
    requests.push(url)
    if (url.includes("execution-evidence"))
      return Response.json({ project_id: "proj_test" })
    if (url.includes("/deployment"))
      return Response.json({
        operator: { name: "Operator" },
        build: {},
        capabilities: {},
      })
    if (url.includes("/log")) {
      const before =
        Number(
          new URL(url, "http://localhost").searchParams.get("before_revision")
        ) || currentRevision + 1
      return Response.json({
        project_id: "proj_test",
        available_from_revision: 1,
        available_from_occurred_at: time,
        current_revision: currentRevision,
        current_revision_occurred_at: time,
        revisions: [1, 2, 3, 4]
          .filter(
            (revision) => revision < before && revision <= currentRevision
          )
          .map((revision) => ({
            revision,
            generation: 1,
            occurred_at: time,
            source: { kind: "operator", name: "Operator" },
            events: [
              {
                event_id: `event-${revision}`,
                kind: "hint_created",
                content: `Atomic group ${revision}`,
                hint_id: `h${revision}`,
                generation: 1,
                creator: "Operator",
              },
            ],
          }))
          .reverse(),
      })
    }
    const revision = Number(/history\/(\d+)/.exec(url)?.[1])
    return Response.json(snapshot(revision || currentRevision))
  })
  render(<ProjectWorkspace />, { wrapper: NavigationGuardProvider })
  await screen.findByRole("heading", { name: "Historical Workspace" })
  await waitFor(() => expect(Stream.current.onmessage).toBeTruthy())
  act(() =>
    Stream.current.send({
      type: "sync_complete",
      project_id: "proj_test",
      data: { current_revision: 3 },
    })
  )
  await screen.findByLabelText("Data Freshness: Live")
  await user.click(
    screen.getByRole("checkbox", {
      name: "Select origin as Completion Evidence",
    })
  )
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  await user.click(screen.getByRole("menuitem", { name: "Replay" }))
  const picker = await screen.findByLabelText("Go to Replay frame")
  await waitFor(() => expect(picker).toBeEnabled())
  await selectOption(user, picker, "R2")
  expect(
    screen.getByRole("button", { name: "Complete Project" })
  ).toBeDisabled()
  expect(
    screen.getByRole("checkbox", {
      name: "Select origin as Completion Evidence",
    })
  ).not.toBeChecked()
  await user.click(
    screen.getByRole("button", { name: "Toggle right inspection panel" })
  )
  await user.click(screen.getByRole("tab", { name: "Log" }))
  expect(screen.getByRole("region", { name: "Project Log" })).toHaveTextContent(
    "Atomic group 2"
  )
  expect(screen.queryByText("Atomic group 3")).toBeNull()
  await user.keyboard("{Escape}")
  await user.click(screen.getByRole("button", { name: "Open Fact f001" }))
  let sheet = screen.getByRole("region", { name: "Record Detail" })
  expect(within(sheet).getByText("Bootstrap evidence")).toBeVisible()
  expect(within(sheet).queryByText("Retraction Reason")).toBeNull()
  await user.click(
    within(sheet).getByRole("button", { name: "Open Intent i001" })
  )
  sheet = screen.getByRole("region", { name: "Record Detail" })
  expect(
    within(sheet).getByRole("button", { name: "Open Fact f001" })
  ).toBeVisible()
  expect(
    within(sheet).getByText(
      "Current Execution Evidence is unavailable in historical Replay."
    )
  ).toBeVisible()
  currentRevision = 4
  act(() =>
    Stream.current.send({
      type: "hint_created",
      project_id: "proj_test",
      revision: 4,
    })
  )
  await user.keyboard("{Escape}")
  await screen.findByText("1 update pending")
  expect(picker).toHaveTextContent("R2")
  await user.click(picker)
  expect(screen.getAllByRole("option")).toHaveLength(3)
  await user.keyboard("{Escape}")
  await user.click(
    screen.getByRole("button", { name: "Toggle right inspection panel" })
  )
  await user.click(screen.getByRole("tab", { name: /^Hints/ }))
  expect(
    within(screen.getByRole("tabpanel", { name: /^Hints/ })).queryByText(
      "Current guidance"
    )
  ).toBeNull()
  await user.keyboard("{Escape}")
  await selectOption(user, picker, "R3")
  for (const name of ["Add Hint", "Complete Project"])
    expect(screen.getByRole("button", { name })).toBeDisabled()
  await user.click(screen.getByRole("tab", { name: "Records" }))
  expect(
    within(screen.getByRole("tabpanel", { name: "Records" })).getByRole(
      "checkbox",
      { name: "Records evidence origin" }
    )
  ).toBeDisabled()
  await user.click(screen.getByRole("tab", { name: "Blackboard Graph" }))
  await user.click(screen.getByRole("button", { name: "Project actions" }))
  for (const name of ["Rename", "Delete Project"])
    expect(screen.getByRole("menuitem", { name })).toHaveAttribute(
      "aria-disabled",
      "true"
    )
  await user.keyboard("{Escape}")
  expect(screen.getByRole("button", { name: "Pause" })).toBeDisabled()
  await user.click(screen.getByRole("button", { name: "Snapshot" }))
  const dialog = screen.getByRole("dialog", { name: "Snapshot Blackboard" })
  await selectOption(user, within(dialog).getByLabelText("Format"), "JSON")
  await user.click(within(dialog).getByRole("button", { name: "Export" }))
  await waitFor(() =>
    expect(
      JSON.parse(
        (within(dialog).getByLabelText("Export result") as HTMLTextAreaElement)
          .value
      )
    ).toEqual(snapshot(3))
  )
  expect(requests.some((url) => url.includes("/export"))).toBe(false)
  await user.keyboard("{Escape}")
  const oldStream = Stream.current
  await user.click(screen.getByRole("button", { name: "Exit Replay" }))
  expect(screen.getByRole("button", { name: "Add Hint" })).toBeDisabled()
  await waitFor(() => expect(Stream.current).not.toBe(oldStream))
  act(() =>
    Stream.current.send({
      type: "sync_complete",
      project_id: "proj_test",
      data: { current_revision: 4 },
    })
  )
  await screen.findByLabelText("Data Freshness: Live")
  expect(screen.getByRole("button", { name: "Add Hint" })).toBeEnabled()
  await user.click(
    screen.getByRole("button", { name: "Toggle right inspection panel" })
  )
  expect(
    within(screen.getByRole("tabpanel", { name: /^Hints/ })).getByText(
      "Current guidance"
    )
  ).toBeVisible()
})
