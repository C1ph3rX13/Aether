import type { ComponentType, ReactNode } from "react"
import { relationshipGraph } from "@/modules/workspace/graph/model"
import type { GraphNodeData, GraphProps } from "@/modules/workspace/graph/view"

// The browser suite owns canvas geometry. Session tests keep the real record
// cards and domain graph while replacing only the layout engines.
export function graphRenderer(Card: ComponentType<{ data: GraphNodeData }>) {
  return function Graph(props: GraphProps) {
    return (
      <section role="application" aria-label="Blackboard graph">
        {relationshipGraph(props.detail).nodes.map((node) => (
          <Card
            key={node.id}
            data={{
              ...node.data,
              inspected:
                props.inspected?.type === node.data.reference.type &&
                props.inspected.id === node.data.reference.id,
              evidenceSelected: props.evidenceIds.includes(
                node.data.reference.id
              ),
              evidenceDisabled: props.evidenceDisabled,
              focus: () => undefined,
              inspect: props.onInspect,
              selectEvidence: (id, selected) =>
                props.onEvidenceChange(
                  selected
                    ? [...props.evidenceIds, id]
                    : props.evidenceIds.filter((value) => value !== id)
                ),
            }}
          />
        ))}
      </section>
    )
  }
}
export const panelRenderer = {
  ResizablePanelGroup: ({ children }: { children: ReactNode }) => (
    <div>{children}</div>
  ),
  ResizablePanel: ({ children }: { children: ReactNode }) => (
    <div>{children}</div>
  ),
  ResizableHandle: () => null,
}
