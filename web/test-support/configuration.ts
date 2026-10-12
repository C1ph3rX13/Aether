import type { ConfigurationRead, DynamicDraft } from "@/lib/configuration"

export function configuration(): ConfigurationRead {
  const draft: DynamicDraft = {
    runtime: {
      scheduler_interval_seconds: 7,
      max_workers: 9,
      max_running_projects: 3,
      max_project_workers: 4,
      healthcheck_timeout_seconds: 20,
      worker_healthcheck: "startup_only",
      prompt_group: "default",
    },
    tasks: {
      bootstrap: {
        primary_seconds: 600,
        conclude_seconds: 90,
        recovery_seconds: 30,
      },
      reason: { primary_seconds: 450, max_intents: 2 },
      explore: {
        primary_seconds: 600,
        conclude_seconds: 90,
        recovery_seconds: 30,
      },
    },
    retry: {
      bootstrap: {
        max_attempts: 3,
        initial_interval_seconds: 30,
        max_interval_seconds: 300,
        multiplier: 2,
      },
      reason: {
        max_attempts: 3,
        initial_interval_seconds: 30,
        max_interval_seconds: 300,
        multiplier: 2,
      },
      explore: {
        max_attempts: 3,
        initial_interval_seconds: 30,
        max_interval_seconds: 300,
        multiplier: 2,
      },
    },
    container: {
      image: "ghcr.io/c1ph3rx13/aether-worker:latest",
      network_mode: "host",
      cap_add: [],
      completed_action: "stop",
      stdout_limit_bytes: 1048576,
      stderr_limit_bytes: 1048576,
    },
    workers: [
      {
        name: "worker-a",
        agent_type: "codex",
        task_types: ["bootstrap", "reason", "explore"],
        max_running: 2,
        priority: 1,
        capabilities: [],
        agent_env: { CODEX_MODEL: "test-model", CODEX_BASE_URL: "https://provider.example" },
        secrets: { OPENAI_API_KEY: { configured: true, mask: "********" } },
        timeout_overrides: {},
      },
    ],
  }
  return {
    deployment_id: "private-deployment",
    draft_version: 11,
    draft,
    recommended_draft: structuredClone(draft),
    validation_problems: [],
    agent_definitions: [],
    provider_presets: [],
    prompt_groups: ["default", "mock"],
  }
}
