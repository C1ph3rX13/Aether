export interface DynamicSecretState {
  configured: boolean
  mask?: string
}

export interface DynamicWorkerDraft {
  name: string
  agent_type: string
  provider_name?: string
  task_types: string[]
  max_running: number
  priority: number
  capabilities: string[]
  agent_env: Record<string, string>
  timeout_overrides: Record<string, number>
  secrets?: Record<string, DynamicSecretState>
}

export interface DynamicRuntimeDraft {
  scheduler_interval_seconds: number
  max_workers: number
  max_running_projects: number
  max_project_workers: number
  healthcheck_timeout_seconds: number
  worker_healthcheck: string
  prompt_group: string
}

export interface DynamicPhasedTaskDraft {
  primary_seconds: number
  conclude_seconds: number
  recovery_seconds: number
}

export interface DynamicReasonTaskDraft {
  primary_seconds: number
  max_intents: number
}

export interface DynamicTasksDraft {
  bootstrap: DynamicPhasedTaskDraft
  reason: DynamicReasonTaskDraft
  explore: DynamicPhasedTaskDraft
}

export interface DynamicRetryPolicyDraft {
  max_attempts: number
  initial_interval_seconds: number
  max_interval_seconds: number
  multiplier: number
}

export interface DynamicRetryDraft {
  bootstrap: DynamicRetryPolicyDraft
  reason: DynamicRetryPolicyDraft
  explore: DynamicRetryPolicyDraft
}

export interface DynamicContainerDraft {
  image: string
  network_mode: string
  cap_add: string[]
  completed_action: string
  stdout_limit_bytes: number
  stderr_limit_bytes: number
}

export interface DynamicDraft {
  runtime: DynamicRuntimeDraft
  tasks: DynamicTasksDraft
  retry: DynamicRetryDraft
  container: DynamicContainerDraft
  workers: DynamicWorkerDraft[]
}

export interface ValidationProblem {
  path: string
  message: string
}

export interface AgentFieldMetadata {
  name: string
  display_name: string
  secret: boolean
  required: boolean
  options?: Array<{ value: string; label: string }>
}

export interface AgentDefinitionMetadata {
  type: string
  display_name: string
  required_fields: AgentFieldMetadata[]
  optional_fields?: AgentFieldMetadata[]
  capabilities: string[]
  result_contract: string
  provider_healthcheck: string
}

export interface ProviderPreset {
  id: string
  display_name: string
  agent_type: string
  provider_name: string
  protocol: string
  agent_env: Record<string, string>
}

export interface ActiveRevisionSummary {
  id: string
}

export interface ConfigurationRead {
  deployment_id: string
  draft_version: number
  draft: DynamicDraft
  active_revision?: ActiveRevisionSummary
  validation_problems: ValidationProblem[]
  agent_definitions: AgentDefinitionMetadata[]
  provider_presets: ProviderPreset[]
  prompt_groups: string[]
  recommended_draft: DynamicDraft
}

export function normalizeConfigurationRead(
  read: ConfigurationRead
): ConfigurationRead {
  return {
    ...read,
    draft: normalizeDynamicDraft(read.draft),
    recommended_draft: normalizeDynamicDraft(read.recommended_draft),
    validation_problems: read.validation_problems ?? [],
  }
}

export function normalizeDynamicDraft(source: DynamicDraft): DynamicDraft {
  const draft = structuredClone(source)
  draft.workers ??= []
  draft.container.cap_add ??= []
  for (const worker of draft.workers) {
    worker.task_types ??= []
    worker.capabilities ??= []
    worker.agent_env ??= {}
    worker.timeout_overrides ??= {}
  }
  return draft
}

/** Secret read projections are not part of a complete Draft write. */
export function stripSecretProjections(source: DynamicDraft): DynamicDraft {
  const draft = normalizeDynamicDraft(source)
  for (const worker of draft.workers) delete worker.secrets
  return draft
}
