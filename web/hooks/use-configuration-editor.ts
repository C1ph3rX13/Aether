"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useNavigationGuard } from "@/components/navigation-guard"
import { ApiError, getJSON, putJSON } from "@/lib/api"
import {
  normalizeConfigurationRead,
  type ConfigurationRead,
  type DynamicDraft,
  type ValidationProblem,
} from "@/lib/configuration"

export interface ConfigurationFormAdapter<Form> {
  read: (
    read: ConfigurationRead,
    savedForm?: Form
  ) => { form: Form | null; baselineForm: Form | null }
  validate: (form: Form, read: ConfigurationRead) => ValidationProblem[]
  buildSave: (
    read: ConfigurationRead,
    form: Form
  ) => {
    expected_draft_version: number
    draft: DynamicDraft
    secrets?: Record<
      string,
      Record<string, { value?: string; clear?: boolean }>
    >
  }
  mapProblems?: (
    problems: ValidationProblem[],
    read: ConfigurationRead,
    form: Form
  ) => ValidationProblem[]
}

type SaveFailure = { kind: "validation" } | { kind: "request"; error: unknown }
type WriteResult<Result> =
  { ok: true; value: Result } | { ok: false; error: unknown }

export function useConfigurationEditor<Form>(
  adapter: ConfigurationFormAdapter<Form>
) {
  const [baseline, setBaseline] = useState<ConfigurationRead | null>(null)
  const [baselineForm, setBaselineForm] = useState<Form | null>(null)
  const [form, setForm] = useState<Form | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadFailed, setLoadFailed] = useState(false)
  const [writeKind, setWriteKind] = useState<"save" | null>(null)
  const [conflict, setConflict] = useState(false)
  const [saved, setSaved] = useState(false)
  const [saveFailure, setSaveFailure] = useState<SaveFailure | null>(null)
  const [saveProblems, setSaveProblems] = useState<ValidationProblem[]>([])
  const lifetime = useRef({ active: false })
  const loadController = useRef<AbortController | null>(null)
  const readPending = useRef(true)
  const writeInFlight = useRef(false)

  const load = useCallback(async () => {
    if (writeInFlight.current || !lifetime.current.active) return
    loadController.current?.abort()
    const controller = new AbortController()
    loadController.current = controller
    readPending.current = true
    setLoading(true)
    setSaveFailure(null)
    try {
      const read = normalizeConfigurationRead(
        await getJSON<ConfigurationRead>("/configuration", controller.signal)
      )
      if (controller.signal.aborted) return
      const projection = adapter.read(read)
      setBaseline(read)
      setBaselineForm(projection.baselineForm)
      setForm(projection.form)
      setLoadFailed(false)
      setSaveProblems([])
      setConflict(false)
      setSaved(false)
      return true
    } catch {
      if (!controller.signal.aborted) setLoadFailed(true)
    } finally {
      if (!controller.signal.aborted) {
        readPending.current = false
        setLoading(false)
      }
    }
  }, [adapter])

  useEffect(() => {
    const session = { active: true }
    lifetime.current = session
    void Promise.resolve().then(() => {
      if (session.active) void load()
    })
    return () => {
      session.active = false
      loadController.current?.abort()
    }
  }, [load])

  const dirty =
    form !== null &&
    baselineForm !== null &&
    JSON.stringify(form) !== JSON.stringify(baselineForm)

  const performWrite = useCallback(
    async <Result>(
      operation: (read: ConfigurationRead) => Promise<Result>
    ): Promise<WriteResult<Result> | null> => {
      const session = lifetime.current
      if (
        !session.active ||
        writeInFlight.current ||
        readPending.current ||
        !baseline ||
        conflict
      )
        return null
      writeInFlight.current = true
      setWriteKind("save")
      try {
        const value = await operation(baseline)
        return session.active && lifetime.current === session
          ? { ok: true, value }
          : null
      } catch (error: unknown) {
        if (!session.active || lifetime.current !== session) return null
        if (error instanceof ApiError && error.code === "conflict")
          setConflict(true)
        return { ok: false, error }
      } finally {
        writeInFlight.current = false
        if (session.active && lifetime.current === session) setWriteKind(null)
      }
    },
    [baseline, conflict]
  )

  const save = useCallback(async () => {
    if (
      writeInFlight.current ||
      readPending.current ||
      !baseline ||
      !form ||
      conflict ||
      !lifetime.current.active
    )
      return false
    if (!dirty) return true
    const problems = adapter.validate(form, baseline)
    setSaved(false)
    setSaveProblems(problems)
    if (problems.length) {
      setSaveFailure({ kind: "validation" })
      return false
    }
    setSaveFailure(null)
    const result = await performWrite(async (read) =>
      normalizeConfigurationRead(
        await putJSON<ConfigurationRead>(
          "/configuration",
          adapter.buildSave(read, form)
        )
      )
    )
    if (!result) return false
    let error: unknown
    if (result.ok) {
      try {
        const projection = adapter.read(result.value, form)
        setBaseline(result.value)
        setBaselineForm(projection.baselineForm)
        setForm(projection.form)
        setLoadFailed(false)
        setConflict(false)
        setSaved(true)
        return true
      } catch (projectionError: unknown) {
        error = projectionError
      }
    } else error = result.error
    if (!(error instanceof ApiError && error.code === "conflict")) {
      setSaveFailure({ kind: "request", error })
      if (error instanceof ApiError) {
        const problems = error.validationProblems ?? []
        setSaveProblems(
          adapter.mapProblems?.(problems, baseline, form) ?? problems
        )
      }
    }
    return false
  }, [adapter, baseline, form, dirty, conflict, performWrite])

  const discard = useCallback(() => {
    if (writeInFlight.current) return
    loadController.current?.abort()
    readPending.current = false
    setLoading(false)
    setForm(baselineForm)
    setSaveFailure(null)
    setSaveProblems([])
    setSaved(false)
  }, [baselineForm])
  const change = (update: (current: Form) => Form, path?: string) => {
    if (writeInFlight.current || readPending.current) return
    setForm((current) => (current === null ? null : update(current)))
    setSaveProblems((current) =>
      path ? current.filter((problem) => problem.path !== path) : []
    )
    setSaved(false)
  }
  useNavigationGuard(
    useMemo(
      () => ({
        isDirty: () => dirty,
        isBusy: () => writeInFlight.current,
        busy: writeKind !== null,
        save,
        discard,
      }),
      [dirty, save, discard, writeKind]
    )
  )
  const writePending = writeKind !== null
  useEffect(() => {
    if (!dirty && !writePending) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ""
    }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirty, writePending])
  return {
    baseline,
    baselineForm,
    form,
    loading,
    loadFailed,
    saving: writeKind === "save",
    writePending,
    dirty,
    conflict,
    saved,
    saveFailure,
    saveProblems,
    load,
    save,
    discard,
    change,
    dismissSaveFailure: () => setSaveFailure(null),
  }
}
