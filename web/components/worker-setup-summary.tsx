import Link from "next/link"
import {
  IconCheck as Check,
  IconCircle as Circle,
  IconAlertCircle as CircleAlert,
} from "@tabler/icons-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import type {
  AgentDefinitionMetadata,
  DynamicSecretState,
  ValidationProblem,
} from "@/lib/configuration"
import type { WorkerForm } from "@/lib/worker-form"

export function workerSetupItems(
  form: WorkerForm,
  definition: AgentDefinitionMetadata,
  configuredSecrets: Record<string, DynamicSecretState>,
  problems: ValidationProblem[]
) {
  const valid = (path: string) =>
    !problems.some((problem) => problem.path === path)
  return [
    {
      label: "Worker name",
      path: "name",
      complete: Boolean(form.name.trim()) && valid("name"),
      step: 0,
    },
    {
      label: "At least one task",
      path: "tasks",
      complete: form.tasks.length > 0 && valid("tasks"),
      step: 0,
    },
    {
      label: "Capacity of at least 1",
      path: "capacity",
      complete: Number(form.capacity) > 0 && valid("capacity"),
      step: 0,
    },
    ...definition.required_fields.map((field) => ({
      label: field.display_name,
      path: field.secret
        ? `secrets.${field.name}`
        : `environment.${form.environment.findIndex((entry) => entry.name === field.name)}`,
      complete: field.secret
        ? valid(`secrets.${field.name}`) &&
          ((form.secrets[field.name]?.mode === "replace" &&
            Boolean(form.secrets[field.name].value)) ||
            (form.secrets[field.name]?.mode === "retain" &&
              configuredSecrets[field.name]?.configured === true))
        : form.environment.some(
            (entry, index) =>
              entry.name === field.name &&
              Boolean(entry.value.trim()) &&
              valid(`environment.${index}`)
          ),
      step: 1,
    })),
  ]
}

export function WorkerSetupSummary({
  form,
  definition,
  items,
}: {
  form: WorkerForm
  definition: AgentDefinitionMetadata
  items: ReturnType<typeof workerSetupItems>
}) {
  const completed = items.filter((item) => item.complete).length
  return (
    <section
      aria-label="Worker summary"
      className="grid min-w-0 gap-4 self-start border-t pt-5 @min-[40rem]/page:grid-cols-2 @min-[56rem]/page:sticky @min-[56rem]/page:top-[var(--page-scroll-start,0px)] @min-[56rem]/page:grid-cols-1 @min-[56rem]/page:border-s @min-[56rem]/page:border-t-0 @min-[56rem]/page:ps-5 @min-[56rem]/page:pt-4"
    >
      <div className="grid content-start gap-3">
        <h2 className="text-base font-semibold">Worker summary</h2>
        <dl className="grid grid-cols-2 gap-3 text-sm @min-[56rem]/page:grid-cols-1 @min-[56rem]/page:gap-2">
          {(
            [
              ["Agent Type", definition.display_name, true],
              ["Name", form.name || "Not named yet", Boolean(form.name)],
              [
                "Provider",
                form.providerName || "Not set",
                Boolean(form.providerName),
              ],
            ] as const
          ).map(([label, value, configured]) => (
            <div
              key={label}
              className="grid min-w-0 gap-1 @min-[56rem]/page:grid-cols-[5rem_minmax(0,1fr)] @min-[56rem]/page:gap-3"
            >
              <dt className="text-muted-foreground">{label}</dt>
              <dd
                className={`wrap-anywhere ${configured ? "font-medium" : "text-muted-foreground"}`}
              >
                <bdi>{value}</bdi>
              </dd>
            </div>
          ))}
          <div className="grid gap-1">
            <dt className="text-muted-foreground">Tasks</dt>
            <dd className="flex flex-wrap gap-1.5">
              {form.tasks.length ? (
                form.tasks.map((task) => (
                  <Badge key={task} variant="secondary">
                    {task[0].toUpperCase() + task.slice(1)}
                  </Badge>
                ))
              ) : (
                <span className="text-muted-foreground">None selected</span>
              )}
            </dd>
          </div>
          <div className="grid gap-1">
            <dt className="text-muted-foreground">Capacity</dt>
            <dd className="font-medium tabular-nums">
              {items.find((item) => item.path === "capacity")?.complete
                ? `${form.capacity} concurrent Run${Number(form.capacity) === 1 ? "" : "s"}`
                : "Needs attention"}
            </dd>
          </div>
        </dl>
      </div>
      <div className="grid content-start gap-3 border-t pt-4 @min-[40rem]/page:border-t-0 @min-[40rem]/page:pt-0 @min-[56rem]/page:border-t @min-[56rem]/page:pt-4">
        <div className="flex min-h-6 items-center justify-between gap-2 text-sm">
          <h3 className="font-medium">Setup progress</h3>
          <span className="text-muted-foreground tabular-nums">
            {completed} / {items.length}
          </span>
        </div>
        <ul className="grid gap-1 text-sm" aria-label="Required setup">
          {items.map((item) => (
            <li key={item.path} className="flex items-start gap-2">
              {item.complete ? (
                <Check
                  aria-hidden="true"
                  className="mt-0.5 size-4 shrink-0 text-foreground"
                />
              ) : (
                <Circle
                  aria-hidden="true"
                  className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                />
              )}
              <span className={item.complete ? "" : "text-muted-foreground"}>
                <span className="sr-only">
                  {item.complete ? "Configured: " : "Needs attention: "}
                </span>
                {item.label}
              </span>
            </li>
          ))}
        </ul>
      </div>
      <p className="border-t pt-4 text-sm leading-relaxed text-muted-foreground @min-[40rem]/page:col-span-2 @min-[56rem]/page:col-span-1">
        Save makes this Worker effective for new Projects. Existing Projects keep their settings.
      </p>
    </section>
  )
}

