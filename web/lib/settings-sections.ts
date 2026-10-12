import {
  stripSecretProjections,
  type DynamicDraft,
  type ValidationProblem,
} from "@/lib/configuration"
import {
  containerFields,
  readProjectForm,
  retryGroups,
  taskFields,
  type ProjectField,
} from "@/lib/project-settings-form"
import {
  readSystemForm,
  systemFields,
  type SystemField,
} from "@/lib/system-settings-form"

export type SettingsSectionID =
  | "task-timeouts"
  | "retry-policies"
  | "project-runtime"
  | "runtime-capacity"
  | "worker-healthchecks"
  | "prompt-group"
export type SettingsField = ProjectField | SystemField
export type SettingsForm = Record<string, string>

export const settingsSections = [
  {
    id: "task-timeouts",
    title: "Task Timeouts",
    description: "Phase deadlines and Reason task limits for new Projects.",
    fields: taskFields,
  },
  {
    id: "retry-policies",
    title: "Retry Policies",
    description: "Retry attempts, delays and backoff for each task.",
    fields: retryGroups.flatMap((group) =>
      group.fields.map((field) => ({
        ...field,
        label: `${group.title} ${field.label[0].toLowerCase()}${field.label.slice(1)}`,
      }))
    ),
  },
  {
    id: "project-runtime",
    title: "Project Runtime",
    description: "Container and output settings for new Projects.",
    fields: containerFields,
  },
  {
    id: "runtime-capacity",
    title: "Runtime & Capacity",
    description: "Scheduling frequency and concurrency limits.",
    fields: systemFields.slice(0, 4),
  },
  {
    id: "worker-healthchecks",
    title: "Worker Healthchecks",
    description: "When Providers are checked and how long checks can run.",
    fields: systemFields.slice(4, 6),
  },
  {
    id: "prompt-group",
    title: "Prompt Group",
    description: "Bundled prompts for Bootstrap, Reason and Explore.",
    fields: systemFields.slice(6),
  },
] as const satisfies ReadonlyArray<{
  id: SettingsSectionID
  title: string
  description: string
  fields: readonly SettingsField[]
}>

export const settingsCategories = [
  {
    id: "tasks",
    title: "Tasks",
    description: "Task behavior for new Projects.",
    sections: ["task-timeouts", "retry-policies"],
  },
  {
    id: "runtime",
    title: "Runtime",
    description:
      "Project environments, execution capacity and Provider checks.",
    sections: ["project-runtime", "runtime-capacity", "worker-healthchecks"],
  },
  {
    id: "prompts",
    title: "Prompts",
    description: "Bundled prompts for new Projects.",
    sections: ["prompt-group"],
  },
] as const satisfies ReadonlyArray<{
  id: string
  title: string
  description: string
  sections: readonly SettingsSectionID[]
}>

export function settingsCategory(id: SettingsSectionID) {
  return settingsCategories.find((category) =>
    (category.sections as readonly SettingsSectionID[]).includes(id)
  )!
}

export const settingsTabLabels: Record<SettingsSectionID, string> = {
  "task-timeouts": "Task Timeouts",
  "retry-policies": "Retry Policies",
  "project-runtime": "Project",
  "runtime-capacity": "Capacity",
  "worker-healthchecks": "Healthchecks",
  "prompt-group": "Prompt Group",
}

export const settingsScopeDescriptions: Record<SettingsSectionID, string> = {
  "task-timeouts":
    "After Save, new Projects use these task limits. Existing Projects keep their settings.",
  "retry-policies":
    "After Save, new Projects use these retry policies. Existing Projects keep their settings.",
  "project-runtime":
    "After Save, new Projects use these container and output settings. Existing Projects keep their settings.",
  "runtime-capacity":
    "After Save, scheduling and global limits affect work that starts next. The Project Active Run limit applies to new Projects; running work continues.",
  "worker-healthchecks":
    "After Save, new Projects use these Provider check settings. Existing Projects keep their settings.",
  "prompt-group":
    "After Save, new Projects use this Prompt Group. Existing Projects keep their settings.",
}

export function settingsSection(id: SettingsSectionID) {
  return settingsSections.find((section) => section.id === id)!
}

