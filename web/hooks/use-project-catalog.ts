"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { readProjectCatalog, type CatalogProject } from "@/lib/project-catalog"

export function useProjectCatalog() {
  const [projects, setProjects] = useState<CatalogProject[] | null>(null)
  const [failed, setFailed] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [updatedAt, setUpdatedAt] = useState<number | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const hasReadRef = useRef(false)

  const refresh = useCallback(async () => {
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setRefreshing(true)
    if (!hasReadRef.current) setFailed(false)
    try {
      const result = await readProjectCatalog(controller.signal)
      if (controller.signal.aborted) return
      hasReadRef.current = true
      setProjects(result)
      setUpdatedAt(Date.now())
      setFailed(false)
      return result
    } catch (error: unknown) {
      if (controller.signal.aborted) return
      setFailed(true)
      throw error
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null
        setRefreshing(false)
      }
    }
  }, [])

  useEffect(() => {
    let active = true
    void Promise.resolve().then(() => {
      if (active) void refresh().catch(() => undefined)
    })
    const refreshVisible = () => {
      if (document.visibilityState === "visible" && !controllerRef.current)
        void refresh().catch(() => undefined)
    }
    const interval = window.setInterval(refreshVisible, 5000)
    window.addEventListener("focus", refreshVisible)
    document.addEventListener("visibilitychange", refreshVisible)
    return () => {
      active = false
      window.clearInterval(interval)
      window.removeEventListener("focus", refreshVisible)
      document.removeEventListener("visibilitychange", refreshVisible)
      controllerRef.current?.abort()
      controllerRef.current = null
    }
  }, [refresh])

  return { projects, failed, refreshing, updatedAt, refresh }
}
