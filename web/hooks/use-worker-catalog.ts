"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useNavigationGuard } from "@/components/navigation-guard"
import { ApiError, deleteJSON, getJSON } from "@/lib/api"
import {
  normalizeConfigurationRead,
  type ConfigurationRead,
} from "@/lib/configuration"

export function useWorkerCatalog() {
  const [read, setRead] = useState<ConfigurationRead | null>(null)
  const [failed, setFailed] = useState(false)
  const [loading, setLoading] = useState(true)
  const [deletePending, setDeletePending] = useState(false)
  const [deleteFailure, setDeleteFailure] = useState<
    "conflict" | "validation" | "request" | null
  >(null)
  const controllerRef = useRef<AbortController | null>(null)
  const writeInFlight = useRef(false)
  const lifetime = useRef({ active: false })
  useNavigationGuard(
    useMemo(
      () => ({
        isBusy: () => writeInFlight.current,
        busy: deletePending,
      }),
      [deletePending]
    )
  )

  const load = useCallback(async () => {
    if (writeInFlight.current) return false
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setLoading(true)
    try {
      const result = normalizeConfigurationRead(
        await getJSON<ConfigurationRead>("/configuration", controller.signal)
      )
      if (controller.signal.aborted) return false
      setRead(result)
      setFailed(false)
      setDeleteFailure(null)
      return true
    } catch {
      if (!controller.signal.aborted) setFailed(true)
      return false
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null
        setLoading(false)
      }
    }
  }, [])

  useEffect(() => {
    const session = { active: true }
    lifetime.current = session
    void Promise.resolve().then(() => {
      if (session.active) void load()
    })
    return () => {
      session.active = false
      controllerRef.current?.abort()
    }
  }, [load])

  const deleteWorker = async (name: string) => {
    const session = lifetime.current
    if (
      !session.active ||
      writeInFlight.current ||
      controllerRef.current ||
      loading ||
      failed ||
      !read ||
      deleteFailure === "conflict"
    )
      return false
    writeInFlight.current = true
    setDeletePending(true)
    setDeleteFailure(null)
    try {
      const updated = normalizeConfigurationRead(
        await deleteJSON<ConfigurationRead>(
          `/configuration/workers/${encodeURIComponent(name)}`,
          { expected_draft_version: read.draft_version }
        )
      )
      if (!session.active) return false
      setRead(updated)
      return true
    } catch (error) {
      if (session.active)
        setDeleteFailure(
          error instanceof ApiError && error.code === "conflict"
            ? "conflict"
            : error instanceof ApiError && error.status === 400
              ? "validation"
              : "request"
        )
      return false
    } finally {
      writeInFlight.current = false
      if (session.active) setDeletePending(false)
    }
  }

  useEffect(() => {
    if (!deletePending) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ""
    }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [deletePending])

  return {
    read,
    failed,
    loading,
    load,
    deleteWorker,
    deletePending,
    deleteFailure,
  }
}
