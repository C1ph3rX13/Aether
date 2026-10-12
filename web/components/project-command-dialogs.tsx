"use client"
import { useState, type ReactNode } from "react"
import { IconCopy as Copy, IconDownload as Download } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import {
  Field,
  FieldGroup,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { getJSON } from "@/lib/api"
import { stringify } from "yaml"
import { commandError } from "@/lib/command-error"
import { exportProject } from "@/lib/project-catalog"
import { cn } from "@/lib/utils"
function restoreCommandFocus(
  event: Event,
  destination: HTMLElement | null | undefined
) {
  if (!destination) return
  event.preventDefault()
  destination.focus()
}

interface ProjectIdentity {
  id: string
  title: string
  revision: number
}
export function RenameDialog({
  project,
  open,
  onOpenChange,
  command,
  disabled = false,
  recovery,
  finalFocus,
}: {
  project: ProjectIdentity
  open: boolean
  onOpenChange: (open: boolean) => void
  command: (title: string) => Promise<unknown>
  disabled?: boolean
  recovery?: ReactNode
  finalFocus?: () => HTMLElement | null
}) {
  const [title, setTitle] = useState(project.title)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const changeOpen = (next: boolean) => {
    if (pending) return
    onOpenChange(next)
  }
  const save = async () => {
    if (!title.trim() || pending || disabled) return
    setPending(true)
    setError(null)
    try {
      await command(title.trim())
      onOpenChange(false)
    } catch (cause: unknown) {
      setError(commandError(cause, "Project could not be renamed."))
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        className="max-h-[calc(100svh-4rem)] overflow-y-auto"
        showCloseButton={!pending}
        onCloseAutoFocus={(event) => restoreCommandFocus(event, finalFocus?.())}
      >
        <DialogHeader>
          <DialogTitle>Rename Project</DialogTitle>
          <DialogDescription>
            Choose a new title for “{project.title}”.
          </DialogDescription>
        </DialogHeader>
        <Field data-invalid={!title.trim() || undefined}>
          <FieldLabel htmlFor={`rename-${project.id}`}>Title</FieldLabel>
          <Input
            id={`rename-${project.id}`}
            value={title}
            aria-invalid={!title.trim() || undefined}
            aria-describedby={
              !title.trim() ? `rename-${project.id}-error` : undefined
            }
            disabled={pending || disabled}
            onChange={(event) => setTitle(event.target.value)}
          />
          <FieldError id={`rename-${project.id}-error`}>
            {!title.trim() ? "Title is required." : null}
          </FieldError>
        </Field>
        {error ? (
          <p className="text-sm wrap-anywhere text-destructive" role="alert">
            {error}
          </p>
        ) : null}
        {recovery}
        <DialogFooter>
          <Button
            variant="outline"
            disabled={pending}
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            disabled={pending || disabled || !title.trim()}
            onClick={() => void save()}
          >
            {pending ? "Renaming..." : "Rename Project"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
export function DeleteDialog({
  project,
  open,
  onOpenChange,
  command,
  disabled = false,
  recovery,
  finalFocus,
}: {
  project: ProjectIdentity
  open: boolean
  onOpenChange: (open: boolean) => void
  command: (expectedTitle: string) => Promise<unknown>
  disabled?: boolean
  recovery?: ReactNode
  finalFocus?: () => HTMLElement | null
}) {
  const [confirmation, setConfirmation] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const changeOpen = (next: boolean) => {
    if (pending) return
    onOpenChange(next)
  }
  const removeProject = async () => {
    if (confirmation !== project.title || pending || disabled) return
    setPending(true)
    setError(null)
    try {
      await command(project.title)
      onOpenChange(false)
      setConfirmation("")
    } catch (cause: unknown) {
      setError(commandError(cause, "Project could not be deleted."))
    } finally {
      setPending(false)
    }
  }
  return (
    <AlertDialog open={open} onOpenChange={changeOpen}>
      <AlertDialogContent
        className="max-h-[calc(100svh-4rem)] overflow-y-auto"
        onCloseAutoFocus={(event) => restoreCommandFocus(event, finalFocus?.())}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>Delete Project</AlertDialogTitle>
          <AlertDialogDescription id={`delete-${project.id}-description`}>
            This permanently deletes “{project.title}” and its Blackboard
            history. Type the current Project title exactly to confirm.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <Field>
          <FieldLabel htmlFor={`delete-${project.id}`}>
            Project title confirmation
          </FieldLabel>
          <Input
            id={`delete-${project.id}`}
            value={confirmation}
            aria-describedby={`delete-${project.id}-description`}
            disabled={pending || disabled}
            onChange={(event) => setConfirmation(event.target.value)}
          />
        </Field>
        {error ? (
          <p className="text-sm wrap-anywhere text-destructive" role="alert">
            {error}
          </p>
        ) : null}
        {recovery}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending || disabled || confirmation !== project.title}
            onClick={(event) => {
              event.preventDefault()
              void removeProject()
            }}
          >
            {pending ? "Deleting..." : "Delete Project"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
export function ReopenDialog({
  project,
  open,
  onOpenChange,
  command,
  disabled = false,
  finalFocus,
  recovery,
}: {
  project: ProjectIdentity
  open: boolean
  onOpenChange: (open: boolean) => void
  command: (feedback: string) => Promise<unknown>
  disabled?: boolean
  finalFocus?: () => HTMLElement | null
  recovery?: ReactNode
}) {
  const [feedback, setFeedback] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const changeOpen = (next: boolean) => {
    if (pending) return
    onOpenChange(next)
  }
  const reopen = async () => {
    if (!feedback.trim() || pending || disabled) return
    setPending(true)
    setError(null)
    try {
      await command(feedback.trim())
      onOpenChange(false)
      setFeedback("")
    } catch (cause: unknown) {
      setError(commandError(cause, "Project could not be reopened."))
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        className="max-h-[calc(100svh-4rem)] overflow-y-auto"
        showCloseButton={!pending}
        onCloseAutoFocus={(event) => restoreCommandFocus(event, finalFocus?.())}
      >
        <DialogHeader>
          <DialogTitle>Reopen Project</DialogTitle>
          <DialogDescription>
            Start a new Generation and record why the Project is being reopened.
          </DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel htmlFor={`reopen-${project.id}`}>
            Reason for reopening
          </FieldLabel>
          <Textarea
            id={`reopen-${project.id}`}
            required
            value={feedback}
            aria-describedby={`reopen-${project.id}-description`}
            disabled={pending || disabled}
            onChange={(event) => setFeedback(event.target.value)}
          />
          <FieldDescription id={`reopen-${project.id}-description`}>
            Required. Your reason becomes the first Hint for the new Generation.
          </FieldDescription>
        </Field>
        {error ? (
          <p className="text-sm wrap-anywhere text-destructive" role="alert">
            {error}
          </p>
        ) : null}
        {recovery}
        <DialogFooter>
          <Button
            variant="outline"
            disabled={pending}
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            disabled={pending || disabled || !feedback.trim()}
            onClick={() => void reopen()}
          >
            {pending ? "Reopening..." : "Reopen"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
export function ExportDialog({
  project,
  open,
  onOpenChange,
  finalFocus,
  snapshotRevision,
}: {
  project: ProjectIdentity
  open: boolean
  onOpenChange: (open: boolean) => void
  finalFocus?: () => HTMLElement | null
  snapshotRevision?: number
}) {
  const [format, setFormat] = useState<"yaml" | "json">("yaml")
  const [output, setOutput] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const close = () => {
    onOpenChange(false)
    setOutput(null)
    setError(null)
    setCopied(false)
  }
  const exportValue = async () => {
    setPending(true)
    setError(null)
    setCopied(false)
    try {
      if (snapshotRevision !== undefined) {
        const snapshot = await getJSON<unknown>(
          `/projects/${encodeURIComponent(project.id)}/history/${snapshotRevision}/blackboard`
        )
        setOutput(
          format === "json"
            ? JSON.stringify(snapshot, null, 2)
            : stringify(snapshot)
        )
      } else setOutput(await exportProject(project.id, format))
    } catch (cause: unknown) {
      setError(commandError(cause, "Project export failed."))
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) {
          if (next) onOpenChange(true)
          else close()
        }
      }}
    >
      <DialogContent
        className={cn(
          "max-h-[calc(100svh-4rem)] overflow-y-auto",
          snapshotRevision === undefined ? "sm:max-w-2xl" : "sm:max-w-4xl"
        )}
        showCloseButton={!pending}
        onCloseAutoFocus={(event) => restoreCommandFocus(event, finalFocus?.())}
      >
        <DialogHeader>
          <DialogTitle>
            {snapshotRevision === undefined
              ? "Export Project"
              : "Snapshot Blackboard"}
          </DialogTitle>
          <DialogDescription>
            {snapshotRevision === undefined
              ? "Export the latest Project state as YAML or JSON."
              : `Export the Blackboard at Revision ${snapshotRevision} as YAML or JSON.`}
          </DialogDescription>
        </DialogHeader>
        <FieldGroup className="flex-row items-end gap-3">
          <Field>
            <FieldLabel htmlFor={`export-format-${project.id}`}>
              Format
            </FieldLabel>
            <Select
              value={format}
              disabled={pending}
              onValueChange={(value) => {
                setFormat(value as "yaml" | "json")
                setOutput(null)
                setCopied(false)
              }}
            >
              <SelectTrigger
                id={`export-format-${project.id}`}
                className="w-full"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="yaml">YAML</SelectItem>
                  <SelectItem value="json">JSON</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </Field>
          <Button onClick={() => void exportValue()} disabled={pending}>
            {pending ? "Exporting..." : "Export"}
          </Button>
        </FieldGroup>
        {error ? (
          <p className="text-sm wrap-anywhere text-destructive" role="alert">
            {error}
          </p>
        ) : null}
        {output !== null ? (
          <Field className="min-w-0">
            <FieldLabel htmlFor={`export-result-${project.id}`}>
              Export result
            </FieldLabel>
            <Textarea
              id={`export-result-${project.id}`}
              className={cn(
                "font-mono",
                snapshotRevision === undefined
                  ? "h-64"
                  : "field-sizing-fixed h-[min(24rem,40svh)] min-w-0 resize-y wrap-anywhere whitespace-pre-wrap"
              )}
              wrap="soft"
              readOnly
              value={output}
            />
          </Field>
        ) : null}
        <DialogFooter>
          {output !== null ? (
            <>
              <Button
                variant="outline"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(output)
                    setCopied(true)
                  } catch {
                    setError(
                      "Could not copy the export. Download the file instead."
                    )
                  }
                }}
              >
                <Copy data-icon="inline-start" />
                {copied ? "Copied" : "Copy"}
              </Button>
              <Button
                onClick={() => {
                  const url = URL.createObjectURL(
                    new Blob([output], {
                      type:
                        format === "json"
                          ? "application/json"
                          : "application/yaml",
                    })
                  )
                  const link = document.createElement("a")
                  link.href = url
                  link.download = `${project.id}${snapshotRevision === undefined ? "" : `-r${snapshotRevision}`}.${format}`
                  link.click()
                  setTimeout(() => URL.revokeObjectURL(url), 0)
                }}
              >
                <Download data-icon="inline-start" />
                Download File
              </Button>
            </>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
export function LifecycleDialog({
  project,
  action,
  open,
  onOpenChange,
  command,
  disabled,
  finalFocus,
  recovery,
}: {
  project: ProjectIdentity
  action: "pause" | "resume"
  open: boolean
  onOpenChange: (open: boolean) => void
  command: () => Promise<unknown>
  disabled: boolean
  finalFocus: () => HTMLElement | null
  recovery?: ReactNode
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const label = action === "pause" ? "Pause" : "Resume"
  const submit = async () => {
    if (pending || disabled) return
    setPending(true)
    setError(null)
    try {
      await command()
      onOpenChange(false)
    } catch (cause) {
      setError(
        commandError(
          cause,
          `Project could not be ${action === "pause" ? "paused" : "resumed"}.`
        )
      )
    } finally {
      setPending(false)
    }
  }
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (pending) {
          return
        }
        onOpenChange(next)
      }}
    >
      <AlertDialogContent
        onCloseAutoFocus={(event) => restoreCommandFocus(event, finalFocus?.())}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{label} Project</AlertDialogTitle>
          <AlertDialogDescription>
            {action === "pause"
              ? `Pause execution for “${project.title}”? Its Blackboard remains readable.`
              : `Resume execution for “${project.title}”?`}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-sm wrap-anywhere text-destructive">
            {error}
          </p>
        )}
        {recovery}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={pending || disabled}
            onClick={(event) => {
              event.preventDefault()
              void submit()
            }}
          >
            {pending ? `${label === "Pause" ? "Pausing" : "Resuming"}…` : label}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
export function PauseActiveDialog({
  open,
  count,
  onOpenChange,
  command,
  finalFocus,
}: {
  open: boolean
  count: number
  onOpenChange: (open: boolean) => void
  command: () => Promise<void>
  finalFocus: React.RefObject<HTMLButtonElement | null>
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent
        onCloseAutoFocus={(event) =>
          restoreCommandFocus(event, finalFocus.current)
        }
      >
        <AlertDialogHeader>
          <AlertDialogTitle>Pause Active Projects</AlertDialogTitle>
          <AlertDialogDescription>
            Pause all {count} Active Projects in the full Catalog, including
            Projects hidden by search, filters or pagination.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={() => void command()}>
            Pause Active
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
