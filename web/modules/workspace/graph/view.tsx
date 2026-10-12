"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  Background,
  Handle,
  MiniMap,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  useStore,
  useNodesState,
  useUpdateNodeInternals,
  useViewport,
  type NodeProps,
  type Node,
} from "@xyflow/react"
import {
  IconCircleDot as CircleDot,
  IconFlag as Flag,
  IconFlask as FlaskConical,
  IconMaximize as Maximize,
  IconMinus as Minus,
  IconPlus as Plus,
  IconRotate as RotateCcw,
  IconTarget as Target,
  IconChevronRight,
  IconGitBranch,
} from "@tabler/icons-react"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { ButtonGroup } from "@/components/ui/button-group"
import { Badge } from "@/components/ui/badge"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  relationshipGraph,
  type GraphDirection,
  type GraphRecord,
  type RelationshipNode,
} from "@/modules/workspace/graph/model"
import type { ProjectDetail } from "@/modules/workspace/model"
import type { RecordReference } from "@/modules/workspace/contract"
import { formatLabel } from "@/lib/format"
import { cn } from "@/lib/utils"

export interface GraphNodeData extends GraphRecord {
  inspected: boolean
  evidenceSelected: boolean
  evidenceDisabled: boolean
  focus: (reference: RecordReference) => void
  inspect: (reference: RecordReference, element?: HTMLElement) => void
  selectEvidence: (id: string, selected: boolean) => void
}
type GraphNode = Node<GraphNodeData, "record">

export function GraphRecordCard({ data }: { data: GraphNodeData }) {
  const Icon =
    data.role === "origin"
      ? Flag
      : data.role === "goal"
        ? Target
        : data.role === "intent"
          ? CircleDot
          : FlaskConical
  return (
    <Card
      className={cn(
        "h-40 w-60 gap-3 py-4",
        data.inspected && "ring-2 ring-ring"
      )}
      onFocus={(event) => {
        if (event.target.matches(":focus-visible")) data.focus(data.reference)
      }}
    >
      <CardHeader className="px-4">
        <CardTitle className="flex items-center justify-between gap-2">
          <span className="flex items-center gap-2">
            <Icon aria-hidden="true" className="size-4 text-muted-foreground" />
            {formatLabel(data.role)}
          </span>
          <StatusBadge value={data.status} />
        </CardTitle>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 px-4">
        <CardDescription className="line-clamp-2 text-foreground">
          {data.description}
        </CardDescription>
      </CardContent>
      <CardFooter className="justify-between gap-2 px-4">
        <Button
          id={`inspect-${data.reference.type}-${data.reference.id}`}
          variant="ghost"
          size="default"
          className="nodrag nopan"
          aria-label={`Open ${formatLabel(data.reference.type)} ${data.reference.id}`}
          onClick={(event) => {
            event.stopPropagation()
            data.inspect(data.reference, event.currentTarget)
          }}
        >
          Details
          <IconChevronRight data-icon="inline-end" aria-hidden="true" />
        </Button>
        {data.completionEligible && (
          <Label
            htmlFor={`evidence-${data.reference.id}`}
            className="nodrag nopan"
            onClick={(event) => event.stopPropagation()}
          >
            <Checkbox
              id={`evidence-${data.reference.id}`}
              aria-labelledby={`evidence-label-${data.reference.id}`}
              checked={data.evidenceSelected}
              disabled={data.evidenceDisabled}
              onCheckedChange={(checked) =>
                data.selectEvidence(data.reference.id, checked === true)
              }
            />
            <span
              id={`evidence-label-${data.reference.id}`}
              className="sr-only"
            >
              Select {data.reference.id} as Completion Evidence
            </span>
            <span aria-hidden="true">Evidence</span>
          </Label>
        )}
      </CardFooter>
    </Card>
  )
}

function RecordNode({ data }: NodeProps<GraphNode>) {
  return (
    <>
      <Handle
        type="target"
        position={Position.Top}
        className="!border-border !bg-muted-foreground"
      />
      <GraphRecordCard data={data} />
      <Handle
        type="source"
        position={Position.Bottom}
        className="!border-border !bg-muted-foreground"
      />
    </>
  )
}
const nodeTypes = { record: RecordNode }

