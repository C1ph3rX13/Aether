"use client"

import { Button } from "@/components/ui/button"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  WorkerSection,
  AdvancedWorkerSection,
  type WorkerFieldsProps,
} from "@/components/worker-editor-fields"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { initializePreset } from "@/lib/worker-form"
import type {
  AgentFieldMetadata,
  DynamicSecretState,
} from "@/lib/configuration"

export function WorkerProviderFields({
  form,
  definition,
  read,
  fields,
  busy,
  creating,
  problems,
  change,
  update,
  input,
  errorFor,
  preset,
  setPreset,
  clearStepProblems,
  configuredSecrets,
}: WorkerFieldsProps & {
  preset: string
  setPreset: (preset: string) => void
  clearStepProblems: () => void
  configuredSecrets: Record<string, DynamicSecretState>
}) {
  const agentType = definition.type
  const providerInput = (field: AgentFieldMetadata) => {
    const index = form.environment.findIndex(
      (entry) => entry.name === field.name
    )
    if (field.options?.length) {
      const path = `environment.${index}`
      const errors = errorFor(path)
      return (
        <Field data-invalid={errors.length > 0 || undefined}>
          <FieldLabel htmlFor={`worker-${path}`}>
            {field.display_name}
          </FieldLabel>
          <Select
            value={form.environment[index]?.value || ""}
            disabled={busy}
            onValueChange={(value) => {
              if (!value) return
              setPreset("custom")
              change(
                "environment",
                form.environment.map((entry, entryIndex) =>
                  entryIndex === index ? { ...entry, value } : entry
                )
              )
            }}
          >
            <SelectTrigger
              id={`worker-${path}`}
              className="w-full"
              aria-invalid={errors.length > 0 || undefined}
              aria-describedby={`worker-${path}-description${errors.length ? ` worker-${path}-error` : ""}`}
            >
              <SelectValue placeholder="Choose an API protocol" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {field.options.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <FieldDescription id={`worker-${path}-description`}>
            The API protocol used by this Provider.
          </FieldDescription>
          {errors.length > 0 && (
            <FieldError id={`worker-${path}-error`} errors={errors} />
          )}
        </Field>
      )
    }
    return input(
      `environment.${index}`,
      field.display_name,
      form.environment[index]?.value ?? "",
      (value) => {
        setPreset("custom")
        change(
          "environment",
          form.environment.map((entry, entryIndex) =>
            entryIndex === index ? { ...entry, value } : entry
          )
        )
      },
      field.display_name.includes("URL")
        ? `${field.name === "CODEX_BASE_URL" ? "Must support the Responses API. " : ""}Use an HTTP or HTTPS base URL without credentials, query parameters or a fragment.`
        : field.required
          ? "Required before Save."
          : "Optional.",
      { required: field.required }
    )
  }
  return (
    <>
      <WorkerSection title="Provider" hideHeading={creating}>
        <Field>
          <FieldLabel htmlFor="worker-provider-choice">
            Provider configuration
          </FieldLabel>
          <Select
            disabled={busy}
            value={preset}
            onValueChange={(choice) => {
              clearStepProblems()
              setPreset(choice)
              const selected = read.provider_presets.find(
                (preset) => preset.id === choice
              )
              if (selected)
                update((current) =>
                  initializePreset(current, selected, definition)
                )
            }}
          >
            <SelectTrigger id="worker-provider-choice" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="custom">Custom provider</SelectItem>
                {read.provider_presets
                  .filter((preset) => preset.agent_type === agentType)
                  .map((preset) => (
                    <SelectItem key={preset.id} value={preset.id}>
                      {preset.display_name}
                    </SelectItem>
                  ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <FieldDescription className="max-w-[70ch] leading-relaxed">
            A preset fills the endpoint and model. Add your credentials below.
            {preset !== "custom"
              ? ` Protocol: ${read.provider_presets.find((entry) => entry.id === preset)?.protocol}.`
              : null}
          </FieldDescription>
        </Field>
        {fields
          .filter((field) => !field.secret && field.required)
          .map(providerInput)}
      </WorkerSection>
      <WorkerSection title="Credentials">
        <p className="max-w-[70ch] text-sm leading-relaxed text-muted-foreground">
          Credentials belong to this Worker. Values stay hidden after Save.
        </p>
        {fields
          .filter((field) => field.secret)
          .map((field) => {
            const secret = form.secrets[field.name]
            const errors = errorFor(`secrets.${field.name}`)
            const errorId = `secret-${field.name}-error`
            const configured =
              configuredSecrets[field.name]?.configured === true
            const update = (mode: "retain" | "replace" | "clear", value = "") =>
              change("secrets", {
                ...form.secrets,
                [field.name]: { mode, value },
              })
            return (
              <div key={field.name} className="grid gap-3">
                {!creating ? (
                  <p className="font-medium">{field.display_name}</p>
                ) : null}
                {!creating ? (
                  <p className="text-sm">
                    {configured ? "Configured" : "Not configured"}
                    {configured ? (
                      <span className="ms-2" aria-hidden="true">
                        ********
                      </span>
                    ) : null}
                  </p>
                ) : null}
                {secret.mode === "replace" || (creating && !configured) ? (
                  <Field>
                    <FieldLabel htmlFor={`secret-${field.name}`}>
                      {creating
                        ? field.display_name
                        : `Replace ${field.display_name}`}
                    </FieldLabel>
                    <Input
                      id={`secret-${field.name}`}
                      type="password"
                      disabled={busy}
                      autoComplete="new-password"
                      aria-required={field.required || undefined}
                      value={secret.value}
                      aria-invalid={errors.length > 0 || undefined}
                      aria-describedby={`secret-${field.name}-help${errors.length > 0 ? ` ${errorId}` : ""}`}
                      onChange={(event) =>
                        update("replace", event.target.value)
                      }
                    />
                    <FieldDescription
                      id={`secret-${field.name}-help`}
                      className="max-w-[70ch] leading-relaxed"
                    >
                      {creating ? (
                        <>
                          <span>
                            {secret.mode === "replace" && secret.value
                              ? "Will be stored on Save."
                              : "Not configured"}
                          </span>{" "}
                          Required before Save. Values stay hidden after Save.
                        </>
                      ) : configured ? (
                        "Leave blank to keep the current Secret."
                      ) : (
                        "Required before Save. Enter your Provider credential."
                      )}
                    </FieldDescription>
                  </Field>
                ) : null}
                {secret.mode === "clear" ? (
                  <p role="status" className="text-sm">
                    Required credentials must be replaced before Save.
                  </p>
                ) : null}
                <FieldError id={errorId} errors={errors} />
                <div className="flex flex-wrap gap-2">
                  {!creating || configured ? (
                    <Button
                      type="button"
                      variant="outline"
                      disabled={busy}
                      onClick={() => update("replace")}
                    >
                      {configured ? "Replace Secret" : "Configure Secret"}
                    </Button>
                  ) : null}
                  {configured ? (
                    <Button
                      type="button"
                      variant="outline"
                      disabled={busy}
                      onClick={() => update("clear")}
                    >
                      Clear Secret
                    </Button>
                  ) : null}
                  {secret.mode !== "retain" ? (
                    <Button
                      type="button"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => update("retain")}
                    >
                      {creating ? "Clear input" : "Keep current Secret"}
                    </Button>
                  ) : null}
                </div>
              </div>
            )
          })}
      </WorkerSection>
      <AdvancedWorkerSection
        title="Optional Provider settings"
        summary="Display name and additional Provider options"
        invalid={problems.some(
          (problem) =>
            problem.path === "providerName" ||
            fields.some(
              (field) =>
                !field.required &&
                problem.path ===
                  `environment.${form.environment.findIndex((entry) => entry.name === field.name)}`
            )
        )}
      >
        {input(
          "providerName",
          "Provider name",
          form.providerName,
          (value) => change("providerName", value),
          "Optional display name. Does not change the connection."
        )}
        {fields
          .filter((field) => !field.secret && !field.required)
          .map(providerInput)}
      </AdvancedWorkerSection>
    </>
  )
}
