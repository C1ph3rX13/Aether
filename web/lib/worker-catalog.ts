import { IconBrandOpenai, IconMessage, IconRobot } from "@tabler/icons-react"
import type { ConfigurationRead } from "@/lib/configuration"

export type AgentType = "codex" | "claude_code" | "pi"
export const workerIcons = {
  codex: IconBrandOpenai,
  claude_code: IconMessage,
  pi: IconRobot,
} satisfies Record<AgentType, typeof IconRobot>

export const workerBranches = {
  codex: { label: "Codex", href: "/workers/codex" },
  claude_code: { label: "Claude", href: "/workers/claude" },
  pi: { label: "Pi", href: "/workers/pi" },
} satisfies Record<AgentType, { label: string; href: string }>

// Keep validation indices in the complete Draft, before filtering by Agent Type.
export function workerNeedsSetup(read: ConfigurationRead, index: number) {
  const prefix = `workers[${index}]`
  return read.validation_problems.some(
    (problem) =>
      problem.path === prefix ||
      problem.path.startsWith(`${prefix}.`) ||
      problem.path === `workers.${read.draft.workers[index].name}`
  )
}