export function WorkerReview({
  form,
  definition,
  items,
  onEdit,
  problems,
  onFix,
  disabled,
}: {
  form: WorkerForm
  definition: AgentDefinitionMetadata
  items: ReturnType<typeof workerSetupItems>
  onEdit: (step: number) => void
  problems: Array<ValidationProblem & { label: string }>
  onFix: (path: string) => void
  disabled: boolean
}) {
  const missing = items.filter((item) => !item.complete)
  return (
    <div className="grid gap-5">
      {problems.length ? (
        <Alert>
          <CircleAlert />
          <AlertTitle>Fix invalid settings before saving</AlertTitle>
          <AlertDescription className="max-w-[70ch] leading-relaxed">
            <ul className="grid gap-3">
              {problems.map((problem) => (
                <li
                  key={`${problem.path}:${problem.message}`}
                  className="grid gap-1"
                >
                  <Button
                    type="button"
                    variant="link"
                    size="default"
                    className="justify-self-start"
                    disabled={disabled}
                    onClick={() => onFix(problem.path)}
                  >
                    Fix {problem.label}
                  </Button>
                  <span>{problem.message}</span>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : missing.length ? (
        <Alert>
          <CircleAlert />
          <AlertTitle>Finish setup before Save</AlertTitle>
          <AlertDescription className="max-w-[70ch] leading-relaxed">
            <p>Complete these items before saving this Worker.</p>
            <ul className="mt-2 grid gap-1">
              {missing.map((item) => (
                <li key={item.path}>
                  <Button
                    type="button"
                    variant="link"
                    size="default"
                    className="justify-start"
                    disabled={disabled}
                    onClick={() => onEdit(item.step)}
                  >
                    Add {item.label.toLowerCase()}
                  </Button>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : (
        <Alert>
          <Check />
          <AlertTitle>Ready for the Provider check</AlertTitle>
          <AlertDescription className="max-w-[70ch] leading-relaxed">
            The Provider is checked when you save. If the check fails, your
            inputs stay here so you can correct them and retry.
          </AlertDescription>
        </Alert>
      )}
      <div className="grid gap-4 text-sm">
        {(
          [
            [
              "Basic information",
              `${form.name} · ${form.tasks.length ? form.tasks.map((task) => task[0].toUpperCase() + task.slice(1)).join(", ") : "No tasks selected"}`,
              0,
            ],
            [
              "Provider configuration",
              form.providerName || "Custom provider",
              1,
            ],
          ] as const
        ).map(([label, value, step]) => (
          <div
            key={label}
            className="flex min-w-0 items-start justify-between gap-4 border-b pb-4"
          >
            <dl className="grid min-w-0 gap-1">
              <dt className="font-medium">{label}</dt>
              <dd className="wrap-anywhere text-muted-foreground">
                <bdi>{value}</bdi>
              </dd>
              {step === 1 ? (
                <dd className="mt-2 grid gap-2">
                  <dl className="grid gap-2">
                    {definition.required_fields
                      .filter((field) => !field.secret)
                      .map((field) => (
                        <div key={field.name} className="grid min-w-0 gap-0.5">
                          <dt className="text-muted-foreground">
                            {field.display_name}
                          </dt>
                          <dd className="wrap-anywhere">
                            <bdi>
                              {form.environment.find(
                                (entry) => entry.name === field.name
                              )?.value || "Not set"}
                            </bdi>
                          </dd>
                        </div>
                      ))}
                  </dl>
                </dd>
              ) : null}
            </dl>
            <Button
              type="button"
              size="default"
              variant="outline"
              disabled={disabled}
              onClick={() => onEdit(step)}
              aria-label={`Edit ${label.toLowerCase()}`}
            >
              Edit
            </Button>
          </div>
        ))}
      </div>
      <div className="grid gap-2 text-sm">
        <h3 className="font-medium">After saving</h3>
        <p className="max-w-[70ch] leading-relaxed text-muted-foreground">
          Open{" "}
          <Link
            href="/settings/runtime-capacity"
            className="text-foreground underline underline-offset-4"
          >
            Runtime &amp; Capacity
          </Link>{" "}
          to review saved configuration. New Projects use the saved configuration;
          existing Projects keep their settings.
        </p>
      </div>
    </div>
  )
}
