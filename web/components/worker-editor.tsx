"use client"
import { useEffect, useRef, useState } from "react"
import Link from "next/link"
import {
  IconArrowLeft as ArrowLeft,
  IconArrowRight as ArrowRight,
  IconCheck as Check,
  IconAlertCircle as CircleAlert,
} from "@tabler/icons-react"
import { WorkerEditorDialogs } from "@/components/worker-editor-dialogs"
import { workerProblemTarget } from "@/components/worker-editor-fields"
import { WorkerBasicFields } from "@/components/worker-basic-fields"
import { WorkerProviderFields } from "@/components/worker-provider-fields"
import { WorkerAdvancedFields } from "@/components/worker-advanced-fields"
import { WorkerSaveStatus } from "@/components/worker-save-status"
import { SettingsPage } from "@/components/settings-page"
import { PageReadError } from "@/components/page-read-error"
import {
  WorkerReview,
  WorkerSetupSummary,
  workerSetupItems,
} from "@/components/worker-setup-summary"
import { Badge } from "@/components/ui/badge"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { useWorkerEditor } from "@/hooks/use-worker-editor"
import { useScrollPadding } from "@/hooks/use-scroll-padding"
import type { ValidationProblem } from "@/lib/configuration"
import { workerBranches, type AgentType } from "@/lib/worker-catalog"
import { validateWorkerForm, type WorkerForm } from "@/lib/worker-form"
const creationSteps = [
  { title: "Basic information" },
  {
    title: "Provider configuration",
  },
  { title: "Review & save" },
]
function workerProblemStep(path: string, declaredFieldCount: number) {
  if (["name", "tasks", "priority", "capacity"].includes(path)) return 0
  if (
    path === "providerName" ||
    path.startsWith("secrets.") ||
    (path.startsWith("environment.") &&
      Number(path.split(".")[1]) < declaredFieldCount)
  )
    return 1
  return 2
}
export function WorkerEditor({
  agentType,
  workerName,
  copy = false,
  saved = false,
  onSaved,
}: {
  agentType: AgentType
  workerName?: string
  copy?: boolean
  saved?: boolean
  onSaved?: (name: string) => void
}) {
  const editor = useWorkerEditor(agentType, workerName, copy)
  const [savedOnEntry, setSavedOnEntry] = useState(saved)
  if (savedOnEntry && editor.dirty) setSavedOnEntry(false)
  const [preset, setPreset] = useState("custom")
  const [conflictDismissed, setConflictDismissed] = useState(false)
  const [step, setStep] = useState(0)
  const [furthestStep, setFurthestStep] = useState(0)
  const [stepProblems, setStepProblems] = useState<ValidationProblem[]>([])
  const [routedSaveProblems, setRoutedSaveProblems] = useState(
    editor.saveProblems
  )
  const stepHeading = useRef<HTMLHeadingElement>(null)
  const saveButton = useRef<HTMLButtonElement>(null)
  const reloadButton = useRef<HTMLButtonElement>(null)
  const footerRef = useScrollPadding()
  const branch = workerBranches[agentType]
  const busy = editor.saving || editor.loading
  const form = editor.form
  const creating = !editor.originalName
  const goToStep = (next: number, path?: string) => {
    setStep(next)
    setFurthestStep((current) => Math.max(current, next))
    requestAnimationFrame(() =>
      (path
        ? (workerProblemTarget(path) ?? stepHeading.current)
        : stepHeading.current
      )?.focus()
    )
  }
  const saveWorker = async () => {
    setStepProblems([])
    if (await editor.save()) {
      if (form) onSaved?.(form.name)
    }
  }
  const definition = editor.definition
  const fields = definition
    ? [...definition.required_fields, ...(definition.optional_fields ?? [])]
    : []
  const declaredFieldCount = fields.filter((field) => !field.secret).length
  const validationProblems =
    form && definition && editor.baseline
      ? validateWorkerForm(
          form,
          definition,
          editor.baseline,
          editor.originalName
        )
      : []
  const readinessProblems = Array.from(
    new Map(
      [...validationProblems, ...editor.problems].map((problem) => [
        `${problem.path}:${problem.message}`,
        problem,
      ])
    ).values()
  )
  const setupItems =
    form && definition
      ? workerSetupItems(
          form,
          definition,
          editor.configuredSecrets,
          readinessProblems
        )
      : []
  const complete =
    setupItems.every((item) => item.complete) && !readinessProblems.length
  const advance = () => {
    if (!form || !definition || !editor.baseline || busy) return
    const problems = validationProblems.filter(
      (problem) => workerProblemStep(problem.path, declaredFieldCount) === step
    )
    setStepProblems(problems)
    if (problems.length) {
      ;(workerProblemTarget(problems[0].path) ?? stepHeading.current)?.focus()
      return
    }
    goToStep(step + 1)
  }
  // Mount the affected step before the dialog can restore focus to its fields.
  if (creating && routedSaveProblems !== editor.saveProblems) {
    setRoutedSaveProblems(editor.saveProblems)
    if (editor.saveProblems.length) {
      const errorStep = workerProblemStep(
        editor.saveProblems[0].path,
        declaredFieldCount
      )
      setStep(errorStep)
      setFurthestStep((current) => Math.max(current, errorStep))
    }
  }
  useEffect(() => {
    if (!creating || !editor.saveProblems.length) return
    requestAnimationFrame(() => stepHeading.current?.focus())
  }, [creating, editor.saveProblems])
  const problems = [...editor.problems, ...stepProblems]
  const errorFor = (path: string) =>
    problems
      .filter(
        (problem) =>
          problem.path === path.replace("environment-name.", "environment.")
      )
      .map((problem) => ({ message: problem.message }))
  const environmentErrors = errorFor("environment")
  const problemLabel = (path: string) => {
    const labels: Record<string, string> = {
      name: "Worker name",
      tasks: "tasks",
      priority: "Priority",
      capacity: "Capacity",
      providerName: "Provider name",
      capabilities: "capabilities",
      environment: "Environment",
    }
    if (labels[path]) return labels[path]
    if (path.startsWith("environment.")) {
      const entry = form?.environment[Number(path.split(".")[1])]
      return (
        fields.find((field) => field.name === entry?.name)?.display_name ??
        (entry?.name
          ? `Environment variable ${entry.name}`
          : "Environment variable")
      )
    }
    if (path.startsWith("secrets."))
      return (
        fields.find((field) => field.name === path.slice("secrets.".length))
          ?.display_name ?? "credentials"
      )
    return "timeout override"
  }
  const change = <K extends keyof WorkerForm>(
    field: K,
    value: WorkerForm[K]
  ) => {
    setStepProblems([])
    editor.change((form) => ({ ...form, [field]: value }))
  }
  const title = editor.originalName
    ? `Edit ${branch.label} Worker`
    : `New ${branch.label} Worker`
  const setupRequired = editor.problems.length > 0 || !complete
  const input = (
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
  ) => {
    const id = `worker-${path}`
    const errors = errorFor(path)
    const environmentProblem = path.startsWith("environment")
    const invalid =
      errors.length > 0 || (environmentProblem && environmentErrors.length > 0)
    const describedBy = [
      `${id}-help`,
      environmentProblem && environmentErrors.length
        ? "worker-environment-error"
        : null,
    ]
      .filter((value): value is string => Boolean(value))
      .join(" ")
    return (
      <Field key={path} data-invalid={invalid}>
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
        <Input
          id={id}
          value={value}
          disabled={busy}
          autoComplete="off"
          placeholder={options?.placeholder}
          inputMode={options?.inputMode}
          aria-required={options?.required || undefined}
          aria-invalid={invalid}
          aria-describedby={describedBy}
          onChange={(event) => {
            setStepProblems([])
            onChange(event.target.value)
          }}
        />
        <div id={`${id}-help`}>
          {help ? (
            <FieldDescription className="max-w-[70ch] leading-relaxed">
              {help}
            </FieldDescription>
          ) : null}
          <FieldError errors={errors} />
        </div>
      </Field>
    )
  }
  const fieldProps =
    form && definition && editor.baseline
      ? {
          form,
          definition,
          read: editor.baseline,
          fields,
          busy,
          creating,
          problems,
          change,
          update: editor.change,
          input,
          errorFor,
        }
      : null
  return (
    <SettingsPage
      title={title}
      description={
        creating
          ? "Set up the Worker, connect a Provider, then review and save."
          : "Update this Worker's configuration. Complete settings pass a Provider check on Save."
      }
      actions={
        <>
          <Button variant="outline" role="link" asChild>
            <Link href={branch.href}>
              <ArrowLeft data-icon="inline-start" />
              Back to Workers
            </Link>
          </Button>
        </>
      }
    >
      {editor.loading ? (
        <div
          role="status"
          aria-label="Loading Worker"
          className="grid max-w-6xl gap-4"
        >
          <FieldDescription className="sr-only motion-reduce:not-sr-only">
            Loading Worker...
          </FieldDescription>
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-96 w-full" />
        </div>
      ) : null}
      {editor.loadFailed ? (
        <PageReadError
          title="Unable to load Worker settings"
          description="Edits kept. Retry to load Worker settings."
          disabled={busy}
          onRetry={() => void editor.load()}
        />
      ) : null}
      {editor.notFound ? (
        <Empty role="alert">
          <EmptyHeader>
            <EmptyTitle>
              <h2>Worker not found</h2>
            </EmptyTitle>
            <EmptyDescription>
              Return to Workers to choose a saved Worker.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : null}
      {(editor.saved || savedOnEntry) &&
      !editor.dirty &&
      !busy &&
      !editor.conflict &&
      !editor.saveFailure &&
      !editor.loadFailed ? (
        <WorkerSaveStatus />
      ) : null}
      {editor.saving ? (
        <p role="status">
          Keep this page open until Save finishes
          {complete ? " and the Provider check completes" : ""}.
        </p>
      ) : null}
      {editor.conflict ? (
        <Alert>
          <CircleAlert />
          <AlertTitle role="heading" aria-level={2}>
            Settings changed elsewhere
          </AlertTitle>
          <AlertDescription>
            Edits kept. Reload before saving again.
          </AlertDescription>
          <AlertDescription className="mt-3">
            <Button
              ref={reloadButton}
              variant="outline"
              size="default"
              disabled={busy}
              onClick={() => setConflictDismissed(false)}
            >
              Reload
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}
      {fieldProps && form && definition ? (
        <div className="@container/page flex w-full max-w-6xl min-w-0 flex-col gap-4">
          {editor.originalName ? (
            <p className="text-sm text-muted-foreground">
              After Save, changes affect new Projects. Existing Projects keep
              their settings.
            </p>
          ) : copy ? (
            <p className="text-sm text-muted-foreground">
              Settings copied; credentials excluded. Use a unique name.
            </p>
          ) : null}
          {editor.originalName && form.name !== editor.originalName ? (
            <Alert role="status">
              <CircleAlert />
              <AlertDescription>
                Name changed; credentials reset. Configure Secrets for the new
                name.
              </AlertDescription>
            </Alert>
          ) : null}
          {!creating ? (
            <Badge variant="outline">
              {setupRequired
                ? "Worker setup required"
                : editor.dirty
                  ? "Unsaved changes"
                  : "Worker Ready"}
            </Badge>
          ) : null}
          {editor.problems.length ? (
            <Alert aria-label="Worker fields need attention">
              <CircleAlert />
              <AlertTitle>Worker fields need attention</AlertTitle>
              <AlertDescription>
                <ul className="grid gap-2">
                  {Array.from(
                    new Set(editor.problems.map((problem) => problem.message))
                  ).map((message) => (
                    <li key={message}>{message}</li>
                  ))}
                </ul>
                <p>
                  Complete the required fields before Save.
                </p>
              </AlertDescription>
            </Alert>
          ) : null}
          {creating ? (
            <nav aria-label="Worker creation steps" className="border-b pb-4">
              <ToggleGroup
                type="single"
                variant="outline"
                spacing={2}
                value={String(step)}
                onValueChange={(value) => {
                  if (value) goToStep(Number(value))
                }}
                className="flex-wrap justify-start"
              >
                {creationSteps.map((item, index) => (
                  <ToggleGroupItem
                    key={item.title}
                    value={String(index)}
                    type="button"
                    disabled={busy || index > furthestStep}
                    aria-current={step === index ? "step" : undefined}
                    aria-label={`Step ${index + 1}: ${item.title}`}
                  >
                    {index < step &&
                    !readinessProblems.some(
                      (problem) =>
                        workerProblemStep(problem.path, declaredFieldCount) ===
                        index
                    ) &&
                    setupItems
                      .filter((item) => item.step === index)
                      .every((item) => item.complete) ? (
                      <Check aria-hidden="true" />
                    ) : (
                      <span aria-hidden="true">{index + 1}.</span>
                    )}
                    {item.title}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </nav>
          ) : null}
          <form
            id="worker-form"
            noValidate
            onSubmit={(event) => {
              event.preventDefault()
              if (busy || editor.conflict) return
              if (creating && step < 2) advance()
              else void saveWorker()
            }}
            className="grid min-w-0 gap-4"
          >
            <div className="grid min-w-0 items-start gap-6 @min-[56rem]/page:grid-cols-[minmax(0,1fr)_15rem]">
              <Card className="min-w-0">
                <CardHeader className="border-b">
                  <CardTitle>
                    <h2
                      ref={stepHeading}
                      tabIndex={-1}
                      className="outline-none"
                    >
                      {creating
                        ? [
                            "Basic information",
                            "Connect a Provider",
                            "Review your Worker",
                          ][step]
                        : "Worker configuration"}
                    </h2>
                  </CardTitle>
                  <CardDescription>
                    {creating
                      ? [
                          "Give this Worker a name and choose the work it can perform.",
                          "Choose a preset or enter your endpoint, model and credentials.",
                          "Confirm the configuration. Advanced options are optional.",
                        ][step]
                      : "Manage identity, tasks, Provider credentials and advanced options."}
                  </CardDescription>
                </CardHeader>
                <CardContent className="@container/worker-fields grid min-w-0 gap-6">
                  {!creating || step === 0 ? (
                    <WorkerBasicFields {...fieldProps} />
                  ) : null}
                  {!creating || step === 1 ? (
                    <WorkerProviderFields
                      {...fieldProps}
                      preset={preset}
                      setPreset={setPreset}
                      clearStepProblems={() => setStepProblems([])}
                      configuredSecrets={editor.configuredSecrets}
                    />
                  ) : null}
                  {creating && step === 2 ? (
                    <WorkerReview
                      form={form}
                      definition={definition}
                      items={setupItems}
                      onEdit={goToStep}
                      problems={readinessProblems.map((problem) => ({
                        ...problem,
                        label: problemLabel(problem.path),
                      }))}
                      onFix={(path) => {
                        setStepProblems(
                          readinessProblems.filter(
                            (problem) => problem.path === path
                          )
                        )
                        goToStep(
                          workerProblemStep(path, declaredFieldCount),
                          path
                        )
                      }}
                      disabled={busy}
                    />
                  ) : null}
                  {!creating || step === 2 ? (
                    <WorkerAdvancedFields {...fieldProps} />
                  ) : null}
                </CardContent>
              </Card>
              <WorkerSetupSummary
                form={form}
                definition={definition}
                items={setupItems}
              />
            </div>
          </form>
          <footer
            id="worker-actions"
            ref={footerRef}
            className="sticky bottom-0 z-10 flex flex-wrap items-center justify-between gap-3 border-t bg-background py-3"
          >
            <p className="text-sm text-muted-foreground">
              {!creating || step === 2
                ? "Save makes this Worker effective for new Projects."
                : `Step ${step + 1} of ${creationSteps.length}`}
            </p>
            <div className="flex w-full min-w-0 flex-wrap items-center justify-end gap-2 @min-[40rem]/page:w-auto">
              {creating && step > 0 ? (
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => goToStep(step - 1)}
                >
                  <ArrowLeft data-icon="inline-start" />
                  Previous
                </Button>
              ) : null}
              <Button
                ref={saveButton}
                className="@min-[40rem]/page:flex-none"
                form="worker-form"
                type="submit"
                disabled={
                  busy ||
                  editor.conflict ||
                  ((!creating || step === 2) && !editor.dirty)
                }
              >
                {editor.saving
                  ? complete
                    ? "Checking Provider..."
                    : "Saving Worker..."
                  : creating && step === 0
                    ? "Continue to Provider"
                    : creating && step === 1
                      ? "Continue to Review"
                      : "Save Worker"}
                {creating && step < 2 ? (
                  <ArrowRight data-icon="inline-end" />
                ) : null}
              </Button>
            </div>
          </footer>
        </div>
      ) : null}
      <WorkerEditorDialogs
        editor={editor}
        busy={busy}
        conflictDismissed={conflictDismissed}
        setConflictDismissed={setConflictDismissed}
        saveButton={saveButton}
        reloadButton={reloadButton}
        stepHeading={stepHeading}
        saveWorker={saveWorker}
      />
    </SettingsPage>
  )
}
