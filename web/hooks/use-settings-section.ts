"use client"

import {
  useConfigurationEditor,
  type ConfigurationFormAdapter,
} from "@/hooks/use-configuration-editor"
import { ApiError } from "@/lib/api"
import {
  buildSettingsDraft,
  readSettingsForm,
  sectionOwnsProblem,
  settingsSections,
  validateSettingsForm,
  type SettingsForm,
  type SettingsSectionID,
} from "@/lib/settings-sections"

const adapters = Object.fromEntries(
  settingsSections.map((section) => [
    section.id,
    {
      read: (read) => {
        const form = readSettingsForm(read.draft, section.id)
        return { form, baselineForm: form }
      },
      validate: (form) => validateSettingsForm(form, section.id),
      buildSave: (read, form) => ({
        expected_draft_version: read.draft_version,
        draft: buildSettingsDraft(read.draft, form, section.id),
      }),
    } satisfies ConfigurationFormAdapter<SettingsForm>,
  ])
)

export function useSettingsSection(id: SettingsSectionID) {
  const editor = useConfigurationEditor(adapters[id])
  const failure = editor.saveFailure
  const validationFailure =
    failure?.kind === "validation" ||
    (failure?.kind === "request" &&
      failure.error instanceof ApiError &&
      Boolean(failure.error.validationProblems?.length))
  const error = editor.loadFailed
    ? "Settings could not be loaded. Try again."
    : validationFailure
      ? "Fix the highlighted settings and try again."
      : failure
        ? "Settings could not be saved. Your changes are still here; try again."
        : null
  return {
    ...editor,
    error,
    problems: (editor.dirty
      ? editor.saveProblems
      : (editor.baseline?.validation_problems ?? [])
    ).filter((problem) => sectionOwnsProblem(id, problem.path)),
    useRecommended: () => {
      const read = editor.baseline
      if (!read || editor.loading || editor.writePending) return
      editor.change(() => readSettingsForm(read.recommended_draft, id))
    },
    change: (path: string, value: string) => {
      if (editor.writePending || editor.loading) return
      editor.change((current) => ({ ...current, [path]: value }), path)
    },
  }
}
