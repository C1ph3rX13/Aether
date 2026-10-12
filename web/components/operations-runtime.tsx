"use client"

import { IconAlertCircle as CircleAlert } from "@tabler/icons-react"
import { AppSidebar } from "@/components/app-sidebar"
import { SiteHeader } from "@/components/site-header"
import { ProjectCreationProvider } from "@/components/project-creation"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { useConsoleMetadata } from "@/modules/console-bootstrap/api"

export function OperationsRuntime({ children }: { children: React.ReactNode }) {
  return <OperationsBootstrap>{children}</OperationsBootstrap>
}

function Loading() {
  return (
    <main
      id="main-content"
      className="flex min-h-svh items-center justify-center"
      role="status"
      aria-label="Loading Aether"
    >
      <p className="text-sm text-muted-foreground">Loading Aether…</p>
    </main>
  )
}

function OperationsBootstrap({ children }: { children: React.ReactNode }) {
  const { data: metadata, error, mutate, isValidating } = useConsoleMetadata()
  if (error && !metadata)
    return (
      <main
        id="main-content"
        tabIndex={-1}
        className="flex min-h-svh items-center justify-center p-6"
      >
        <Empty className="max-w-md" role="alert">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <CircleAlert aria-hidden="true" />
            </EmptyMedia>
            <EmptyTitle>
              <h1>Aether unavailable</h1>
            </EmptyTitle>
            <EmptyDescription>
              Could not load Aether. Check your connection and retry.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button
              variant="outline"
              disabled={isValidating}
              onClick={() => void mutate().catch(() => undefined)}
            >
              Retry Aether
            </Button>
          </EmptyContent>
        </Empty>
      </main>
    )
  if (!metadata) return <Loading />
  return (
    <SidebarProvider
      style={
        {
          "--sidebar-width": "calc(var(--spacing) * 72)",
          "--header-height": "calc(var(--spacing) * 12)",
        } as React.CSSProperties
      }
    >
      <ProjectCreationProvider>
        <AppSidebar />
        <SidebarInset id="main-content" tabIndex={-1} className="min-w-0">
          <SiteHeader />
          {children}
        </SidebarInset>
      </ProjectCreationProvider>
    </SidebarProvider>
  )
}
