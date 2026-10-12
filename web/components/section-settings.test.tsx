import { selectOption } from "@/test-support/select-option"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import { SectionSettings } from "@/components/section-settings"
import { configuration } from "@/test-support/configuration"
import { stripSecretProjections, type DynamicDraft } from "@/lib/configuration"
import type { SettingsSectionID } from "@/lib/settings-sections"

const { push } = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push }),
  usePathname: () => "/settings/task-timeouts",
}))
const json = (body: unknown, status = 200) => Response.json(body, { status })
function mount(section: SettingsSectionID = "task-timeouts") {
  return render(
    <NavigationGuardProvider>
      <SectionSettings section={section} />
    </NavigationGuardProvider>
  )
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
afterEach(() => vi.unstubAllGlobals())

it("keeps dirty settings and keyboard focus when a tab change is cancelled", async () => {
  const user = userEvent.setup()
  mount()
  const input = await screen.findByLabelText("Reason maximum intents")
  fireEvent.change(input, { target: { value: "8" } })
  screen.getByRole("tab", { name: "Task Timeouts" }).focus()
  await user.keyboard("{Enter}")
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(push).not.toHaveBeenCalled()
  await user.keyboard("{ArrowDown}")
  const retry = screen.getByRole("tab", { name: "Retry Policies" })
  expect(retry).toHaveFocus()
  expect(push).not.toHaveBeenCalled()
  await user.keyboard("{Enter}")
  const dialog = screen.getByRole("dialog", { name: "Unsaved settings" })
  expect(
    within(dialog).getByText("Save your changes before leaving this page?")
  ).toBeVisible()
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
  expect(input).toHaveValue("8")
  expect(screen.getByRole("tab", { name: "Task Timeouts" })).toHaveAttribute(
    "aria-selected",
    "true"
  )
  expect(retry).toHaveFocus()
  expect(push).not.toHaveBeenCalled()
})

it("only leaves for the selected tab after a successful scoped Save", async () => {
  const user = userEvent.setup()
  let fail = true
  let submitted: DynamicDraft | undefined
  const initial = configuration()
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method !== "PUT") return json(initial)
      submitted = JSON.parse(String(init.body)).draft
      return fail
        ? json({}, 503)
        : json({ ...initial, draft_version: 12, draft: submitted })
    })
  )
  mount()
  const input = await screen.findByLabelText("Reason maximum intents")
  fireEvent.change(input, { target: { value: "8" } })
  const retry = screen.getByRole("tab", { name: "Retry Policies" })
  await user.click(retry)
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Save" })
  )
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(push).not.toHaveBeenCalled()
  expect(input).toHaveValue("8")
  fail = false
  await user.click(retry)
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Save" })
  )
  await waitFor(() =>
    expect(push).toHaveBeenCalledWith("/settings/retry-policies")
  )
  expect(submitted!.tasks.reason.max_intents).toBe(8)
  expect(submitted!.retry).toEqual(initial.draft.retry)
})

it("discards only the current edits before navigating to a tab", async () => {
  const user = userEvent.setup()
  mount("runtime-capacity")
  const input = await screen.findByLabelText("Scheduler interval (seconds)")
  fireEvent.change(input, { target: { value: "19" } })
  const healthchecks = screen.getByRole("tab", { name: "Healthchecks" })
  healthchecks.focus()
  await user.keyboard(" ")
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Discard" })
  )
  await waitFor(() =>
    expect(push).toHaveBeenCalledWith("/settings/worker-healthchecks")
  )
  expect(
    vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === "PUT")
  ).toBe(false)
})

it.each([
  ["task-timeouts", "Reason maximum intents", "3", "tasks"],
  ["retry-policies", "Bootstrap maximum attempts", "5", "retry"],
  ["project-runtime", "Network mode", "bridge", "container"],
  ["runtime-capacity", "Scheduler interval (seconds)", "11", "runtime"],
  ["worker-healthchecks", "Healthcheck timeout (seconds)", "33", "runtime"],
  ["prompt-group", "Prompt Group", "mock", "runtime"],
] as const)(
  "saves only the %s section against its captured complete configuration",
  async (section, label, value, group) => {
    const user = userEvent.setup()
    const initial = configuration()
    let submitted:
      { expected_draft_version: number; draft: DynamicDraft } | undefined
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        submitted = JSON.parse(String(init.body))
        return json({ ...initial, draft_version: 12, draft: submitted!.draft })
      })
    )
    mount(section)
    const input =
      section === "prompt-group"
        ? await screen.findByRole("combobox", { name: label })
        : await screen.findByLabelText(label)
    if (section === "prompt-group") await selectOption(user, input, "Mock")
    else fireEvent.change(input, { target: { value } })
    expect(
      screen.queryByRole("button", { name: "Apply" })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(submitted).toBeDefined())
    expect(submitted!.expected_draft_version).toBe(11)
    for (const key of [
      "tasks",
      "retry",
      "container",
      "runtime",
      "workers",
    ] as const)
      if (key !== group)
        expect(submitted!.draft[key]).toEqual(stripSecretProjections(initial.draft)[key])
    if (group === "runtime") {
      const changed =
        section === "runtime-capacity"
          ? "scheduler_interval_seconds"
          : section === "worker-healthchecks"
            ? "healthcheck_timeout_seconds"
            : "prompt_group"
      for (const key of Object.keys(initial.draft.runtime) as Array<
        keyof DynamicDraft["runtime"]
      >)
        if (key !== changed)
          expect(submitted!.draft.runtime[key]).toEqual(
            initial.draft.runtime[key]
          )
      expect(submitted!.draft.runtime[changed]).toBe(
        section === "prompt-group" ? value : Number(value)
      )
    }
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
    )
  }
)

