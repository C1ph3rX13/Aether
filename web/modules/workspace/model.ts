import type {
  CompletionRecordWire,
  FactWire,
  HintWire,
  IntentWire,
  ProjectDetailWire,
  ProjectWire,
} from "@/modules/workspace/wire"

export type ProjectStatus = ProjectWire["status"]
export type IntentKind = IntentWire["kind"]
export type IntentStatus = IntentWire["status"]

export interface ProjectMeta {
  id: string
  title: string
  status: ProjectStatus
  bootstrapEnabled: boolean
  createdAt: string
  generation: number
  revision: number
}

export interface Fact {
  id: string
  description: string
  status: FactWire["status"]
  supersedes?: string
  retractedAt?: string
  retractedBy?: string
  retractionReason?: string
  replacementFactId?: string
}

export interface Intent {
  id: string
  kind: IntentKind
  status: IntentStatus
  from: string[]
  to?: string
  description: string
  creator: string
  createdAt: string
  concludedAt?: string
  cancelledAt?: string
  cancelledBy?: string
  cancellationReason?: string
}

export interface Hint {
  id: string
  content: string
  creator: string
  createdAt: string
  status: HintWire["status"]
  generation: number
  withdrawnAt?: string
  withdrawnBy?: string
  withdrawalReason?: string
}

export interface CompletionRecord {
  generation: number
  intentId: string
  description: string
  actor: string
  completedAt: string
}

export interface ProjectDetail {
  project: ProjectMeta
  facts: Fact[]
  intents: Intent[]
  hints: Hint[]
  completionRecords: CompletionRecord[]
}

export function projectDetailFromWire(
  detail: ProjectDetailWire
): ProjectDetail {
  return {
    project: projectFromWire(detail.project),
    facts: detail.facts.map(factFromWire),
    intents: detail.intents.map(intentFromWire),
    hints: detail.hints.map(hintFromWire),
    completionRecords: (detail.completion_records ?? []).map(
      completionRecordFromWire
    ),
  }
}

export function projectFromWire(project: ProjectWire): ProjectMeta {
  return {
    id: project.id,
    title: project.title,
    status: project.status,
    bootstrapEnabled: project.bootstrap_enabled,
    createdAt: project.created_at,
    generation: project.generation,
    revision: project.revision,
  }
}

export function factFromWire(fact: FactWire): Fact {
  return {
    id: fact.id,
    description: fact.description,
    status: fact.status,
    ...(fact.supersedes ? { supersedes: fact.supersedes } : {}),
    ...(fact.retracted_at ? { retractedAt: fact.retracted_at } : {}),
    ...(fact.retracted_by ? { retractedBy: fact.retracted_by } : {}),
    ...(fact.retraction_reason
      ? { retractionReason: fact.retraction_reason }
      : {}),
    ...(fact.replacement_fact_id
      ? { replacementFactId: fact.replacement_fact_id }
      : {}),
  }
}

export function intentFromWire(intent: IntentWire): Intent {
  return {
    id: intent.id,
    kind: intent.kind,
    status: intent.status,
    from: intent.from,
    ...(intent.to ? { to: intent.to } : {}),
    description: intent.description,
    creator: intent.creator,
    createdAt: intent.created_at,
    ...(intent.concluded_at ? { concludedAt: intent.concluded_at } : {}),
    ...(intent.cancelled_at ? { cancelledAt: intent.cancelled_at } : {}),
    ...(intent.cancelled_by ? { cancelledBy: intent.cancelled_by } : {}),
    ...(intent.cancellation_reason
      ? { cancellationReason: intent.cancellation_reason }
      : {}),
  }
}

export function hintFromWire(hint: HintWire): Hint {
  return {
    id: hint.id,
    content: hint.content,
    creator: hint.creator,
    createdAt: hint.created_at,
    status: hint.status,
    generation: hint.generation,
    ...(hint.withdrawn_at ? { withdrawnAt: hint.withdrawn_at } : {}),
    ...(hint.withdrawn_by ? { withdrawnBy: hint.withdrawn_by } : {}),
    ...(hint.withdrawal_reason
      ? { withdrawalReason: hint.withdrawal_reason }
      : {}),
  }
}

export function completionRecordFromWire(
  record: CompletionRecordWire
): CompletionRecord {
  return {
    generation: record.generation,
    intentId: record.intent_id,
    description: record.description,
    actor: record.actor,
    completedAt: record.completed_at,
  }
}

export type FactType = "origin" | "goal" | "fact"

export function getFactType(id: string): FactType {
  if (id === "origin") return "origin"
  if (id === "goal") return "goal"
  return "fact"
}

export function projectIDFromPathname(pathname: string): string | null {
  const match = /^\/projects\/([^/]+)\/?$/.exec(pathname)
  if (!match || match[1] === "__placeholder__") return null
  try {
    const projectID = decodeURIComponent(match[1])
    return projectID && !projectID.includes("/") ? projectID : null
  } catch {
    return null
  }
}
