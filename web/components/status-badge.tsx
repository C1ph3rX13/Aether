import { IconCheck, IconPlayerPause, IconCircleDot } from "@tabler/icons-react"
import { Badge } from "@/components/ui/badge"
import { formatLabel } from "@/lib/format"
export function StatusBadge({
  value,
  label,
}: {
  value: string
  label?: string
}) {
  const Icon =
    value === "completed" || value === "concluded"
      ? IconCheck
      : value === "paused"
        ? IconPlayerPause
        : IconCircleDot
  return (
    <Badge variant="outline">
      <Icon aria-hidden="true" />
      {label ?? formatLabel(value)}
    </Badge>
  )
}
