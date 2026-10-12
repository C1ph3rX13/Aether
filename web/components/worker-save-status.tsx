import Link from "next/link"
import { IconCheck as Check } from "@tabler/icons-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
export function WorkerSaveStatus() {
  return (
    <Alert role="status">
      <Check aria-hidden="true" />
      <AlertTitle>Worker saved.</AlertTitle>
      <AlertDescription>
        This Worker is effective for new Projects. Existing Projects keep their
        settings.
        <Button
          variant="outline"
          size="default"
          className="mt-2 w-fit"
          role="link"
          asChild
        >
          <Link href="/settings/runtime-capacity">Review saved settings</Link>
        </Button>
      </AlertDescription>
    </Alert>
  )
}
