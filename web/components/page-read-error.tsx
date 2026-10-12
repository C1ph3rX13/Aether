import { IconAlertCircle as CircleAlert } from "@tabler/icons-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"

/** Shared read recovery presentation; the page owns its request and saved edits. */
export function PageReadError({
  title,
  description,
  onRetry,
  disabled = false,
}: {
  title: string
  description: string
  onRetry: () => void
  disabled?: boolean
}) {
  return (
    <Alert variant="destructive">
      <CircleAlert aria-hidden="true" />
      <AlertTitle
        role="heading"
        aria-level={2}
        className="min-w-0 wrap-anywhere"
      >
        {title}
      </AlertTitle>
      <AlertDescription className="min-w-0 wrap-anywhere">
        <p>{description}</p>
        <Button variant="outline" disabled={disabled} onClick={onRetry}>
          Retry
        </Button>
      </AlertDescription>
    </Alert>
  )
}
