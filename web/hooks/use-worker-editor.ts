"use client"

import { useMemo, useRef } from "react"
import {
  useConfigurationEditor,
  type ConfigurationFormAdapter,
} from "@/hooks/use-configuration-editor"
import { ApiError } from "@/lib/api"
import type { AgentType } from "@/lib/worker-catalog"
import {
  buildWorkerSave,
  readWorkerForm,
  uniqueCopyName,
  validateWorkerForm,
  workerValidationProblems,
  type WorkerForm,
} from "@/lib/worker-form"

export function useWorkerEditor(
  agentType: AgentType,
  workerName?: string,
  copy = false
) {
  const savedName = useRef(copy ? undefined : workerName)
  const adapter = useMemo<ConfigurationFormAdapter<WorkerForm>>(
    () => ({
      read: (read, savedForm) => {
        const definition = read.agent_definitions.find(
          (definition) => definition.type === agentType
        )
        if (!definition) throw new Error("Agent Definition unavailable")
        const targetName = savedForm?.name ?? savedName.current ?? workerName
        const worker = read.draft.workers.find(
          (worker) =>
            worker.name === targetName && worker.agent_type === agentType
        )
        if (savedForm) {
          if (!worker) throw new Error("Saved Worker missing")
          const form = readWorkerForm(definition, worker)
          savedName.current = worker.name
          return { form, baselineForm: form }
        }
        if (targetName && !worker) return { form: null, baselineForm: null }
        const copying = copy && savedName.current === undefined
        const form = readWorkerForm(
          definition,
          worker,
          copying && worker ? uniqueCopyName(worker.name, read) : undefined
        )
        return {
          form,
          baselineForm: copying ? readWorkerForm(definition) : form,
        }
      },
      validate: (form, read) => {
        const definition = read.agent_definitions.find(
          (definition) => definition.type === agentType
        )!
        return validateWorkerForm(form, definition, read, savedName.current)
      },
      buildSave: (read, form) =>
        buildWorkerSave(read, form, agentType, savedName.current),
      mapProblems: (problems, read, form) => {
        const request = buildWorkerSave(
          read,
          form,
          agentType,
          savedName.current
        )
        const index = request.draft.workers.findIndex(
          (worker) => worker.name === form.name
        )
        return workerValidationProblems(problems, index, form.name, form)
      },
    }),
    [agentType, workerName, copy]
  )
  const editor = useConfigurationEditor(adapter)
  const { baseline, form } = editor
  const originalName = editor.baselineForm
    ? editor.baselineForm.name || undefined
    : copy
      ? undefined
      : workerName
  const definition = baseline?.agent_definitions.find(
    (definition) => definition.type === agentType
  )
  const index =
    baseline?.draft.workers.findIndex(
      (worker) => worker.name === originalName
    ) ?? -1
  const failure =
    editor.saveFailure?.kind === "request" ? editor.saveFailure.error : null
  const providerFailure =
    failure instanceof ApiError && failure.code === "provider_check_failed"
  const problems =
    !editor.dirty && baseline && form && index >= 0
      ? workerValidationProblems(
          baseline.validation_problems,
          index,
          form.name,
          form
        )
      : editor.saveProblems
  return {
    ...editor,
    originalName,
    definition,
    problems,
    notFound: baseline !== null && form === null,
    configuredSecrets:
      baseline && index >= 0 && form?.name === originalName
        ? (baseline.draft.workers[index].secrets ?? {})
        : {},
    failure: failure
      ? {
          title: providerFailure
            ? "Provider check failed"
            : "Worker could not be saved",
          message: providerFailure
            ? "Check the endpoint, model and credentials, then retry. Saved Worker unchanged."
            : "Edits kept. Review the fields and retry. If the connection was interrupted, reload to check saved settings first.",
        }
      : null,
    dismissFailure: editor.dismissSaveFailure,
  }
}
