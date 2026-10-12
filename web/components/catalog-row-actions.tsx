"use client"
import {
  IconDots as Ellipsis,
  IconArrowRight,
  IconDownload as Download,
  IconPencil as Pencil,
  IconTrash as Trash2,
  IconPlayerPause as Pause,
  IconPlayerPlay as Play,
  IconRotate as RotateCcw,
} from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import type { CatalogProject } from "@/lib/project-catalog"
import Link from "next/link"
import { useRef } from "react"
export type CatalogAction =
  "rename" | "delete" | "reopen" | "pause" | "resume" | "export"
export function CatalogRowActions({
  project,
  disabled,
  onAction,
}: {
  project: CatalogProject
  disabled: boolean
  onAction: (action: CatalogAction, trigger: HTMLButtonElement | null) => void
}) {
  const lifecycle =
    project.status === "active"
      ? { action: "pause", label: "Pause", Icon: Pause }
      : project.status === "paused"
        ? { action: "resume", label: "Resume", Icon: Play }
        : { action: "reopen", label: "Reopen", Icon: RotateCcw }
  const trigger = useRef<HTMLButtonElement | null>(null)
  const openingDialog = useRef(false)
  const selectAction = (action: CatalogAction) => {
    openingDialog.current = true
    onAction(action, trigger.current)
  }
  return (
    <DropdownMenu
      onOpenChange={(open) => {
        if (open) openingDialog.current = false
      }}
    >
      <DropdownMenuTrigger asChild>
        <Button
          id={`project-actions-${project.id}`}
          ref={trigger}
          variant="ghost"
          size="icon"
          aria-label={`Actions for ${project.title}`}
        >
          <Ellipsis aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        onCloseAutoFocus={(event) => {
          if (openingDialog.current) event.preventDefault()
        }}
      >
        <DropdownMenuGroup>
          <DropdownMenuItem asChild>
            <Link href={`/projects/${encodeURIComponent(project.id)}`}>
              <IconArrowRight aria-hidden="true" />
              Open Project
            </Link>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => selectAction("export")}>
            <Download aria-hidden="true" />
            Export Project
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={disabled}
            onSelect={() => selectAction("rename")}
          >
            <Pencil aria-hidden="true" />
            Rename
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={disabled}
            onSelect={() => selectAction(lifecycle.action as CatalogAction)}
          >
            <lifecycle.Icon aria-hidden="true" />
            {lifecycle.label}
          </DropdownMenuItem>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem
            variant="destructive"
            disabled={disabled}
            onSelect={() => selectAction("delete")}
          >
            <Trash2 aria-hidden="true" />
            Delete Project
          </DropdownMenuItem>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
