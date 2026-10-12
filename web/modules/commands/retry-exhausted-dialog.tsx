"use client"

import { useRef, useState, type ReactNode } from "react"
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
import { ApiError } from "@/lib/api"
import { commandError } from "@/lib/command-error"

export function RetryExhaustedDialog({
  title,
  revision,
  disabled,
  onOpenChange,
  finalFocus,
  recovery,
  command,
}: {
  title: string
  revision: number
  disabled: boolean
  onOpenChange: (open: boolean) => void
  finalFocus: () => HTMLElement | null
  recovery: ReactNode
  command: (requestId: string, revision: number) => Promise<unknown>
}) {
  const request = useRef<{ id: string; revision: number } | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const submit = async () => {
    if (pending || disabled) return
    setPending(true)
    setError(null)
    try {
      request.current ??= { id: crypto.randomUUID(), revision }
      await command(request.current.id, request.current.revision)
      onOpenChange(false)
    } catch (cause) {
      // A rejected Revision never committed. Connection failures retain the
      // request identity so an acknowledgement can be replayed safely.
      if (cause instanceof ApiError && cause.code === "conflict") {
        request.current = null
      }
      setError(commandError(cause, "Could not retry exhausted work."))
    } finally {
      setPending(false)
    }
  }
  return (
    <AlertDialog open onOpenChange={(open) => !pending && onOpenChange(open)}>
      <AlertDialogContent
        onCloseAutoFocus={(event) => {
          const destination = finalFocus()
          if (destination) {
            event.preventDefault()
            destination.focus()
          }
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>Retry exhausted work</AlertDialogTitle>
          <AlertDialogDescription>
            Give unfinished tasks in “{title}” that reached their retry limit a
            fresh retry budget. Existing results and Project settings are kept.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-sm text-destructive">
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
            {pending ? "Requesting retry…" : "Retry exhausted work"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
