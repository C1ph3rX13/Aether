"use client"

import { IconArrowRight, IconBulb, IconChevronDown } from "@tabler/icons-react"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { StatusBadge } from "@/components/status-badge"
import {
  Item,
  ItemContent,
  ItemDescription,
  ItemFooter,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Empty,
  EmptyHeader,
  EmptyDescription,
  EmptyTitle,
  EmptyMedia,
} from "@/components/ui/empty"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { RecordDetail } from "@/modules/workspace/record-detail"
import { ProjectLog } from "@/modules/log/view"
import type { useWorkspaceSession } from "@/modules/workspace/session"
import type { RecordReference } from "@/modules/workspace/contract"
import { formatCount, formatDateTime } from "@/lib/format"

export type InspectorTab = "detail" | "hints" | "log"
export function WorkspaceInspector({
  session,
  tab,
  onTabChange,
  onInspect,
  onReveal,
  onShowLog,
  logTarget,
}: {
  session: ReturnType<typeof useWorkspaceSession>
  tab: InspectorTab
  onTabChange: (tab: InspectorTab) => void
  onInspect: (reference: RecordReference, element?: HTMLElement) => void
  onReveal: (reference: RecordReference) => void
  onShowLog: (reference: { type: "fact" | "intent"; id: string }) => void
  logTarget: { type: "fact" | "intent"; id: string } | null
}) {
  if (!session.view) return null
  const { detail, selection, completionFactIds } = session.view
  const evidenceFact =
    selection?.type === "fact" &&
    selection.fact.status === "active" &&
    selection.fact.id !== "goal"
      ? selection.fact
      : null
  const last = detail.completionRecords.at(-1)
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <Tabs
        value={tab}
        onValueChange={(value) => onTabChange(value as InspectorTab)}
        className="min-h-0 flex-1 gap-0"
      >
        <div className="px-4 pb-4">
          <TabsList
            id="workspace-inspector-tabs"
            aria-label="Workspace inspection tabs"
            className="w-full"
          >
            <TabsTrigger value="detail">Details</TabsTrigger>
            <TabsTrigger value="hints">
              Hints
              <Badge variant="secondary">
                {formatCount(detail.hints.length)}
              </Badge>
            </TabsTrigger>
            <TabsTrigger value="log">Log</TabsTrigger>
          </TabsList>
        </div>
        <Separator />
        <TabsContent value="detail" className="min-h-0 overflow-y-auto p-4">
          <div className="flex flex-col gap-5">
            <RecordDetail
              project={detail}
              historical={session.replay.active}
              selection={selection}
              evidence={session.intentEvidence}
              onReveal={onReveal}
              onShowLog={onShowLog}
              evidenceControl={
                evidenceFact ? (
                  <Label htmlFor={`inspection-evidence-${evidenceFact.id}`}>
                    <Checkbox
                      id={`inspection-evidence-${evidenceFact.id}`}
                      aria-label={`Use ${evidenceFact.id} as Completion Evidence`}
                      checked={completionFactIds.includes(evidenceFact.id)}
                      disabled={!session.availability.completeProject}
                      onCheckedChange={(checked) =>
                        session.actions.selectCompletionEvidence(
                          checked === true
                            ? [...completionFactIds, evidenceFact.id]
                            : completionFactIds.filter(
                                (id) => id !== evidenceFact.id
                              )
                        )
                      }
                    />
                    Completion Evidence
                  </Label>
                ) : undefined
              }
            />
            <Separator />
            <Collapsible>
              <CollapsibleTrigger asChild>
                <Button variant="outline" className="w-full justify-between">
                  Project information
                  <IconChevronDown data-icon="inline-end" aria-hidden="true" />
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-4">
                <div className="flex flex-col gap-3">
                  <dl className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-4 gap-y-2 text-sm">
                    <dt className="text-muted-foreground">Lifecycle</dt>
                    <dd>
                      <StatusBadge value={detail.project.status} />
                    </dd>
                    <dt className="text-muted-foreground">Generation</dt>
                    <dd>{formatCount(detail.project.generation)}</dd>
                    <dt className="text-muted-foreground">Revision</dt>
                    <dd>{formatCount(detail.project.revision)}</dd>
                  </dl>
                  {last && (
                    <p className="text-xs leading-relaxed text-muted-foreground">
                      Generation {formatCount(last.generation)} completed{" "}
                      {formatDateTime(last.completedAt)} by {last.actor}.
                      {last.generation < detail.project.generation
                        ? " This Project was reopened into a new generation."
                        : ""}
                    </p>
                  )}
                </div>
              </CollapsibleContent>
            </Collapsible>
          </div>
        </TabsContent>
        <TabsContent value="hints" className="min-h-0 overflow-y-auto p-4">
          <div className="flex flex-col gap-4" aria-label="Project Hints">
            <p className="text-sm text-muted-foreground">
              Guidance recorded for this Project.
            </p>
            {detail.hints.length ? (
              <ItemGroup className="gap-3">
                {detail.hints.map((hint) => (
                  <Item
                    key={hint.id}
                    variant="outline"
                    size="sm"
                    role="listitem"
                  >
                    <ItemContent className="min-w-0">
                      <ItemTitle>
                        <IconBulb
                          className="size-4 shrink-0"
                          aria-hidden="true"
                        />
                        Hint {hint.id}
                      </ItemTitle>
                      <ItemDescription className="wrap-anywhere">
                        {hint.content}
                      </ItemDescription>
                    </ItemContent>
                    <ItemFooter className="flex-wrap">
                      <StatusBadge value={hint.status} />
                      <Button
                        variant="ghost"
                        aria-label={`Open Hint ${hint.id}`}
                        onClick={(event) =>
                          onInspect(
                            { type: "hint", id: hint.id },
                            event.currentTarget
                          )
                        }
                      >
                        Open Hint
                        <IconArrowRight
                          data-icon="inline-end"
                          aria-hidden="true"
                        />
                      </Button>
                    </ItemFooter>
                  </Item>
                ))}
              </ItemGroup>
            ) : (
              <Empty>
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <IconBulb aria-hidden="true" />
                  </EmptyMedia>
                  <EmptyTitle>No Hints</EmptyTitle>
                  <EmptyDescription>
                    No guidance has been recorded for this Project.
                  </EmptyDescription>
                </EmptyHeader>
              </Empty>
            )}
          </div>
        </TabsContent>
        <TabsContent
          forceMount
          hidden={tab !== "log"}
          value="log"
          className="min-h-0 overflow-y-auto p-4"
        >
          <ProjectLog
            projectId={detail.project.id}
            frozenLog={session.replayLog}
            visibleThroughRevision={
              session.replay.active ? detail.project.revision : undefined
            }
            target={logTarget}
            onInspect={onInspect}
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}
