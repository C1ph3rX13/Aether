export interface FactWire {
  id: string
  description: string
  status: "active" | "retracted"
  supersedes?: string
  retracted_at?: string
  retracted_by?: string
  retraction_reason?: string
  replacement_fact_id?: string
}

export interface IntentWire {
  id: string
  kind: "bootstrap" | "explore" | "completion"
  status: "open" | "concluded" | "cancelled"
  from: string[]
  to?: string
  description: string
  creator: string
  created_at: string
  concluded_at?: string
  cancelled_at?: string
  cancelled_by?: string
  cancellation_reason?: string
}

export interface HintWire {
  id: string
  content: string
  creator: string
  created_at: string
  status: "active" | "withdrawn"
  generation: number
  withdrawn_at?: string
  withdrawn_by?: string
  withdrawal_reason?: string
}

export interface ProjectWire {
  id: string
  title: string
  status: "active" | "paused" | "completed"
  bootstrap_enabled: boolean
  created_at: string
  generation: number
  revision: number
}

export interface CompletionRecordWire {
  generation: number
  intent_id: string
  description: string
  actor: string
  completed_at: string
}

export interface ProjectDetailWire {
  project: ProjectWire
  facts: FactWire[]
  intents: IntentWire[]
  hints: HintWire[]
  completion_records?: CompletionRecordWire[]
}

export interface ProjectEventWire {
  type: string
  project_id: string
  revision?: number
  data?: unknown
}
