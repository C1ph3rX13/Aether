"use client"
import type { RefObject } from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { workerProblemTarget } from "@/components/worker-editor-fields"
import type { useWorkerEditor } from "@/hooks/use-worker-editor"
export function WorkerEditorDialogs({
  editor,
  busy,
  conflictDismissed,
  setConflictDismissed,
  saveButton,
  reloadButton,
  stepHeading,
  saveWorker,
}: {
  editor: Pick<
    ReturnType<typeof useWorkerEditor>,
    "conflict" | "failure" | "saveProblems" | "load" | "dismissFailure"
  >
  busy: boolean
  conflictDismissed: boolean
  setConflictDismissed: (dismissed: boolean) => void
  saveButton: RefObject<HTMLButtonElement | null>
  reloadButton: RefObject<HTMLButtonElement | null>
  stepHeading: RefObject<HTMLHeadingElement | null>
  saveWorker: () => Promise<void>
}) {
  return (
    <>
      <Dialog
        open={editor.conflict && !conflictDismissed}
        onOpenChange={(open) => {
          if (!open && !busy) setConflictDismissed(true)
        }}
      >
        <DialogContent
          className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-lg"
          showCloseButton={false}
          onPointerDownOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => {
            if (busy) event.preventDefault()
          }}
          onCloseAutoFocus={(event) => {
            const destination = reloadButton.current
            destination?.scrollIntoView({ block: "nearest" })
            if (destination) {
              event.preventDefault()
              destination.focus()
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>Settings changed elsewhere</DialogTitle>
            <DialogDescription>
              Reload discards edits and credential inputs, then loads saved
              settings.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              disabled={busy}
              variant="outline"
              onClick={() => setConflictDismissed(true)}
            >
              Keep editing
            </Button>
            <Button
              disabled={busy}
              onClick={() => {
                setConflictDismissed(true)
                void editor.load()
              }}
            >
              Reload
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={editor.failure !== null}
        onOpenChange={(open) => {
          if (!open && !busy) editor.dismissFailure()
        }}
      >
        <DialogContent
          className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-lg"
          showCloseButton={!busy}
          onPointerDownOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => {
            if (busy) event.preventDefault()
          }}
          onCloseAutoFocus={(event) => {
            const path = editor.saveProblems[0]?.path
            const errorTarget = path ? workerProblemTarget(path) : null
            const target = errorTarget ?? saveButton.current
            const destination =
              target && !target.hasAttribute("disabled")
                ? target
                : stepHeading.current
            destination?.scrollIntoView({ block: "center" })
            if (destination) {
              event.preventDefault()
              destination.focus()
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>{editor.failure?.title}</DialogTitle>
            <DialogDescription>{editor.failure?.message}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              disabled={busy}
              variant="outline"
              onClick={editor.dismissFailure}
            >
              Keep editing
            </Button>
            <Button disabled={busy} onClick={() => void saveWorker()}>
              Retry save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