it("uses recommendations for the current section without copying sibling configuration", async () => {
  const user = userEvent.setup()
  const initial = configuration()
  initial.recommended_draft.runtime.healthcheck_timeout_seconds = 32
  initial.recommended_draft.runtime.prompt_group = "mock"
  let saved: DynamicDraft | undefined
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method !== "PUT") return json(initial)
      saved = JSON.parse(String(init.body)).draft
      return json({ ...initial, draft_version: 12, draft: saved })
    })
  )
  mount("worker-healthchecks")
  await screen.findByLabelText("Healthcheck timeout (seconds)")
  await user.click(
    screen.getByRole("button", { name: "Use recommended values" })
  )
  expect(screen.getByLabelText("Healthcheck timeout (seconds)")).toHaveValue(
    "32"
  )
  await user.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(saved).toBeDefined())
  expect(saved!.runtime.healthcheck_timeout_seconds).toBe(32)
  expect(saved!.runtime.prompt_group).toBe("default")
})

it("retains local edits and a failed reload until an explicit successful reload", async () => {
  const user = userEvent.setup()
  let failedReload = false
  const initial = configuration()
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, init) => {
      if (init?.method === "PUT")
        return json(
          { code: "conflict", detail: "changed", current_draft_version: 20 },
          409
        )
      if (failedReload) return json({}, 503)
      return json(initial)
    })
  )
  mount()
  const input = await screen.findByLabelText("Reason maximum intents")
  fireEvent.change(input, { target: { value: "8" } })
  await user.click(screen.getByRole("button", { name: "Save" }))
  const dialog = await screen.findByRole("dialog", {
    name: "Settings changed elsewhere",
  })
  expect(input).toHaveValue("8")
  failedReload = true
  await user.click(within(dialog).getByRole("button", { name: "Reload" }))
  await screen.findByText("Unable to load settings")
  expect(input).toHaveValue("8")
  failedReload = false
  await user.click(within(dialog).getByRole("button", { name: "Reload" }))
  await waitFor(() => expect(input).toHaveValue("2"))
})

it("makes settings effective on Save without an Apply action", async () => {
  const user = userEvent.setup()
  const initial = configuration()
  let writes = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      if (init?.method === "PUT") {
        writes++
        initial.draft = JSON.parse(String(init.body)).draft
        initial.draft_version++
        return json(initial)
      }
      if (String(url).endsWith("/configuration/revisions/active"))
        return json({ configuration: initial.draft })
      return json(initial)
    })
  )
  mount("prompt-group")
  const input = await screen.findByRole("combobox", { name: "Prompt Group" })
  expect(
    screen.queryByRole("button", { name: "Apply" })
  ).not.toBeInTheDocument()
  await selectOption(user, input, "Mock")
  await user.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByText("Prompt Group saved.")
  expect(writes).toBe(1)
  await user.click(screen.getByRole("button", { name: "Saved settings" }))
  expect(await screen.findByText("Mock")).toBeVisible()
})

it("routes global validation problems directly to the owning sections", async () => {
  const initial = configuration()
  initial.validation_problems = [
    { path: "tasks.reason.max_intents", message: "is required" },
    { path: "runtime.prompt_group", message: "is required" },
    { path: "runtime.worker_healthcheck", message: "is required" },
  ]
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => json(initial))
  )
  mount("runtime-capacity")
  await screen.findByLabelText("Scheduler interval (seconds)")
  for (const [title, route] of [
    ["Task Timeouts", "task-timeouts"],
    ["Prompt Group", "prompt-group"],
    ["Worker Healthchecks", "worker-healthchecks"],
  ])
    expect(
      screen.getByRole("link", { name: `Open section: ${title}` })
    ).toHaveAttribute("href", `/settings/${route}`)
  expect(
    screen.queryByRole("button", { name: "Apply" })
  ).not.toBeInTheDocument()
})

it("does not expose an editable empty form when configuration reads fail", async () => {
  const user = userEvent.setup()
  let fail = true
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => (fail ? json({}, 503) : json(configuration())))
  )
  mount("project-runtime")
  await screen.findByText("Unable to load settings")
  expect(screen.queryByLabelText("Network mode")).not.toBeInTheDocument()
  fail = false
  await user.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByLabelText("Network mode")).toHaveValue("host")
})
