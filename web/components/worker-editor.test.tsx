import { selectOption } from "@/test-support/select-option"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
  act,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import { WorkerEditorRoute } from "@/components/worker-editor-route"
import { WorkerEditor } from "@/components/worker-editor"
import {
  stripSecretProjections,
  type ConfigurationRead,
  type DynamicDraft,
} from "@/lib/configuration"

const { push, replace } = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace }),
  useSearchParams: () => new URLSearchParams(window.location.search),
}))

function configuration(): ConfigurationRead {
  const retry = {
    max_attempts: 3,
    initial_interval_seconds: 30,
    max_interval_seconds: 300,
    multiplier: 2,
  }
  const draft: DynamicDraft = {
    runtime: {
      scheduler_interval_seconds: 7,
      max_workers: 9,
      max_running_projects: 3,
      max_project_workers: 4,
      healthcheck_timeout_seconds: 20,
      worker_healthcheck: "startup_only",
      prompt_group: "default",
    },
    tasks: {
      bootstrap: {
        primary_seconds: 600,
        conclude_seconds: 90,
        recovery_seconds: 30,
      },
      reason: { primary_seconds: 450, max_intents: 2 },
      explore: {
        primary_seconds: 600,
        conclude_seconds: 90,
        recovery_seconds: 30,
      },
    },
    retry: {
      bootstrap: { ...retry },
      reason: { ...retry },
      explore: { ...retry },
    },
    container: {
      image: "ghcr.io/c1ph3rx13/aether-worker:latest",
      network_mode: "host",
      cap_add: [],
      completed_action: "stop",
      stdout_limit_bytes: 1048576,
      stderr_limit_bytes: 1048576,
    },
    workers: [
      {
        name: "codex-primary",
        agent_type: "codex",
        provider_name: "DeepSeek",
        task_types: ["reason"],
        max_running: 2,
        priority: 1,
        capabilities: [],
        agent_env: {
          CODEX_BASE_URL: "https://api.deepseek.com",
          CODEX_MODEL: "deepseek-v4-flash",
        },
        timeout_overrides: {},
        secrets: { OPENAI_API_KEY: { configured: true, mask: "********" } },
      },
      {
        name: "pi-incomplete",
        agent_type: "pi",
        provider_name: "Custom",
        task_types: [],
        max_running: 1,
        priority: 0,
        capabilities: [],
        agent_env: {},
        timeout_overrides: {},
      },
    ],
  }
  return {
    deployment_id: "private-deployment",
    draft_version: 11,
    draft,
    recommended_draft: structuredClone(draft),
    validation_problems: [
      { path: "workers[1].task_types", message: "must not be empty" },
    ],
    agent_definitions: [
      {
        type: "codex",
        display_name: "Codex",
        required_fields: [
          {
            name: "CODEX_BASE_URL",
            display_name: "Responses Base URL",
            required: true,
            secret: false,
          },
          {
            name: "CODEX_MODEL",
            display_name: "Model",
            required: true,
            secret: false,
          },
          {
            name: "OPENAI_API_KEY",
            display_name: "OpenAI API Key",
            required: true,
            secret: true,
          },
        ],
        capabilities: ["process_recovery"],
        result_contract: "aether.run-result/v1",
        provider_healthcheck: "http",
      },
      {
        type: "pi",
        display_name: "Pi",
        required_fields: [
          {
            name: "PI_BASE_URL",
            display_name: "Provider Base URL",
            required: true,
            secret: false,
          },
          {
            name: "PI_MODEL",
            display_name: "Model",
            required: true,
            secret: false,
          },
          {
            name: "PI_PROVIDER_API",
            display_name: "Provider API",
            required: true,
            secret: false,
          },
          {
            name: "PI_API_KEY",
            display_name: "Provider API Key",
            required: true,
            secret: true,
          },
        ],
        optional_fields: [
          {
            name: "PI_MODEL_CONTEXT_WINDOW",
            display_name: "Model Context Window",
            required: false,
            secret: false,
          },
        ],
        capabilities: ["process_recovery"],
        result_contract: "aether.run-result/v1",
        provider_healthcheck: "http",
      },
      {
        type: "claude_code",
        display_name: "Claude Code",
        required_fields: [
          {
            name: "IS_SANDBOX",
            display_name: "Sandbox",
            required: true,
            secret: false,
          },
          {
            name: "ANTHROPIC_BASE_URL",
            display_name: "Anthropic Base URL",
            required: true,
            secret: false,
          },
          {
            name: "ANTHROPIC_MODEL",
            display_name: "Model",
            required: true,
            secret: false,
          },
          {
            name: "ANTHROPIC_AUTH_TOKEN",
            display_name: "Anthropic Auth Token",
            required: true,
            secret: true,
          },
        ],
        capabilities: ["process_recovery"],
        result_contract: "aether.run-result/v1",
        provider_healthcheck: "http",
      },
    ],
    provider_presets: [
      {
        id: "deepseek_codex",
        display_name: "DeepSeek + Codex",
        agent_type: "codex",
        provider_name: "DeepSeek",
        protocol: "openai-responses",
        agent_env: {
          CODEX_BASE_URL: "https://api.deepseek.com",
          CODEX_MODEL: "deepseek-flash",
        },
      },
    ],
    prompt_groups: ["default", "mock"],
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function mount(children: React.ReactNode) {
  return render(<NavigationGuardProvider>{children}</NavigationGuardProvider>)
}

describe("Worker editor", () => {
  beforeEach(() => {
    HTMLElement.prototype.getAnimations = () => []
    HTMLElement.prototype.scrollIntoView = () => {}
    push.mockReset()
    replace.mockReset()
    window.history.replaceState(null, "", "/workers/codex/editor")
    vi.stubGlobal(
      "navigation",
      Object.assign(new EventTarget(), { traverseTo: vi.fn() })
    )
  })
  afterEach(() => vi.unstubAllGlobals())

  it("clears the entry save notice when editing, even after failed edits are reverted", async () => {
    const user = userEvent.setup()
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) =>
        init?.method === "PUT"
          ? json(
              { code: "provider_check_failed", detail: "private-secret" },
              400
            )
          : json(configuration())
      )
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" saved />)
    await screen.findByText("Worker saved.")
    const priority = screen.getByLabelText("Priority") as HTMLInputElement
    const originalPriority = priority.value
    fireEvent.change(screen.getByLabelText("Priority"), {
      target: { value: "2" },
    })
    expect(screen.queryByText("Worker saved.")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByRole("dialog", { name: "Provider check failed" })
    expect(screen.queryByText("Worker saved.")).not.toBeInTheDocument()
    expect(screen.queryByText("private-secret")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    fireEvent.change(priority, { target: { value: originalPriority } })
    expect(screen.queryByText("Worker saved.")).not.toBeInTheDocument()
  })

  it("requires complete setup before saving a Worker and preserves unrelated settings", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    const requests: Array<{
      expected_draft_version: number
      draft: DynamicDraft
      secrets?: unknown
    }> = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        requests.push(structuredClone(request))
        return json({
          ...initial,
          draft_version: 12,
          draft: request.draft,
          validation_problems: [],
        })
      })
    )
    mount(<WorkerEditor agentType="codex" />)
    const name = await screen.findByLabelText("Worker name")
    await user.type(name, "codex-new")
    expect(screen.getByRole("checkbox", { name: "Reason" })).toBeChecked()
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    await selectOption(
      user,
      screen.getByLabelText("Provider configuration"),
      "DeepSeek + Codex"
    )
    expect(screen.getByLabelText("Responses Base URL")).toHaveValue(
      "https://api.deepseek.com"
    )
    expect(screen.getByLabelText("Model")).toHaveValue("deepseek-flash")
    expect(screen.getByText(/openai-responses/)).toBeInTheDocument()
    expect(screen.getByText("Not configured")).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    expect(
      screen.getByRole("radio", { name: "Step 2: Provider configuration" })
    ).toHaveAttribute("aria-current", "step")
    expect(screen.getByLabelText("OpenAI API Key")).toHaveAttribute(
      "aria-invalid",
      "true"
    )
    expect(requests).toHaveLength(0)
    fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
      target: { value: "creation-credential" },
    })
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests[0].expected_draft_version).toBe(11)
    expect(requests[0].draft.runtime).toEqual(initial.draft.runtime)
    expect(requests[0].draft.tasks).toEqual(initial.draft.tasks)
    expect(requests[0].draft.workers.slice(0, 2)).toEqual(
      stripSecretProjections(initial.draft).workers
    )
    expect(requests[0].secrets).toEqual({
      "codex-new": { OPENAI_API_KEY: { value: "creation-credential" } },
    })
    expect(requests[0].draft.workers[2]).toMatchObject({
      name: "codex-new",
      agent_type: "codex",
      provider_name: "DeepSeek",
      task_types: ["bootstrap", "reason", "explore"],
      agent_env: {
        CODEX_BASE_URL: "https://api.deepseek.com",
        CODEX_MODEL: "deepseek-flash",
      },
    })

    expect(
      screen.queryByText(/private-deployment|draft_version|workers\[2\]/)
    ).not.toBeInTheDocument()
  })

  it("associates a rejected Secret with the retained replacement input", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        return json(
          {
            code: "validation_failed",
            detail: "Invalid credential",
            validation_problems: [
              {
                path: "workers[0].secrets.OPENAI_API_KEY",
                message: "Secret slot invalid",
              },
            ],
          },
          400
        )
      })
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    await screen.findByLabelText("Worker name")
    await user.click(screen.getByRole("button", { name: "Replace Secret" }))
    const secret = screen.getByLabelText("Replace OpenAI API Key")
    await user.type(secret, "rejected-credential")
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await waitFor(() => expect(secret).toHaveAttribute("aria-invalid", "true"))
    expect(secret).toHaveAccessibleDescription(
      /Configure this credential in Secrets/
    )
    expect(secret).toHaveValue("rejected-credential")
  })

  it("guides creation, validates identity and retains credentials while moving between steps", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    let request:
      { draft: DynamicDraft; secrets: Record<string, unknown> } | undefined
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        request = JSON.parse(String(init.body))
        const draft = structuredClone(request!.draft)
        draft.workers.at(-1)!.secrets = { OPENAI_API_KEY: { configured: true } }
        return json({
          ...initial,
          draft,
          draft_version: 12,
          validation_problems: [],
        })
      })
    )
    mount(<WorkerEditor agentType="codex" />)
    const name = await screen.findByLabelText("Worker name")
    expect(
      screen.getByRole("radio", { name: "Step 2: Provider configuration" })
    ).toBeDisabled()
    expect(
      screen.queryByLabelText("Responses Base URL")
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    expect(name).toHaveFocus()
    expect(name).toHaveAttribute("aria-invalid", "true")
    fireEvent.change(name, { target: { value: "codex-primary" } })
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    expect(name).toHaveAccessibleDescription(/unique Worker name/)
    fireEvent.change(name, { target: { value: "codex-secondary" } })
    expect(screen.getByRole("checkbox", { name: "Explore" })).toBeChecked()
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    await selectOption(
      user,
      screen.getByLabelText("Provider configuration"),
      "DeepSeek + Codex"
    )
    fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
      target: { value: "creation-test-credential" },
    })
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    expect(screen.getByText("Ready for the Provider check")).toBeInTheDocument()
    expect(
      screen.queryByText("creation-test-credential")
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Add environment variable" })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole("button", { name: "Edit provider configuration" })
    )
    expect(screen.getByLabelText("OpenAI API Key")).toHaveValue(
      "creation-test-credential"
    )
    await user.click(screen.getByRole("button", { name: "Previous" }))
    expect(screen.getByLabelText("Worker name")).toHaveValue("codex-secondary")
    expect(screen.getByRole("checkbox", { name: "Explore" })).toBeChecked()
    await user.click(
      screen.getByRole("radio", { name: "Step 3: Review & save" })
    )
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(request!.secrets).toEqual({
      "codex-secondary": {
        OPENAI_API_KEY: { value: "creation-test-credential" },
      },
    })
    expect(request!.draft.workers.at(-1)!.task_types).toEqual([
      "bootstrap",
      "reason",
      "explore",
    ])
    expect(screen.queryByLabelText("OpenAI API Key")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("navigation", { name: "Worker creation steps" })
    ).not.toBeInTheDocument()
  })

  it("returns a rejected creation credential to its step without discarding the input", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) =>
        init?.method === "PUT"
          ? json(
              {
                code: "validation_failed",
                validation_problems: [
                  {
                    path: "workers[2].secrets.OPENAI_API_KEY",
                    message: "Secret slot invalid",
                  },
                ],
              },
              400
            )
          : json(initial)
      )
    )
    mount(<WorkerEditor agentType="codex" />)
    fireEvent.change(await screen.findByLabelText("Worker name"), {
      target: { value: "rejected-worker" },
    })
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    await selectOption(
      user,
      screen.getByLabelText("Provider configuration"),
      "DeepSeek + Codex"
    )
    fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
      target: { value: "retained-test-credential" },
    })
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByRole("dialog")
    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    expect(
      screen.getByRole("radio", { name: "Step 2: Provider configuration" })
    ).toHaveAttribute("aria-current", "step")
    expect(screen.getByLabelText("OpenAI API Key")).toHaveValue(
      "retained-test-credential"
    )
    expect(screen.getByLabelText("OpenAI API Key")).toHaveAccessibleDescription(
      /Configure this credential in Secrets/
    )
    await waitFor(() =>
      expect(screen.getByLabelText("OpenAI API Key")).toHaveFocus()
    )
  })

  it("keeps advanced validation on Review and preserves extra variables through preset changes", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    const requests: Array<{ draft: DynamicDraft }> = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        requests.push(request)
        return json({ ...initial, draft: request.draft, draft_version: 12 })
      })
    )
    mount(<WorkerEditor agentType="codex" />)
    fireEvent.change(await screen.findByLabelText("Worker name"), {
      target: { value: "advanced-worker" },
    })
    await user.click(screen.getByRole("checkbox", { name: "Reason" }))
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    await selectOption(
      user,
      screen.getByLabelText("Provider configuration"),
      "DeepSeek + Codex"
    )
    fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
      target: { value: "retained-credential" },
    })
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    await user.click(screen.getByRole("button", { name: "Environment" }))
    await user.click(
      screen.getByRole("button", { name: "Add environment variable" })
    )
    fireEvent.change(screen.getByLabelText("Environment name 3"), {
      target: { value: "PATH" },
    })
    fireEvent.change(screen.getByLabelText("Environment value 3"), {
      target: { value: "  keep these bytes  " },
    })
    await user.click(screen.getByRole("button", { name: "Previous" }))
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    expect(
      screen.getByRole("heading", { name: "Review your Worker" })
    ).toBeInTheDocument()
    expect(
      screen.getByText("Fix invalid settings before saving")
    ).toBeInTheDocument()
    await user.click(
      screen.getByRole("button", { name: "Fix Environment variable PATH" })
    )
    const value = screen.getByLabelText("Environment value 3")
    await waitFor(() => expect(value).toHaveFocus())
    expect(
      screen.getByLabelText("Environment name 3")
    ).toHaveAccessibleDescription(/reserved by Aether/)
    fireEvent.change(screen.getByLabelText("Environment name 3"), {
      target: { value: "CUSTOM_BASE_URL" },
    })
    await user.click(screen.getByRole("button", { name: "Previous" }))
    await selectOption(
      user,
      screen.getByLabelText("Provider configuration"),
      "Custom provider"
    )
    await selectOption(
      user,
      screen.getByLabelText("Provider configuration"),
      "DeepSeek + Codex"
    )
    expect(screen.getByLabelText("OpenAI API Key")).toHaveValue(
      "retained-credential"
    )
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    expect(screen.getByText("1 additional variable")).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Environment" }))
    expect(screen.getByLabelText("Environment name 3")).toHaveValue(
      "CUSTOM_BASE_URL"
    )
    expect(screen.getByLabelText("Environment value 3")).toHaveValue(
      "  keep these bytes  "
    )
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests).toHaveLength(1)
    expect(requests[0].draft.workers.at(-1)!.agent_env.CUSTOM_BASE_URL).toBe(
      "  keep these bytes  "
    )
  })

  it.each([
    ["Capacity", "1.5", "Fix Capacity"],
    ["Priority", "1025", "Fix Priority"],
    ["Worker name", "codex-primary", "Fix Worker name"],
  ])(
    "shows invalid %s when revisiting Review and links back to the field",
    async (label, value, action) => {
      const user = userEvent.setup()
      const fetch = vi.fn(async () => json(configuration()))
      vi.stubGlobal("fetch", fetch)
      mount(<WorkerEditor agentType="codex" />)
      fireEvent.change(await screen.findByLabelText("Worker name"), {
        target: { value: "valid-worker" },
      })
      await user.click(screen.getByRole("checkbox", { name: "Reason" }))
      await user.click(
        screen.getByRole("button", { name: "Continue to Provider" })
      )
      await selectOption(
        user,
        screen.getByLabelText("Provider configuration"),
        "DeepSeek + Codex"
      )
      fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
        target: { value: "test-credential" },
      })
      await user.click(
        screen.getByRole("button", { name: "Continue to Review" })
      )
      await user.click(
        screen.getByRole("button", { name: "Edit basic information" })
      )
      fireEvent.change(screen.getByLabelText(label), { target: { value } })
      await user.click(
        screen.getByRole("radio", { name: "Step 3: Review & save" })
      )
      expect(
        screen.queryByText("Ready for the Provider check")
      ).not.toBeInTheDocument()
      expect(
        screen.getByText("Fix invalid settings before saving")
      ).toBeInTheDocument()
      await user.click(screen.getByRole("button", { name: action }))
      await waitFor(() => expect(screen.getByLabelText(label)).toHaveFocus())
      expect(screen.getByLabelText(label)).toHaveAttribute(
        "aria-invalid",
        "true"
      )
      expect(fetch).toHaveBeenCalledTimes(1)
    }
  )

  it.each([
    ["provider_check_failed", 422, "Provider check failed", "Save Worker"],
    ["conflict", 409, "Settings changed elsewhere", "Reload"],
  ])(
    "restores a useful focus target after %s",
    async (code, status, title, target) => {
      const user = userEvent.setup()
      vi.stubGlobal(
        "fetch",
        vi.fn(async (_url, init) =>
          init?.method === "PUT"
            ? json({ code }, Number(status))
            : json(configuration())
        )
      )
      mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
      fireEvent.change(await screen.findByLabelText("Priority"), {
        target: { value: "2" },
      })
      await user.click(screen.getByRole("button", { name: "Save Worker" }))
      const dialog = await screen.findByRole("dialog", { name: String(title) })
      await user.click(
        within(dialog).getByRole("button", { name: "Keep editing" })
      )
      await waitFor(() =>
        expect(
          screen.getByRole("button", { name: String(target) })
        ).toHaveFocus()
      )
      expect(screen.getByLabelText("Priority")).toHaveValue("2")
    }
  )

  it("retains omitted and empty Secrets, replaces explicitly, rejects clearing a required Secret, and erases successful inputs", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    const requests: Array<{
      expected_draft_version: number
      draft: DynamicDraft
      secrets?: Record<
        string,
        Record<string, { value?: string; clear?: boolean }>
      >
    }> = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        requests.push(structuredClone(request))
        request.draft.workers[0].secrets = {
          OPENAI_API_KEY: { configured: requests.length < 4, mask: "********" },
        }
        return json({
          ...initial,
          draft_version: 11 + requests.length,
          draft: request.draft,
        })
      })
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    const priority = await screen.findByLabelText("Priority")
    expect(screen.getByText("Configured")).toBeInTheDocument()
    expect(screen.getByText("Worker Ready")).toBeInTheDocument()
    fireEvent.change(priority, { target: { value: "3" } })
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument()
    expect(screen.queryByText("Worker setup required")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(screen.getByText("Worker Ready")).toBeInTheDocument()
    expect(requests[0]).not.toHaveProperty("secrets")
    await user.click(screen.getByRole("button", { name: "Replace Secret" }))
    expect(screen.getByLabelText("Replace OpenAI API Key")).toHaveValue("")
    fireEvent.change(priority, { target: { value: "4" } })
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests[1]).not.toHaveProperty("secrets")
    await user.click(screen.getByRole("button", { name: "Replace Secret" }))
    await user.type(
      screen.getByLabelText("Replace OpenAI API Key"),
      "replacement-credential"
    )
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests[2].secrets).toEqual({
      "codex-primary": { OPENAI_API_KEY: { value: "replacement-credential" } },
    })
    expect(
      screen.queryByLabelText("Replace OpenAI API Key")
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Clear Secret" }))
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findAllByText("Enter the required credential before saving.")
    expect(requests).toHaveLength(3)
    expect(requests.map((request) => request.expected_draft_version)).toEqual([
      11, 12, 13,
    ])
    expect(document.body.textContent).not.toContain("replacement-credential")
    expect(JSON.stringify(window.localStorage)).not.toContain(
      "replacement-credential"
    )
  })

  it("bounds and masks Provider failures, retains edits and credentials for retry, and prevents pending changes", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    const requests: Array<{
      expected_draft_version: number
      draft: DynamicDraft
    }> = []
    let complete: ((response: Response) => void) | undefined
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        requests.push(structuredClone(request))
        if (requests.length === 1)
          return await new Promise<Response>((resolve) => {
            complete = resolve
          })
        request.draft.workers[0].secrets = {
          OPENAI_API_KEY: { configured: true, mask: "********" },
        }
        return json({ ...initial, draft: request.draft, draft_version: 12 })
      })
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    await screen.findByLabelText("Worker name")
    await user.click(screen.getByRole("button", { name: "Replace Secret" }))
    await user.type(
      screen.getByLabelText("Replace OpenAI API Key"),
      "candidate-credential"
    )
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    expect(
      screen.getByRole("button", { name: "Checking Provider..." })
    ).toBeDisabled()
    expect(screen.getByLabelText("Worker name")).toBeDisabled()
    expect(screen.getByLabelText("Replace OpenAI API Key")).toBeDisabled()
    await act(async () =>
      complete?.(
        json(
          {
            code: "provider_check_failed",
            detail: `Provider rejected candidate-credential ${"private output".repeat(100)}`,
          },
          503
        )
      )
    )
    const failure = await screen.findByRole("dialog", {
      name: "Provider check failed",
    })
    expect(failure.textContent).not.toContain("candidate-credential")
    expect(failure.textContent!.length).toBeLessThan(500)
    await user.click(
      within(failure).getByRole("button", { name: "Keep editing" })
    )
    expect(screen.getByLabelText("Replace OpenAI API Key")).toHaveValue(
      "candidate-credential"
    )
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests.map((request) => request.expected_draft_version)).toEqual([
      11, 11,
    ])
    expect(requests[0]).toEqual(requests[1])
  })

  it("copies with a unique identity and clears credential input and inherited state when renaming", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    initial.draft.workers.push({
      ...initial.draft.workers[0],
      name: "codex-primary-copy",
    })
    const requests: Array<{ draft: DynamicDraft; secrets?: unknown }> = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        requests.push(structuredClone(request))
        return json({
          ...initial,
          draft: request.draft,
          draft_version: 12,
          validation_problems: [
            {
              path: `workers[${request.draft.workers.length - 1}].secrets.OPENAI_API_KEY`,
              message: "required",
            },
          ],
        })
      })
    )
    const copied = mount(
      <WorkerEditor agentType="codex" workerName="codex-primary" copy />
    )
    expect(await screen.findByLabelText("Worker name")).toHaveValue(
      "codex-primary-copy-2"
    )
    await user.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    expect(screen.getByText("Not configured")).toBeInTheDocument()
    expect(screen.queryByText("Configured")).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
      target: { value: "copy-credential" },
    })
    await user.click(screen.getByRole("button", { name: "Continue to Review" }))
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests[0].draft.workers.slice(0, 3)).toEqual(
      stripSecretProjections(initial.draft).workers
    )
    expect(requests[0].draft.workers[3]).not.toHaveProperty("secrets")
    expect(requests[0].secrets).toEqual({
      "codex-primary-copy-2": { OPENAI_API_KEY: { value: "copy-credential" } },
    })
    copied.unmount()
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    await screen.findByLabelText("Worker name")
    await user.click(screen.getByRole("button", { name: "Replace Secret" }))
    await user.type(
      screen.getByLabelText("Replace OpenAI API Key"),
      "must-be-erased"
    )
    fireEvent.change(screen.getByLabelText("Worker name"), {
      target: { value: "renamed-worker" },
    })
    expect(
      screen.queryByLabelText("Replace OpenAI API Key")
    ).not.toBeInTheDocument()
    expect(screen.getByText("Not configured")).toBeInTheDocument()
    expect(
      screen.getByText(/Name changed; credentials reset/)
    ).toHaveTextContent("Name changed; credentials reset")
    await user.click(screen.getByRole("button", { name: "Configure Secret" }))
    fireEvent.change(screen.getByLabelText("Replace OpenAI API Key"), {
      target: { value: "rename-credential" },
    })
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests[1].draft.workers.map((worker) => worker.name)).toEqual([
      "renamed-worker",
      "pi-incomplete",
      "codex-primary-copy",
    ])
    expect(requests[1].secrets).toEqual({
      "renamed-worker": { OPENAI_API_KEY: { value: "rename-credential" } },
    })
  })

  it("keeps conflicted edits until explicit reload, preserves them on read failure and clears Secrets on successful reload", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    let failRead = false
    let puts = 0
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method === "PUT") {
          puts++
          return json({ code: "conflict", detail: "private version" }, 409)
        }
        return failRead
          ? json({ detail: "private database" }, 500)
          : json(initial)
      })
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    await screen.findByLabelText("Worker name")
    fireEvent.change(screen.getByLabelText("Priority"), {
      target: { value: "5" },
    })
    await user.click(screen.getByRole("button", { name: "Replace Secret" }))
    await user.type(
      screen.getByLabelText("Replace OpenAI API Key"),
      "local-secret"
    )
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    const conflict = await screen.findByRole("dialog", {
      name: "Settings changed elsewhere",
    })
    await user.click(
      within(conflict).getByRole("button", { name: "Keep editing" })
    )
    expect(screen.getByLabelText("Priority")).toHaveValue("5")
    expect(screen.getByRole("button", { name: "Save Worker" })).toBeDisabled()
    failRead = true
    await user.click(screen.getByRole("button", { name: "Reload" }))
    await user.click(
      within(
        await screen.findByRole("dialog", {
          name: "Settings changed elsewhere",
        })
      ).getByRole("button", { name: "Reload" })
    )
    await screen.findByText("Unable to load Worker settings")
    expect(screen.getByLabelText("Replace OpenAI API Key")).toHaveValue(
      "local-secret"
    )
    expect(screen.getByLabelText("Priority")).toHaveValue("5")
    failRead = false
    await user.click(screen.getByRole("button", { name: "Retry" }))
    await waitFor(() =>
      expect(screen.getByLabelText("Priority")).toHaveValue("1")
    )
    expect(
      screen.queryByLabelText("Replace OpenAI API Key")
    ).not.toBeInTheDocument()
    expect(puts).toBe(1)
    expect(screen.queryByText("private version")).not.toBeInTheDocument()
  })

  it("validates environment collisions before saving, preserves ordinary bytes, and only sends supported task overrides", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    const requests: Array<{ draft: DynamicDraft }> = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        requests.push(request)
        return json({ ...initial, draft: request.draft, draft_version: 12 })
      })
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    await screen.findByLabelText("Worker name")
    expect(
      screen.queryByRole("button", { name: "Add environment variable" })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Environment" }))
    await user.click(
      screen.getByRole("button", { name: "Add environment variable" })
    )
    const name = screen.getByLabelText("Environment name 3")
    const value = screen.getByLabelText("Environment value 3")
    fireEvent.change(value, { target: { value: "ordinary value" } })
    for (const [variable, message] of [
      ["PATH", /reserved by Aether/],
      ["OPENAI_API_KEY", /Configure this credential in Secrets/],
      ["CODEX_MODEL", /Environment names must be unique/],
      ["BAD=NAME", /without = or NUL/],
    ]) {
      fireEvent.change(name, { target: { value: variable } })
      await user.click(screen.getByRole("button", { name: "Save Worker" }))
      expect(
        screen.getByLabelText("Environment value 3")
      ).toHaveAccessibleDescription(message)
      expect(requests).toHaveLength(0)
      expect(screen.getByRole("button", { name: "Environment" })).toBeDisabled()
    }
    fireEvent.change(name, { target: { value: "CUSTOM_BASE_URL" } })
    fireEvent.change(value, { target: { value: "" } })
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    expect(value).toHaveAccessibleDescription(/non-empty value/)
    fireEvent.change(value, { target: { value: "  not-a-url; keep bytes  " } })
    await user.click(screen.getByRole("checkbox", { name: "Explore" }))
    expect(
      screen.queryByLabelText("Reason conclude (seconds)")
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Timeout overrides" }))
    fireEvent.change(screen.getByLabelText("Reason primary (seconds)"), {
      target: { value: "80" },
    })
    fireEvent.change(screen.getByLabelText("Explore conclude (seconds)"), {
      target: { value: "60" },
    })
    await user.click(screen.getByRole("checkbox", { name: "Explore" }))
    await user.click(screen.getByRole("checkbox", { name: "Process recovery" }))
    await user.click(screen.getByRole("button", { name: "Save Worker" }))
    await screen.findByText("Worker saved.")
    expect(requests[0].draft.workers[0].agent_env.CUSTOM_BASE_URL).toBe(
      "  not-a-url; keep bytes  "
    )
    expect(requests[0].draft.workers[0].timeout_overrides).toEqual({
      "reason.primary": 80,
    })
    expect(requests[0].draft.workers[0].capabilities).toEqual([
      "process_recovery",
    ])
  })

  it("preserves edits after structural failure and offers Save, Discard and Cancel on navigation", async () => {
    const user = userEvent.setup()
    const initial = configuration()
    let failSave = true
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, init) => {
        if (init?.method !== "PUT") return json(initial)
        const request = JSON.parse(String(init.body))
        if (failSave)
          return json(
            {
              code: "validation_failed",
              validation_problems: [
                {
                  path: "workers[0].agent_env.CODEX_BASE_URL",
                  message: "must be an HTTP URL",
                },
              ],
            },
            400
          )
        return json({ ...initial, draft: request.draft, draft_version: 12 })
      })
    )
    mount(<WorkerEditor agentType="codex" workerName="codex-primary" />)
    fireEvent.change(await screen.findByLabelText("Responses Base URL"), {
      target: { value: "invalid-url" },
    })
    const unload = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(unload)
    expect(unload.defaultPrevented).toBe(true)
    await user.click(screen.getByRole("link", { name: "Back to Workers" }))
    let navigation = await screen.findByRole("dialog", {
      name: "Unsaved settings",
    })
    await user.click(within(navigation).getByRole("button", { name: "Cancel" }))
    expect(push).not.toHaveBeenCalled()
    await user.click(screen.getByRole("link", { name: "Back to Workers" }))
    navigation = await screen.findByRole("dialog", { name: "Unsaved settings" })
    await user.click(within(navigation).getByRole("button", { name: "Save" }))
    const failure = await screen.findByRole("dialog", {
      name: "Worker could not be saved",
    })
    await user.click(
      within(failure).getByRole("button", { name: "Keep editing" })
    )
    expect(push).not.toHaveBeenCalled()
    expect(screen.getByLabelText("Responses Base URL")).toHaveValue(
      "invalid-url"
    )
    expect(
      screen.getByLabelText("Responses Base URL")
    ).toHaveAccessibleDescription(/HTTP or HTTPS URL/)
    failSave = false
    fireEvent.change(screen.getByLabelText("Responses Base URL"), {
      target: { value: "https://provider.example" },
    })
    await user.click(screen.getByRole("link", { name: "Back to Workers" }))
    navigation = await screen.findByRole("dialog", { name: "Unsaved settings" })
    await user.click(within(navigation).getByRole("button", { name: "Save" }))
    await waitFor(() => expect(push).toHaveBeenCalledWith("/workers/codex"))
    await user.click(screen.getByRole("button", { name: "Configure Secret" }))
    await user.type(
      screen.getByLabelText("Replace OpenAI API Key"),
      "discard-me"
    )
    await user.click(screen.getByRole("link", { name: "Back to Workers" }))
    navigation = await screen.findByRole("dialog", { name: "Unsaved settings" })
    await user.click(
      within(navigation).getByRole("button", { name: "Discard" })
    )
    expect(
      screen.queryByLabelText("Replace OpenAI API Key")
    ).not.toBeInTheDocument()
    const cleanUnload = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(cleanUnload)
    expect(cleanUnload.defaultPrevented).toBe(false)
  })

  it("renders optional Definition fields, handles nullable Worker collections and refuses another branch’s Worker", async () => {
    const read = configuration()
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        json({
          ...read,
          draft: { ...read.draft, workers: null },
          validation_problems: null,
        })
      )
    )
    const view = mount(<WorkerEditor agentType="pi" />)
    fireEvent.change(await screen.findByLabelText("Worker name"), {
      target: { value: "pi-new" },
    })
    expect(screen.getByLabelText("Agent Type")).toHaveValue("Pi")
    await userEvent.click(
      screen.getByRole("button", { name: "Continue to Provider" })
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Optional Provider settings" })
    )
    await screen.findByLabelText("Model Context Window")
    expect(screen.getByLabelText("Provider API")).toBeInTheDocument()
    view.unmount()
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json(read))
    )
    mount(<WorkerEditor agentType="claude_code" workerName="codex-primary" />)
    await screen.findByText("Worker not found")
    expect(screen.queryByLabelText("Worker name")).not.toBeInTheDocument()
  })

  it.each(["new", "copy", "rename"])(
    "replaces the %s editor URL with the saved identity for reload",
    async (operation) => {
      const user = userEvent.setup()
      const initial = configuration()
      const query =
        operation === "new"
          ? ""
          : operation === "copy"
            ? "?worker=codex-primary&copy=1"
            : "?worker=codex-primary"
      window.history.replaceState(null, "", `/workers/codex/editor${query}`)
      vi.stubGlobal(
        "fetch",
        vi.fn(async (_url, init) => {
          if (init?.method !== "PUT") return json(initial)
          const request = JSON.parse(String(init.body))
          return json({ ...initial, draft: request.draft, draft_version: 12 })
        })
      )
      mount(<WorkerEditorRoute agentType="codex" />)
      fireEvent.change(await screen.findByLabelText("Worker name"), {
        target: { value: "saved worker/+" },
      })
      if (operation !== "rename") {
        await user.click(
          screen.getByRole("button", { name: "Continue to Provider" })
        )
        await selectOption(
          user,
          screen.getByLabelText("Provider configuration"),
          "DeepSeek + Codex"
        )
        fireEvent.change(screen.getByLabelText("OpenAI API Key"), {
          target: { value: "new-credential" },
        })
        await user.click(
          screen.getByRole("button", { name: "Continue to Review" })
        )
      }
      if (operation === "rename") {
        await user.click(
          screen.getByRole("button", { name: "Configure Secret" })
        )
        fireEvent.change(screen.getByLabelText("Replace OpenAI API Key"), {
          target: { value: "renamed-credential" },
        })
      }
      await user.click(screen.getByRole("button", { name: "Save Worker" }))
      await waitFor(() =>
        expect(replace).toHaveBeenCalledWith(
          "/workers/codex/editor?worker=saved%20worker%2F%2B"
        )
      )
    }
  )
})
