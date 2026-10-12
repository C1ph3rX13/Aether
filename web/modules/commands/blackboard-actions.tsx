"use client"
import { IconBulb as Lightbulb, IconCheck as Check } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { HintDialog, CompleteDialog } from "./workspace-dialogs"
import {
  useWorkspaceAction,
  type WorkspaceActionProps,
} from "./use-workspace-action"
export function BlackboardActions(
  props: WorkspaceActionProps & {
    onAddHint?: () => void
  }
) {
  const { session } = props
  const { action, openAction, closeAction, finalFocus, recovery, finish } =
    useWorkspaceAction<"hint" | "complete">(props)
  if (!session.view) return null
  const { detail, completionFactIds } = session.view
  const project = detail.project
  const { commands, availability } = session
  return (
    <>
      <div
        role="group"
        aria-label="Blackboard actions"
        className="flex flex-wrap items-center gap-2"
      >
        {completionFactIds.length > 0 && (
          <Button
            variant="ghost"
            size="default"
            disabled={commands.pending}
            onClick={() => session.actions.selectCompletionEvidence([])}
          >
            Clear Evidence
          </Button>
        )}
        <Button
          variant="outline"
          disabled={!availability.addHint}
          onClick={(event) => {
            props.onAddHint?.()
            openAction("hint", event.currentTarget)
          }}
        >
          <Lightbulb data-icon="inline-start" aria-hidden="true" />
          Add Hint
        </Button>
        <Button
          disabled={
            !availability.completeProject || completionFactIds.length === 0
          }
          onClick={(event) => openAction("complete", event.currentTarget)}
        >
          <Check data-icon="inline-start" aria-hidden="true" />
          Complete Project
        </Button>
      </div>
      {action === "hint" && (
        <HintDialog
          open
          onOpenChange={closeAction}
          disabled={!availability.addHint}
          recovery={recovery}
          finalFocus={finalFocus}
          command={(content) =>
            finish(
              () => commands.createHint(content, project.revision),
              "Hint added"
            )
          }
        />
      )}
      {action === "complete" && (
        <CompleteDialog
          project={project}
          facts={detail.facts.filter((fact) =>
            completionFactIds.includes(fact.id)
          )}
          open
          onOpenChange={closeAction}
          disabled={!availability.completeProject}
          recovery={recovery}
          finalFocus={finalFocus}
          command={(description) =>
            finish(
              () =>
                commands.completeProject(
                  completionFactIds,
                  description,
                  project.revision
                ),
              "Project completed"
            )
          }
        />
      )}
    </>
  )
}
