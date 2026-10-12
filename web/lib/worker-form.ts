import {
  stripSecretProjections,
  type AgentDefinitionMetadata,
  ConfigurationRead,
  DynamicWorkerDraft,
  ProviderPreset,
  ValidationProblem,
} from "@/lib/configuration"

import type { AgentType } from "@/lib/worker-catalog"

export interface SecretEdit {
  mode: "retain" | "replace" | "clear"
  value: string
}
export interface WorkerForm {
  name: string
  providerName: string
  tasks: string[]
  priority: string
  capacity: string
  capabilities: string[]
  environment: Array<{ name: string; value: string }>
  timeouts: Record<string, string>
  secrets: Record<string, SecretEdit>
}

export function readWorkerForm(
  definition: AgentDefinitionMetadata,
  worker?: DynamicWorkerDraft,
  copyName?: string
): WorkerForm {
  const fields = [
    ...definition.required_fields,
    ...(definition.optional_fields ?? []),
  ]
  return {
    name: copyName ?? worker?.name ?? "",
    providerName: worker?.provider_name ?? "",
    tasks: [...(worker?.task_types ?? ["bootstrap", "reason", "explore"])],
    priority: String(worker?.priority ?? 0),
    capacity: String(worker?.max_running ?? 1),
    capabilities: [...(worker?.capabilities ?? [])],
    environment: [
      ...fields
        .filter((field) => !field.secret)
        .map((field) => ({
          name: field.name,
          value: worker?.agent_env[field.name] ?? "",
        })),
      ...Object.entries(worker?.agent_env ?? {})
        .filter(([name]) => !fields.some((field) => field.name === name))
        .map(([name, value]) => ({ name, value })),
    ],
    timeouts: Object.fromEntries(
      Object.entries(worker?.timeout_overrides ?? {}).map(
        ([phase, seconds]) => [phase, String(seconds)]
      )
    ),
    secrets: Object.fromEntries(
      fields
        .filter((field) => field.secret)
        .map((field) => [field.name, { mode: "retain", value: "" }])
    ),
  }
}

export function initializePreset(
  form: WorkerForm,
  preset: ProviderPreset,
  definition: AgentDefinitionMetadata
): WorkerForm {
  const initialized = readWorkerForm(definition, {
    name: form.name,
    agent_type: definition.type,
    provider_name: preset.provider_name,
    agent_env: preset.agent_env,
    task_types: form.tasks,
    priority: Number(form.priority),
    max_running: Number(form.capacity),
    capabilities: form.capabilities,
    timeout_overrides: {},
  })
  return {
    ...initialized,
    environment: [
      ...initialized.environment,
      ...form.environment
        .slice(
          [
            ...definition.required_fields,
            ...(definition.optional_fields ?? []),
          ].filter((field) => !field.secret).length
        )
        .filter((entry) => !Object.hasOwn(preset.agent_env, entry.name)),
    ],
    priority: form.priority,
    capacity: form.capacity,
    timeouts: form.timeouts,
    secrets: form.secrets,
  }
}

export function uniqueCopyName(name: string, read: ConfigurationRead) {
  let candidate = `${name}-copy`
  for (
    let index = 2;
    read.draft.workers.some((worker) => worker.name === candidate);
    index++
  )
    candidate = `${name}-copy-${index}`
  return candidate
}

export function buildWorkerSave(
  read: ConfigurationRead,
  form: WorkerForm,
  agentType: AgentType,
  originalName?: string
) {
  const draft = stripSecretProjections(read.draft)
  const worker: DynamicWorkerDraft = {
    name: form.name,
    agent_type: agentType,
    provider_name: form.providerName,
    task_types: [...form.tasks],
    max_running: Number(form.capacity),
    priority: Number(form.priority),
    capabilities: [...form.capabilities],
    agent_env: Object.fromEntries(
      form.environment
        .filter((entry) => entry.value !== "")
        .map((entry) => [entry.name, entry.value])
    ),
    timeout_overrides: Object.fromEntries(
      Object.entries(form.timeouts)
        .filter(([, seconds]) => seconds !== "")
        .map(([phase, seconds]) => [phase, Number(seconds)])
    ),
  }
  const index =
    originalName === undefined
      ? -1
      : draft.workers.findIndex((worker) => worker.name === originalName)
  if (index < 0) draft.workers.push(worker)
  else draft.workers[index] = worker
  const changes: Record<string, { clear?: boolean; value?: string }> = {}
  for (const [slot, change] of Object.entries(form.secrets)) {
    if (change.mode === "clear") changes[slot] = { clear: true }
    else if (change.mode === "replace" && change.value !== "")
      changes[slot] = { value: change.value }
  }
  return {
    expected_draft_version: read.draft_version,
    draft,
    ...(Object.keys(changes).length
      ? { secrets: { [form.name]: changes } }
      : {}),
  }
}

const reservedVariables = new Set([
  "AETHER_RESULT_PROTOCOL",
  "HOME",
  "LD_LIBRARY_PATH",
  "LD_PRELOAD",
  "MOCK_CONTEXT",
  "MOCK_PHASE",
  "PATH",
  "PWD",
  "SHELL",
  "TMPDIR",
])

