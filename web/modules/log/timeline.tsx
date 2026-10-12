"use client"
import { useId, useRef, useState } from "react"
import {
  IconArrowRight,
  IconChevronDown,
  IconInfoCircle,
  IconCheck,
  IconCopy,
  IconCircleDot,
} from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Item,
  ItemContent,
  ItemTitle,
  ItemDescription,
  ItemFooter,
  ItemGroup,
} from "@/components/ui/item"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
  PopoverHeader,
  PopoverTitle,
  PopoverDescription,
} from "@/components/ui/popover"
import {
  Field,
  FieldGroup,
  FieldLabel,
  FieldDescription,
} from "@/components/ui/field"
import { Textarea } from "@/components/ui/textarea"
import { Separator } from "@/components/ui/separator"
import type {
  ConsoleLogEventView,
  ConsoleLogFocusTarget,
  ConsoleLogRevisionView,
} from "@/modules/log/model"
import { formatCreatedTime, formatLabel } from "@/lib/format"
import { cn } from "@/lib/utils"
function TimelineEvent({
  event,
  onInspect,
  selected,
}: {
  event: ConsoleLogEventView
  onInspect: (reference: ConsoleLogFocusTarget, element: HTMLElement) => void
  selected: boolean
}) {
  const [expanded, setExpanded] = useState(false)
  const eventLabel = event.label
  return (
    <Collapsible open={expanded} onOpenChange={setExpanded}>
      <Item
        variant={selected ? "muted" : "outline"}
        size="sm"
        role="listitem"
        id={`log-${event.id}`}
        data-log-target={selected ? "true" : undefined}
      >
        <ItemContent className="min-w-0">
          <ItemTitle>
            <event.icon className="size-4 shrink-0" aria-hidden="true" />
            {event.kindLabel}
          </ItemTitle>
          {eventLabel.length <= 100 ? (
            <p className="text-sm leading-relaxed wrap-anywhere whitespace-pre-wrap">
              {eventLabel}
            </p>
          ) : (
            !expanded && (
              <ItemDescription className="wrap-anywhere whitespace-pre-wrap">
                {eventLabel}
              </ItemDescription>
            )
          )}
          <CollapsibleContent>
            <p className="text-sm leading-relaxed wrap-anywhere whitespace-pre-wrap">
              {eventLabel}
            </p>
          </CollapsibleContent>
        </ItemContent>
        {(eventLabel.length > 100 || event.focusTarget) && (
          <ItemFooter className="flex-wrap">
            {eventLabel.length > 100 && (
              <CollapsibleTrigger asChild>
                <Button size="default" variant="ghost">
                  {expanded ? "Hide details" : "Show details"}
                  <IconChevronDown data-icon="inline-end" />
                </Button>
              </CollapsibleTrigger>
            )}
            {event.focusTarget && (
              <Button
                size="default"
                variant="ghost"
                aria-label={`Inspect ${formatLabel(event.focusTarget.type)} ${event.focusTarget.id} from Log`}
                onClick={(click) =>
                  onInspect(event.focusTarget!, click.currentTarget)
                }
              >
                Open {event.focusTarget.id}
                <IconArrowRight data-icon="inline-end" />
              </Button>
            )}
          </ItemFooter>
        )}
      </Item>
    </Collapsible>
  )
}
function LogSource({ revision }: { revision: ConsoleLogRevisionView }) {
  const fieldId = useId()
  const field = useRef<HTMLTextAreaElement>(null)
  const [copied, setCopied] = useState(false)
  const [copyError, setCopyError] = useState(false)
  const runId = revision.runId
  if (!runId) {
    return (
      <p className="text-xs wrap-anywhere text-muted-foreground">
        {formatLabel(revision.sourceKind)} · {revision.sourceLabel}
      </p>
    )
  }
  const copy = async () => {
    let success = false
    try {
      await navigator.clipboard.writeText(runId)
      success = true
    } catch {
      // Use the same selectable native control path as the existing Snapshot dialog.
      field.current?.select()
      try {
        success = document.execCommand("copy")
      } catch {
        success = false
      }
    }
    setCopied(success)
    setCopyError(!success)
  }
  return (
    <Popover
      onOpenChange={() => {
        setCopied(false)
        setCopyError(false)
      }}
    >
      <PopoverTrigger asChild>
        <Button
          size="default"
          variant="ghost"
          className="self-start"
          aria-label={`View Run source for Revision ${revision.revision}`}
        >
          {formatLabel(revision.sourceKind)} · {revision.sourceLabel}
          <IconInfoCircle data-icon="inline-end" aria-hidden="true" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="flex flex-col gap-4"
        aria-labelledby={`${fieldId}-title`}
        aria-describedby={`${fieldId}-description`}
      >
        <PopoverHeader>
          <PopoverTitle id={`${fieldId}-title`}>Run source</PopoverTitle>
          <PopoverDescription id={`${fieldId}-description`}>
            Execution that produced Revision {revision.revision}.
          </PopoverDescription>
        </PopoverHeader>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor={fieldId}>Run ID</FieldLabel>
            <Textarea
              ref={field}
              id={fieldId}
              readOnly
              rows={2}
              value={runId}
            />
            <FieldDescription role="status" aria-live="polite">
              {copyError
                ? "Could not copy. Select the Run ID and copy it manually."
                : copied
                  ? "Run ID copied."
                  : "Preserved from this Revision’s source."}
            </FieldDescription>
          </Field>
        </FieldGroup>
        <Button
          size="default"
          variant="outline"
          className="self-start"
          onClick={copy}
        >
          {copied ? (
            <IconCheck data-icon="inline-start" />
          ) : (
            <IconCopy data-icon="inline-start" />
          )}
          {copied ? "Copied" : "Copy Run ID"}
        </Button>
      </PopoverContent>
    </Popover>
  )
}
export function ProjectTimeline({
  revisions,
  currentRevision,
  selected,
  onInspect,
}: {
  revisions: ConsoleLogRevisionView[]
  currentRevision: number
  selected?: string
  onInspect: (reference: ConsoleLogFocusTarget, element: HTMLElement) => void
}) {
  return (
    <ol className="flex flex-col">
      {revisions.map((group, index) => (
        <li
          key={group.revision}
          className="grid grid-cols-[1rem_minmax(0,1fr)] gap-3"
          data-revision={group.revision}
        >
          <div className="relative flex justify-center" aria-hidden="true">
            {revisions.length > 1 && (
              <Separator
                orientation="vertical"
                className={cn(
                  "absolute",
                  index === 0 ? "top-3" : "top-0",
                  index === revisions.length - 1
                    ? "data-[orientation=vertical]:h-3"
                    : "bottom-0 data-[orientation=vertical]:h-auto"
                )}
              />
            )}
            <IconCircleDot
              className={cn(
                "relative mt-1 size-4 shrink-0 bg-background",
                group.revision === currentRevision
                  ? "text-foreground"
                  : "text-muted-foreground"
              )}
            />
          </div>
          <section
            aria-label={`Revision ${group.revision}`}
            className={cn(
              "flex min-w-0 flex-col gap-3",
              index < revisions.length - 1 && "pb-6"
            )}
          >
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <Badge
                  variant={
                    group.revision === currentRevision ? "default" : "secondary"
                  }
                >
                  Revision {group.revision}
                </Badge>
                <Badge variant="outline">Generation {group.generation}</Badge>
              </div>
              <time
                dateTime={group.occurredAt}
                className="text-xs text-muted-foreground"
              >
                {formatCreatedTime(group.occurredAt)}
              </time>
              <LogSource revision={group} />
            </div>
            <ItemGroup className="gap-2">
              {group.events.map((event) => (
                <TimelineEvent
                  key={event.id}
                  event={event}
                  onInspect={onInspect}
                  selected={event.id === selected}
                />
              ))}
            </ItemGroup>
          </section>
        </li>
      ))}
    </ol>
  )
}