export function settingsFieldGroups(id: SettingsSectionID): Array<{
  title: string
  fields: readonly SettingsField[]
}> {
  const fields = settingsSection(id).fields
  if (id === "task-timeouts" || id === "retry-policies") {
    const prefix = id === "task-timeouts" ? "tasks" : "retry"
    return ["bootstrap", "reason", "explore"].map((task) => ({
      title: task[0].toUpperCase() + task.slice(1),
      fields: fields.filter((field) =>
        field.path.startsWith(`${prefix}.${task}.`)
      ),
    }))
  }
  if (id === "project-runtime")
    return [
      { title: "Container", fields: fields.slice(0, 4) },
      { title: "Output limits", fields: fields.slice(4) },
    ]
  if (id === "runtime-capacity")
    return [
      { title: "Scheduling", fields: fields.slice(0, 1) },
      { title: "Global capacity", fields: fields.slice(1, 3) },
      { title: "Project capacity", fields: fields.slice(3) },
    ]
  return [{ title: settingsSection(id).title, fields }]
}

export function sectionOwnsProblem(id: SettingsSectionID, path: string) {
  return settingsSection(id).fields.some(
    (field) =>
      field.path === path ||
      field.path.startsWith(`${path}.`) ||
      path.startsWith(`${field.path}[`) ||
      path.startsWith(`${field.path}.`)
  )
}

export function readSettingsForm(
  draft: DynamicDraft,
  id: SettingsSectionID
): SettingsForm {
  const values: Record<string, string> = {
    ...readProjectForm(draft),
    ...readSystemForm(draft),
  }
  return Object.fromEntries(
    settingsSection(id).fields.map((field) => [field.path, values[field.path]])
  )
}

export function validateSettingsForm(
  form: SettingsForm,
  id: SettingsSectionID
): ValidationProblem[] {
  return settingsSection(id).fields.flatMap((field) => {
    if (field.kind !== "integer" && field.kind !== "decimal") return []
    const value = form[field.path].trim()
    if (value === "") return []
    const number = Number(value)
    if (
      Number.isFinite(number) &&
      (field.kind !== "integer" || Number.isSafeInteger(number))
    )
      return []
    return [
      {
        path: field.path,
        message:
          field.kind === "integer"
            ? "Enter a whole number."
            : "Enter a number.",
      },
    ]
  })
}

export function buildSettingsDraft(
  baseline: DynamicDraft,
  form: SettingsForm,
  id: SettingsSectionID
): DynamicDraft {
  const draft = stripSecretProjections(baseline)
  const number = (path: string) => Number(form[path].trim())
  switch (id) {
    case "task-timeouts": {
      const phased = (task: "bootstrap" | "explore") => ({
        primary_seconds: number(`tasks.${task}.primary_seconds`),
        conclude_seconds: number(`tasks.${task}.conclude_seconds`),
        recovery_seconds: number(`tasks.${task}.recovery_seconds`),
      })
      return {
        ...draft,
        tasks: {
          bootstrap: phased("bootstrap"),
          reason: {
            primary_seconds: number("tasks.reason.primary_seconds"),
            max_intents: number("tasks.reason.max_intents"),
          },
          explore: phased("explore"),
        },
      }
    }
    case "retry-policies": {
      const retry = (task: "bootstrap" | "reason" | "explore") => ({
        max_attempts: number(`retry.${task}.max_attempts`),
        initial_interval_seconds: number(
          `retry.${task}.initial_interval_seconds`
        ),
        max_interval_seconds: number(`retry.${task}.max_interval_seconds`),
        multiplier: number(`retry.${task}.multiplier`),
      })
      return {
        ...draft,
        retry: {
          bootstrap: retry("bootstrap"),
          reason: retry("reason"),
          explore: retry("explore"),
        },
      }
    }
    case "project-runtime":
      return {
        ...draft,
        container: {
          image: form["container.image"],
          network_mode: form["container.network_mode"],
          cap_add: form["container.cap_add"]
            .split(",")
            .map((value) => value.trim())
            .filter(Boolean),
          completed_action: form["container.completed_action"],
          stdout_limit_bytes: number("container.stdout_limit_bytes"),
          stderr_limit_bytes: number("container.stderr_limit_bytes"),
        },
      }
    case "runtime-capacity":
      return {
        ...draft,
        runtime: {
          ...draft.runtime,
          scheduler_interval_seconds: number(
            "runtime.scheduler_interval_seconds"
          ),
          max_workers: number("runtime.max_workers"),
          max_running_projects: number("runtime.max_running_projects"),
          max_project_workers: number("runtime.max_project_workers"),
        },
      }
    case "worker-healthchecks":
      return {
        ...draft,
        runtime: {
          ...draft.runtime,
          healthcheck_timeout_seconds: number(
            "runtime.healthcheck_timeout_seconds"
          ),
          worker_healthcheck: form["runtime.worker_healthcheck"],
        },
      }
    case "prompt-group":
      return {
        ...draft,
        runtime: {
          ...draft.runtime,
          prompt_group: form["runtime.prompt_group"],
        },
      }
  }
}
