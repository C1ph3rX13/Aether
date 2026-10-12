"use client"
import { Fragment, useState } from "react"
import { IconAlertCircle as CircleAlert } from "@tabler/icons-react"
import { useSettingsSection } from "@/hooks/use-settings-section"
import {
  settingsSection,
  settingsFieldGroups,
  settingsCategory,
  settingsScopeDescriptions,
  type SettingsField,
  type SettingsSectionID,
} from "@/lib/settings-sections"
import { projectProblemMessage } from "@/lib/project-settings-form"
import { systemProblemMessage } from "@/lib/system-settings-form"
import { SettingsPage } from "@/components/settings-page"
import { SettingsNavigation } from "@/components/settings-navigation"
import { ConfigurationProblems } from "@/components/configuration-problems"
import { AppliedSettings } from "@/components/applied-settings"
import { ProjectAvailabilityStatus } from "@/components/project-availability"
import { SettingsSaveStatus } from "@/components/settings-save-status"
import { PageReadError } from "@/components/page-read-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSeparator,
  FieldSet,
} from "@/components/ui/field"
import { Separator } from "@/components/ui/separator"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
export function SectionSettings({
  section: id,
}: {
  section: SettingsSectionID
}) {
  const section = settingsSection(id)
  const category = settingsCategory(id)
  const groups = settingsFieldGroups(id)
  const editor = useSettingsSection(id)
  const [conflictDismissed, setConflictDismissed] = useState(false)
  const busy = editor.loading || editor.writePending
  const field = (definition: SettingsField) => {
    const inputID = `setting-${definition.path.replaceAll(".", "-")}`
    const errors = editor.problems.filter(
      (problem) =>
        problem.path === definition.path ||
        definition.path.startsWith(`${problem.path}.`) ||
        problem.path.startsWith(`${definition.path}[`)
    )
    const options =
      definition.kind === "action"
        ? [
            ["stop", "Stop"],
            ["remove", "Remove"],
          ]
        : definition.kind === "healthcheck"
          ? [
              ["disabled", "Disabled"],
              ["startup_only", "Startup only"],
              ["startup_and_task", "Startup and task"],
            ]
          : definition.kind === "prompt-group"
            ? (editor.baseline?.prompt_groups ?? []).map((value) => [
                value,
                value[0].toUpperCase() + value.slice(1),
              ])
            : null
    const props = {
      id: inputID,
      value: editor.form?.[definition.path] ?? "",
      disabled: busy,
      "aria-invalid": errors.length > 0 || undefined,
      "aria-describedby": `${inputID}-description${errors.length ? ` ${inputID}-error` : ""}`,
    }
    return (
      <Field
        key={definition.path}
        data-invalid={errors.length > 0 || undefined}
        data-disabled={busy || undefined}
      >
        <FieldLabel htmlFor={inputID}>{definition.label}</FieldLabel>
        <FieldDescription id={`${inputID}-description`}>
          {definition.description}
        </FieldDescription>
        {options ? (
          <Select
            value={props.value}
            disabled={busy}
            onValueChange={(value) => editor.change(definition.path, value)}
          >
            <SelectTrigger
              id={inputID}
              aria-invalid={props["aria-invalid"]}
              aria-describedby={props["aria-describedby"]}
              className="w-full"
            >
              <SelectValue placeholder="Choose an option" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {options.map(([value, label]) => (
                  <SelectItem key={value} value={value}>
                    {label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        ) : (
          <Input
            {...props}
            inputMode={
              definition.kind === "integer"
                ? "numeric"
                : definition.kind === "decimal"
                  ? "decimal"
                  : undefined
            }
            onChange={(event) =>
              editor.change(definition.path, event.target.value)
            }
          />
        )}
        {errors.length > 0 && (
          <FieldError
            id={`${inputID}-error`}
            errors={errors.map((problem) => ({
              message: definition.path.startsWith("runtime.")
                ? systemProblemMessage(problem)
                : projectProblemMessage(problem),
            }))}
          />
        )}
      </Field>
    )
  }
  return (
    <SettingsPage
      title={category.title}
      description={category.description}
      status={
        editor.baseline && (
          <ProjectAvailabilityStatus
            refreshToken={editor.baseline.draft_version}
            compact
          />
        )
      }
      actions={
        editor.baseline ? (
          <>
            <AppliedSettings refreshToken={editor.baseline.draft_version} />
          </>
        ) : undefined
      }
    >
      <SettingsNavigation section={id}>
        <section className="flex flex-col gap-4">
          {!editor.baseline && editor.loading && (
            <div
              className="grid max-w-6xl gap-4 @min-[48rem]/page:grid-cols-2"
              role="status"
              aria-label="Loading settings"
            >
              <FieldDescription className="col-span-full hidden motion-reduce:block">
                Loading settings…
              </FieldDescription>
              <Skeleton className="h-80" />
              <Skeleton className="h-80" />
            </div>
          )}
          {editor.error && editor.loadFailed ? (
            <PageReadError
              title="Unable to load settings"
              description={editor.error}
              disabled={busy}
              onRetry={() => void editor.load()}
            />
          ) : (
            editor.error && (
              <Alert variant="destructive">
                <CircleAlert />
                <AlertTitle>Settings could not be saved</AlertTitle>
                <AlertDescription>{editor.error}</AlertDescription>
              </Alert>
            )
          )}
          {editor.baseline && editor.form && (
            <>
              <ConfigurationProblems
                problems={editor.baseline.validation_problems}
              />
              {editor.conflict && (
                <Alert>
                  <CircleAlert />
                  <AlertTitle>Settings changed elsewhere</AlertTitle>
                  <AlertDescription>
                    Edits kept. Reload before saving again.
                  </AlertDescription>
                  <Button
                    className="mt-3 w-fit"
                    variant="outline"
                    disabled={busy}
                    onClick={() => setConflictDismissed(false)}
                  >
                    Reload
                  </Button>
                </Alert>
              )}
              <section
                aria-labelledby="settings-section-title"
                className="flex min-w-0 flex-col gap-8"
              >
                <header className="flex flex-col gap-2">
                  <h3
                    id="settings-section-title"
                    className="text-lg font-semibold"
                  >
                    {section.title}
                  </h3>
                  <FieldDescription className="max-w-[70ch]">
                    {section.description}
                  </FieldDescription>
                </header>
                <FieldGroup>
                  {groups.map((group, index) => (
                    <Fragment key={group.title}>
                      {index > 0 && <FieldSeparator />}
                      <FieldSet>
                        {groups.length > 1 && (
                          <FieldLegend>{group.title}</FieldLegend>
                        )}
                        <FieldGroup className="grid gap-x-6 gap-y-6 @min-[30rem]/field-group:grid-cols-2">
                          {group.fields.map(field)}
                        </FieldGroup>
                      </FieldSet>
                    </Fragment>
                  ))}
                </FieldGroup>
                <Separator />
                <footer className="flex flex-col gap-3">
                  <div className="flex flex-wrap items-center justify-between gap-4">
                    <SettingsSaveStatus
                      page={section.title}
                      dirty={editor.dirty}
                      saving={editor.saving}
                      loading={editor.loading}
                      saved={editor.saved}
                    />
                    <div className="flex max-w-full flex-wrap items-center gap-2">
                      <Button
                        variant="outline"
                        className="h-auto min-h-9 max-w-full whitespace-normal"
                        disabled={busy}
                        onClick={editor.useRecommended}
                      >
                        Use recommended values
                      </Button>
                      <Button
                        disabled={busy || !editor.dirty || editor.conflict}
                        onClick={() => {
                          setConflictDismissed(false)
                          void editor.save()
                        }}
                      >
                        {editor.saving ? "Saving..." : "Save"}
                      </Button>
                    </div>
                  </div>
                  <FieldDescription>
                    {settingsScopeDescriptions[id]}
                  </FieldDescription>
                </footer>
              </section>
            </>
          )}
          <Dialog
            open={editor.conflict && !conflictDismissed}
            onOpenChange={(open) => {
              if (!open && !busy) setConflictDismissed(true)
            }}
          >
            <DialogContent
              showCloseButton={false}
              onPointerDownOutside={(event) => event.preventDefault()}
              onEscapeKeyDown={(event) => {
                if (busy) event.preventDefault()
              }}
            >
              <DialogHeader>
                <DialogTitle>Settings changed elsewhere</DialogTitle>
                <DialogDescription>
                  Reload discards your edits and loads the latest saved
                  settings.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => setConflictDismissed(true)}
                >
                  Keep editing
                </Button>
                <Button disabled={busy} onClick={() => void editor.load()}>
                  Reload
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </section>
      </SettingsNavigation>
    </SettingsPage>
  )
}
