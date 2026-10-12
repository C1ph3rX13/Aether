import {
  IconCircleCheck as CheckCircle2,
  IconCircleCheck as CircleCheck,
  IconCircleMinus as CircleMinus,
  IconCircleX as CircleX,
  IconDatabase as Database,
  IconFolderPlus as FolderPlus,
  IconBulb as Lightbulb,
  IconBulbOff as LightbulbOff,
  IconPlayerPause as Pause,
  IconPencil as Pencil,
  IconPlayerPlay as Play,
  IconReplace as Replace,
  IconRotate as RotateCcw,
  IconRoute as Route,
  type Icon,
} from "@tabler/icons-react"
import type {
  ConsoleLogEvent as ConsoleLogEventWire,
  ConsoleLogEventKind,
  ConsoleLogFocusTarget,
  ConsoleLogPage as ConsoleLogPageWire,
  ConsoleLogRevision as ConsoleLogRevisionWire,
  ConsoleLogSourceKind,
} from "@/modules/log/wire"
import { formatDateTime } from "@/lib/format"

export type {
  ConsoleLogEventKind,
  ConsoleLogFocusTarget,
  ConsoleLogSourceKind,
} from "@/modules/log/wire"

export type ConsoleLogTone =
  "project" | "hint" | "intent" | "fact" | "migration"

export interface ConsoleLogEventView {
  id: string
  kind: ConsoleLogEventKind
  kindLabel: string
  label: string
  icon: Icon
  tone: ConsoleLogTone
  focusTarget?: ConsoleLogFocusTarget
}

export interface ConsoleLogRevisionView {
  revision: number
  generation: number
  occurredAt: string
  occurredAtLabel: string
  sourceKind: ConsoleLogSourceKind
  sourceLabel: string
  runId?: string
  events: ConsoleLogEventView[]
}

export interface ConsoleLogPage {
  projectId: string
  availableFromRevision: number
  availableFromOccurredAt: string
  currentRevision: number
  currentRevisionOccurredAt: string
  revisions: ConsoleLogRevisionView[]
  nextBeforeRevision?: number
}

const presentation: Record<
  ConsoleLogEventKind,
  {
    kindLabel: string
    icon: Icon
    tone: ConsoleLogTone
  }
> = {
  project_created: {
    kindLabel: "Project created",
    icon: FolderPlus,
    tone: "project",
  },
  project_renamed: {
    kindLabel: "Project renamed",
    icon: Pencil,
    tone: "project",
  },
  project_paused: { kindLabel: "Project paused", icon: Pause, tone: "project" },
  project_resumed: {
    kindLabel: "Project resumed",
    icon: Play,
    tone: "project",
  },
  project_reopened: {
    kindLabel: "Project reopened",
    icon: RotateCcw,
    tone: "project",
  },
  project_completed: {
    kindLabel: "Project completed",
    icon: CheckCircle2,
    tone: "project",
  },
  hint_created: { kindLabel: "Hint added", icon: Lightbulb, tone: "hint" },
  hint_withdrawn: {
    kindLabel: "Hint withdrawn",
    icon: LightbulbOff,
    tone: "hint",
  },
  intent_created: { kindLabel: "Intent declared", icon: Route, tone: "intent" },
  intent_concluded: {
    kindLabel: "Intent concluded",
    icon: CircleCheck,
    tone: "intent",
  },
  intent_cancelled: {
    kindLabel: "Intent cancelled",
    icon: CircleX,
    tone: "intent",
  },
  fact_retracted: {
    kindLabel: "Fact retracted",
    icon: CircleMinus,
    tone: "fact",
  },
  fact_superseded: {
    kindLabel: "Fact superseded",
    icon: Replace,
    tone: "fact",
  },
  migration: { kindLabel: "Migration", icon: Database, tone: "migration" },
}

export function consoleLogPageFromWire(
  page: ConsoleLogPageWire
): ConsoleLogPage {
  return {
    projectId: page.project_id,
    availableFromRevision: page.available_from_revision,
    availableFromOccurredAt: page.available_from_occurred_at,
    currentRevision: page.current_revision,
    currentRevisionOccurredAt: page.current_revision_occurred_at,
    revisions: page.revisions.map(consoleLogRevisionFromWire),
    ...(page.next_before_revision !== undefined
      ? { nextBeforeRevision: page.next_before_revision }
      : {}),
  }
}

export function consoleLogRevisionFromWire(
  revision: ConsoleLogRevisionWire
): ConsoleLogRevisionView {
  return {
    revision: revision.revision,
    generation: revision.generation,
    occurredAt: revision.occurred_at,
    occurredAtLabel: formatDateTime(revision.occurred_at),
    sourceKind: revision.source.kind,
    sourceLabel: revision.source.name,
    ...(revision.source.run_id ? { runId: revision.source.run_id } : {}),
    events: revision.events.map(consoleLogEventFromWire),
  }
}

export function consoleLogEventFromWire(
  event: ConsoleLogEventWire
): ConsoleLogEventView {
  const config = presentation[event.kind]
  return {
    id: event.event_id,
    kind: event.kind,
    kindLabel: config.kindLabel,
    label: eventLabel(event),
    icon: config.icon,
    tone: config.tone,
    ...(event.focus_target ? { focusTarget: event.focus_target } : {}),
  }
}

function eventLabel(event: ConsoleLogEventWire): string {
  switch (event.kind) {
    case "project_created":
      return event.title
    case "project_renamed":
      return `${event.previous_title} to ${event.title}`
    case "project_paused":
    case "project_resumed":
      return `${event.from_status} to ${event.to_status}`
    case "project_reopened":
      return `Generation ${event.previous_generation} to Generation ${event.current_generation}`
    case "project_completed":
      return event.description
    case "hint_created":
    case "hint_withdrawn":
      return event.content
    case "intent_created":
    case "intent_concluded":
    case "intent_cancelled":
      return event.description
    case "fact_retracted":
      return event.description
    case "fact_superseded":
      return event.replacement_fact.description
    case "migration":
      return `Legacy ${event.original_change_kind.replaceAll("_", " ")}`
  }
}
