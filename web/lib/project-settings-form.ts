import { type DynamicDraft, type ValidationProblem } from "@/lib/configuration"

type Task = "bootstrap" | "reason" | "explore"
type Phase = "primary_seconds" | "conclude_seconds" | "recovery_seconds"
type RetryField =
  | "max_attempts"
  | "initial_interval_seconds"
  | "max_interval_seconds"
  | "multiplier"
type ContainerField =
  | "image"
  | "network_mode"
  | "cap_add"
  | "completed_action"
  | "stdout_limit_bytes"
  | "stderr_limit_bytes"
export type ProjectFieldPath =
  | `tasks.${"bootstrap" | "explore"}.${Phase}`
  | "tasks.reason.primary_seconds"
  | "tasks.reason.max_intents"
  | `retry.${Task}.${RetryField}`
  | `container.${ContainerField}`
export type ProjectForm = Record<ProjectFieldPath, string>

export interface ProjectField {
  path: ProjectFieldPath
  label: string
  description: string
  kind: "integer" | "decimal" | "text" | "capabilities" | "action"
}

export const taskFields: ProjectField[] = [
  {
    path: "tasks.bootstrap.primary_seconds",
    label: "Bootstrap primary (seconds)",
    description: "Time limit for the initial Bootstrap phase.",
    kind: "integer",
  },
  {
    path: "tasks.bootstrap.conclude_seconds",
    label: "Bootstrap conclude (seconds)",
    description: "Time for Bootstrap to finish after its primary phase.",
    kind: "integer",
  },
  {
    path: "tasks.bootstrap.recovery_seconds",
    label: "Bootstrap recovery (seconds)",
    description: "Time to resume the Bootstrap session after an eligible exit.",
    kind: "integer",
  },
  {
    path: "tasks.reason.primary_seconds",
    label: "Reason primary (seconds)",
    description: "Time to assess the Blackboard and propose Intents.",
    kind: "integer",
  },
  {
    path: "tasks.reason.max_intents",
    label: "Reason maximum intents",
    description: "Intents allowed per Reason result.",
    kind: "integer",
  },
  {
    path: "tasks.explore.primary_seconds",
    label: "Explore primary (seconds)",
    description: "Time limit for the initial Explore phase.",
    kind: "integer",
  },
  {
    path: "tasks.explore.conclude_seconds",
    label: "Explore conclude (seconds)",
    description: "Time for Explore to finish after its primary phase.",
    kind: "integer",
  },
  {
    path: "tasks.explore.recovery_seconds",
    label: "Explore recovery (seconds)",
    description: "Time to resume the Explore session after an eligible exit.",
    kind: "integer",
  },
]

export const retryGroups = (["bootstrap", "reason", "explore"] as const).map(
  (task) => ({
    title: `${task[0].toUpperCase()}${task.slice(1)}`,
    fields: [
      {
        path: `retry.${task}.max_attempts`,
        label: "Maximum attempts",
        description: "Failed attempts allowed before retries stop.",
        kind: "integer",
      },
      {
        path: `retry.${task}.initial_interval_seconds`,
        label: "Initial interval (seconds)",
        description: "Delay before the first retry.",
        kind: "integer",
      },
      {
        path: `retry.${task}.max_interval_seconds`,
        label: "Maximum interval (seconds)",
        description: "Longest delay between retries.",
        kind: "integer",
      },
      {
        path: `retry.${task}.multiplier`,
        label: "Backoff multiplier",
        description: "Multiplies each retry delay, up to the maximum interval.",
        kind: "decimal",
      },
    ] satisfies ProjectField[],
  })
)

export const containerFields: ProjectField[] = [
  {
    path: "container.image",
    label: "Worker runtime image",
    description: "Docker image shared by a Project’s Workers.",
    kind: "text",
  },
  {
    path: "container.network_mode",
    label: "Network mode",
    description: "Docker network mode, e.g. bridge or host.",
    kind: "text",
  },
  {
    path: "container.cap_add",
    label: "Added capabilities",
    description: "Extra Linux capabilities. Separate names with commas.",
    kind: "capabilities",
  },
  {
    path: "container.completed_action",
    label: "Completed action",
    description: "Stop keeps the container. Remove deletes it and its files.",
    kind: "action",
  },
  {
    path: "container.stdout_limit_bytes",
    label: "Stdout limit (bytes)",
    description:
      "Per-invocation stdout cap; exceeding it fails the invocation.",
    kind: "integer",
  },
  {
    path: "container.stderr_limit_bytes",
    label: "Stderr limit (bytes)",
    description:
      "Per-invocation stderr cap; exceeding it fails the invocation.",
    kind: "integer",
  },
]

const fields = [
  ...taskFields,
  ...retryGroups.flatMap((group) => group.fields),
  ...containerFields,
]

export function readProjectForm(draft: DynamicDraft): ProjectForm {
  const entries: Array<[string, string]> = []
  for (const task of ["bootstrap", "reason", "explore"] as const) {
    for (const [field, value] of Object.entries(draft.tasks[task]))
      entries.push([`tasks.${task}.${field}`, value === 0 ? "" : String(value)])
    for (const [field, value] of Object.entries(draft.retry[task]))
      entries.push([`retry.${task}.${field}`, value === 0 ? "" : String(value)])
  }
  for (const [field, value] of Object.entries(draft.container)) {
    entries.push([
      `container.${field}`,
      field === "cap_add"
        ? (draft.container.cap_add ?? []).join(", ")
        : value === 0
          ? ""
          : String(value),
    ])
  }
  return Object.fromEntries(entries) as ProjectForm
}

export function projectProblemLabel(path: string) {
  const field = fields.find(
    (item) => item.path === path || path.startsWith(`${item.path}[`)
  )
  if (field) return field.label
  const group = retryGroups.find((item) =>
    item.fields[0].path.startsWith(`${path}.`)
  )
  return group ? `${group.title} retry policy` : "Project settings"
}

export function projectProblemMessage(problem: ValidationProblem) {
  if (problem.message.includes("canonical"))
    return "Remove leading or trailing spaces and unsupported characters."
  if (problem.message === "is required") return "Enter a value before Save."
  if (problem.message === "backoff intervals are invalid")
    return "Use non-negative intervals; the maximum must be at least the initial interval."
  if (problem.message === "backoff intervals are required")
    return "Enter both intervals before Save."
  if (problem.message === "contains a duplicate capability")
    return "List each capability once."
  if (problem.message === "must be stop or remove")
    return "Choose Stop or Remove."
  if (problem.message === "is outside the allowed output limit")
    return "Enter an output limit between 65536 and 8388608 bytes."
  return problem.message
}
