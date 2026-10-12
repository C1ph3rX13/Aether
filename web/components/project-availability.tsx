"use client"
import {
  IconAlertCircle as CircleAlert,
  IconInfoCircle as Info,
} from "@tabler/icons-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { useProjectAvailability } from "@/hooks/use-project-availability"
import type { ProjectAvailability } from "@/lib/project-availability"
import Link from "next/link"
const messages = {
  checking: [
    "Checking Project availability",
    "Checking whether Projects can be created.",
  ],
  "configuration-required": [
    "Project creation unavailable",
    "Complete and save Worker task coverage to enable Project creation.",
  ],
  ready: [
    "Project creation ready",
    "Saved settings are ready for new Projects.",
  ],
  "dispatcher-unavailable": [
    "Project creation ready",
    "New Projects will wait until execution returns.",
  ],
  "database-unavailable": [
    "Project creation unavailable",
    "Could not read Project data. Retry after the service recovers.",
  ],
  unreadable: [
    "Cannot check Project availability",
    "Could not read creation availability. Retry to check again.",
  ],
  "transport-failure": [
    "Cannot check Project availability",
    "Could not reach Aether. Check your connection and retry.",
  ],
} as const
export function ProjectAvailabilityStatus({
  refreshToken = 0,
  compact = false,
}: {
  refreshToken?: number
  compact?: boolean
}) {
  const { state, missingTasks, refresh } = useProjectAvailability(refreshToken)
  if (compact)
    return (
      <p
        role="status"
        className="mt-2 flex flex-wrap items-center gap-2 text-sm text-muted-foreground"
      >
        {messages[state][0]}
        {state === "configuration-required" &&
          missingTasks.length > 0 &&
          ` — Missing Worker capabilities: ${missingTasks.join(", ")}.`}
        {["unreadable", "transport-failure", "database-unavailable"].includes(
          state
        ) && (
          <Button
            variant="outline"
            size="default"
            onClick={() => void refresh()}
            aria-label="Retry creation availability"
          >
            Retry
          </Button>
        )}
      </p>
    )
  return (
    <ProjectAvailabilityNotice
      state={state}
      missingTasks={missingTasks}
      refresh={refresh}
    />
  )
}
export function ProjectAvailabilityNotice({
  state,
  refresh,
  showSettingsAction = false,
  missingTasks = [],
}: {
  missingTasks?: string[]
  state: ProjectAvailability
  refresh: () => Promise<void>
  showSettingsAction?: boolean
}) {
  const [title, description] = messages[state]
  const failed = [
    "unreadable",
    "transport-failure",
    "database-unavailable",
  ].includes(state)
  const Icon = failed ? CircleAlert : Info
  return (
    <Alert role={failed ? "alert" : "status"}>
      <Icon aria-hidden="true" />
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>
        {state === "configuration-required" && missingTasks.length
          ? `Missing Worker capabilities: ${missingTasks.join(", ")}. Add or edit Workers to cover these tasks.`
          : description}
      </AlertDescription>
      {showSettingsAction && state === "configuration-required" && (
        <Button
          variant="outline"
          size="default"
          className="mt-2 w-fit"
          role="link"
          asChild
        >
          <Link href="/workers/codex">Open Workers</Link>
        </Button>
      )}
      {failed && (
        <AlertDescription className="mt-3">
          <Button
            variant="outline"
            size="default"
            onClick={() => void refresh()}
            aria-label="Retry creation availability"
          >
            Retry
          </Button>
        </AlertDescription>
      )}
    </Alert>
  )
}
