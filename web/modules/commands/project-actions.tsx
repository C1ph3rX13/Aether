"use client"
import { useRef, useState } from "react"
import { useRouter } from "next/navigation"
import {
  IconDownload as Download,
  IconPencil as Pencil,
  IconTrash as Trash2,
  IconPlayerPause as Pause,
  IconPlayerPlay as Play,
  IconRotate as RotateCcw,
  IconDots,
  IconCamera,
  IconHistory,
  IconRefresh,
} from "@tabler/icons-react"
import {
  RenameDialog,
  DeleteDialog,
  ReopenDialog,
  LifecycleDialog,
  ExportDialog,
} from "@/components/project-command-dialogs"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { formatLabel } from "@/lib/format"
import { RetryExhaustedDialog } from "./retry-exhausted-dialog"
import {
  useWorkspaceAction,
  type WorkspaceActionProps,
} from "./use-workspace-action"
export function ProjectActions(
  props: WorkspaceActionProps & { onReplay?: () => void }
) {
  const { session } = props
  const router = useRouter()
  const [snapshotRevision, setSnapshotRevision] = useState(0)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const openingDialog = useRef(false)
  const { action, openAction, closeAction, finalFocus, recovery, finish } =
    useWorkspaceAction<
      | "rename"
      | "delete"
      | "reopen"
      | "pause"
      | "resume"
      | "snapshot"
      | "export"
      | "retry-exhausted"
    >(props)
  const selectAction = (
    next: NonNullable<typeof action>,
    element = trigger.current
  ) => {
    if (!element) return
    openingDialog.current = true
    openAction(next, element)
  }
  if (!session.view) return null
  const project = session.view.detail.project
  const { commands, availability } = session
  const lifecycle =
    project.status === "active"
      ? "pause"
      : project.status === "paused"
        ? "resume"
        : "reopen"
  const lifecycleEnabled =
    lifecycle === "pause"
      ? availability.pauseProject
      : lifecycle === "resume"
        ? availability.resumeProject
        : availability.reopenProject
  const LifecycleIcon =
    lifecycle === "pause" ? Pause : lifecycle === "resume" ? Play : RotateCcw
  return (
    <>
      <Button
        variant="outline"
        disabled={commands.pending}
        onClick={(event) => {
          setSnapshotRevision(project.revision)
          selectAction("snapshot", event.currentTarget)
        }}
      >
        <IconCamera data-icon="inline-start" aria-hidden="true" />
        Snapshot
      </Button>
      <Button
        variant="outline"
        disabled={!lifecycleEnabled}
        onClick={(event) => selectAction(lifecycle, event.currentTarget)}
      >
        <LifecycleIcon data-icon="inline-start" aria-hidden="true" />
        {formatLabel(lifecycle)}
      </Button>
      <DropdownMenu
        onOpenChange={(open) => {
          if (open) openingDialog.current = false
        }}
      >
        <DropdownMenuTrigger asChild>
          <Button
            ref={trigger}
            variant="outline"
            size="icon"
            aria-label="Project actions"
            disabled={commands.pending}
          >
            <IconDots aria-hidden="true" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="end"
          onCloseAutoFocus={(event) => {
            if (openingDialog.current) event.preventDefault()
          }}
        >
          <DropdownMenuGroup>
            {!session.replay.active && props.onReplay && (
              <DropdownMenuItem
                disabled={commands.pending}
                onSelect={props.onReplay}
              >
                <IconHistory aria-hidden="true" />
                Replay
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onSelect={() => selectAction("export")}>
              <Download aria-hidden="true" />
              Export Project
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={!availability.renameProject}
              onSelect={() => selectAction("rename")}
            >
              <Pencil aria-hidden="true" />
              Rename
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={!availability.retryExhaustedWork}
              onSelect={() => selectAction("retry-exhausted")}
            >
              <RotateCcw aria-hidden="true" />
              Retry exhausted work
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={session.refreshing || commands.pending}
              onSelect={() => void session.actions.retry()}
            >
              <IconRefresh aria-hidden="true" />
              {session.refreshing ? "Refreshing…" : "Refresh Project"}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem
              variant="destructive"
              disabled={!availability.deleteProject}
              onSelect={() => selectAction("delete")}
            >
              <Trash2 aria-hidden="true" />
              Delete Project
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      {action === "retry-exhausted" && (
        <RetryExhaustedDialog
          title={project.title}
          revision={project.revision}
          disabled={!availability.retryExhaustedWork}
          onOpenChange={closeAction}
          finalFocus={finalFocus}
          recovery={recovery}
          command={async (requestId, revision) => {
            const receipt = await commands.retryExhaustedWork(
              requestId,
              revision
            )
            const count = receipt.run_ids.length
            props.onSuccess(
              count === 0
                ? "No exhausted work is currently eligible to retry"
                : `Retry requested for ${count} ${count === 1 ? "task" : "tasks"}`
            )
            return receipt
          }}
        />
      )}
      {action === "rename" && (
        <RenameDialog
          project={project}
          open
          onOpenChange={closeAction}
          disabled={!availability.renameProject}
          recovery={recovery}
          finalFocus={finalFocus}
          command={(title) =>
            finish(
              () => commands.updateProjectTitle(title, project.revision),
              "Project renamed"
            )
          }
        />
      )}
      {action === "delete" && (
        <DeleteDialog
          project={project}
          open
          onOpenChange={closeAction}
          disabled={!availability.deleteProject}
          recovery={recovery}
          finalFocus={finalFocus}
          command={async (title) => {
            await commands.deleteProject(title, project.revision)
            router.push("/projects")
          }}
        />
      )}
      {action === "reopen" && (
        <ReopenDialog
          project={project}
          open
          onOpenChange={closeAction}
          disabled={!availability.reopenProject}
          recovery={recovery}
          finalFocus={finalFocus}
          command={(feedback) =>
            finish(
              () => commands.reopenProject(feedback, project.revision),
              "Project reopened"
            )
          }
        />
      )}
      {(action === "pause" || action === "resume") && (
        <LifecycleDialog
          project={project}
          action={action}
          open
          onOpenChange={closeAction}
          disabled={
            action === "pause"
              ? !availability.pauseProject
              : !availability.resumeProject
          }
          recovery={recovery}
          finalFocus={finalFocus}
          command={() =>
            finish(
              () =>
                commands.updateProjectStatus(
                  action === "pause" ? "paused" : "active",
                  project.revision
                ),
              action === "pause" ? "Project paused" : "Project resumed"
            )
          }
        />
      )}
      {action === "export" && (
        <ExportDialog
          project={project}
          open
          onOpenChange={closeAction}
          finalFocus={finalFocus}
        />
      )}
      {action === "snapshot" && (
        <ExportDialog
          project={project}
          snapshotRevision={snapshotRevision}
          open
          onOpenChange={closeAction}
          finalFocus={finalFocus}
        />
      )}
    </>
  )
}
