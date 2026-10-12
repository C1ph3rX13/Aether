"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { ApiError } from "@/lib/api"
import { useProjectWorkspace } from "@/modules/workspace/api"
import { useProjectLiveSync } from "@/modules/workspace/live-sync"
import { useReplaySession } from "@/modules/replay/session"
import { useWorkspaceExecutionEvidence } from "@/modules/execution-evidence/api"
import {
  selectRecord,
  type RecordReference,
  type WorkspaceProjection,
} from "@/modules/workspace/contract"
import { completionEvidenceIds } from "@/modules/workspace/records"
import { useProjectCommands } from "@/modules/commands/controller"
import type { ProjectCommandAuthority } from "@/modules/commands/gateway"
import { resolveProjectCommandAvailability } from "@/modules/workspace/command-availability"

export function useWorkspaceSession(id: string) {
  const query = useProjectWorkspace(id)
  const replay = useReplaySession(id, query.data)
  const sync = useProjectLiveSync(
    id,
    query.data?.project.revision ?? null,
    replay.noteRevision
  )
  const [inspection, setInspection] = useState<RecordReference | null>(null)
  const mode = replay.state.active ? "replay" : "live"
  const displayed = replay.display ?? query.data
  // Completion Evidence is owned separately from record inspection. Commands and
  // Replay can consume it without changing which record the Operator is reading.
  const [evidence, setEvidence] = useState<{
    ids: string[]
    notice: string | null
  }>({ ids: [], notice: null })
  const liveFreshness = query.error
    ? "stale"
    : query.isValidating
      ? "refreshing"
      : sync.status
  const freshness = mode === "replay" ? "replay" : liveFreshness
  const authority = useMemo<ProjectCommandAuthority>(
    () => ({
      project: displayed?.project ?? null,
      freshness,
      mode,
      markCommandCommitted: sync.markCommandCommitted,
    }),
    [displayed?.project, freshness, mode, sync.markCommandCommitted]
  )
  const commands = useProjectCommands(id, authority)
  const availability = resolveProjectCommandAvailability(
    displayed?.project.status ?? "completed",
    freshness,
    commands.pending
  )
  const intentEvidence = useWorkspaceExecutionEvidence(
    mode === "live" && inspection?.type === "intent" ? id : null,
    mode === "live" && inspection?.type === "intent" ? inspection.id : null
  )
  const facts = query.data?.facts
  useEffect(() => {
    if (!facts) return
    // Reconcile selected evidence with the refreshed authoritative projection.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setEvidence((current) => {
      const valid = completionEvidenceIds(facts, current.ids)
      const removed = current.ids.length - valid.length
      return removed === 0
        ? current
        : {
            ids: valid,
            notice: `${removed} selected ${removed === 1 ? "Fact is" : "Facts are"} no longer eligible and ${removed === 1 ? "was" : "were"} removed from Completion Evidence.`,
          }
    })
  }, [facts])
  const refresh = sync.refresh
  const retry = useCallback(async () => {
    await refresh().catch(() => undefined)
  }, [refresh])
  const view: WorkspaceProjection | null = displayed
    ? {
        detail: displayed,
        mode,
        freshness,
        selection: selectRecord(displayed, inspection),
        completionFactIds:
          mode === "replay"
            ? []
            : completionEvidenceIds(displayed.facts, evidence.ids),
      }
    : null
  const notFound = query.error instanceof ApiError && query.error.status === 404
  return {
    status: notFound
      ? "not-found"
      : view
        ? "ready"
        : query.error
          ? "error"
          : "loading",
    view: notFound ? null : view,
    refreshFailed: Boolean(query.error),
    refreshing: query.isValidating,
    // Read SWR state in its owning component even while the native Sheet or
    // Evidence disclosure is closed, so later errors and retries stay subscribed.
    intentEvidence: {
      data: intentEvidence.data,
      error: intentEvidence.error,
      isLoading: intentEvidence.isLoading,
      mutate: intentEvidence.mutate,
    },
    commands,
    availability,
    replay: replay.state,
    liveFreshness,
    replayLog: replay.log,
    replayError: replay.error,
    evidenceNotice: evidence.notice,
    actions: {
      retry,
      inspect: setInspection,
      selectCompletionEvidence: (ids: string[]) =>
        setEvidence({
          ids: mode === "replay" ? [] : completionEvidenceIds(facts ?? [], ids),
          notice: null,
        }),
      startReplay: () => {
        if (commands.pending) return Promise.resolve()
        setEvidence({ ids: [], notice: null })
        return replay.start()
      },
      replay: {
        ...replay.actions,
        exit: async () => {
          // Invalidate write eligibility before switching back to the live view.
          const synchronization = refresh()
          replay.actions.exit()
          setInspection(null)
          setEvidence({ ids: [], notice: null })
          await synchronization
        },
      },
    },
  }
}
