"use client"
import { useRef, useState } from "react"
import Link from "next/link"
import { IconDots, IconPencil, IconCopy, IconTrash } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardAction,
  CardContent,
  CardFooter,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
  DialogTrigger,
} from "@/components/ui/dialog"
import type { DynamicWorkerDraft } from "@/lib/configuration"
import { workerBranches, type AgentType } from "@/lib/worker-catalog"
import { formatLabel } from "@/lib/format"
export function WorkerCard({
  worker,
  needsSetup,
  disabled,
  deleteDisabled,
  onDelete,
}: {
  worker: DynamicWorkerDraft
  needsSetup: boolean
  disabled: boolean
  deleteDisabled: boolean
  onDelete: (trigger: HTMLElement | null) => void
}) {
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const deleting = useRef(false)
  const branch = workerBranches[worker.agent_type as AgentType]
  const model =
    worker.agent_env[
      worker.agent_type === "codex"
        ? "CODEX_MODEL"
        : worker.agent_type === "pi"
          ? "PI_MODEL"
          : "ANTHROPIC_MODEL"
    ] || "Not configured"
  const editor = `${branch.href}/editor?worker=${encodeURIComponent(worker.name)}`
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        deleting.current = false
        setOpen(value)
      }}
    >
      <Card
        role="listitem"
        className="h-full min-w-0"
        aria-label={`Worker ${worker.name}`}
      >
        <CardHeader>
          <CardTitle className="min-w-0">
            <h3 className="h-4 truncate" title={worker.name}>
              {worker.name}
            </h3>
          </CardTitle>
          <CardDescription
            className="line-clamp-2 h-10 wrap-anywhere"
            title={model}
          >
            {model}
          </CardDescription>
          <CardAction>
            <DialogTrigger asChild>
              <Button
                id={`worker-menu-${encodeURIComponent(worker.name)}`}
                ref={trigger}
                variant="ghost"
                size="icon"
                aria-label={`View ${worker.name} configuration`}
                disabled={disabled}
              >
                <IconDots aria-hidden="true" />
              </Button>
            </DialogTrigger>
          </CardAction>
        </CardHeader>
        <CardContent className="flex min-h-6 flex-wrap content-start gap-2">
          {worker.task_types.length ? (
            worker.task_types.map((task) => (
              <Badge key={task} variant="secondary">
                {formatLabel(task)}
              </Badge>
            ))
          ) : (
            <p className="text-sm text-muted-foreground">No tasks selected</p>
          )}
        </CardContent>
        <CardFooter className="mt-auto flex min-h-6 flex-wrap justify-between gap-2">
          <p className="text-sm text-muted-foreground">
            Max running {worker.max_running}
          </p>
          <Badge variant="outline">
            {needsSetup ? "Setup required" : "Configured"}
          </Badge>
        </CardFooter>
      </Card>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto"
        onCloseAutoFocus={(event) => {
          if (deleting.current) event.preventDefault()
        }}
      >
        <DialogHeader>
          <DialogTitle>Worker configuration</DialogTitle>
          <DialogDescription>{worker.name}</DialogDescription>
        </DialogHeader>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-4 gap-y-3 text-sm">
          {[
            ["Agent type", branch.label],
            ["Provider", worker.provider_name || "Custom provider"],
            ["Model", model],
            ["Tasks", worker.task_types.map(formatLabel).join(", ") || "None"],
            ["Max running", worker.max_running],
            ["Priority", worker.priority],
            ["Capabilities", worker.capabilities.join(", ") || "None"],
          ].map(([name, value]) => (
            <div key={name} className="contents">
              <dt className="text-muted-foreground">{name}</dt>
              <dd className="wrap-anywhere">{value}</dd>
            </div>
          ))}
        </dl>
        <DialogFooter className="flex-wrap">
          <Button variant="outline" asChild disabled={disabled}>
            <Link
              href={editor}
              aria-disabled={disabled || undefined}
              tabIndex={disabled ? -1 : undefined}
              onClick={(event) => {
                if (disabled) event.preventDefault()
              }}
              aria-label={`Edit ${worker.name}`}
            >
              <IconPencil data-icon="inline-start" aria-hidden="true" />
              Edit
            </Link>
          </Button>
          <Button variant="outline" asChild disabled={disabled}>
            <Link
              href={`${editor}&copy=1`}
              aria-disabled={disabled || undefined}
              tabIndex={disabled ? -1 : undefined}
              onClick={(event) => {
                if (disabled) event.preventDefault()
              }}
              aria-label={`Copy ${worker.name}`}
            >
              <IconCopy data-icon="inline-start" aria-hidden="true" />
              Copy
            </Link>
          </Button>
          <Button
            variant="destructive"
            disabled={disabled || deleteDisabled}
            aria-label={`Delete ${worker.name}`}
            onClick={() => {
              deleting.current = true
              setOpen(false)
              onDelete(trigger.current)
            }}
          >
            <IconTrash data-icon="inline-start" aria-hidden="true" />
            Delete
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
