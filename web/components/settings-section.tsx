"use client"
import { startTransition, useEffect, useState, type ReactNode } from "react"
import { IconChevronDown as ChevronDown } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
export function SettingsSection({
  title,
  children,
  description,
  defaultOpen = false,
  forceOpen = false,
}: {
  title: string
  children: ReactNode
  description?: ReactNode
  defaultOpen?: boolean
  forceOpen?: boolean
}) {
  const id = title.replaceAll(" ", "-").toLowerCase()
  const [open, setOpen] = useState(defaultOpen)
  useEffect(() => {
    if (forceOpen) startTransition(() => setOpen(true))
  }, [forceOpen])
  const visible = forceOpen || open
  return (
    <Collapsible open={visible} onOpenChange={setOpen}>
      <Card
        className="scroll-mt-24"
        id={id}
        role="region"
        aria-labelledby={`${id}-title`}
      >
        <CardHeader className="flex flex-wrap items-start justify-between gap-3 @min-[32rem]/page:flex-row">
          <div className="min-w-0">
            <CardTitle>
              <h2 id={`${id}-title`}>{title}</h2>
              {forceOpen ? (
                <p className="mt-1 text-xs font-normal text-muted-foreground">
                  Resolve validation errors to hide these fields.
                </p>
              ) : null}
            </CardTitle>
            {description ? (
              <CardDescription className="mt-1 max-w-[70ch] leading-relaxed">
                {description}
              </CardDescription>
            ) : null}
          </div>
          <CollapsibleTrigger asChild>
            <Button
              variant="ghost"
              size="default"
              className="shrink-0"
              disabled={forceOpen}
              aria-expanded={visible}
              aria-controls={`${id}-content`}
            >
              {visible ? "Hide fields" : "Show fields"}
              <ChevronDown
                data-icon="inline-end"
                className={`motion-safe:transition-transform ${visible ? "rotate-180" : ""}`}
                aria-hidden="true"
              />
            </Button>
          </CollapsibleTrigger>
        </CardHeader>
        <CollapsibleContent id={`${id}-content`} forceMount hidden={!visible}>
          <CardContent>{children}</CardContent>
        </CollapsibleContent>
      </Card>
    </Collapsible>
  )
}
