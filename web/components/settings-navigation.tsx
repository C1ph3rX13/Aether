"use client"

import { useRef, type ReactNode } from "react"
import { useNavigationActions } from "@/components/navigation-guard"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  settingsCategory,
  settingsTabLabels,
  type SettingsSectionID,
} from "@/lib/settings-sections"

export function SettingsNavigation({
  section,
  children,
}: {
  section: SettingsSectionID
  children: ReactNode
}) {
  const category = settingsCategory(section)
  const { requestNavigation, busy } = useNavigationActions()
  const triggers = useRef<Record<string, HTMLButtonElement | null>>({})
  if (category.sections.length === 1) return children
  return (
    <Tabs
      value={section}
      onValueChange={(value) =>
        requestNavigation(`/settings/${value}`, triggers.current[value])
      }
      activationMode="manual"
      orientation="vertical"
      className="min-w-0 flex-col gap-8 @min-[48rem]/page:flex-row"
    >
      <div className="shrink-0 @min-[48rem]/page:w-48">
        <TabsList aria-label={`${category.title} settings`} className="w-full">
          {category.sections.map((id) => (
            <TabsTrigger
              key={id}
              value={id}
              ref={(element) => {
                triggers.current[id] = element
              }}
              disabled={busy}
              onKeyDown={(event) => {
                if (event.key !== "Enter" && event.key !== " ") return
                // Opening the guard moves focus; suppress the key's native click
                // so its keyup cannot activate the Dialog's focused action.
                event.preventDefault()
                if (id !== section)
                  requestNavigation(`/settings/${id}`, event.currentTarget)
              }}
            >
              {settingsTabLabels[id]}
            </TabsTrigger>
          ))}
        </TabsList>
      </div>
      {category.sections.map((id) => (
        <TabsContent key={id} value={id} className="min-w-0 flex-1">
          {id === section ? children : null}
        </TabsContent>
      ))}
    </Tabs>
  )
}
