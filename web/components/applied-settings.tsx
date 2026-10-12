"use client"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { FieldDescription } from "@/components/ui/field"
import { readAppliedSettings } from "@/lib/applied-settings"
import type { DynamicDraft } from "@/lib/configuration"
import {
  containerFields,
  readProjectForm,
  retryGroups,
  taskFields,
} from "@/lib/project-settings-form"
import { readSystemForm, systemFields } from "@/lib/system-settings-form"
function Values({ items }: { items: Array<[string, string | number]> }) {
  return (
    <dl className="grid gap-3 text-sm">
      {items.map(([label, value]) => (
        <div key={label} className="grid min-w-0 grid-cols-2 gap-3">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 wrap-anywhere">
            {value === "" ? "None" : value}
          </dd>
        </div>
      ))}
    </dl>
  )
}
function timeoutLabel(phase: string) {
  return `${phase
    .split(".")
    .map((part) => part[0].toUpperCase() + part.slice(1))
    .join(" ")} (seconds)`
}
function ConfigurationGroups({ draft }: { draft: DynamicDraft }) {
  const project = readProjectForm(draft)
  const system = readSystemForm(draft)
  return (
    <div className="flex flex-col gap-6">
      <section aria-labelledby="applied-project-title">
        <h2 id="applied-project-title" className="mb-4 text-base font-semibold">
          Project settings
        </h2>
        <h3 className="mb-3 font-medium">Task timeouts</h3>
        <Values
          items={taskFields.map((field) => [field.label, project[field.path]])}
        />
        <h3 className="mt-5 mb-3 font-medium">Retry policies</h3>
        {retryGroups.map((group) => (
          <div key={group.title} className="mb-4">
            <h4 className="mb-2 text-sm font-medium">{group.title}</h4>
            <Values
              items={group.fields.map((field) => [
                field.label,
                project[field.path],
              ])}
            />
          </div>
        ))}
        <h3 className="mt-5 mb-3 font-medium">Project runtime</h3>
        <Values
          items={containerFields.map((field) => [
            field.label,
            project[field.path],
          ])}
        />
      </section>
      <section aria-labelledby="applied-system-title">
        <h2 id="applied-system-title" className="mb-4 text-base font-semibold">
          System settings
        </h2>
        <Values
          items={systemFields.map((field) => [field.label, system[field.path]])}
        />
      </section>
      <section aria-labelledby="applied-workers-title">
        <h2 id="applied-workers-title" className="mb-4 text-base font-semibold">
          Workers
        </h2>
        {draft.workers.length === 0 ? (
          <p className="text-sm text-muted-foreground">No Workers.</p>
        ) : null}
        {draft.workers.map((worker) => (
          <Card
            key={worker.name}
            role="region"
            className="mb-4"
            aria-label={`Saved Worker ${worker.name}`}
          >
            <CardHeader>
              <CardTitle>
                <h3 className="wrap-anywhere">{worker.name}</h3>
              </CardTitle>
            </CardHeader>
            <CardContent>
              <Values
                items={[
                  [
                    "Agent Type",
                    worker.agent_type === "claude_code"
                      ? "Claude Code"
                      : worker.agent_type === "codex"
                        ? "Codex"
                        : "Pi",
                  ],
                  ...(worker.provider_name &&
                  worker.provider_name !== worker.name
                    ? [["Provider", worker.provider_name] as [string, string]]
                    : []),
                  ["Tasks", worker.task_types.join(", ")],
                  ["Max running", worker.max_running],
                  ["Priority", worker.priority],
                  ["Capabilities", worker.capabilities.join(", ")],
                ]}
              />
              <h4 className="mt-4 mb-3 text-sm font-medium">Environment</h4>
              <Values items={Object.entries(worker.agent_env)} />
              <h4 className="mt-4 mb-3 text-sm font-medium">
                Timeout overrides
              </h4>
              {Object.keys(worker.timeout_overrides).length ? (
                <Values
                  items={Object.entries(worker.timeout_overrides).map(
                    ([phase, seconds]) => [timeoutLabel(phase), seconds]
                  )}
                />
              ) : (
                <p className="text-sm text-muted-foreground">
                  Uses Project task timeouts.
                </p>
              )}
              <h4 className="mt-4 mb-3 text-sm font-medium">Credentials</h4>
              <Values
                items={Object.entries(worker.secrets ?? {}).map(
                  ([name, state]) => [
                    name,
                    state.configured
                      ? "Configured · ********"
                      : "Not configured",
                  ]
                )}
              />
            </CardContent>
          </Card>
        ))}
      </section>
    </div>
  )
}
export function AppliedSettings({ refreshToken }: { refreshToken: number }) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<DynamicDraft | null>(null)
  const [state, setState] = useState<"loading" | "loaded" | "failed">("loading")
  const [retryToken, setRetryToken] = useState(0)
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    void Promise.resolve().then(async () => {
      if (controller.signal.aborted) return
      setState("loading")
      setDraft(null)
      try {
        const result = await readAppliedSettings(controller.signal)
        if (controller.signal.aborted) return
        setDraft(result)
        setState("loaded")
      } catch {
        if (!controller.signal.aborted) setState("failed")
      }
    })
    return () => controller.abort()
  }, [open, refreshToken, retryToken])
  return (
    <Sheet open={open} onOpenChange={setOpen} modal={false}>
      <SheetTrigger asChild>
        <Button variant="outline">Saved settings</Button>
      </SheetTrigger>
      <SheetContent
        className="sm:max-w-xl"
        onInteractOutside={(event) => event.preventDefault()}
      >
        <SheetHeader>
          <SheetTitle>Saved settings</SheetTitle>
          <SheetDescription>
            Current settings for new Projects. Read-only.
          </SheetDescription>
        </SheetHeader>
        <div className="min-h-0 flex-1 overflow-auto px-4 pb-6">
          {state === "loading" ? (
            <div role="status" aria-label="Loading saved settings">
              <FieldDescription className="mb-4 hidden motion-reduce:block">
                Loading saved settings…
              </FieldDescription>
              <Skeleton className="h-64" />
            </div>
          ) : null}
          {state === "failed" ? (
            <div role="alert">
              <p className="font-medium">Unable to load saved settings</p>
              <p className="mt-2 text-sm text-muted-foreground">
                Retry to view the current settings.
              </p>
              <Button
                className="mt-4"
                variant="outline"
                onClick={() => setRetryToken((token) => token + 1)}
              >
                Retry
              </Button>
            </div>
          ) : null}
          {state === "loaded" && !draft ? (
            <p role="status">
              No saved settings yet. Save configuration to make it effective.
            </p>
          ) : null}
          {state === "loaded" && draft ? (
            <ConfigurationGroups draft={draft} />
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  )
}
