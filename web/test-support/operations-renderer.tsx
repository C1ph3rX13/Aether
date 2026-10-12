import { render } from "@testing-library/react"
import { vi } from "vitest"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import { ProjectCreationProvider } from "@/components/project-creation"
import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

export function renderOperations(ui: React.ReactNode) {
  vi.stubGlobal("navigation", new EventTarget())
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }))
  return render(ui, {
    wrapper: ({ children }) => (
      <NavigationGuardProvider>
        <SidebarProvider>
          <ProjectCreationProvider>
            <AppSidebar />
            {children}
          </ProjectCreationProvider>
        </SidebarProvider>
      </NavigationGuardProvider>
    ),
  })
}
