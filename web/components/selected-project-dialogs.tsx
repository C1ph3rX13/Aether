"use client"
import { useEffect, useRef, useState } from "react"
import { stringify } from "yaml"
import { IconCopy, IconDownload, IconTrash } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@/components/ui/alert-dialog"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { exportProject, type CatalogProject } from "@/lib/project-catalog"
import { commandError } from "@/lib/command-error"

export function DeleteSelectedProjectsDialog({
  projects,
  disabled,
  pending,
  onClose,
  onConfirm,
  finalFocus,
}: {
  projects: CatalogProject[]
  disabled: boolean
  pending: boolean
  onClose: () => void
  onConfirm: (projects: CatalogProject[]) => Promise<void>
  finalFocus: () => HTMLElement | null
}) {
  const [confirmations, setConfirmations] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)
  const confirmed =
    projects.length > 0 &&
    projects.every((project) => confirmations[project.id] === project.title)
  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <AlertDialogContent
        className="max-h-[85svh] overflow-y-auto"
        onEscapeKeyDown={(event) => {
          if (pending) event.preventDefault()
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          ;(finalFocus() ?? document.getElementById("main-content"))?.focus()
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>
            Delete {projects.length} selected Projects
          </AlertDialogTitle>
          <AlertDialogDescription>
            Type each current Project title exactly to confirm. Deletion
            permanently removes the Project and its Blackboard history.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <FieldGroup>
          {projects.map((project) => (
            <Field key={project.id}>
              <FieldLabel htmlFor={`delete-${project.id}`}>
                Confirm “{project.title}”
              </FieldLabel>
              <Input
                id={`delete-${project.id}`}
                value={confirmations[project.id] ?? ""}
                disabled={pending}
                onChange={(event) =>
                  setConfirmations((current) => ({
                    ...current,
                    [project.id]: event.target.value,
                  }))
                }
                autoComplete="off"
              />
            </Field>
          ))}
        </FieldGroup>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={!confirmed || disabled || pending}
            onClick={async (event) => {
              event.preventDefault()
              setError(null)
              try {
                await onConfirm(projects)
              } catch (cause) {
                setError(
                  commandError(
                    cause,
                    "Selected Projects could not be deleted. Refresh the Catalog before retrying."
                  )
                )
              }
            }}
          >
            <IconTrash data-icon="inline-start" aria-hidden="true" />
            {pending ? "Deleting…" : "Delete Projects"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

export function ExportSelectedProjectsDialog({
  projects,
  onClose,
  finalFocus,
}: {
  projects: CatalogProject[]
  onClose: () => void
  finalFocus: () => HTMLElement | null
}) {
  const [format, setFormat] = useState<"json" | "yaml">("json")
  const [output, setOutput] = useState<unknown[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [copied, setCopied] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const field = useRef<HTMLTextAreaElement | null>(null)
  useEffect(() => {
    let active = true
    Promise.all(
      projects.map(
        async (project) =>
          JSON.parse(await exportProject(project.id, "json")) as unknown
      )
    )
      .then((values) => {
        if (active) {
          setOutput(values)
          setLoading(false)
        }
      })
      .catch((cause) => {
        if (active) {
          setError(
            commandError(
              cause,
              "Selected Projects could not be exported. Retry to load all selected Projects."
            )
          )
          setLoading(false)
        }
      })
    return () => {
      active = false
    }
  }, [projects, attempt])
  const serialized =
    output === null
      ? ""
      : format === "json"
        ? JSON.stringify(output, null, 2)
        : stringify(output)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          ;(finalFocus() ?? document.getElementById("main-content"))?.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>Export selected Projects</DialogTitle>
          <DialogDescription>
            {projects.length} Projects. Each export contains its latest backend
            state.
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="selected-export-format">Format</FieldLabel>
            <Select
              value={format}
              onValueChange={(value) => {
                setFormat(value as "json" | "yaml")
                setCopied(false)
              }}
            >
              <SelectTrigger id="selected-export-format">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="json">JSON</SelectItem>
                  <SelectItem value="yaml">YAML</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </Field>
          {loading ? (
            <p role="status" className="text-sm text-muted-foreground">
              Loading selected Project exports…
            </p>
          ) : (
            output !== null && (
              <Field>
                <FieldLabel htmlFor="selected-project-export">
                  Export result
                </FieldLabel>
                <Textarea
                  ref={field}
                  id="selected-project-export"
                  value={serialized}
                  readOnly
                  className="h-64 font-mono"
                />
              </Field>
            )
          )}
        </FieldGroup>
        {error && (
          <div className="flex flex-col gap-2">
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
            <Button
              variant="outline"
              className="self-start"
              disabled={loading}
              onClick={() => {
                setError(null)
                setLoading(true)
                setAttempt((current) => current + 1)
              }}
            >
              Retry export
            </Button>
          </div>
        )}
        <DialogFooter>
          <Button
            variant="outline"
            disabled={output === null}
            onClick={async () => {
              let success = false
              try {
                await navigator.clipboard.writeText(serialized)
                success = true
              } catch {
                field.current?.select()
                try {
                  success = document.execCommand("copy")
                } catch {
                  success = false
                }
              }
              setCopied(success)
              if (!success)
                setError(
                  "Could not copy. Select the export and copy it manually, or download the file."
                )
            }}
          >
            <IconCopy data-icon="inline-start" aria-hidden="true" />
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button
            disabled={output === null}
            onClick={() => {
              const url = URL.createObjectURL(
                new Blob([serialized], {
                  type:
                    format === "json" ? "application/json" : "application/yaml",
                })
              )
              const link = document.createElement("a")
              link.href = url
              link.download = `aether-selected-projects.${format}`
              link.click()
              setTimeout(() => URL.revokeObjectURL(url), 1000)
            }}
          >
            <IconDownload data-icon="inline-start" aria-hidden="true" />
            Download
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
