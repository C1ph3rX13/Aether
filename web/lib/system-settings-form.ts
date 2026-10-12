import { type DynamicDraft, type ValidationProblem } from "@/lib/configuration"

export type SystemFieldPath =
  | "runtime.scheduler_interval_seconds"
  | "runtime.max_workers"
  | "runtime.max_running_projects"
  | "runtime.max_project_workers"
  | "runtime.healthcheck_timeout_seconds"
  | "runtime.worker_healthcheck"
  | "runtime.prompt_group"

export type SystemForm = Record<SystemFieldPath, string>

export interface SystemField {
  path: SystemFieldPath
  label: string
  description: string
  kind: "integer" | "healthcheck" | "prompt-group"
}

export const systemFields: SystemField[] = [
  {
    path: "runtime.scheduler_interval_seconds",
    label: "Scheduler interval (seconds)",
    description: "Time between checks for work and capacity.",
    kind: "integer",
  },
  {
    path: "runtime.max_workers",
    label: "Global Active Run limit",
    description: "Active Runs across all Projects.",
    kind: "integer",
  },
  {
    path: "runtime.max_running_projects",
    label: "Active Project limit",
    description: "Projects running tasks at the same time.",
    kind: "integer",
  },
  {
    path: "runtime.max_project_workers",
    label: "Project Active Run limit",
    description: "Active Runs per Project, also constrained by global capacity.",
    kind: "integer",
  },
  {
    path: "runtime.healthcheck_timeout_seconds",
    label: "Healthcheck timeout (seconds)",
    description: "Time limit for each Provider check.",
    kind: "integer",
  },
  {
    path: "runtime.worker_healthcheck",
    label: "Worker healthcheck mode",
    description: "Check at startup, before tasks, or never.",
    kind: "healthcheck",
  },
  {
    path: "runtime.prompt_group",
    label: "Prompt Group",
    description: "Bundled prompts for Bootstrap, Reason and Explore.",
    kind: "prompt-group",
  },
]

export function readSystemForm(draft: DynamicDraft): SystemForm {
  const runtime = draft.runtime
  return {
    "runtime.scheduler_interval_seconds": value(
      runtime.scheduler_interval_seconds
    ),
    "runtime.max_workers": value(runtime.max_workers),
    "runtime.max_running_projects": value(runtime.max_running_projects),
    "runtime.max_project_workers": value(runtime.max_project_workers),
    "runtime.healthcheck_timeout_seconds": value(
      runtime.healthcheck_timeout_seconds
    ),
    "runtime.worker_healthcheck": runtime.worker_healthcheck,
    "runtime.prompt_group": runtime.prompt_group,
  }
}

function value(number: number) {
  return number === 0 ? "" : String(number)
}

export function systemProblemLabel(path: string) {
  const field = systemFields.find((item) => item.path === path)
  return field?.label ?? "Runtime & Capacity"
}

export function systemProblemMessage(problem: ValidationProblem) {
  if (problem.message === "Enter a whole number.")
    return "Enter a whole number."
  if (problem.message === "must be disabled, startup_only, or startup_and_task")
    return "Choose Disabled, Startup only, or Startup and task."
  if (problem.message === "is required") return "Enter a value before Save."
  if (problem.message.includes("unknown"))
    return "Choose one of the bundled Prompt Groups."
  if (problem.message === "runtime values cannot be negative")
    return "Use non-negative values."
  if (problem.message === "runtime value exceeds its allowed range")
    return "Use values within the supported range."
  if (problem.message.includes("between"))
    return problem.message.replace("must be", "Use a value that is") + "."
  if (problem.message.includes("cannot be negative"))
    return "Use a non-negative value."
  return "Review this setting and try again."
}
