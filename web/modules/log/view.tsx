"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { ProjectTimeline } from "./timeline"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { getProjectLog, useProjectLog } from "@/modules/log/api"
import type {
  ConsoleLogFocusTarget,
  ConsoleLogRevisionView,
  ConsoleLogPage,
} from "@/modules/log/model"
import { formatCount } from "@/lib/format"

export function ProjectLog({
  projectId,
  target,
  onInspect,
  frozenLog,
  visibleThroughRevision,
}: {
  projectId: string
  target?: ConsoleLogFocusTarget | null
  onInspect: (reference: ConsoleLogFocusTarget, element: HTMLElement) => void
  frozenLog?: ConsoleLogPage | null
  visibleThroughRevision?: number
}) {
  const historical = visibleThroughRevision !== undefined
  const query = useProjectLog(historical ? null : projectId, undefined, 50)
  const page = historical ? frozenLog : query.data
  const [loadedRevisions, setLoadedRevisions] = useState<
    ConsoleLogRevisionView[]
  >([])
  useEffect(() => {
    const incoming = query.data?.revisions
    if (!incoming) return
    // Retain every loaded atomic group when the newest History window advances.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoadedRevisions((loaded) => {
      const groups = new Map(
        loaded.map((revision) => [revision.revision, revision])
      )
      for (const revision of incoming) groups.set(revision.revision, revision)
      return [...groups.values()]
    })
  }, [query.data])
  const [loading, setLoading] = useState(false)
  const [olderError, setOlderError] = useState<string | null>(null)
  const request = useRef<AbortController | null>(null)
  const revisions = useMemo(() => {
    const groups = new Map(
      (historical ? [] : loadedRevisions).map((revision) => [
        revision.revision,
        revision,
      ])
    )
    for (const revision of page?.revisions ?? [])
      groups.set(revision.revision, revision)
    return [...groups.values()]
      .filter(
        (group) =>
          visibleThroughRevision === undefined ||
          group.revision <= visibleThroughRevision
      )
      .sort((a, b) => a.revision - b.revision)
  }, [loadedRevisions, page, historical, visibleThroughRevision])
  // A refreshed newest page can leave an unloaded window between retained pages.
  // Request that window before continuing toward the server's available boundary.
  const gap = revisions.find(
    (revision, index) =>
      index > 0 && revision.revision > revisions[index - 1].revision + 1
  )
  const oldest = revisions[0]?.revision
  const nextBefore = historical
    ? undefined
    : (gap?.revision ??
      (oldest !== undefined &&
      query.data &&
      oldest > query.data.availableFromRevision
        ? oldest
        : undefined))
  const loadOlder = useCallback(async () => {
    if (nextBefore === undefined || request.current) return
    const controller = new AbortController()
    request.current = controller
    setLoading(true)
    setOlderError(null)
    try {
      const page = await getProjectLog(
        projectId,
        nextBefore,
        50,
        controller.signal
      )
      if (controller.signal.aborted) return
      setLoadedRevisions((current) => [...page.revisions, ...current])
    } catch {
      if (!controller.signal.aborted)
        setOlderError(
          "Older Log could not be loaded. Retry without losing the loaded Revision groups."
        )
    } finally {
      if (request.current === controller) {
        request.current = null
        setLoading(false)
      }
    }
  }, [nextBefore, projectId])
  useEffect(() => () => request.current?.abort(), [])
  const selected = useMemo(
    () =>
      target
        ? [...revisions]
            .reverse()
            .flatMap((revision) => [...revision.events].reverse())
            .find(
              (event) =>
                event.focusTarget?.type === target.type &&
                event.focusTarget.id === target.id
            )?.id
        : undefined,
    [revisions, target]
  )
  useEffect(() => {
    if (
      target &&
      !selected &&
      !olderError &&
      !loading &&
      nextBefore !== undefined
    ) {
      // Resolve a requested record against authoritative History pages.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void loadOlder()
    }
  }, [target, selected, olderError, loading, nextBefore, loadOlder])
  useEffect(() => {
    if (selected)
      document
        .getElementById(`log-${selected}`)
        ?.scrollIntoView({ block: "nearest" })
  }, [selected])
  const retry = () => void query.mutate().catch(() => undefined)
  if (query.isLoading && !query.data)
    return (
      <p className="py-6 text-muted-foreground" role="status">
        Loading Log…
      </p>
    )
  return (
    <section aria-label="Project Log" className="flex flex-col gap-4">
      <div>
        <h3 className="text-sm font-medium">Project history</h3>
        <p className="text-xs text-muted-foreground">
          Oldest first · Chronological Blackboard changes grouped by atomic
          Revision.
        </p>
      </div>
      {!historical && query.error && (
        <Alert>
          <AlertTitle>Log unavailable</AlertTitle>
          <AlertDescription>
            Could not refresh Project Log. Loaded Revision groups remain
            readable.
          </AlertDescription>
          <Button variant="outline" size="default" onClick={retry}>
            Retry Log
          </Button>
        </Alert>
      )}
      {page && (
        <p className="text-xs text-muted-foreground">
          Available Revisions {formatCount(page.availableFromRevision)}–
          {formatCount(page.currentRevision)}
          {historical && ` · Displayed through R${visibleThroughRevision}`}
        </p>
      )}
      {nextBefore !== undefined && (
        <Button
          variant="outline"
          className="self-start"
          disabled={loading}
          onClick={() => void loadOlder()}
        >
          {loading ? "Loading older Log…" : "Load older Log"}
        </Button>
      )}
      {!historical && olderError && (
        <p role="alert" className="text-sm text-destructive">
          {olderError}
        </p>
      )}
      {!query.error && revisions.length === 0 && (
        <p className="py-6 text-muted-foreground">
          {historical && !page
            ? "Replay Log will be available when history has loaded."
            : "No Log events"}
        </p>
      )}
      <ProjectTimeline
        revisions={revisions}
        currentRevision={
          visibleThroughRevision ??
          page?.currentRevision ??
          revisions.at(-1)?.revision ??
          0
        }
        selected={selected}
        onInspect={onInspect}
      />
    </section>
  )
}
