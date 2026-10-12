import { describe, expect, it } from "vitest"
import { relationshipGraph } from "@/modules/workspace/graph/model"
import type { ProjectDetail } from "@/modules/workspace/model"

// The persisted six-record topology used to approve the graph prototype.
const detail: ProjectDetail = {
  project: {
    id: "proj_graph",
    title: "Graph verification",
    status: "active",
    bootstrapEnabled: true,
    createdAt: "2026-10-11T15:00:45Z",
    generation: 1,
    revision: 3,
  },
  facts: ["f001", "goal", "origin"].map((id) => ({
    id,
    description: id,
    status: "active",
  })),
  intents: [
    {
      id: "i001",
      kind: "bootstrap",
      status: "concluded",
      from: ["origin"],
      to: "f001",
      description: "bootstrap",
      creator: "dispatcher.bootstrap",
      createdAt: "2026-10-11T15:00:45Z",
    },
    ...["i002", "i003"].map((id) => ({
      id,
      kind: "explore" as const,
      status: "open" as const,
      from: ["f001", "origin"],
      description: id,
      creator: "worker",
      createdAt: "2026-10-11T15:01:00Z",
    })),
  ],
  hints: [],
  completionRecords: [],
}

describe("Blackboard graph arrangement", () => {
  it("aligns the horizontal main row and spaces the branch without resizing cards", () => {
    const graph = relationshipGraph(detail, 1, "LR")
    const nodes = new Map(graph.nodes.map((node) => [node.id, node]))
    const row = ["fact:origin", "intent:i001", "fact:f001", "intent:i003"].map(
      (id) => nodes.get(id)!
    )
    expect(new Set(row.map((node) => node.position.y)).size).toBe(1)
    expect(
      row.slice(1).map((node, index) => node.position.x - row[index].position.x)
    ).toEqual([312, 312, 312])
    const branch = nodes.get("intent:i002")!
    expect(branch.position.x).toBe(row[3].position.x)
    expect(branch.position.y - row[3].position.y - 160).toBe(44)
    expect(
      graph.nodes.every((node) => node.width === 240 && node.height === 160)
    ).toBe(true)
    expect(
      graph.edges.some(
        (edge) => edge.source === "fact:goal" || edge.target === "fact:goal"
      )
    ).toBe(false)
  })

  it("changes direction while preserving every authoritative relationship across Reopen", () => {
    const reopened = {
      ...detail,
      project: { ...detail.project, generation: 2, revision: 5 },
    }
    const horizontal = relationshipGraph(detail, 1, "LR")
    const vertical = relationshipGraph(reopened, 1, "TB")
    expect(horizontal.edges).toHaveLength(6)
    expect(vertical.edges).toEqual(horizontal.edges)
    expect(
      vertical.nodes.find((node) => node.id === "intent:i001")!.position
    ).not.toEqual(
      horizontal.nodes.find((node) => node.id === "intent:i001")!.position
    )
    expect(
      vertical.edges.every(
        (edge) =>
          edge.type === undefined &&
          edge.markerEnd === undefined &&
          edge.label === undefined
      )
    ).toBe(true)
  })

  it("keeps cancelled sources and supersession visible with their distinct dashed relations", () => {
    const changed: ProjectDetail = {
      ...detail,
      facts: [
        ...detail.facts,
        {
          id: "f002",
          description: "Replacement",
          status: "active",
          supersedes: "f001",
        },
      ],
      intents: detail.intents.map((intent) =>
        intent.id === "i002" ? { ...intent, status: "cancelled" } : intent
      ),
    }
    const graph = relationshipGraph(changed)
    expect(graph.edges).toHaveLength(7)
    expect(
      graph.edges
        .filter((edge) => edge.target === "intent:i002")
        .every((edge) => edge.style?.strokeDasharray === "5 4")
    ).toBe(true)
    expect(
      graph.edges.find((edge) => edge.id === "ReplacedBy:fact:f001:fact:f002")
    ).toMatchObject({
      source: "fact:f001",
      target: "fact:f002",
      ariaLabel: "fact:f001 is replaced by fact:f002",
      label: "replaced by",
      data: { relationship: "ReplacedBy" },
      style: { strokeDasharray: "6 4", stroke: "var(--muted-foreground)" },
    })
  })

  it("distinguishes achieving the existing Goal from producing new evidence across Generations", () => {
    const completed: ProjectDetail = {
      ...detail,
      project: { ...detail.project, status: "completed", generation: 2 },
      intents: [
        ...detail.intents,
        ...["i004", "i005"].map((id) => ({
          id,
          kind: "completion" as const,
          status: "concluded" as const,
          from: ["f001"],
          to: "goal",
          description: "Goal achieved",
          creator: "operator",
          createdAt: "2026-10-12T00:00:00Z",
        })),
      ],
    }
    for (const direction of ["LR", "TB"] as const) {
      const graph = relationshipGraph(completed, 1, direction)
      expect(
        graph.nodes.filter((node) => node.id === "fact:goal")
      ).toHaveLength(1)
      expect(graph.edges).toHaveLength(10)
      expect(
        graph.edges.find((edge) => edge.source === "intent:i001")
      ).toMatchObject({
        target: "fact:f001",
        ariaLabel: "intent:i001 Produces fact:f001",
        data: { relationship: "Produces" },
      })
      for (const id of ["i004", "i005"]) {
        expect(
          graph.edges.find(
            (edge) =>
              edge.source === `fact:f001` && edge.target === `intent:${id}`
          )
        ).toMatchObject({ data: { relationship: "From" } })
        expect(
          graph.edges.find((edge) => edge.source === `intent:${id}`)
        ).toMatchObject({
          target: "fact:goal",
          ariaLabel: `intent:${id} Achieves fact:goal`,
          data: { relationship: "Achieves" },
          animated: false,
        })
      }
    }
  })
})