export interface GraphProps {
  detail: ProjectDetail
  inspected: RecordReference | null
  evidenceIds: string[]
  evidenceDisabled: boolean
  onInspect: (reference: RecordReference, element?: HTMLElement) => void
  onEvidenceChange: (ids: string[]) => void
  target: { reference: RecordReference; sequence: number } | null
}

function GraphCanvas({
  detail,
  inspected,
  evidenceIds,
  evidenceDisabled,
  onInspect,
  onEvidenceChange,
  target,
}: GraphProps) {
  const [layout, setLayout] = useState<{
    fontScale: number
    direction: GraphDirection
  }>({ fontScale: 1, direction: "LR" })
  const [relayoutSignal, setRelayoutSignal] = useState(0)
  useEffect(() => {
    const update = () => {
      const fontScale =
        parseFloat(getComputedStyle(document.documentElement).fontSize) / 16
      setLayout((current) =>
        current.fontScale === fontScale ? current : { ...current, fontScale }
      )
    }
    const observer = new ResizeObserver(update)
    observer.observe(document.documentElement)
    update()
    return () => observer.disconnect()
  }, [])
  const graph = useMemo(
    () => relationshipGraph(detail, layout.fontScale, layout.direction),
    [detail, layout]
  )
  const [recordNodes, setRecordNodes, onNodesChange] =
    useNodesState<RelationshipNode>(graph.nodes)
  const previousLayout = useRef(layout)
  const updateNodeInternals = useUpdateNodeInternals()
  useEffect(() => {
    const resetPositions = previousLayout.current !== layout
    previousLayout.current = layout
    // Revision updates preserve manual positions and React Flow measurements.
    // Explicit layout changes restore Dagre coordinates.
    setRecordNodes((current) =>
      graph.nodes.map((node) => {
        const existing = current.find((record) => record.id === node.id)
        return {
          ...node,
          position:
            !resetPositions && existing ? existing.position : node.position,
          measured: existing?.measured,
        }
      })
    )
  }, [graph.nodes, layout, setRecordNodes])
  const flow = useReactFlow<GraphNode>()
  const { zoom } = useViewport()
  const width = useStore((state) => state.width)
  const height = useStore((state) => state.height)
  const fitGraph = useCallback(
    () =>
      flow.fitView({
        padding: {
          top: `${(width < 480 ? 108 : 64) * layout.fontScale}px`,
          bottom: `${112 * layout.fontScale}px`,
          left: `${24 * layout.fontScale}px`,
          right: `${24 * layout.fontScale}px`,
        },
        maxZoom: 1,
      }),
    [flow, width, layout.fontScale]
  )
  useEffect(() => {
    if (!relayoutSignal) return
    let fitFrame = 0
    const frame = requestAnimationFrame(() => {
      fitFrame = requestAnimationFrame(() => void fitGraph())
    })
    return () => {
      cancelAnimationFrame(frame)
      cancelAnimationFrame(fitFrame)
    }
  }, [relayoutSignal, fitGraph])
  const readingScale = useRef<number | null>(null)
  const viewportSize = useRef({ width: 0, height: 0 })
  const focusRecord = useCallback(
    (reference: RecordReference) => {
      const node = flow.getNode(`${reference.type}:${reference.id}`)
      if (!node) return false
      const bounds = flow.getNodesBounds([node])
      void flow.setCenter(
        bounds.x + bounds.width / 2,
        bounds.y + bounds.height / 2,
        { zoom: 1 }
      )
      return true
    },
    [flow]
  )
  const nodes: GraphNode[] = recordNodes.map((node) => ({
    ...node,
    data: {
      ...node.data,
      inspected:
        inspected?.type === node.data.reference.type &&
        inspected.id === node.data.reference.id,
      evidenceSelected: evidenceIds.includes(node.data.reference.id),
      evidenceDisabled,
      focus: focusRecord,
      inspect: (reference, element) => {
        onInspect(reference, element)
      },
      selectEvidence: (id, selected) =>
        onEvidenceChange(
          selected
            ? [...evidenceIds, id]
            : evidenceIds.filter((value) => value !== id)
        ),
    },
  }))
  useEffect(() => {
    updateNodeInternals(graph.nodes.map((node) => node.id))
  }, [graph.nodes, updateNodeInternals])
  useEffect(() => {
    if (!flow.viewportInitialized || !width || !height || !nodes.length) return
    const previous = viewportSize.current
    viewportSize.current = { width, height }
    if (readingScale.current === null) {
      readingScale.current = layout.fontScale
      void fitGraph()
      return
    }
    if (readingScale.current !== layout.fontScale) {
      if (focusRecord(inspected ?? { type: "fact", id: "origin" })) {
        readingScale.current = layout.fontScale
      }
      return
    }
    if (
      previous.width &&
      previous.height &&
      (previous.width !== width || previous.height !== height)
    ) {
      const viewport = flow.getViewport()
      void flow.setViewport({
        ...viewport,
        x: viewport.x + (width - previous.width) / 2,
        y: viewport.y + (height - previous.height) / 2,
      })
    }
  }, [
    flow,
    width,
    height,
    nodes,
    layout.fontScale,
    focusRecord,
    inspected,
    fitGraph,
  ])
  useEffect(() => {
    if (
      !flow.viewportInitialized ||
      !target ||
      target.reference.type === "hint"
    )
      return
    focusRecord(target.reference)
  }, [target, flow.viewportInitialized, focusRecord])
  return (
    <ReactFlow<GraphNode>
      nodes={nodes}
      onNodesChange={onNodesChange}
      edges={graph.edges}
      nodeTypes={nodeTypes}
      nodesDraggable
      nodesConnectable={false}
      nodesFocusable={false}
      edgesFocusable={false}
      elementsSelectable={false}
      minZoom={0.15}
      maxZoom={1.5}
      deleteKeyCode={null}
      proOptions={{ hideAttribution: true }}
      aria-label="Blackboard graph"
      className="@container/graph bg-muted/30"
      onNodeClick={(event, node) => {
        onInspect(
          node.data.reference,
          event.currentTarget instanceof HTMLElement
            ? (event.currentTarget.querySelector<HTMLButtonElement>(
                '[data-slot="button"]'
              ) ?? undefined)
            : undefined
        )
      }}
    >
      <Background color="var(--border)" gap={24} />
      <Panel position="top-left" className="flex flex-wrap items-center gap-2">
        <ButtonGroup aria-label="Graph controls">
          <Button
            variant="outline"
            size="icon"
            aria-label="Zoom in"
            onClick={() => void flow.zoomIn()}
          >
            <Plus />
          </Button>
          <Button
            variant="outline"
            size="icon"
            aria-label="Zoom out"
            onClick={() => void flow.zoomOut()}
          >
            <Minus />
          </Button>
          <Button
            variant="outline"
            aria-label="Actual size"
            onClick={() =>
              focusRecord(inspected ?? { type: "fact", id: "origin" })
            }
          >
            {Math.round(zoom * 100)}%
          </Button>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="icon"
                aria-label="Fit all nodes"
                onClick={() => void fitGraph()}
              >
                <Maximize />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Fit all nodes</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="icon"
                aria-label="Re-layout graph"
                onClick={() => {
                  setLayout((current) => ({ ...current }))
                  setRelayoutSignal((current) => current + 1)
                }}
              >
                <RotateCcw />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Re-layout graph</TooltipContent>
          </Tooltip>
        </ButtonGroup>
        <ToggleGroup
          type="single"
          variant="outline"
          spacing={0}
          value={layout.direction}
          onValueChange={(direction) => {
            if (direction !== "TB" && direction !== "LR") return
            setLayout((current) => ({ ...current, direction }))
            setRelayoutSignal((current) => current + 1)
          }}
          aria-label="Graph layout direction"
        >
          <ToggleGroupItem value="TB">Top down</ToggleGroupItem>
          <ToggleGroupItem value="LR">Left right</ToggleGroupItem>
        </ToggleGroup>
      </Panel>
      <MiniMap
        position="bottom-left"
        style={{ width: 140, height: 88 }}
        nodeColor="var(--muted-foreground)"
        maskColor="var(--muted)"
        pannable
        zoomable
        ariaLabel="Blackboard overview"
      />
      <Panel position="bottom-right" className="hidden sm:block">
        <Badge variant="secondary">
          <IconGitBranch aria-hidden="true" />
          {graph.nodes.length} nodes · {graph.edges.length} connections
        </Badge>
      </Panel>
    </ReactFlow>
  )
}

export function WorkspaceGraph(props: GraphProps) {
  return (
    <ReactFlowProvider>
      <GraphCanvas {...props} />
    </ReactFlowProvider>
  )
}