export function validateWorkerForm(
  form: WorkerForm,
  definition: AgentDefinitionMetadata,
  read: ConfigurationRead,
  originalName?: string
): ValidationProblem[] {
  const problems: ValidationProblem[] = []
  const add = (path: string, message: string) =>
    problems.push({ path, message })
  const byteLength = (value: string) => new TextEncoder().encode(value).length
  if (
    !form.name ||
    form.name.trim() !== form.name ||
    form.name.includes("\0") ||
    byteLength(form.name) > 256
  )
    add(
      "name",
      "Enter a name without surrounding whitespace or NUL characters, up to 256 bytes."
    )
  if (
    read.draft.workers.some(
      (worker) => worker.name === form.name && worker.name !== originalName
    )
  )
    add("name", "Choose a unique Worker name.")
  if (byteLength(form.providerName) > 256)
    add("providerName", "Use a Provider name up to 256 bytes.")
  for (const [path, value] of [
    ["priority", form.priority],
    ["capacity", form.capacity],
  ]) {
    if (
      !/^\d+$/.test(value) ||
      Number(value) > 1024 ||
      (path === "capacity" && Number(value) < 1)
    )
      add(
        path,
        `Enter a whole number between ${path === "capacity" ? 1 : 0} and 1024.`
      )
  }
  const fields = [
    ...definition.required_fields,
    ...(definition.optional_fields ?? []),
  ]
  const declared = new Set(
    fields.filter((field) => !field.secret).map((field) => field.name)
  )
  const secretSlots = new Set(
    fields.filter((field) => field.secret).map((field) => field.name)
  )
  const seen = new Set<string>()
  let environmentBytes = 0
  for (let index = 0; index < form.environment.length; index++) {
    const { name, value } = form.environment[index]
    // Optional declared fields may be left empty; required fields are checked below.
    if (declared.has(name) && value === "" && !seen.has(name)) {
      seen.add(name)
      continue
    }
    const path = `environment.${index}`
    if (!name || byteLength(name) > 128 || /[=\0]/.test(name))
      add(
        path,
        "Enter an environment name up to 128 bytes without = or NUL characters."
      )
    if (seen.has(name)) add(path, "Environment names must be unique.")
    if (reservedVariables.has(name))
      add(path, "This environment name is reserved by Aether.")
    if (secretSlots.has(name))
      add(path, "Configure this credential in Secrets.")
    if (value === "" || value.includes("\0"))
      add(path, "Enter a non-empty value without NUL characters.")
    seen.add(name)
    environmentBytes += byteLength(name) + byteLength(value) + 1
  }
  if (
    form.environment.filter((entry) => entry.value !== "").length > 256 ||
    environmentBytes > 256 * 1024
  )
    add(
      "environment",
      "Use at most 256 variables and 256 KiB of environment values."
    )
  for (const [phase, seconds] of Object.entries(form.timeouts))
    if (
      seconds !== "" &&
      (!/^\d+$/.test(seconds) || Number(seconds) < 1 || Number(seconds) > 86400)
    )
      add(
        `timeouts.${phase}`,
        "Enter a whole number between 1 and 86400 seconds, or leave blank to use Project settings."
      )
  if (!form.tasks.length) add("tasks", "Choose at least one task.")
  for (const field of definition.required_fields) {
    if (field.secret) {
      const secret = form.secrets[field.name]
      const existing = read.draft.workers.find(
        (worker) => worker.name === originalName
      )
      if (
        !(secret?.mode === "replace" && secret.value.trim()) &&
        !(
          form.name === originalName &&
          secret?.mode !== "clear" &&
          !secret?.value.trim() &&
          existing?.secrets?.[field.name]?.configured
        )
      )
        add(
          `secrets.${field.name}`,
          "Enter the required credential before saving."
        )
    } else {
      const index = form.environment.findIndex(
        (entry) => entry.name === field.name
      )
      if (index < 0 || !form.environment[index].value.trim())
        add(
          `environment.${index < 0 ? field.name : index}`,
          "Enter the required value before saving."
        )
    }
  }
  return problems
}

export function timeoutPhases(read: ConfigurationRead, task: string) {
  return Object.entries(read.draft.tasks[task as keyof typeof read.draft.tasks])
    .filter(([field]) => field.endsWith("_seconds"))
    .map(([field, seconds]) => ({
      phase: `${task}.${field.replace("_seconds", "")}`,
      seconds,
    }))
}

export function workerValidationProblems(
  problems: ValidationProblem[],
  index: number,
  name: string,
  form: WorkerForm
): ValidationProblem[] {
  const prefix = `workers[${index}]`
  return problems
    .filter(
      (problem) =>
        problem.path === prefix ||
        problem.path.startsWith(`${prefix}.`) ||
        problem.path === `workers.${name}`
    )
    .map((problem) => {
      let path = problem.path.slice(prefix.length + 1)
      if (path.startsWith("agent_env."))
        path = `environment.${form.environment.findIndex((entry) => entry.name === path.slice("agent_env.".length))}`
      else if (path === "agent_env") path = "environment"
      else if (path === "task_types") path = "tasks"
      else if (path === "max_running") path = "capacity"
      else if (path === "provider_name") path = "providerName"
      else if (path.startsWith("timeout_overrides."))
        path = path.replace("timeout_overrides.", "timeouts.")
      const message = /duplicate|unique/.test(problem.message)
        ? "Use a unique name and list each value once."
        : /reserved/.test(problem.message)
          ? "This environment name is reserved by Aether."
          : /Secret slot/.test(problem.message)
            ? "Configure this credential in Secrets."
            : /HTTP URL/.test(problem.message)
              ? "Enter an HTTP or HTTPS URL without credentials, query parameters or a fragment."
              : /NUL|non-empty|canonical/.test(problem.message)
                ? "Enter a valid value without NUL characters or surrounding whitespace."
                : /required|empty/.test(problem.message)
                  ? "Complete this field before saving settings."
                  : "Check this value against the field requirements and try again."
      return { path, message }
    })
}
