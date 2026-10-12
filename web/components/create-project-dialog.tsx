"use client"
import { useState } from "react"
import Link from "next/link"
import { IconPlus as Plus, IconX as X } from "@tabler/icons-react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ApiError } from "@/lib/api"
import type { CreateProjectInput } from "@/lib/project-catalog"
interface CreateProjectDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  disabled?: boolean
  finalFocus?: React.RefObject<HTMLElement | null>
  onSubmit: (input: CreateProjectInput) => Promise<void>
  onConfigurationRequired?: () => void
}
export function CreateProjectDialog({
  open,
  onOpenChange,
  disabled = false,
  finalFocus,
  onSubmit,
  onConfigurationRequired,
}: CreateProjectDialogProps) {
  const [submitted, setSubmitted] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [configurationRequired, setConfigurationRequired] = useState(false)
  const [title, setTitle] = useState("")
  const [origin, setOrigin] = useState("")
  const [goal, setGoal] = useState("")
  const [bootstrapEnabled, setBootstrapEnabled] = useState(true)
  const [hints, setHints] = useState<string[]>([])
  const [wasOpen, setWasOpen] = useState(open)
  if (open !== wasOpen) {
    setWasOpen(open)
    if (open) {
      setSubmitted(false)
      setError(null)
      setConfigurationRequired(false)
    }
  }
  const setOpen = (next: boolean) => {
    if (loading) return
    onOpenChange(next)
  }
  const closeDialog = () => {
    onOpenChange(false)
  }
  const reset = () => {
    setTitle("")
    setOrigin("")
    setGoal("")
    setBootstrapEnabled(true)
    setHints([])
    setSubmitted(false)
    setError(null)
  }
  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (loading || disabled) return
    setSubmitted(true)
    if (!title.trim() || !origin.trim() || !goal.trim()) return
    setLoading(true)
    setError(null)
    setConfigurationRequired(false)
    try {
      const trimmedHints = hints
        .map((hint) => hint.trim())
        .filter(Boolean)
        .map((content) => ({ content }))
      await onSubmit({
        title: title.trim(),
        origin: origin.trim(),
        goal: goal.trim(),
        bootstrapEnabled,
        ...(trimmedHints.length ? { hints: trimmedHints } : {}),
      })
      reset()
      closeDialog()
    } catch (cause: unknown) {
      if (
        cause instanceof ApiError &&
        cause.code === "configuration_required"
      ) {
        setConfigurationRequired(true)
        const missingTasks = ["bootstrap", "reason", "explore"].filter((task) =>
          new RegExp(`\\b${task}\\b`).test(cause.message)
        )
        setError(
          missingTasks.length
            ? `Missing Worker capabilities: ${missingTasks.join(", ")}. Add or edit Workers to cover these tasks. Input kept.`
            : "Complete and save Worker task coverage to enable Project creation. Input kept."
        )
        onConfigurationRequired?.()
      } else {
        setError(
          "Could not create Project. Input kept; review fields and retry."
        )
      }
    } finally {
      setLoading(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg"
        showCloseButton={!loading}
        onPointerDownOutside={(event) => {
          if (loading) event.preventDefault()
        }}
        onEscapeKeyDown={(event) => {
          if (loading) event.preventDefault()
        }}
        onCloseAutoFocus={(event) => {
          const trigger = finalFocus?.current
          const destination =
            trigger?.isConnected &&
            !trigger.matches(":disabled") &&
            !trigger.closest("[inert]")
              ? trigger
              : document.getElementById("main-content")
          if (destination) {
            event.preventDefault()
            destination.focus()
          }
        }}
      >
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Create Project</DialogTitle>
            <DialogDescription>
              Title, Origin and Goal are required.
            </DialogDescription>
          </DialogHeader>

          <FieldGroup className="py-5">
            <Field data-invalid={(submitted && !title.trim()) || undefined}>
              <FieldLabel htmlFor="project-title">Title</FieldLabel>
              <Input
                id="project-title"
                autoFocus
                disabled={loading}
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                aria-invalid={(submitted && !title.trim()) || undefined}
                aria-describedby={
                  submitted && !title.trim() ? "project-title-error" : undefined
                }
                placeholder="Security posture review"
              />
              <FieldError id="project-title-error">
                {submitted && !title.trim() ? "Title is required." : null}
              </FieldError>
            </Field>

            <Field data-invalid={(submitted && !origin.trim()) || undefined}>
              <FieldLabel htmlFor="project-origin">Origin</FieldLabel>
              <Textarea
                id="project-origin"
                disabled={loading}
                value={origin}
                onChange={(event) => setOrigin(event.target.value)}
                aria-invalid={(submitted && !origin.trim()) || undefined}
                aria-describedby={`project-origin-help${submitted && !origin.trim() ? " project-origin-error" : ""}`}
                placeholder="What is already known?"
              />
              <FieldDescription id="project-origin-help">
                Known facts at the start.
              </FieldDescription>
              <FieldError id="project-origin-error">
                {submitted && !origin.trim() ? "Origin is required." : null}
              </FieldError>
            </Field>

            <Field data-invalid={(submitted && !goal.trim()) || undefined}>
              <FieldLabel htmlFor="project-goal">Goal</FieldLabel>
              <Textarea
                id="project-goal"
                disabled={loading}
                value={goal}
                onChange={(event) => setGoal(event.target.value)}
                aria-invalid={(submitted && !goal.trim()) || undefined}
                aria-describedby={`project-goal-help${submitted && !goal.trim() ? " project-goal-error" : ""}`}
                placeholder="What should the Project achieve?"
              />
              <FieldDescription id="project-goal-help">
                The result to achieve.
              </FieldDescription>
              <FieldError id="project-goal-error">
                {submitted && !goal.trim() ? "Goal is required." : null}
              </FieldError>
            </Field>

            <Field
              orientation="horizontal"
              data-disabled={loading || undefined}
            >
              <Checkbox
                id="project-bootstrap"
                disabled={loading}
                checked={bootstrapEnabled}
                aria-describedby="project-bootstrap-help"
                onCheckedChange={(checked) =>
                  setBootstrapEnabled(checked === true)
                }
              />
              <FieldContent>
                <FieldLabel htmlFor="project-bootstrap">Bootstrap</FieldLabel>
                <FieldDescription id="project-bootstrap-help">
                  Try Origin to Goal directly. Disable to start with Reason.
                </FieldDescription>
              </FieldContent>
            </Field>

            <div className="grid gap-3">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <p className="text-sm font-medium">
                    Initial Hints{" "}
                    <span className="font-normal text-muted-foreground">
                      (optional)
                    </span>
                  </p>
                  <p className="text-sm text-muted-foreground">
                    Initial guidance and constraints.
                  </p>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="default"
                  disabled={loading}
                  onClick={() => setHints((current) => [...current, ""])}
                >
                  <Plus data-icon="inline-start" />
                  Add Hint
                </Button>
              </div>
              {hints.map((hint, index) => (
                <div className="flex items-center gap-2" key={index}>
                  <FieldLabel
                    htmlFor={`project-hint-${index}`}
                    className="sr-only"
                  >
                    Hint {index + 1}
                  </FieldLabel>
                  <Input
                    id={`project-hint-${index}`}
                    disabled={loading}
                    value={hint}
                    onChange={(event) =>
                      setHints((current) =>
                        current.map((value, itemIndex) =>
                          itemIndex === index ? event.target.value : value
                        )
                      )
                    }
                    placeholder="Context or constraint"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={loading}
                    aria-label={`Remove Hint ${index + 1}`}
                    onClick={() =>
                      setHints((current) =>
                        current.filter((_, itemIndex) => itemIndex !== index)
                      )
                    }
                  >
                    <X />
                  </Button>
                </div>
              ))}
            </div>

            {error && (
              <div className="grid gap-3">
                <p className="text-sm text-destructive" role="alert">
                  {error}
                </p>
                {configurationRequired && (
                  <Button variant="outline" asChild>
                    <Link href="/workers/codex" onClick={closeDialog}>
                      Open Workers
                    </Link>
                  </Button>
                )}
              </div>
            )}
          </FieldGroup>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={loading}
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={loading || disabled}>
              {loading ? "Creating..." : "Create Project"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
