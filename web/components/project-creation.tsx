"use client"

import { createContext, useContext, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { CreateProjectDialog } from "@/components/create-project-dialog"
import { useNavigationActions } from "@/components/navigation-guard"
import { useProjectAvailability } from "@/hooks/use-project-availability"
import { createProject, type CreateProjectInput } from "@/lib/project-catalog"

interface ProjectCreation {
  availability: ReturnType<typeof useProjectAvailability>
  disabled: boolean
  requestCreate: (finalFocus: HTMLElement | null) => void
}

const ProjectCreationContext = createContext<ProjectCreation | null>(null)

export function ProjectCreationProvider({
  children,
}: {
  children: React.ReactNode
}) {
  const router = useRouter()
  const availability = useProjectAvailability()
  const navigation = useNavigationActions()
  const [open, setOpen] = useState(false)
  const inFlight = useRef(false)
  const finalFocus = useRef<HTMLElement | null>(null)
  const canCreate =
    availability.state === "ready" ||
    availability.state === "dispatcher-unavailable"
  const disabled = !canCreate || navigation.busy
  const requestCreate = (destination: HTMLElement | null) => {
    if (disabled || inFlight.current) return
    finalFocus.current = destination
    navigation.requestAction(() => setOpen(true), destination)
  }
  const submit = async (input: CreateProjectInput) => {
    if (!canCreate || navigation.isBusy() || inFlight.current)
      throw new Error("Project creation is unavailable")
    inFlight.current = true
    try {
      const result = await createProject(input)
      const id = result.project?.id
      if (typeof id !== "string" || !id)
        throw new Error("Project creation response is unreadable")
      finalFocus.current = document.getElementById("main-content")
      router.push(`/projects/${encodeURIComponent(id)}`)
    } finally {
      inFlight.current = false
    }
  }
  return (
    <ProjectCreationContext.Provider
      value={{ availability, disabled, requestCreate }}
    >
      {children}
      <CreateProjectDialog
        open={open}
        onOpenChange={setOpen}
        disabled={disabled}
        finalFocus={finalFocus}
        onSubmit={submit}
        onConfigurationRequired={() => {
          availability.markConfigurationRequired()
          void availability.refresh()
        }}
      />
    </ProjectCreationContext.Provider>
  )
}

export function useProjectCreation() {
  const context = useContext(ProjectCreationContext)
  if (!context)
    throw new Error(
      "useProjectCreation must be used inside ProjectCreationProvider"
    )
  return context
}
