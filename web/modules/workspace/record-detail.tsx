"use client"

import {
  IconArrowUpRight as ArrowUpRight,
  IconChevronDown,
} from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  ExecutionEvidence,
  type EvidenceRead,
} from "@/modules/execution-evidence/view"
import type { WorkspaceExecutionEvidence } from "@/modules/execution-evidence/model"
import type {
  RecordReference,
  RecordSelection,
} from "@/modules/workspace/contract"
import { getFactType, type ProjectDetail } from "@/modules/workspace/model"
import { formatCount, formatDateTime, formatLabel } from "@/lib/format"

export function RecordDetail({
  project,
  selection,
  evidence,
  onReveal,
  onShowLog,
  evidenceControl,
  historical = false,
}: {
  project: ProjectDetail
  selection: RecordSelection | null
  evidence: EvidenceRead & { data?: WorkspaceExecutionEvidence }
  onReveal: (reference: RecordReference) => void
  onShowLog: (reference: { type: "fact" | "intent"; id: string }) => void
  evidenceControl?: React.ReactNode
  historical?: boolean
}) {
  const reference =
    selection?.type === "fact"
      ? { type: "fact" as const, id: selection.fact.id }
      : selection?.type === "intent"
        ? { type: "intent" as const, id: selection.intent.id }
        : selection?.type === "hint"
          ? { type: "hint" as const, id: selection.hint.id }
          : selection?.reference
  const status =
    selection?.type === "fact"
      ? selection.fact.status
      : selection?.type === "intent"
        ? selection.intent.status
        : selection?.type === "hint"
          ? selection.hint.status
          : undefined
  return (
    <section aria-label="Record Detail" className="flex flex-col gap-4">
      <header className="border-b pb-4">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-sm font-medium wrap-anywhere">
            {reference
              ? `${formatLabel(reference.type)} ${reference.id}`
              : "Record details"}
          </h3>
          {status && <Badge variant="outline">{formatLabel(status)}</Badge>}
        </div>
        <p className="mt-1 text-sm text-muted-foreground">
          Generation {formatCount(project.project.generation)} · Revision{" "}
          {formatCount(project.project.revision)}
        </p>
      </header>
      {!selection && (
        <p className="text-sm text-muted-foreground">
          Select a Fact or Intent to inspect its details.
        </p>
      )}
      <div className="flex flex-col gap-5 pb-4">
        {evidenceControl}
        {reference && reference.type !== "hint" && (
          <Button
            size="default"
            variant="outline"
            className="self-start"
            onClick={() =>
              onShowLog({
                type: reference.type === "fact" ? "fact" : "intent",
                id: reference.id,
              })
            }
          >
            Show in Log
          </Button>
        )}
        {selection?.type === "missing" && (
          <p role="status">
            This record is not present in the displayed Blackboard Revision.
          </p>
        )}
        {selection?.type === "fact" && (
          <>
            <Metadata
              items={[
                ["ID", selection.fact.id],
                ["Role", formatLabel(getFactType(selection.fact.id))],
                [
                  "Lifecycle",
                  <Badge key="lifecycle" variant="outline">
                    {formatLabel(selection.fact.status)}
                  </Badge>,
                ],
              ]}
            />
            <Content title="Description" text={selection.fact.description} />
            <Relations
              title="Producing Intents"
              references={selection.producingIntents.map((intent) => ({
                type: "intent",
                id: intent.id,
              }))}
              onReveal={onReveal}
            />
            <Relations
              title="Source Facts"
              references={[
                ...new Set(
                  selection.producingIntents.flatMap((intent) => intent.from)
                ),
              ].map((id) => ({ type: "fact", id }))}
              onReveal={onReveal}
            />
            {selection.fact.supersedes && (
              <Relations
                title="Supersedes"
                references={[{ type: "fact", id: selection.fact.supersedes }]}
                onReveal={onReveal}
              />
            )}
            {selection.fact.replacementFactId && (
              <Relations
                title="Replacement"
                references={[
                  { type: "fact", id: selection.fact.replacementFactId },
                ]}
                onReveal={onReveal}
              />
            )}
            {(selection.fact.retractedAt || selection.fact.retractedBy) && (
              <Metadata
                title="Retraction information"
                items={[
                  [
                    "Retracted",
                    selection.fact.retractedAt ? (
                      <Timestamp
                        key="time"
                        value={selection.fact.retractedAt}
                      />
                    ) : (
                      "Not reported"
                    ),
                  ],
                  [
                    "Retracted by",
                    selection.fact.retractedBy ?? "Not reported",
                  ],
                ]}
              />
            )}
            {selection.fact.retractionReason && (
              <Content
                title="Retraction Reason"
                text={selection.fact.retractionReason}
              />
            )}
          </>
        )}
        {selection?.type === "intent" && (
          <>
            <Metadata
              items={[
                ["ID", selection.intent.id],
                ["Kind", formatLabel(selection.intent.kind)],
                [
                  "Lifecycle",
                  <Badge key="lifecycle" variant="outline">
                    {formatLabel(selection.intent.status)}
                  </Badge>,
                ],
                ["Creator", selection.intent.creator],
                [
                  "Created",
                  <Timestamp
                    key="created"
                    value={selection.intent.createdAt}
                  />,
                ],
                ...(selection.intent.concludedAt
                  ? [
                      [
                        "Concluded",
                        <Timestamp
                          key="concluded"
                          value={selection.intent.concludedAt}
                        />,
                      ] as [string, React.ReactNode],
                    ]
                  : []),
                ...(selection.intent.cancelledAt
                  ? [
                      [
                        "Cancelled",
                        <Timestamp
                          key="cancelled"
                          value={selection.intent.cancelledAt}
                        />,
                      ] as [string, React.ReactNode],
                    ]
                  : []),
                ...(selection.intent.cancelledBy
                  ? [
                      ["Cancelled by", selection.intent.cancelledBy] as [
                        string,
                        React.ReactNode,
                      ],
                    ]
                  : []),
              ]}
            />
            <Content title="Description" text={selection.intent.description} />
            <Relations
              title="From Facts"
              references={selection.intent.from.map((id) => ({
                type: "fact",
                id,
              }))}
              onReveal={onReveal}
            />
            <Relations
              title="To Fact"
              references={
                selection.intent.to
                  ? [{ type: "fact", id: selection.intent.to }]
                  : []
              }
              onReveal={onReveal}
            />
            {selection.intent.cancellationReason && (
              <Content
                title="Cancellation Reason"
                text={selection.intent.cancellationReason}
              />
            )}
            {project.completionRecords
              .filter((record) => record.intentId === selection.intent.id)
              .map((record) => (
                <section
                  key={record.generation}
                  className="flex flex-col gap-3"
                >
                  <h3 className="font-medium">Completion Record</h3>
                  <Metadata
                    title="Completion information"
                    items={[
                      ["Generation", formatCount(record.generation)],
                      ["Actor", record.actor],
                      [
                        "Completed",
                        <Timestamp
                          key="completed"
                          value={record.completedAt}
                        />,
                      ],
                    ]}
                  />
                  <Content
                    title="Completion Description"
                    text={record.description}
                  />
                </section>
              ))}
            {historical ? (
              <p className="text-sm text-muted-foreground">
                Current Execution Evidence is unavailable in historical Replay.
              </p>
            ) : (
              <Collapsible>
                <CollapsibleTrigger asChild>
                  <Button
                    size="default"
                    variant="outline"
                    className="w-full justify-between"
                  >
                    Execution Evidence
                    <IconChevronDown
                      data-icon="inline-end"
                      aria-hidden="true"
                    />
                  </Button>
                </CollapsibleTrigger>
                <CollapsibleContent className="flex flex-col gap-4 pt-4">
                  <ExecutionEvidence
                    title="Current Active Reason"
                    run={evidence.data?.activeReason}
                    query={evidence}
                    empty="No Active Reason"
                    compact
                  />
                  <ExecutionEvidence
                    title="Intent Execution Evidence"
                    run={evidence.data?.selectedIntentRun}
                    query={evidence}
                    empty="Not run yet"
                  />
                </CollapsibleContent>
              </Collapsible>
            )}
          </>
        )}
        {selection?.type === "hint" && (
          <>
            <Metadata
              items={[
                ["ID", selection.hint.id],
                [
                  "Lifecycle",
                  <Badge key="lifecycle" variant="outline">
                    {formatLabel(selection.hint.status)}
                  </Badge>,
                ],
                ["Creator", selection.hint.creator],
                ["Generation", formatCount(selection.hint.generation)],
                [
                  "Created",
                  <Timestamp key="created" value={selection.hint.createdAt} />,
                ],
                ...(selection.hint.withdrawnAt
                  ? [
                      [
                        "Withdrawn",
                        <Timestamp
                          key="withdrawn"
                          value={selection.hint.withdrawnAt}
                        />,
                      ] as [string, React.ReactNode],
                    ]
                  : []),
                ...(selection.hint.withdrawnBy
                  ? [
                      ["Withdrawn by", selection.hint.withdrawnBy] as [
                        string,
                        React.ReactNode,
                      ],
                    ]
                  : []),
              ]}
            />
            <Content title="Content" text={selection.hint.content} />
            {selection.hint.withdrawalReason && (
              <Content
                title="Withdrawal Reason"
                text={selection.hint.withdrawalReason}
              />
            )}
          </>
        )}
      </div>
    </section>
  )
}
function Metadata({
  items,
  title = "Record information",
}: {
  items: [string, React.ReactNode][]
  title?: string
}) {
  return (
    <Collapsible>
      <CollapsibleTrigger asChild>
        <Button
          size="default"
          variant="outline"
          className="w-full justify-between"
        >
          {title}
          <IconChevronDown data-icon="inline-end" aria-hidden="true" />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="pt-4">
        <dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-x-4 gap-y-2 text-sm [&_dd]:wrap-anywhere [&_dt]:text-muted-foreground">
          {items.map(([name, value]) => (
            <div key={name} className="contents">
              <dt>{name}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      </CollapsibleContent>
    </Collapsible>
  )
}
function Timestamp({ value }: { value: string }) {
  return <time dateTime={value}>{formatDateTime(value)}</time>
}
function Content({ title, text }: { title: string; text: string }) {
  return (
    <section>
      <h3 className="font-medium">{title}</h3>
      <p className="mt-2 max-w-prose text-sm leading-6 wrap-anywhere whitespace-pre-wrap">
        {text}
      </p>
    </section>
  )
}
function Relations({
  title,
  references,
  onReveal,
}: {
  title: string
  references: RecordReference[]
  onReveal: (reference: RecordReference) => void
}) {
  return (
    <section>
      <h3 className="font-medium">{title}</h3>
      {references.length ? (
        <div className="mt-2 flex flex-wrap gap-2">
          {references.map((reference) => (
            <Button
              key={reference.id}
              size="default"
              variant="outline"
              aria-label={`Open ${formatLabel(reference.type)} ${reference.id}`}
              onClick={() => onReveal(reference)}
            >
              {reference.id}
              <ArrowUpRight data-icon="inline-end" aria-hidden="true" />
            </Button>
          ))}
        </div>
      ) : (
        <p className="mt-1 text-sm text-muted-foreground">None</p>
      )}
    </section>
  )
}
