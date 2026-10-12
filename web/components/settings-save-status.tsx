import { Badge } from "@/components/ui/badge"
import { FieldDescription } from "@/components/ui/field"

export function SettingsSaveStatus({
  page,
  dirty,
  saving,
  loading,
  saved,
}: {
  page: string
  dirty: boolean
  saving: boolean
  loading: boolean
  saved: boolean
}) {
  return (
    <div
      role="status"
      aria-label={`${page} settings save status`}
      className="flex min-w-0 flex-col items-start gap-2"
    >
      <Badge variant={dirty ? "secondary" : "outline"}>
        {saving
          ? "Saving settings..."
          : loading
            ? "Loading latest settings..."
            : dirty
              ? "Unsaved changes"
              : saved
                ? `${page} saved.`
                : "No unsaved changes"}
      </Badge>
      {!saving && !loading && (
        <FieldDescription>
          {dirty
            ? "Save to make these changes effective."
            : "Saved settings are effective for new Projects."}
        </FieldDescription>
      )}
    </div>
  )
}
