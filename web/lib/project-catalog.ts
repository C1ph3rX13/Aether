import { getJSON, getText, postJSON } from "@/lib/api"

export type ProjectStatus = "active" | "paused" | "completed"
export type ProjectExecutionStatus =
  "retry_exhausted" | "failed_or_interrupted" | "retrying" | "running" | "idle"

export interface CatalogProject {
  id: string
  title: string
  status: ProjectStatus
  createdAt: string
  generation: number
  revision: number
  blackboard: {
    facts: { total: number; current: number }
    intents: { total: number; current: number }
    hints: { total: number; current: number }
  }
  execution: {
    status: ProjectExecutionStatus
  }
}

interface CatalogProjectWire {
  project?: {
    id?: unknown
    title?: unknown
    status?: unknown
    created_at?: unknown
    generation?: unknown
    revision?: unknown
  }
  blackboard?: {
    facts?: { total?: unknown; active?: unknown }
    intents?: { total?: unknown; open?: unknown }
    hints?: { total?: unknown; active?: unknown }
  }
  execution?: {
    status?: unknown
  }
}

function isNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value)
}

export function catalogProjectFromWire(value: unknown): CatalogProject | null {
  if (!value || typeof value !== "object") return null
  const entry = value as CatalogProjectWire
  const project = entry.project
  if (
    !project ||
    typeof project !== "object" ||
    typeof project.id !== "string" ||
    typeof project.title !== "string" ||
    (project.status !== "active" &&
      project.status !== "paused" &&
      project.status !== "completed") ||
    typeof project.created_at !== "string" ||
    !isNumber(project.generation) ||
    !isNumber(project.revision)
  ) {
    return null
  }
  const blackboard = entry.blackboard
  const execution = entry.execution
  if (
    !blackboard ||
    !execution ||
    !blackboard.facts ||
    !blackboard.intents ||
    !blackboard.hints
  )
    return null
  const facts = blackboard.facts
  const intents = blackboard.intents
  const hints = blackboard.hints
  if (
    !isNumber(facts.total) ||
    !isNumber(facts.active) ||
    !isNumber(intents.total) ||
    !isNumber(intents.open) ||
    !isNumber(hints.total) ||
    !isNumber(hints.active) ||
    ![
      "retry_exhausted",
      "failed_or_interrupted",
      "retrying",
      "running",
      "idle",
    ].includes(execution.status as string)
  )
    return null
  return {
    id: project.id,
    title: project.title,
    status: project.status as ProjectStatus,
    createdAt: project.created_at,
    generation: project.generation,
    revision: project.revision,
    blackboard: {
      facts: { total: facts.total, current: facts.active },
      intents: { total: intents.total, current: intents.open },
      hints: { total: hints.total, current: hints.active },
    },
    execution: {
      status: execution.status as ProjectExecutionStatus,
    },
  }
}

export async function readProjectCatalog(signal?: AbortSignal) {
  const result = await getJSON<unknown[]>("/projects", signal)
  if (!Array.isArray(result))
    throw new Error("Project Catalog response is unreadable")
  const projects = result.map(catalogProjectFromWire)
  if (projects.some((project) => project === null))
    throw new Error("Project Catalog response is unreadable")
  return projects as CatalogProject[]
}

interface ProjectCommandReceipt {
  project_revision: number
  [key: string]: unknown
}

export interface CreateProjectInput {
  title: string
  origin: string
  goal: string
  bootstrapEnabled: boolean
  hints?: Array<{ content: string }>
}

export async function createProject(input: CreateProjectInput) {
  return postJSON<{ project?: { id?: unknown } }>("/projects", {
    title: input.title,
    origin: input.origin,
    goal: input.goal,
    bootstrap_enabled: input.bootstrapEnabled,
    ...(input.hints?.length ? { hints: input.hints } : {}),
  })
}

export async function renameProject(
  id: string,
  title: string,
  expectedRevision: number
) {
  return postJSON<ProjectCommandReceipt>(
    `/projects/${encodeURIComponent(id)}/actions/rename`,
    {
      title,
      expected_revision: expectedRevision,
    }
  )
}

export async function deleteProject(
  id: string,
  expectedTitle: string,
  expectedRevision: number
) {
  return postJSON<ProjectCommandReceipt>(
    `/projects/${encodeURIComponent(id)}/actions/delete`,
    {
      expected_title: expectedTitle,
      expected_revision: expectedRevision,
    }
  )
}

export async function setProjectStatus(
  id: string,
  status: "paused" | "active",
  expectedRevision: number
) {
  const action = status === "paused" ? "pause" : "resume"
  return postJSON<ProjectCommandReceipt>(
    `/projects/${encodeURIComponent(id)}/actions/${action}`,
    {
      expected_revision: expectedRevision,
    }
  )
}

export async function reopenProject(
  id: string,
  feedback: string,
  expectedRevision: number
) {
  return postJSON<ProjectCommandReceipt>(
    `/projects/${encodeURIComponent(id)}/actions/reopen`,
    {
      feedback,
      expected_revision: expectedRevision,
    }
  )
}

export async function exportProject(id: string, format: "yaml" | "json") {
  return getText(`/projects/${encodeURIComponent(id)}/export?format=${format}`)
}
