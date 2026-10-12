"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  readProjectAvailability,
  type ProjectAvailability,
} from "@/lib/project-availability"

export function useProjectAvailability(refreshToken = 0) {
  const [state, setState] = useState<ProjectAvailability>("checking")
  const [missingTasks, setMissingTasks] = useState<string[]>([])
  const controllerRef = useRef<AbortController | null>(null)
  const refresh = useCallback(async () => {
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    try {
      const result = await readProjectAvailability(controller.signal)
      if (!controller.signal.aborted) {
        setState(result.state)
        setMissingTasks(result.missingTasks)
      }
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null
    }
  }, [])

  useEffect(() => {
    let active = true
    void Promise.resolve().then(() => {
      if (active) void refresh()
    })
    const refreshVisible = () => {
      if (document.visibilityState === "visible" && !controllerRef.current)
        void refresh()
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
    }
  }, [refresh, refreshToken])

  const markConfigurationRequired = () => {
    controllerRef.current?.abort()
    setState("configuration-required")
  }
  return { state, missingTasks, refresh, markConfigurationRequired }
}
