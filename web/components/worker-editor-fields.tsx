"use client"
import { startTransition, useEffect, useState, type ReactNode } from "react"
import { IconChevronRight as ChevronRight } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { FieldGroup, FieldSet, FieldLegend } from "@/components/ui/field"
import { Separator } from "@/components/ui/separator"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import type {
  AgentDefinitionMetadata,
  AgentFieldMetadata,
  ConfigurationRead,
  ValidationProblem,
} from "@/lib/configuration"
import type { WorkerForm } from "@/lib/worker-form"
export interface WorkerFieldsProps {
  form: WorkerForm
  definition: AgentDefinitionMetadata
  read: ConfigurationRead
  fields: AgentFieldMetadata[]
  busy: boolean
  creating: boolean
  problems: ValidationProblem[]
  change: <K extends keyof WorkerForm>(field: K, value: WorkerForm[K]) => void
  update: (update: (current: WorkerForm) => WorkerForm) => void
  errorFor: (path: string) => Array<{
    message: string
  }>
  input: (
    path: string,
    label: string,
    value: string,
    onChange: (value: string) => void,
    help?: string,
    options?: {
      placeholder?: string
      inputMode?: "numeric"
      required?: boolean
    }
  ) => ReactNode
}
export function WorkerSection({
  title,
  children,
  hideHeading = false,
}: {
  title: string
  children: ReactNode
  hideHeading?: boolean
}) {
  return (
    <FieldSet>
      {!hideHeading ? <FieldLegend>{title}</FieldLegend> : null}
      <FieldGroup>{children}</FieldGroup>
    </FieldSet>
  )
}
export function AdvancedWorkerSection({
  title,
  summary,
  invalid,
  children,
}: {
  title: string
  summary: string
  invalid: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (invalid) startTransition(() => setOpen(true))
  }, [invalid])
  const visible = invalid || open
  return (
    <Collapsible open={visible} onOpenChange={setOpen} asChild>
      <section className="group/advanced flex min-w-0 flex-col gap-4">
        <Separator />
        <div className="grid gap-1">
          <h3>
            <CollapsibleTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                className="w-full justify-between"
                disabled={invalid}
              >
                {title}
                <ChevronRight
                  data-icon="inline-end"
                  aria-hidden="true"
                  className="group-data-[state=open]/advanced:rotate-90 motion-safe:transition-transform motion-safe:duration-200"
                />
              </Button>
            </CollapsibleTrigger>
          </h3>
          <p className="max-w-[70ch] text-sm leading-relaxed text-muted-foreground">
            {summary}
          </p>
          {invalid ? (
            <p className="text-xs text-destructive">
              Fix errors to collapse this section.
            </p>
          ) : null}
        </div>
        <CollapsibleContent>
          <FieldGroup>{children}</FieldGroup>
        </CollapsibleContent>
      </section>
    </Collapsible>
  )
}
export function workerProblemTarget(path: string) {
  return document.getElementById(
    path.startsWith("secrets.")
      ? `secret-${path.slice("secrets.".length)}`
      : `worker-${path}`
  )
}
