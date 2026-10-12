import type {
  Fact,
  Hint,
  Intent,
  ProjectDetail,
} from "@/modules/workspace/model"
import type { ConsoleFreshnessStatus } from "@/modules/workspace/live-sync"

export type WorkspaceMode = "live" | "replay"
export type RecordReference = { type: "fact" | "intent" | "hint"; id: string }
export type RecordSelection =
  | { type: "fact"; fact: Fact; producingIntents: Intent[] }
  | { type: "intent"; intent: Intent }
  | { type: "hint"; hint: Hint }
  | { type: "missing"; reference: RecordReference }

export interface WorkspaceProjection {
  mode: WorkspaceMode
  detail: ProjectDetail
  freshness: ConsoleFreshnessStatus
  selection: RecordSelection | null
  completionFactIds: string[]
}

export function selectRecord(
  detail: ProjectDetail,
  reference: RecordReference | null
): RecordSelection | null {
  if (!reference) return null
  if (reference.type === "fact") {
    const fact = detail.facts.find((record) => record.id === reference.id)
    if (fact)
      return {
        type: "fact",
        fact,
        producingIntents: detail.intents.filter(
          (intent) => intent.to === fact.id
        ),
      }
  } else if (reference.type === "intent") {
    const intent = detail.intents.find((record) => record.id === reference.id)
    if (intent) return { type: "intent", intent }
  } else {
    const hint = detail.hints.find((record) => record.id === reference.id)
    if (hint) return { type: "hint", hint }
  }
  return { type: "missing", reference }
}
