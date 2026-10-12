"use client"

import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldLabel,
  FieldSet,
  FieldLegend,
  FieldGroup,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  WorkerSection,
  type WorkerFieldsProps,
} from "@/components/worker-editor-fields"
import { Checkbox } from "@/components/ui/checkbox"

const taskDescriptions: Record<string, string> = {
  bootstrap: "Start a Project from its Origin and Goal.",
  reason: "Evaluate Facts and decide what to explore next.",
  explore: "Investigate Intents and produce new Facts.",
}

export function WorkerBasicFields({
  form,
  definition,
  read,
  busy,
  creating,
  change,
  update,
  input,
  errorFor,
}: WorkerFieldsProps) {
  const agentType = definition.type
  const taskErrors = errorFor("tasks")
  return (
    <>
      <WorkerSection title="Identity" hideHeading={creating}>
        <FieldGroup className="grid @min-[30rem]/worker-fields:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
          {input(
            "name",
            "Worker name",
            form.name,
            (name) =>
              update((current) => ({
                ...current,
                name,
                secrets: Object.fromEntries(
                  Object.keys(current.secrets).map((slot) => [
                    slot,
                    { mode: "retain", value: "" },
                  ])
                ),
              })),
            "Unique across all Workers.",
            {
              placeholder: `${agentType === "claude_code" ? "claude" : agentType}-primary`,
              required: true,
            }
          )}
          <Field>
            <FieldLabel htmlFor="worker-agent-type">Agent Type</FieldLabel>
            <Input
              id="worker-agent-type"
              value={definition.display_name}
              readOnly
            />
          </Field>
        </FieldGroup>
      </WorkerSection>
      <WorkerSection title="Tasks and capacity">
        <FieldSet
          aria-label="Supported tasks"
          aria-describedby={
            taskErrors.length ? "worker-tasks-error" : undefined
          }
        >
          <FieldLegend variant="label">Supported tasks</FieldLegend>
          <FieldGroup className="grid @min-[38rem]/worker-fields:grid-cols-3">
            {Object.keys(read.draft.tasks).map((task) => (
              <Field
                key={task}
                orientation="horizontal"
                data-invalid={taskErrors.length > 0 || undefined}
              >
                <Checkbox
                  id={`task-${task}`}
                  aria-labelledby={`task-${task}-label`}
                  disabled={busy}
                  checked={form.tasks.includes(task)}
                  aria-invalid={taskErrors.length > 0 || undefined}
                  aria-describedby={`task-${task}-help${taskErrors.length ? " worker-tasks-error" : ""}`}
                  onCheckedChange={(checked) =>
                    update((current) => ({
                      ...current,
                      tasks: checked
                        ? [...current.tasks, task]
                        : current.tasks.filter((value) => value !== task),
                      timeouts: checked
                        ? current.timeouts
                        : Object.fromEntries(
                            Object.entries(current.timeouts).filter(
                              ([phase]) => !phase.startsWith(`${task}.`)
                            )
                          ),
                    }))
                  }
                />
                <FieldContent>
                  <FieldLabel
                    htmlFor={`task-${task}`}
                    id={`task-${task}-label`}
                  >
                    {task[0].toUpperCase() + task.slice(1)}
                  </FieldLabel>
                  <FieldDescription id={`task-${task}-help`}>
                    {taskDescriptions[task]}
                  </FieldDescription>
                </FieldContent>
              </Field>
            ))}
          </FieldGroup>
          <FieldDescription className="max-w-[70ch] leading-relaxed">
            Choose at least one task before Save. All Workers together must
            cover Bootstrap, Reason and Explore.
          </FieldDescription>
          <FieldError id="worker-tasks-error" errors={taskErrors} />
        </FieldSet>
        <FieldGroup className="grid @min-[24rem]/worker-fields:grid-cols-2">
          {input(
            "priority",
            "Priority",
            form.priority,
            (value) => change("priority", value),
            "0–1024. Lower values are selected first.",
            { inputMode: "numeric" }
          )}
          {input(
            "capacity",
            "Capacity",
            form.capacity,
            (value) => change("capacity", value),
            "1–1024 concurrent Runs. Blank or zero requires setup.",
            { inputMode: "numeric" }
          )}
        </FieldGroup>
      </WorkerSection>
    </>
  )
}
