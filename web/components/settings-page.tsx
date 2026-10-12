import type { ReactNode } from "react"
import { Separator } from "@/components/ui/separator"

export function SettingsPage({
  title,
  description,
  actions,
  status,
  children,
}: {
  title: string
  description: string
  actions?: ReactNode
  status?: ReactNode
  children: ReactNode
}) {
  return (
    <section
      className="@container/page flex w-full min-w-0 flex-col gap-6 p-4 lg:p-6"
      aria-labelledby="settings-page-title"
    >
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1 basis-80">
          <h2
            id="settings-page-title"
            className="text-2xl font-semibold text-balance"
          >
            {title}
          </h2>
          <p className="mt-1 max-w-[70ch] text-sm text-pretty text-muted-foreground">
            {description}
          </p>
          {status}
        </div>
        <div className="flex max-w-full min-w-0 flex-wrap items-center gap-2">
          {actions}
        </div>
      </header>
      <Separator />
      {children}
    </section>
  )
}
