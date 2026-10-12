"use client"

import { useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import type { useWorkspaceSession } from "@/modules/workspace/session"

export interface WorkspaceActionProps {
  session: ReturnType<typeof useWorkspaceSession>
  onSuccess: (notice: string) => void
}

/** Shared dialog focus, recovery and receipt notices for Workspace actions. */
export function useWorkspaceAction<Action extends string>({
  session,
  onSuccess,
}: WorkspaceActionProps) {
  const [action, setAction] = useState<Action | null>(null)
  const actionFocus = useRef<HTMLElement | null>(null)
  const openAction = (next: Action, element: HTMLElement) => {
    actionFocus.current = element
    setAction(next)
  }
  const closeAction = (open: boolean) => {
    if (!open) setAction(null)
  }
  const finalFocus = () =>
    actionFocus.current?.isConnected &&
    !actionFocus.current.matches(":disabled, [aria-disabled='true']")
      ? actionFocus.current
      : (document
          .getElementById("workspace-reading-tabs")
          ?.querySelector<HTMLElement>('[aria-selected="true"]') ?? null)
  const recovery = !session.commands.pending ? (
    <div className="flex flex-col gap-2">
      {session.evidenceNotice && (
        <p role="status" className="text-sm">
          {session.evidenceNotice}
        </p>
      )}
      {session.view?.freshness !== "live" && (
        <>
          <p role="status" className="text-sm text-muted-foreground">
            Input kept. Wait for Live data before submitting.
          </p>
          <Button
            variant="outline"
            disabled={session.refreshing}
            onClick={() => void session.actions.retry()}
          >
            Refresh Project
          </Button>
        </>
      )}
    </div>
  ) : null
  const finish = async <T,>(operation: () => Promise<T>, notice: string) => {
    const receipt = await operation()
    onSuccess(notice)
    return receipt
  }
  return { action, openAction, closeAction, finalFocus, recovery, finish }
}
