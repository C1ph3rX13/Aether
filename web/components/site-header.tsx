"use client"

import { usePathname } from "next/navigation"
import { Separator } from "@/components/ui/separator"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { workerBranches } from "@/lib/worker-catalog"
import { settingsCategory, settingsSections } from "@/lib/settings-sections"

export function SiteHeader() {
  const pathname = usePathname()
  const workerBranch = Object.values(workerBranches).find(
    (branch) =>
      branch.href === pathname || pathname.startsWith(`${branch.href}/`)
  )
  const section = settingsSections.find(
    (section) => pathname === `/settings/${section.id}`
  )
  return (
    <header className="flex h-(--header-height) shrink-0 items-center gap-2 border-b ease-linear motion-safe:transition-[width,height]">
      <div className="flex w-full items-center gap-1 px-4 lg:gap-2 lg:px-6">
        <SidebarTrigger className="-ml-1" />
        <Separator
          orientation="vertical"
          className="mx-2 h-4 data-vertical:self-auto"
        />
        <h1 className="text-base font-medium">
          {workerBranch
            ? `${workerBranch.label} Workers`
            : section
              ? `Settings / ${settingsCategory(section.id).title}`
              : pathname.startsWith("/projects/")
                ? "Project Workspace"
                : "Projects"}
        </h1>
      </div>
    </header>
  )
}
