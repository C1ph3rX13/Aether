"use client"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import type { WorkspaceExecutionRun } from "@/modules/execution-evidence/model"
import { formatCount, formatDateTime, formatLabel } from "@/lib/format"

export interface EvidenceRead {
  isLoading: boolean
  error?: unknown
  mutate: () => Promise<unknown>
}

export function ExecutionEvidence({
  title,
  run,
  query,
  empty,
  compact = false,
}: {
  title: string
  run?: WorkspaceExecutionRun
  query: EvidenceRead
  empty: string
  compact?: boolean
}) {
  const retry = () => void query.mutate().catch(() => undefined)
  return (
    <section
      aria-label={title}
      className={
        compact
          ? "flex flex-wrap items-center gap-3 text-sm"
          : "flex flex-col gap-3 border-t pt-4"
      }
    >
      <h3 className="font-medium">{title}</h3>
      {query.isLoading && !run ? (
        <p role="status" className="text-sm text-muted-foreground">
          Loading Execution Evidence…
        </p>
      ) : query.error ? (
        <div role="alert" className="flex flex-wrap items-center gap-3">
          <span className="text-sm text-muted-foreground">
            Execution Evidence unavailable.
          </span>
          <Button
            size="default"
            variant="outline"
            onClick={retry}
            aria-label={`Retry ${title}`}
          >
            Retry
          </Button>
        </div>
      ) : !run ? (
        <p className="text-sm text-muted-foreground">{empty}</p>
      ) : compact ? (
        <>
          <Badge variant="secondary">Reason Active</Badge>
          <span>
            {run.worker} · Attempt {formatCount(run.attempt)}
          </span>
          <time dateTime={run.startedAt} className="text-muted-foreground">
            {formatDateTime(run.startedAt)}
          </time>
        </>
      ) : (
        <dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-x-4 gap-y-2 text-sm [&_dd]:wrap-anywhere [&_dt]:text-muted-foreground">
          <dt>Run ID</dt>
          <dd className="font-mono text-xs">{run.runId}</dd>
          <dt>Task</dt>
          <dd>{formatLabel(run.task)}</dd>
          <dt>Status</dt>
          <dd>
            <Badge variant="outline">{formatLabel(run.status)}</Badge>
          </dd>
          <dt>Attempt</dt>
          <dd>{formatCount(run.attempt)}</dd>
          <dt>Worker</dt>
          <dd>{run.worker}</dd>
          <dt>Started</dt>
          <dd>
            <time dateTime={run.startedAt}>
              {formatDateTime(run.startedAt)}
            </time>
          </dd>
          <dt>Outcome</dt>
          <dd>
            {run.outcomeKind ? formatLabel(run.outcomeKind) : "Not reported"}
          </dd>
          {run.errorCode && (
            <>
              <dt>Error Code</dt>
              <dd>{formatLabel(run.errorCode)}</dd>
            </>
          )}
          {run.captureFailureReason && (
            <>
              <dt>Capture Failure</dt>
              <dd>{formatLabel(run.captureFailureReason)}</dd>
            </>
          )}
          {run.captureDiagnostic && (
            <>
              <dt>Capture Diagnostic</dt>
              <dd>{formatLabel(run.captureDiagnostic)}</dd>
            </>
          )}
          {run.resultEvidenceSource && (
            <>
              <dt>Evidence Source</dt>
              <dd>{formatLabel(run.resultEvidenceSource)}</dd>
            </>
          )}
        </dl>
      )}
    </section>
  )
}
