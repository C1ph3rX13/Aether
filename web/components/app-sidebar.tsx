"use client"

import { useRef } from "react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import {
  IconCirclePlusFilled,
  IconFolder,
  IconInnerShadowTop,
  IconListCheck,
  IconServer2,
  IconFileText,
} from "@tabler/icons-react"
import { workerBranches, workerIcons } from "@/lib/worker-catalog"
import { settingsCategories } from "@/lib/settings-sections"
import { useProjectCreation } from "@/components/project-creation"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"

export function AppSidebar() {
  const pathname = usePathname()
  const { isMobile, setOpenMobile } = useSidebar()
  const creation = useProjectCreation()
  const mobileCreate = useRef(false)
  const projectsActive = pathname === "/" || pathname.startsWith("/projects")
  return (
    <Sidebar
      collapsible="offcanvas"
      variant="inset"
      role="navigation"
      aria-label="Operations navigation"
      onMobileCloseAutoFocus={(event) => {
        const toggle = document.querySelector<HTMLElement>(
          '[data-slot="sidebar-trigger"]'
        )
        event.preventDefault()
        toggle?.focus()
        if (mobileCreate.current) {
          mobileCreate.current = false
          creation.requestCreate(toggle)
        }
      }}
      onClick={(event) => {
        if (
          isMobile &&
          event.target instanceof Element &&
          event.target.closest("a[href]")
        )
          setOpenMobile(false)
      }}
    >
      <SidebarHeader className="pe-12 md:pe-2">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              className="data-[slot=sidebar-menu-button]:p-1.5!"
              asChild
            >
              <Link href="/projects" aria-label="Aether home">
                <IconInnerShadowTop aria-hidden="true" className="size-5!" />
                <span className="text-base font-semibold">Aether</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent className="flex flex-col gap-2">
            <SidebarMenu>
              <SidebarMenuItem className="flex items-center gap-2">
                <SidebarMenuButton
                  disabled={creation.disabled}
                  className="min-w-8 bg-primary text-primary-foreground duration-200 ease-linear hover:bg-primary/90 hover:text-primary-foreground active:bg-primary/90 active:text-primary-foreground"
                  onClick={(event) => {
                    if (isMobile) {
                      mobileCreate.current = true
                      setOpenMobile(false)
                    } else creation.requestCreate(event.currentTarget)
                  }}
                >
                  <IconCirclePlusFilled aria-hidden="true" />
                  <span>New Project</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton isActive={projectsActive} asChild>
                  <Link
                    href="/projects"
                    aria-current={projectsActive ? "page" : undefined}
                  >
                    <IconFolder aria-hidden="true" />
                    <span>Projects</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel>Workers</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {Object.entries(workerBranches).map(([type, branch]) => {
                const Icon = workerIcons[type as keyof typeof workerIcons]
                const active =
                  pathname === branch.href ||
                  pathname.startsWith(`${branch.href}/`)
                return (
                  <SidebarMenuItem key={branch.href}>
                    <SidebarMenuButton isActive={active} asChild>
                      <Link
                        href={branch.href}
                        aria-current={active ? "page" : undefined}
                      >
                        <Icon aria-hidden="true" />
                        <span>{branch.label}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                )
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup className="mt-auto">
          <SidebarGroupLabel>Settings</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {settingsCategories.map((category) => {
                const active = category.sections.some(
                  (id) => pathname === `/settings/${id}`
                )
                const Icon =
                  category.id === "tasks"
                    ? IconListCheck
                    : category.id === "runtime"
                      ? IconServer2
                      : IconFileText
                return (
                  <SidebarMenuItem key={category.id}>
                    <SidebarMenuButton isActive={active} asChild>
                      <Link
                        href={`/settings/${category.sections[0]}`}
                        aria-current={active ? "page" : undefined}
                      >
                        <Icon aria-hidden="true" />
                        <span>{category.title}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                )
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild>
              <div aria-label="Aether account placeholder">
                <Avatar className="rounded-lg grayscale">
                  <AvatarFallback className="rounded-lg">AE</AvatarFallback>
                </Avatar>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-medium">Aether</span>
                </div>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
    </Sidebar>
  )
}
