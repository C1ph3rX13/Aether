/* eslint-disable react-hooks/set-state-in-effect */

import { useCallback, useEffect, useRef, useState } from "react"
import useSWR, { mutate as globalMutate, useSWRConfig } from "swr"
import { API_BASE } from "@/lib/api"
import type { ProjectDetail } from "@/modules/workspace/model"
import type { ProjectEventWire } from "@/modules/workspace/wire"

const RECONNECT_MAX_MS = 30_000

export type DataFreshnessStatus =
  "connecting" | "reconnecting" | "refreshing" | "stale" | "live"
export type ConsoleFreshnessStatus = DataFreshnessStatus | "replay"

interface ProjectFreshness {
  projectId: string | null
  status: DataFreshnessStatus
  projectionRevision: number
  streamRevision: number
  targetRevision: number | null
  syncComplete: boolean
}

export interface ProjectLiveSync {
  status: DataFreshnessStatus
  projectionRevision: number
  streamRevision: number
  targetRevision: number | null
  syncComplete: boolean
  markCommandCommitted: (revision?: number) => void
  refresh: () => Promise<void>
}

const initialFreshness: ProjectFreshness = {
  projectId: null,
  status: "connecting",
  projectionRevision: 0,
  streamRevision: 0,
  targetRevision: null,
  syncComplete: false,
}

