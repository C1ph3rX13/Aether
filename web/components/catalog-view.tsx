"use client"
import {
  IconAlertCircle as CircleAlert,
  IconPlayerPause as Pause,
  IconRefresh as RefreshCw,
  IconDownload,
  IconTrash,
  IconCheck,
} from "@tabler/icons-react"
import { useCallback, useMemo, useRef, useState } from "react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { FieldDescription } from "@/components/ui/field"
import {
  DeleteSelectedProjectsDialog,
  ExportSelectedProjectsDialog,
} from "@/components/selected-project-dialogs"
import { DataTable } from "@/components/data-table"
import { ProjectAvailabilityNotice } from "@/components/project-availability"
import { SectionCards } from "@/components/section-cards"
import { useProjectCatalog } from "@/hooks/use-project-catalog"
import { useProjectCreation } from "@/components/project-creation"
import { useNavigationGuard } from "@/components/navigation-guard"
import {
  deleteProject,
  renameProject,
  reopenProject,
  setProjectStatus,
  type CatalogProject,
} from "@/lib/project-catalog"
import {
  CatalogRowActions,
  type CatalogAction,
} from "@/components/catalog-row-actions"
import {
  DeleteDialog,
  ExportDialog,
  LifecycleDialog,
  RenameDialog,
  ReopenDialog,
  PauseActiveDialog,
} from "@/components/project-command-dialogs"
import { formatDateTime } from "@/lib/format"
type CatalogSelection = {
  action: CatalogAction
  project: CatalogProject
}
function refreshedSelection(
  current: CatalogSelection | null,
  projects: CatalogProject[]
) {
  if (!current) return null
  const project = projects.find((project) => project.id === current.project.id)
  if (
    !project ||
    (current.action === "pause" && project.status !== "active") ||
    (current.action === "resume" && project.status !== "paused") ||
    (current.action === "reopen" && project.status !== "completed")
  )
    return null
  return { ...current, project }
}
export function CatalogView() {
  const { availability } = useProjectCreation()
  const refreshTrigger = useRef<HTMLButtonElement | null>(null)
  const actionTrigger = useRef<HTMLButtonElement | null>(null)
  const bulkTrigger = useRef<HTMLButtonElement | null>(null)
  const [selection, setSelection] = useState<CatalogSelection | null>(null)
  const [bulkProjects, setBulkProjects] = useState<CatalogProject[] | null>(
    null
  )
  const [pending, setPending] = useState<"project" | "bulk" | null>(null)
  const [selectedBatch, setSelectedBatch] = useState<{
    action: "delete" | "export"
    projects: CatalogProject[]
    clear: () => void
  } | null>(null)
  const batchTrigger = useRef<HTMLElement | null>(null)
  const inFlight = useRef(false)
  const [message, setMessage] = useState<string | null>(null)
  const { projects, failed, refreshing, updatedAt, refresh } =
    useProjectCatalog()
  useNavigationGuard(
    useMemo(
      () => ({
        isBusy: () => inFlight.current,
        busy: pending !== null,
      }),
      [pending]
    )
  )
  const retry = () =>
    void refresh()
      .then((fresh) => {
        if (fresh) setSelection((current) => refreshedSelection(current, fresh))
      })
      .catch(() => undefined)
  const activeProjects = (projects ?? []).filter(
    (project) => project.status === "active"
  )
  const writesDisabled = failed || pending !== null
  const finalFocus = () =>
    (actionTrigger.current &&
      document.getElementById(actionTrigger.current.id)) ??
    refreshTrigger.current
  const renderActions = useCallback(
    (project: CatalogProject) => (
      <CatalogRowActions
        project={project}
        disabled={writesDisabled}
        onAction={(action, trigger) => {
          actionTrigger.current = trigger
          setSelection({ action, project })
        }}
      />
    ),
    [writesDisabled]
  )
  // A removed Project can no longer be inspected or commanded from this Catalog.
  if (
    selection &&
    projects &&
    !pending &&
    !projects.some((project) => project.id === selection.project.id)
  )
    setSelection(null)
  const execute = async <T,>(
    command: () => Promise<T>,
    scope: "project" | "bulk" = "project"
  ) => {
    if (inFlight.current || failed)
      throw new Error(
        "Wait for the current action and refresh the Catalog before retrying."
      )
    inFlight.current = true
    setPending(scope)
    setMessage(null)
    let committed = false
    try {
      const result = await command()
      committed = true
      return result
    } finally {
      const fresh = await refresh().catch(() => undefined)
      if (fresh && !committed) {
        setSelection((current) => refreshedSelection(current, fresh))
        setMessage(
          "This Project action failed. Review the refreshed Catalog before retrying."
        )
      }
      inFlight.current = false
      setPending(null)
    }
  }
  const pauseActive = async () => {
    if (!bulkProjects || writesDisabled || inFlight.current) return
    const targets = bulkProjects
    setBulkProjects(null)
    const results = await execute(
      () =>
        Promise.allSettled(
          targets.map((project) =>
            setProjectStatus(project.id, "paused", project.revision)
          )
        ),
      "bulk"
    )
    const paused = results.filter(
      (result) => result.status === "fulfilled"
    ).length
    const failures = results.length - paused
    setMessage(
      failures
        ? `Paused ${paused} Projects. Could not pause ${failures}; review the refreshed Catalog and retry.`
        : `Paused ${paused} active Project${paused === 1 ? "" : "s"}.`
    )
  }
  const toolbarActions = (
    <>
      <Button
        ref={refreshTrigger}
        variant="outline"
        disabled={refreshing || pending !== null}
        onClick={retry}
      >
        <RefreshCw aria-hidden="true" data-icon="inline-start" />
        {refreshing ? "Refreshing…" : "Refresh"}
      </Button>
      <Button
        ref={bulkTrigger}
        variant="outline"
        disabled={writesDisabled || activeProjects.length === 0}
        onClick={() => setBulkProjects(activeProjects)}
      >
        <Pause aria-hidden="true" data-icon="inline-start" />
        {pending === "bulk" ? "Pausing…" : "Pause Active"}
      </Button>
    </>
  )
  return (
    <div className="@container/main flex flex-1 flex-col gap-6 py-4 lg:py-6">
      <div className="px-4 lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-2xl font-semibold">Projects</h2>
          {availability.state === "ready" && (
            <Badge variant="outline" role="status">
              <IconCheck data-icon="inline-start" aria-hidden="true" />
              Project creation ready
            </Badge>
          )}
        </div>
        <p className="mt-1 text-sm text-muted-foreground">
          Open a Project to inspect its Blackboard.
        </p>
      </div>
      {projects !== null && <SectionCards projects={projects} />}
      <div className="flex min-w-0 flex-col gap-4 px-4 lg:px-6">
        {availability.state !== "ready" && (
          <ProjectAvailabilityNotice
            state={availability.state}
            missingTasks={availability.missingTasks}
            refresh={availability.refresh}
            showSettingsAction
          />
        )}
        {projects === null && (
          <div className="flex flex-wrap justify-end gap-2">
            {toolbarActions}
          </div>
        )}
        {message && (
          <p role="status" className="text-sm">
            {message}
          </p>
        )}
        {failed && (
          <Alert>
            <CircleAlert aria-hidden="true" />
            <AlertTitle>
              {projects === null
                ? "Project Catalog unavailable"
                : "Catalog update unavailable"}
            </AlertTitle>
            <AlertDescription>
              {projects === null
                ? "Could not read Projects. Check your connection and retry."
                : "Showing last loaded data. Lifecycle and execution status may be out of date."}
            </AlertDescription>
            <AlertDescription className="mt-3">
              <Button
                variant="outline"
                size="default"
                disabled={refreshing}
                onClick={retry}
                aria-label="Retry Catalog"
              >
                Retry
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {projects === null ? (
          !failed && (
            <div
              role="status"
              aria-label="Loading Project Catalog"
              className="flex flex-col gap-4"
            >
              <FieldDescription className="hidden motion-reduce:block">
                Loading Project Catalog…
              </FieldDescription>
              <Skeleton className="h-24 w-full" />
              <Skeleton className="h-64 w-full" />
            </div>
          )
        ) : (
          <DataTable
            data={projects}
            toolbarActions={toolbarActions}
            renderActions={renderActions}
            renderBulkActions={(selected, clear) => (
              <>
                <Button
                  variant="outline"
                  disabled={!selected.length || pending !== null}
                  onClick={(event) => {
                    batchTrigger.current = event.currentTarget
                    setSelectedBatch({
                      action: "export",
                      projects: selected,
                      clear,
                    })
                  }}
                >
                  <IconDownload data-icon="inline-start" aria-hidden="true" />
                  Export selected
                </Button>
                {selected.length > 0 && (
                  <Button
                    variant="destructive"
                    disabled={writesDisabled}
                    onClick={(event) => {
                      batchTrigger.current = event.currentTarget
                      setSelectedBatch({
                        action: "delete",
                        projects: selected,
                        clear,
                      })
                    }}
                  >
                    <IconTrash data-icon="inline-start" aria-hidden="true" />
                    Delete selected
                  </Button>
                )}
              </>
            )}
          />
        )}
        {updatedAt !== null && (
          <p className="text-xs text-muted-foreground">
            Last updated{" "}
            <time dateTime={new Date(updatedAt).toISOString()}>
              {formatDateTime(updatedAt)}
            </time>{" "}
            · Refreshes every 5 seconds
          </p>
        )}
      </div>
      {selection && (
        <CatalogSelectionDialog
          selection={selection}
          onClose={() => setSelection(null)}
          execute={execute}
          disabled={writesDisabled}
          failed={failed}
          refreshing={refreshing}
          retry={retry}
          finalFocus={finalFocus}
        />
      )}
      {selectedBatch?.action === "export" && (
        <ExportSelectedProjectsDialog
          projects={selectedBatch.projects}
          onClose={() => setSelectedBatch(null)}
          finalFocus={() => batchTrigger.current}
        />
      )}
      {selectedBatch?.action === "delete" && (
        <DeleteSelectedProjectsDialog
          projects={selectedBatch.projects}
          disabled={writesDisabled}
          pending={pending === "bulk"}
          onClose={() => setSelectedBatch(null)}
          finalFocus={() =>
            batchTrigger.current?.isConnected
              ? batchTrigger.current
              : refreshTrigger.current
          }
          onConfirm={async (targets) => {
            const results = await execute(
              () =>
                Promise.allSettled(
                  targets.map((project) =>
                    deleteProject(project.id, project.title, project.revision)
                  )
                ),
              "bulk"
            )
            const removed = results.filter(
              (result) => result.status === "fulfilled"
            ).length
            selectedBatch.clear()
            setSelectedBatch(null)
            setMessage(
              removed === targets.length
                ? `Deleted ${removed} Projects.`
                : `Deleted ${removed} Projects. Could not delete ${targets.length - removed}; review the refreshed Catalog and select them again before retrying.`
            )
          }}
        />
      )}
      <PauseActiveDialog
        open={bulkProjects !== null}
        count={bulkProjects?.length ?? 0}
        onOpenChange={(open) => {
          if (!open) setBulkProjects(null)
        }}
        command={pauseActive}
        finalFocus={bulkTrigger}
      />
    </div>
  )
}
function CatalogSelectionDialog({
  selection,
  onClose,
  execute,
  disabled: writesDisabled,
  failed,
  refreshing,
  retry,
  finalFocus,
}: {
  selection: CatalogSelection
  onClose: () => void
  execute: (command: () => Promise<unknown>) => Promise<unknown>
  disabled: boolean
  failed: boolean
  refreshing: boolean
  retry: () => void
  finalFocus: () => HTMLElement | null
}) {
  const { project, action } = selection
  const recovery = failed ? (
    <div className="flex flex-col gap-2">
      <p role="status" className="text-sm text-muted-foreground">
        Catalog update unavailable. Input kept; refresh to review the latest
        state before retrying.
      </p>
      <Button variant="outline" disabled={refreshing} onClick={retry}>
        Refresh Catalog
      </Button>
    </div>
  ) : undefined
  const props = {
    project,
    open: true,
    onOpenChange: (open: boolean) => {
      if (!open) onClose()
    },
    finalFocus,
    recovery,
  }
  if (action === "export") return <ExportDialog {...props} />
  if (action === "rename")
    return (
      <RenameDialog
        {...props}
        disabled={writesDisabled}
        command={(title) =>
          execute(() => renameProject(project.id, title, project.revision))
        }
      />
    )
  if (action === "delete")
    return (
      <DeleteDialog
        {...props}
        disabled={writesDisabled}
        command={(title) =>
          execute(() => deleteProject(project.id, title, project.revision))
        }
      />
    )
  if (action === "reopen")
    return (
      <ReopenDialog
        {...props}
        disabled={writesDisabled}
        command={(feedback) =>
          execute(() => reopenProject(project.id, feedback, project.revision))
        }
      />
    )
  return (
    <LifecycleDialog
      {...props}
      action={action}
      disabled={writesDisabled}
      command={() =>
        execute(() =>
          setProjectStatus(
            project.id,
            action === "pause" ? "paused" : "active",
            project.revision
          )
        )
      }
    />
  )
}
