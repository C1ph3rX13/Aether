"use client"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useMemo, useRef, useState } from "react"
import { SWRConfig } from "swr"
import {
  IconArrowLeft as ArrowLeft,
  IconLayoutSidebarRight,
  IconLayoutSidebarRightCollapse,
  IconGitBranch,
  IconListCheck,
} from "@tabler/icons-react"
import { ReplayControl } from "@/modules/replay/view"
import { ProjectActions } from "@/modules/commands/project-actions"
import { BlackboardActions } from "@/modules/commands/blackboard-actions"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { StatusBadge } from "@/components/status-badge"
import { Separator } from "@/components/ui/separator"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { FieldDescription } from "@/components/ui/field"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet"
import { WorkspaceGraph } from "@/modules/workspace/graph/view"
import { WorkspaceRecords } from "@/modules/workspace/record-list"
import {
  WorkspaceInspector,
  type InspectorTab,
} from "@/modules/workspace/inspector"
import { projectIDFromPathname } from "@/modules/workspace/model"
import { useWorkspaceSession } from "@/modules/workspace/session"
import type { RecordReference } from "@/modules/workspace/contract"
import { formatCount, formatLabel } from "@/lib/format"
import { useNavigationGuard } from "@/components/navigation-guard"
export function ProjectWorkspace() {
  const id = projectIDFromPathname(usePathname())
  if (!id)
    return (
      <WorkspaceMessage
        title="Invalid Project"
        description="This URL does not contain a valid Project identifier."
      />
    )
  return (
    <SWRConfig value={{ provider: () => new Map() }}>
      <WorkspaceScreen key={id} id={id} />
    </SWRConfig>
  )
}
function WorkspaceScreen({ id }: { id: string }) {
  const session = useWorkspaceSession(id)
  const commandPending = session.commands.pending
  useNavigationGuard(
    useMemo(
      () => ({
        isBusy: () => commandPending,
        busy: commandPending,
      }),
      [commandPending]
    )
  )
  const [commandNotice, setCommandNotice] = useState<string | null>(null)
  const [tab, setTab] = useState<InspectorTab>("detail")
  const [workspaceView, setWorkspaceView] = useState("graph")
  const [reveal, setReveal] = useState<{
    reference: RecordReference
    sequence: number
  } | null>(null)
  const returnFocus = useRef<HTMLElement | null>(null)
  const revealSequence = useRef(0)
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [logTarget, setLogTarget] = useState<{
    type: "fact" | "intent"
    id: string
  } | null>(null)
  const expandInspector = () => setInspectorOpen(true)
  const inspect = (reference: RecordReference, element?: HTMLElement) => {
    setReveal(null)
    if (!inspectorOpen)
      returnFocus.current =
        element ??
        (document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null)
    setTab("detail")
    expandInspector()
    session.actions.inspect(reference)
  }
  const revealRecord = (reference: RecordReference) => {
    setWorkspaceView("graph")
    setTab("detail")
    setReveal({ reference, sequence: ++revealSequence.current })
    expandInspector()
    session.actions.inspect(reference)
  }
  const showLog = (reference: { type: "fact" | "intent"; id: string }) => {
    setLogTarget(reference)
    setTab("log")
    expandInspector()
  }
  const retry = () => void session.actions.retry()
  if (session.status === "loading")
    return (
      <div
        role="status"
        aria-label="Loading Workspace"
        className="flex flex-col gap-4 p-6"
      >
        <FieldDescription className="hidden motion-reduce:block">
          Loading Workspace…
        </FieldDescription>
        <Skeleton className="h-24" />
        <Skeleton className="h-64" />
      </div>
    )
  if (session.status !== "ready" || !session.view)
    return (
      <WorkspaceMessage
        title={
          session.status === "not-found"
            ? "Project Not Found"
            : "Project unavailable"
        }
        description={
          session.status === "not-found"
            ? "This Project does not exist or has been deleted."
            : "Could not read this Project. Check your connection and retry."
        }
        retry={session.status === "error" ? retry : undefined}
        pending={session.refreshing}
      />
    )
  const { detail, freshness, selection, completionFactIds } = session.view
  const project = detail.project
  const { availability } = session
  const workspaceContent = (
    <>
      <TabsContent value="graph" className="h-full min-h-0">
        <WorkspaceGraph
          detail={detail}
          inspected={
            selection?.type === "fact"
              ? { type: "fact", id: selection.fact.id }
              : selection?.type === "intent"
                ? { type: "intent", id: selection.intent.id }
                : null
          }
          evidenceIds={completionFactIds}
          evidenceDisabled={!availability.completeProject}
          onInspect={inspect}
          onEvidenceChange={session.actions.selectCompletionEvidence}
          target={reveal}
        />
      </TabsContent>
      <TabsContent value="records" className="h-full min-h-0 overflow-hidden">
        <WorkspaceRecords
          detail={detail}
          evidenceIds={completionFactIds}
          evidenceDisabled={!availability.completeProject}
          onInspect={inspect}
          onEvidenceChange={session.actions.selectCompletionEvidence}
        />
      </TabsContent>
    </>
  )
  return (
    <div className="@container/main flex min-h-[42rem] min-w-0 flex-col gap-3 p-4 lg:h-[calc(100svh-4rem)] lg:px-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <Button variant="outline" size="icon" asChild>
            <Link href="/projects" aria-label="Back to Projects">
              <ArrowLeft aria-hidden="true" />
            </Link>
          </Button>
          <div className="flex min-w-0 flex-col gap-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-2xl font-semibold wrap-anywhere">
                {project.title}
              </h2>
              <StatusBadge value={project.status} />
              <Badge
                className={freshness === "live" ? "sr-only" : undefined}
                aria-label={`Data Freshness: ${formatLabel(freshness)}`}
                variant="outline"
              >
                {formatLabel(freshness)}
              </Badge>
            </div>
            <p className="text-sm text-muted-foreground">
              {formatCount(detail.facts.length)} Facts ·{" "}
              {formatCount(detail.intents.length)} Intents · Generation{" "}
              {formatCount(project.generation)} · Revision{" "}
              {formatCount(project.revision)}
              {session.replay.active ? " · Historical snapshot" : ""}
            </p>
          </div>
        </div>
        <div className="flex max-w-full flex-wrap items-center gap-2">
          <ProjectActions
            session={session}
            onSuccess={setCommandNotice}
            onReplay={() => {
              void session.actions.startReplay()
            }}
          />
        </div>
      </header>
      <ReplayControl replay={session.replay} actions={session.actions.replay} />
      {session.replay.active && (
        <p role="status" className="text-sm text-muted-foreground">
          Current data: {formatLabel(session.liveFreshness)}. Live updates are
          kept outside the frozen Replay.
        </p>
      )}
      {session.replayError && (
        <Alert>
          <AlertTitle>Replay unavailable</AlertTitle>
          <AlertDescription>{session.replayError}</AlertDescription>
          {session.replay.frameRevisions.length === 0 && (
            <Button
              variant="outline"
              size="default"
              disabled={session.replay.preparing}
              onClick={() => void session.actions.startReplay()}
            >
              Retry Replay
            </Button>
          )}
        </Alert>
      )}
      {commandNotice && (
        <p role="status" className="text-sm">
          {commandNotice}
        </p>
      )}
      {session.evidenceNotice && (
        <Alert>
          <AlertTitle>Completion Evidence updated</AlertTitle>
          <AlertDescription>{session.evidenceNotice}</AlertDescription>
        </Alert>
      )}
      {project.status === "paused" && (
        <p className="text-sm text-muted-foreground">
          Execution is paused. The Blackboard remains readable and Hints can
          still guide the next tasks.
        </p>
      )}
      {session.refreshFailed && (
        <Alert>
          <AlertTitle>Project update unavailable</AlertTitle>
          <AlertDescription>
            The last valid Blackboard projection remains readable. Retry to load
            current data.
          </AlertDescription>
        </Alert>
      )}
      {freshness !== "live" &&
        freshness !== "replay" &&
        !session.refreshFailed && (
          <p role="status" className="text-sm text-muted-foreground">
            Synchronizing current data. The last valid projection remains
            read-only.
          </p>
        )}
      <section
        aria-label="Blackboard Workspace"
        className="flex min-h-0 min-w-0 flex-1 flex-col"
      >
        <Tabs
          value={workspaceView}
          onValueChange={setWorkspaceView}
          className="min-h-0 min-w-0 flex-1 gap-3"
        >
          <div className="flex flex-wrap items-center justify-between gap-3">
            <TabsList id="workspace-reading-tabs" aria-label="Blackboard view">
              <TabsTrigger value="graph">
                <IconGitBranch aria-hidden="true" />
                Blackboard Graph
              </TabsTrigger>
              <TabsTrigger value="records">
                <IconListCheck aria-hidden="true" />
                Records
              </TabsTrigger>
            </TabsList>
            <div className="flex flex-wrap items-center gap-2">
              <BlackboardActions
                session={session}
                onSuccess={setCommandNotice}
                onAddHint={() => setTab("hints")}
              />
              <Button
                variant="outline"
                size="icon"
                aria-label="Toggle right inspection panel"
                aria-expanded={inspectorOpen}
                aria-controls="project-inspection"
                onClick={(event) => {
                  returnFocus.current = event.currentTarget
                  setInspectorOpen(!inspectorOpen)
                }}
              >
                {inspectorOpen ? (
                  <IconLayoutSidebarRightCollapse aria-hidden="true" />
                ) : (
                  <IconLayoutSidebarRight aria-hidden="true" />
                )}
              </Button>
            </div>
          </div>
          <div
            className="flex h-[70svh] min-h-[32rem] min-w-0 flex-1 overflow-hidden rounded-xl border lg:h-auto"
            data-testid="project-canvas"
          >
            {workspaceContent}
          </div>
        </Tabs>
      </section>
      <p
        role="status"
        className={
          completionFactIds.length ? "text-xs text-muted-foreground" : "sr-only"
        }
      >
        {completionFactIds.length
          ? `${formatCount(completionFactIds.length)} Facts selected as Completion Evidence`
          : "Select a node to inspect. Select Evidence independently to prepare completion."}
      </p>
      <Sheet open={inspectorOpen} onOpenChange={setInspectorOpen}>
        <SheetContent
          id="project-inspection"
          side="right"
          className="w-full gap-0 sm:max-w-md"
          onCloseAutoFocus={(event) => {
            const destination =
              returnFocus.current?.isConnected &&
              !returnFocus.current.matches(":disabled, [aria-disabled='true']")
                ? returnFocus.current
                : document.getElementById("main-content")
            if (destination) {
              event.preventDefault()
              destination.focus()
            }
          }}
        >
          <SheetHeader>
            <SheetTitle>Project inspection</SheetTitle>
            <SheetDescription>{project.title}</SheetDescription>
          </SheetHeader>
          <Separator />
          <div className="min-h-0 flex-1">
            <WorkspaceInspector
              session={session}
              tab={tab}
              onTabChange={setTab}
              onInspect={inspect}
              onReveal={revealRecord}
              onShowLog={showLog}
              logTarget={logTarget}
            />
          </div>
        </SheetContent>
      </Sheet>
    </div>
  )
}
function WorkspaceMessage({
  title,
  description,
  retry,
  pending,
}: {
  title: string
  description: string
  retry?: () => void
  pending?: boolean
}) {
  return (
    <Empty className="m-4 w-auto lg:m-6" role={retry ? "alert" : "status"}>
      <EmptyHeader>
        <EmptyTitle>
          <h2>{title}</h2>
        </EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        {retry && (
          <Button variant="outline" disabled={pending} onClick={retry}>
            Retry Project
          </Button>
        )}
        <Button variant="outline" role="link" asChild>
          <Link href="/projects">
            <ArrowLeft data-icon="inline-start" aria-hidden="true" /> Return to
            Projects
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