export function useProjectLiveSync(
  projectId: string | null,
  currentRevision: number | null,
  onRevision?: (revision: number) => void
): ProjectLiveSync {
  const { mutate } = useSWR<ProjectDetail>(
    projectId ? `/projects/${encodeURIComponent(projectId)}` : null
  )
  const { mutate: mutateCache, cache } = useSWRConfig()
  const [freshness, setFreshness] = useState<ProjectFreshness>(initialFreshness)
  const onRevisionRef = useRef(onRevision)
  const refreshRef = useRef<(() => Promise<void>) | null>(null)
  const refresh = useCallback(async () => {
    if (refreshRef.current) await refreshRef.current()
    else await mutate()
  }, [mutate])
  useEffect(() => {
    onRevisionRef.current = onRevision
  }, [onRevision])

  const updateFreshness = useCallback(
    (
      scopedProjectId: string,
      update: Partial<Omit<ProjectFreshness, "projectId">>
    ) => {
      setFreshness((state) =>
        state.projectId === scopedProjectId ? { ...state, ...update } : state
      )
    },
    []
  )

  const markCommandCommitted = useCallback(
    (revision?: number) => {
      if (!projectId) return
      setFreshness((state) => {
        if (state.projectId !== projectId) return state
        const targetRevision =
          revision === undefined
            ? Math.max(
                state.targetRevision ?? 0,
                state.projectionRevision + 1,
                state.streamRevision + 1
              )
            : Math.max(state.targetRevision ?? 0, revision)
        return { ...state, status: "refreshing", targetRevision }
      })
    },
    [projectId]
  )

  const reconnectAttempt = useRef(0)
  const cursorRef = useRef(0)
  const projectionRevisionRef = useRef(0)
  const cursorProjectRef = useRef<string | null>(null)

  useEffect(() => {
    if (!projectId || currentRevision === null) return
    projectionRevisionRef.current = currentRevision
    if (cursorProjectRef.current !== projectId) {
      cursorProjectRef.current = projectId
      cursorRef.current = currentRevision
    }
    setFreshness((state) =>
      state.projectId === projectId
        ? {
            ...state,
            projectionRevision: Math.max(
              state.projectionRevision,
              currentRevision
            ),
          }
        : {
            projectId,
            status: "connecting",
            projectionRevision: currentRevision,
            streamRevision: currentRevision,
            targetRevision: null,
            syncComplete: false,
          }
    )
  }, [currentRevision, projectId])

  useEffect(() => {
    if (
      !projectId ||
      currentRevision === null ||
      freshness.projectId !== projectId ||
      !freshness.syncComplete
    )
      return
    const requiredRevision = Math.max(
      freshness.streamRevision,
      freshness.targetRevision ?? 0
    )
    if (
      currentRevision >= requiredRevision &&
      (freshness.status !== "live" ||
        freshness.targetRevision !== null ||
        freshness.projectionRevision !== currentRevision)
    ) {
      updateFreshness(projectId, {
        status: "live",
        projectionRevision: currentRevision,
        targetRevision: null,
      })
    }
  }, [currentRevision, freshness, projectId, updateFreshness])

  const hasRevision = currentRevision !== null
  useEffect(() => {
    if (!projectId || !hasRevision) return
    const scopedProjectId = projectId
    let source: EventSource | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let disposed = false
    let refreshing = false
    let refreshAgain = false
    let reconnectRequested = false
    let requiredRefreshRevision = 0

    const setScopedFreshness = (
      update: Partial<Omit<ProjectFreshness, "projectId">>
    ) => {
      updateFreshness(scopedProjectId, update)
    }

    const scheduleReconnect = () => {
      if (disposed || reconnectTimer !== null) return
      reconnectAttempt.current++
      const delay = Math.min(
        1000 * 2 ** (reconnectAttempt.current - 1),
        RECONNECT_MAX_MS
      )
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null
        connect()
      }, delay)
    }

    const refreshProjection = async (
      requiredRevision: number,
      reconnectAfter: boolean
    ) => {
      requiredRefreshRevision = Math.max(
        requiredRefreshRevision,
        requiredRevision
      )
      refreshAgain = true
      reconnectRequested = reconnectRequested || reconnectAfter
      if (reconnectAfter) {
        source?.close()
        source = null
        if (reconnectTimer !== null) clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      setScopedFreshness(
        reconnectAfter
          ? { status: "refreshing", syncComplete: false }
          : { status: "refreshing" }
      )
      if (refreshing) return
      refreshing = true
      try {
        do {
          refreshAgain = false
          const projection = await mutate()
          if (disposed) return
          // SWR revalidation returns retained data even when the read failed.
          if (
            !projection ||
            cache.get(`/projects/${encodeURIComponent(scopedProjectId)}`)?.error
          ) {
            setScopedFreshness({ status: "stale", syncComplete: false })
            return
          }
          const revision = projection.project.revision
          projectionRevisionRef.current = revision
          onRevisionRef.current?.(revision)
          cursorRef.current = revision
          setScopedFreshness({
            projectionRevision: revision,
            streamRevision: Math.max(revision, requiredRefreshRevision),
          })
          void globalMutate("/projects")
          void mutateCache(
            (key) =>
              typeof key === "string" &&
              key.startsWith(
                `/projects/${encodeURIComponent(scopedProjectId)}/execution-evidence`
              )
          )
          void mutateCache(
            (key) =>
              typeof key === "string" &&
              key.startsWith(
                `/projects/${encodeURIComponent(scopedProjectId)}/log`
              )
          )
        } while (refreshAgain)
        if (reconnectRequested) {
          reconnectRequested = false
          reconnectAttempt.current = 0
          connect()
        }
      } catch {
        if (!disposed)
          setScopedFreshness({ status: "stale", syncComplete: false })
      } finally {
        refreshing = false
      }
    }

    const handleProtocolFailure = () => {
      setScopedFreshness({ status: "stale", syncComplete: false })
      source?.close()
      source = null
      scheduleReconnect()
    }
    refreshRef.current = () =>
      refreshProjection(
        Math.max(cursorRef.current, projectionRevisionRef.current),
        true
      )

    function connect() {
      if (disposed || source !== null) return
      setScopedFreshness({
        status: reconnectAttempt.current > 0 ? "reconnecting" : "connecting",
        syncComplete: false,
      })
      source = new EventSource(
        `${API_BASE}/projects/${encodeURIComponent(scopedProjectId)}/events?after_revision=${cursorRef.current}`
      )
      const connectedSource = source

      source.onopen = () => undefined
      source.onmessage = (event) => {
        if (disposed || source !== connectedSource) return
        let data: ProjectEventWire
        try {
          const decoded: unknown = JSON.parse(event.data)
          if (
            !decoded ||
            typeof decoded !== "object" ||
            !("type" in decoded) ||
            !("project_id" in decoded) ||
            typeof decoded.type !== "string" ||
            typeof decoded.project_id !== "string"
          ) {
            handleProtocolFailure()
            return
          }
          data = decoded as ProjectEventWire
        } catch {
          handleProtocolFailure()
          return
        }
        if (data.project_id !== scopedProjectId) return
        reconnectAttempt.current = 0

        if (data.type === "project_deleted") {
          disposed = true
          source?.close()
          source = null
          setFreshness(initialFreshness)
          // A deletion event arrives outside a React event handler, so a full
          // navigation keeps the route and server projection in sync.
          // eslint-disable-next-line @next/next/no-location-assign-relative-destination
          window.location.assign("/projects")
          return
        }
        if (data.type === "activity_changed") {
          void mutateCache(
            (key) =>
              typeof key === "string" &&
              key.startsWith(
                `/projects/${encodeURIComponent(scopedProjectId)}/execution-evidence`
              )
          )
          void globalMutate("/projects")
          return
        }
        if (data.type === "replay_required") {
          const required = eventCurrentRevision(data.data)
          if (required === null) {
            handleProtocolFailure()
            return
          }
          void refreshProjection(required, true)
          return
        }
        if (data.type === "sync_complete") {
          const serverRevision = eventCurrentRevision(data.data)
          if (serverRevision === null || serverRevision < cursorRef.current) {
            handleProtocolFailure()
            return
          }
          setScopedFreshness({
            syncComplete: true,
            streamRevision: serverRevision,
          })
          onRevisionRef.current?.(serverRevision)
          if (projectionRevisionRef.current < serverRevision) {
            void refreshProjection(serverRevision, false)
          }
          return
        }

        const eventRevision = data.revision
        if (
          typeof eventRevision !== "number" ||
          !Number.isSafeInteger(eventRevision) ||
          eventRevision < 1
        ) {
          handleProtocolFailure()
          return
        }
        if (eventRevision <= cursorRef.current) return
        onRevisionRef.current?.(eventRevision)
        if (eventRevision !== cursorRef.current + 1) {
          void refreshProjection(eventRevision, true)
          return
        }

        cursorRef.current = eventRevision
        setScopedFreshness({
          status: "refreshing",
          streamRevision: eventRevision,
        })
        void refreshProjection(eventRevision, false)
      }

      source.onerror = () => {
        if (disposed || source !== connectedSource) return
        setScopedFreshness({ status: "reconnecting", syncComplete: false })
        source?.close()
        source = null
        scheduleReconnect()
      }
    }

    connect()
    return () => {
      disposed = true
      refreshRef.current = null
      if (reconnectTimer !== null) clearTimeout(reconnectTimer)
      source?.close()
      setFreshness((state) =>
        state.projectId === scopedProjectId ? initialFreshness : state
      )
    }
  }, [hasRevision, mutate, mutateCache, cache, projectId, updateFreshness])

  return {
    status: freshness.projectId === projectId ? freshness.status : "connecting",
    projectionRevision:
      freshness.projectId === projectId ? freshness.projectionRevision : 0,
    streamRevision:
      freshness.projectId === projectId ? freshness.streamRevision : 0,
    targetRevision:
      freshness.projectId === projectId ? freshness.targetRevision : null,
    syncComplete: freshness.projectId === projectId && freshness.syncComplete,
    markCommandCommitted,
    refresh,
  }
}

function eventCurrentRevision(data: unknown): number | null {
  if (
    typeof data !== "object" ||
    data === null ||
    !("current_revision" in data)
  )
    return null
  const revision = (data as { current_revision?: unknown }).current_revision
  return Number.isSafeInteger(revision) &&
    typeof revision === "number" &&
    revision >= 0
    ? revision
    : null
}
