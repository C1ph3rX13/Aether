"use client"
import { useEffect, useState, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Textarea } from "@/components/ui/textarea"
import { commandError } from "@/lib/command-error"
import { previewCompletionImpact } from "@/modules/commands/api"
import { useConsoleMetadata } from "@/modules/console-bootstrap/api"
import type { Fact, ProjectMeta } from "@/modules/workspace/model"
import { formatCount } from "@/lib/format"
interface CommandDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  disabled: boolean
  finalFocus: () => HTMLElement | null
  recovery: ReactNode
}
export function HintDialog({
  open,
  onOpenChange,
  disabled,
  finalFocus,
  recovery,
  command,
}: CommandDialogProps & {
  command: (content: string) => Promise<unknown>
}) {
  const [content, setContent] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const submit = async () => {
    if (pending || disabled || !content.trim()) return
    setPending(true)
    setError(null)
    try {
      await command(content.trim())
      onOpenChange(false)
    } catch (cause) {
      setError(commandError(cause, "Could not add the Hint. Try again."))
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) onOpenChange(next)
      }}
    >
      <DialogContent
        showCloseButton={!pending}
        className="max-h-[calc(100dvh-4rem)] overflow-y-auto sm:max-w-lg"
        onCloseAutoFocus={(event) => {
          const destination = finalFocus?.()
          if (destination) {
            event.preventDefault()
            destination.focus()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>Add Hint</DialogTitle>
          <DialogDescription>Guide the next tasks.</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
        >
          <Field>
            <FieldLabel htmlFor="hint-content">Hint text</FieldLabel>
            <Textarea
              id="hint-content"
              required
              aria-describedby="hint-content-description"
              value={content}
              disabled={pending}
              onChange={(event) => setContent(event.target.value)}
            />
            <FieldDescription id="hint-content-description">
              Required. Recorded under your Operator identity.
            </FieldDescription>
          </Field>
          {error && (
            <p role="alert" className="text-sm wrap-anywhere text-destructive">
              {error}
            </p>
          )}
          {recovery}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={pending || disabled || !content.trim()}
            >
              {pending ? "Adding…" : "Add Hint"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
export function CompleteDialog({
  project,
  facts,
  open,
  onOpenChange,
  disabled,
  finalFocus,
  recovery,
  command,
}: CommandDialogProps & {
  project: ProjectMeta
  facts: Fact[]
  command: (description: string) => Promise<unknown>
}) {
  const [description, setDescription] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [affected, setAffected] = useState<number | null>(null)
  const { data: metadata } = useConsoleMetadata()
  useEffect(() => {
    if (!open) return
    let current = true
    previewCompletionImpact(project.id, project.revision)
      .then((result) => {
        if (current) setAffected(result.affected_run_count)
      })
      .catch(() => {
        if (current) setAffected(null)
      })
    return () => {
      current = false
    }
  }, [open, project.id, project.revision])
  const submit = async () => {
    if (pending || disabled || !description.trim() || !facts.length) return
    setPending(true)
    setError(null)
    try {
      await command(description.trim())
      onOpenChange(false)
    } catch (cause) {
      setError(
        commandError(cause, "Could not complete the Project. Try again.")
      )
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) onOpenChange(next)
      }}
    >
      <DialogContent
        showCloseButton={!pending}
        className="max-h-[calc(100dvh-4rem)] overflow-y-auto sm:max-w-xl"
        onCloseAutoFocus={(event) => {
          const destination = finalFocus?.()
          if (destination) {
            event.preventDefault()
            destination.focus()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>Complete Project</DialogTitle>
          <DialogDescription>
            Confirm the selected Active Facts meet the Goal.
          </DialogDescription>
        </DialogHeader>
        <section
          aria-label="Selected Completion Evidence"
          className="flex flex-col gap-2"
        >
          <h3 className="font-medium">
            {formatCount(facts.length)} selected Facts
          </h3>
          <p className="text-sm text-muted-foreground">
            Includes sources hidden by table search, filters or pagination.
          </p>
          <ul className="max-h-48 flex flex-col gap-3 overflow-y-auto rounded-md border p-3">
            {facts.map((fact) => (
              <li key={fact.id}>
                <p className="font-mono text-xs">{fact.id}</p>
                <p className="mt-1 text-sm leading-6 wrap-anywhere whitespace-pre-wrap">
                  {fact.description}
                </p>
              </li>
            ))}
          </ul>
        </section>
        <Field>
          <FieldLabel htmlFor="completion-description">
            Completion description
          </FieldLabel>
          <Textarea
            id="completion-description"
            required
            aria-describedby="completion-description-help"
            value={description}
            disabled={pending}
            placeholder="How do these Facts meet the Goal?"
            onChange={(event) => setDescription(event.target.value)}
          />
          <FieldDescription id="completion-description-help">
            Required. Marks the Goal achieved and cancels{" "}
            {affected === null
              ? "active Runs"
              : `${formatCount(affected)} active ${affected === 1 ? "Run" : "Runs"}`}{" "}
            in this Generation. Operator:{" "}
            {metadata?.operator.name ?? "Loading…"}.
          </FieldDescription>
        </Field>
        {error && (
          <p role="alert" className="text-sm wrap-anywhere text-destructive">
            {error}
          </p>
        )}
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
            disabled={
              pending || disabled || !description.trim() || !facts.length
            }
            onClick={() => void submit()}
          >
            {pending ? "Completing…" : "Complete Project"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
