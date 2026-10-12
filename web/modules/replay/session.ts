"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { loadFrozenReplay } from "./api"
import type { ReplayFrame } from "./model"
import type { ConsoleLogPage } from "@/modules/log/model"
import { getProjectWorkspace } from "@/modules/workspace/api"
import type { ProjectDetail } from "@/modules/workspace/model"

interface FrozenReplay {
  source: ProjectDetail
  captured: boolean
  frames: ReplayFrame[]
  log: ConsoleLogPage | null
  fromRevision: number
  toRevision: number
  pendingCount: number
  frameIndex: number
  playing: boolean
  stepMs: number
  loading: boolean
  error: string | null
}

export function useReplaySession(id: string, live: ProjectDetail | undefined) {
  const [replay, setReplay] = useState<FrozenReplay | null>(null)
  const request = useRef<AbortController | null>(null)
  const latestRevision = useRef(0)
  const noteRevision = useCallback((revision: number) => {
    latestRevision.current = Math.max(latestRevision.current, revision)
    setReplay((current) =>
      current && revision - current.toRevision > current.pendingCount
        ? {
            ...current,
            pendingCount: revision - current.toRevision,
          }
        : current
    )
  }, [])
  const liveRevision = live?.project.revision
  useEffect(() => {
    if (liveRevision !== undefined)
      latestRevision.current = Math.max(latestRevision.current, liveRevision)
  }, [liveRevision])

  const load = async (restart: boolean) => {
    if (request.current) return
    const source = replay?.source ?? live
    if (!source) return
    const controller = new AbortController()
    request.current = controller
    const initial: FrozenReplay = {
      source,
      captured: false,
      frames: [],
      log: null,
      fromRevision: 0,
      toRevision: source.project.revision,
      pendingCount: Math.max(
        0,
        latestRevision.current - source.project.revision
      ),
      frameIndex: 0,
      playing: false,
      stepMs: 1100,
      loading: true,
      error: null,
    }
    setReplay((current) =>
      current
        ? { ...current, loading: true, playing: false, error: null }
        : initial
    )
    try {
      const nextSource =
        restart || !replay?.captured
          ? await getProjectWorkspace(id, controller.signal)
          : source
      controller.signal.throwIfAborted()
      if (request.current !== controller) return
      if (!restart && !replay?.captured) {
        // Capture once from the current API, then retain this boundary on retry.
        setReplay((current) =>
          current
            ? {
                ...current,
                source: nextSource,
                captured: true,
                toRevision: nextSource.project.revision,
                pendingCount: Math.max(
                  0,
                  latestRevision.current - nextSource.project.revision
                ),
              }
            : current
        )
      }
      const { frames, log } = await loadFrozenReplay(
        id,
        nextSource.project.revision,
        controller.signal
      )
      controller.signal.throwIfAborted()
      if (request.current !== controller) return
      setReplay({
        ...initial,
        source: nextSource,
        captured: true,
        frames,
        log,
        fromRevision: frames[0].revision,
        toRevision: nextSource.project.revision,
        pendingCount: Math.max(
          0,
          latestRevision.current - nextSource.project.revision
        ),
        loading: false,
        playing: true,
      })
    } catch {
      if (controller.signal.aborted || request.current !== controller) return
      setReplay((current) =>
        current
          ? {
              ...current,
              loading: false,
              playing: false,
              error: restart
                ? "Could not restart Replay. The frozen view remains readable. Retry Restart with Latest."
                : "Could not load Replay history. The frozen view remains readable. Retry Replay or exit to current data.",
            }
          : current
      )
    } finally {
      if (request.current === controller) request.current = null
    }
  }

  const selectFrame = (index: number) =>
    setReplay((current) =>
      current &&
      !current.loading &&
      Number.isInteger(index) &&
      index >= 0 &&
      index < current.frames.length
        ? { ...current, frameIndex: index, playing: false }
        : current
    )
  useEffect(() => {
    if (!replay?.playing || replay.loading) return
    const timer = setTimeout(
      () =>
        setReplay((current) => {
          if (!current?.playing || current.loading) return current
          const next = current.frameIndex + 1
          return next >= current.frames.length
            ? { ...current, playing: false }
            : {
                ...current,
                frameIndex: next,
                playing: next < current.frames.length - 1,
              }
        }),
      replay.stepMs
    )
    return () => clearTimeout(timer)
  }, [replay?.playing, replay?.loading, replay?.frameIndex, replay?.stepMs])
  const exit = () => {
    request.current?.abort()
    request.current = null
    setReplay(null)
  }
  useEffect(() => () => request.current?.abort(), [id])

  return {
    display: replay
      ? (replay.frames[replay.frameIndex]?.project ?? replay.source)
      : null,
    log: replay?.log ?? null,
    error: replay?.error ?? null,
    state: {
      active: replay !== null,
      preparing: Boolean(replay?.loading && !replay.frames.length),
      restarting: Boolean(replay?.loading && replay.frames.length),
      playing: replay?.playing ?? false,
      stepMs: replay?.stepMs ?? 1100,
      frameIndex: replay?.frameIndex ?? 0,
      frameRevisions: replay?.frames.map((frame) => frame.revision) ?? [],
      fromRevision: replay?.fromRevision ?? 0,
      toRevision: replay?.toRevision ?? 0,
      pendingCount: replay
        ? Math.max(replay.pendingCount, (liveRevision ?? 0) - replay.toRevision)
        : 0,
    },
    noteRevision,
    start: () => load(false),
    actions: {
      previous: () => selectFrame((replay?.frameIndex ?? 0) - 1),
      next: () => selectFrame((replay?.frameIndex ?? 0) + 1),
      selectFrame,
      togglePlayback: () =>
        setReplay((current) =>
          current && !current.loading && current.frames.length
            ? {
                ...current,
                playing: !current.playing,
                frameIndex:
                  !current.playing &&
                  current.frameIndex === current.frames.length - 1
                    ? 0
                    : current.frameIndex,
              }
            : current
        ),
      setStepMs: (stepMs: number) =>
        setReplay((current) =>
          current && Number.isSafeInteger(stepMs) && stepMs > 0
            ? { ...current, stepMs }
            : current
        ),
      restart: () => load(true),
      exit,
    },
  }
}
