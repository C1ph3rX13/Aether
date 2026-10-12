import dagre from "@dagrejs/dagre"
import { type Edge, type Node } from "@xyflow/react"
import { getFactType, type ProjectDetail } from "@/modules/workspace/model"
import type { RecordReference } from "@/modules/workspace/contract"

export interface GraphRecord extends Record<string, unknown> {
  reference: RecordReference
  role: string
  description: string
  status: string
  completionEligible: boolean
}
export type RelationshipNode = Node<GraphRecord, "record">
export type GraphDirection = "TB" | "LR"

export function relationshipGraph(
  detail: ProjectDetail,
  scale = 1,
  direction: GraphDirection = "LR"
): {
  nodes: RelationshipNode[]
  edges: Edge[]
} {
  const nodes: RelationshipNode[] = [
    ...detail.facts.map((fact) => ({
      id: `fact:${fact.id}`,
      type: "record" as const,
      position: { x: 0, y: 0 },
      width: 240 * scale,
      height: 160 * scale,
      data: {
        reference: { type: "fact" as const, id: fact.id },
        role: getFactType(fact.id),
        description: fact.description,
        status: fact.status,
        completionEligible: fact.status === "active" && fact.id !== "goal",
      },
    })),
    ...detail.intents.map((intent) => ({
      id: `intent:${intent.id}`,
      type: "record" as const,
      position: { x: 0, y: 0 },
      width: 240 * scale,
      height: 160 * scale,
      data: {
        reference: { type: "intent" as const, id: intent.id },
        role: "intent",
        description: intent.description,
        status: intent.status,
        completionEligible: false,
      },
    })),
  ]
  const ids = new Set(nodes.map((node) => node.id))
  const edges: Edge[] = []
  const addEdge = (
    source: string,
    target: string,
    label: string,
    lineage = false,
    cancelled = false
  ) => {
    if (!ids.has(source) || !ids.has(target)) return
    edges.push({
      id: `${label}:${source}:${target}`,
      source,
      target,
      ariaLabel: lineage
        ? `${source} is replaced by ${target}`
        : `${source} ${label} ${target}`,
      data: { relationship: label },
      ...(lineage ? { label: "replaced by" } : {}),
      style: {
        stroke: "var(--muted-foreground)",
        ...(lineage
          ? { strokeDasharray: "6 4" }
          : cancelled
            ? { strokeDasharray: "5 4" }
            : {}),
      },
      labelStyle: { fill: "var(--muted-foreground)", fontSize: "0.6875rem" },
      labelBgStyle: { fill: "var(--background)" },
      animated: false,
    })
  }
  for (const intent of detail.intents) {
    for (const from of intent.from)
      addEdge(
        `fact:${from}`,
        `intent:${intent.id}`,
        "From",
        false,
        intent.status === "cancelled"
      )
    if (intent.to)
      addEdge(
        `intent:${intent.id}`,
        `fact:${intent.to}`,
        intent.kind === "completion" ? "Achieves" : "Produces"
      )
  }
  for (const fact of detail.facts)
    if (fact.supersedes)
      addEdge(`fact:${fact.supersedes}`, `fact:${fact.id}`, "ReplacedBy", true)
  const graph = new dagre.graphlib.Graph()
    .setGraph({
      rankdir: direction,
      nodesep: 44 * scale,
      ranksep: 72 * scale,
      ...(direction === "LR" ? { align: "DR" } : {}),
    })
    .setDefaultEdgeLabel(() => ({}))
  for (const node of nodes)
    graph.setNode(node.id, { width: node.width, height: node.height })
  for (const edge of edges) graph.setEdge(edge.source, edge.target)
  dagre.layout(graph)
  for (const node of nodes) {
    const position = graph.node(node.id)
    node.position = { x: position.x - 120 * scale, y: position.y - 80 * scale }
  }
  return { nodes, edges }
}
