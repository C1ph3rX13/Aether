"use client"

import { Button } from "@/components/ui/button"
import {
  Field,
  FieldError,
  FieldLabel,
  FieldSet,
  FieldGroup,
} from "@/components/ui/field"
import {
  WorkerSection,
  AdvancedWorkerSection,
  type WorkerFieldsProps,
} from "@/components/worker-editor-fields"
import { Checkbox } from "@/components/ui/checkbox"
import { timeoutPhases } from "@/lib/worker-form"

export function WorkerAdvancedFields({
  form,
  definition,
  read,
  fields,
  busy,
  creating,
  problems,
  change,
  input,
  errorFor,
}: WorkerFieldsProps) {
  const CapabilitiesSection = creating ? AdvancedWorkerSection : WorkerSection
  const capabilityErrors = errorFor("capabilities")
  const environmentErrors = errorFor("environment")
  const additionalEnvironment = form.environment.slice(
    fields.filter((field) => !field.secret).length
  ).length
  const timeoutOverrides = form.tasks
    .flatMap((task) => timeoutPhases(read, task))
    .filter(({ phase }) => form.timeouts[phase]?.trim()).length
  return (
    <>
      <CapabilitiesSection
        title="Capabilities"
        summary={
          form.capabilities.length
            ? `${form.capabilities.length} enabled`
            : "No optional capabilities enabled"
        }
        invalid={capabilityErrors.length > 0}
      >
        {definition.capabilities.map((capability) => (
          <Field key={capability} orientation="horizontal">
            <Checkbox
              id={`capability-${capability}`}
              disabled={busy}
              checked={form.capabilities.includes(capability)}
              aria-invalid={capabilityErrors.length > 0 || undefined}
              aria-describedby={
                capabilityErrors.length
                  ? "worker-capabilities-error"
                  : undefined
              }
              onCheckedChange={(checked) =>
                change(
                  "capabilities",
                  checked
                    ? [...form.capabilities, capability]
                    : form.capabilities.filter((value) => value !== capability)
                )
              }
            />
            <FieldLabel htmlFor={`capability-${capability}`}>
              {capability
                .replaceAll("_", " ")
                .replace(/^./, (value) => value.toUpperCase())}
            </FieldLabel>
          </Field>
        ))}
        <FieldError id="worker-capabilities-error" errors={capabilityErrors} />
      </CapabilitiesSection>
      <AdvancedWorkerSection
        title="Environment"
        summary={
          additionalEnvironment
            ? `${additionalEnvironment} additional variable${additionalEnvironment === 1 ? "" : "s"}`
            : "No additional variables"
        }
        invalid={problems.some((problem) =>
          problem.path.startsWith("environment")
        )}
      >
        <p className="max-w-[70ch] text-sm leading-relaxed text-muted-foreground">
          Variables apply to this Worker only. Put credentials in Secrets.
        </p>
        {form.environment.map((entry, index) =>
          fields.some((field) => field.name === entry.name) &&
          index < fields.filter((field) => !field.secret).length ? null : (
            <FieldSet
              key={index}
              aria-label={`Environment variable ${index + 1}`}
            >
              <FieldGroup>
                {input(
                  `environment-name.${index}`,
                  `Environment name ${index + 1}`,
                  entry.name,
                  (value) =>
                    change(
                      "environment",
                      form.environment.map((entry, position) =>
                        position === index ? { ...entry, name: value } : entry
                      )
                    )
                )}
                {input(
                  `environment.${index}`,
                  `Environment value ${index + 1}`,
                  entry.value,
                  (value) =>
                    change(
                      "environment",
                      form.environment.map((entry, position) =>
                        position === index ? { ...entry, value } : entry
                      )
                    )
                )}
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  className="justify-self-start"
                  aria-label={`Remove environment ${index + 1}`}
                  onClick={() =>
                    change(
                      "environment",
                      form.environment.filter(
                        (_, position) => position !== index
                      )
                    )
                  }
                >
                  Remove
                </Button>
              </FieldGroup>
            </FieldSet>
          )
        )}
        <FieldError id="worker-environment-error" errors={environmentErrors} />
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          className="justify-self-start"
          onClick={() =>
            change("environment", [
              ...form.environment,
              { name: "", value: "" },
            ])
          }
        >
          Add environment variable
        </Button>
      </AdvancedWorkerSection>
      <AdvancedWorkerSection
        title="Timeout overrides"
        summary={
          timeoutOverrides
            ? `${timeoutOverrides} phase${timeoutOverrides === 1 ? "" : "s"} overridden`
            : "Using Project settings"
        }
        invalid={problems.some((problem) =>
          problem.path.startsWith("timeouts")
        )}
      >
        <p className="max-w-[70ch] text-sm leading-relaxed text-muted-foreground">
          Leave a phase blank to use its Project setting.
        </p>
        {form.tasks.length ? (
          form.tasks.flatMap((task) =>
            timeoutPhases(read, task).map(({ phase, seconds }) =>
              input(
                `timeouts.${phase}`,
                `${phase.replace(".", " ").replace(/^./, (value) => value.toUpperCase())} (seconds)`,
                form.timeouts[phase] ?? "",
                (value) =>
                  change("timeouts", {
                    ...form.timeouts,
                    [phase]: value,
                  }),
                `Project: ${seconds} seconds. Range: 1–86400.`
              )
            )
          )
        ) : (
          <p className="text-sm text-muted-foreground">
            Select tasks to set overrides.
          </p>
        )}
      </AdvancedWorkerSection>
    </>
  )
}
