import type { IntentWire, ProjectWire } from "@/modules/workspace/wire"

export type ConsoleLogSourceKind = "operator" | "run" | "migration"

export interface ConsoleLogSource {
  kind: ConsoleLogSourceKind
  name: string
  run_id?: string
}

export type ConsoleLogFocusType = "fact" | "intent"

export interface ConsoleLogFocusTarget {
  type: ConsoleLogFocusType
  id: string
}

export type ConsoleLogEventKind =
  | "project_created"
  | "project_renamed"
  | "project_paused"
  | "project_resumed"
  | "project_reopened"
  | "project_completed"
  | "hint_created"
  | "hint_withdrawn"
  | "intent_created"
  | "intent_concluded"
  | "intent_cancelled"
  | "fact_retracted"
  | "fact_superseded"
  | "migration"

export type ConsoleLogChangeKind =
  | "project_baseline"
  | "project_created"
  | "project_renamed"
  | "project_paused"
  | "project_resumed"
  | "intent_created"
  | "intent_concluded"
  | "intent_cancelled"
  | "hint_created"
  | "hint_withdrawn"
  | "fact_retracted"
  | "fact_superseded"
  | "project_completed"
  | "project_reopened"
  | "intents_proposed"
  | "bootstrap_completed"

export interface ConsoleLogEventBase<Kind extends ConsoleLogEventKind> {
  event_id: string
  kind: Kind
  focus_target?: ConsoleLogFocusTarget
}

export interface ConsoleFactReference {
  id: string
  description: string
}

export interface ProjectCreatedEvent extends ConsoleLogEventBase<"project_created"> {
  project_id: string
  title: string
  origin: ConsoleFactReference
  goal: ConsoleFactReference
}

export interface ProjectRenamedEvent extends ConsoleLogEventBase<"project_renamed"> {
  project_id: string
  previous_title: string
  title: string
}

export interface ProjectPausedEvent extends ConsoleLogEventBase<"project_paused"> {
  project_id: string
  from_status: ProjectWire["status"]
  to_status: ProjectWire["status"]
}

export interface ProjectResumedEvent extends ConsoleLogEventBase<"project_resumed"> {
  project_id: string
  from_status: ProjectWire["status"]
  to_status: ProjectWire["status"]
}

export interface ProjectReopenedEvent extends ConsoleLogEventBase<"project_reopened"> {
  project_id: string
  previous_generation: number
  current_generation: number
  feedback_hint_id: string
}

export interface ProjectCompletedEvent extends ConsoleLogEventBase<"project_completed"> {
  project_id: string
  completion_intent_id: string
  description: string
  source_fact_ids: string[]
  completion_generation: number
  affected_run_count?: number
}

export interface HintCreatedEvent extends ConsoleLogEventBase<"hint_created"> {
  hint_id: string
  content: string
  creator: string
  generation: number
  status: string
  affected_run_count?: number
}

export interface HintWithdrawnEvent extends ConsoleLogEventBase<"hint_withdrawn"> {
  hint_id: string
  content: string
  generation: number
  withdrawn_by: string
  reason: string
  affected_run_count?: number
}

export interface IntentCreatedEvent extends ConsoleLogEventBase<"intent_created"> {
  intent_id: string
  intent_kind: IntentWire["kind"]
  status: IntentWire["status"]
  description: string
  creator: string
  source_fact_ids: string[]
  target_fact_id?: string
}

export interface IntentConcludedEvent extends ConsoleLogEventBase<"intent_concluded"> {
  intent_id: string
  intent_kind: IntentWire["kind"]
  description: string
  source_fact_ids: string[]
  produced_fact: ConsoleFactReference
}

export interface IntentCancelledEvent extends ConsoleLogEventBase<"intent_cancelled"> {
  intent_id: string
  intent_kind: IntentWire["kind"]
  description: string
  source_fact_ids: string[]
  cancelled_by: string
  reason: string
  affected_run_count?: number
}

export interface FactRetractedEvent extends ConsoleLogEventBase<"fact_retracted"> {
  fact_id: string
  description: string
  retracted_by: string
  reason: string
  replacement_fact_id?: string
  affected_run_count?: number
}

export interface FactSupersededEvent extends ConsoleLogEventBase<"fact_superseded"> {
  original_fact: ConsoleFactReference
  replacement_fact: ConsoleFactReference
  actor: string
  reason: string
  affected_run_count?: number
}

export interface MigrationEvent extends ConsoleLogEventBase<"migration"> {
  original_change_kind: ConsoleLogChangeKind
  schema_version: number
  entity_type: string
  entity_id?: string
}

export type ConsoleLogEvent =
  | ProjectCreatedEvent
  | ProjectRenamedEvent
  | ProjectPausedEvent
  | ProjectResumedEvent
  | ProjectReopenedEvent
  | ProjectCompletedEvent
  | HintCreatedEvent
  | HintWithdrawnEvent
  | IntentCreatedEvent
  | IntentConcludedEvent
  | IntentCancelledEvent
  | FactRetractedEvent
  | FactSupersededEvent
  | MigrationEvent

export interface ConsoleLogRevision {
  revision: number
  generation: number
  change_kind: ConsoleLogChangeKind
  occurred_at: string
  source: ConsoleLogSource
  events: ConsoleLogEvent[]
}

export interface ConsoleLogPage {
  project_id: string
  available_from_revision: number
  available_from_occurred_at: string
  current_revision: number
  current_revision_occurred_at: string
  revisions: ConsoleLogRevision[]
  next_before_revision?: number
}
