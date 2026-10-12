"use client"
import { useState } from "react"
import { IconChevronRight } from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { Input } from "@/components/ui/input"
import { Field, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { StatusBadge } from "@/components/status-badge"
import { getFactType, type ProjectDetail } from "@/modules/workspace/model"
import type { RecordReference } from "@/modules/workspace/contract"
import { formatLabel } from "@/lib/format"
export function WorkspaceRecords({
  detail,
  onInspect,
  evidenceIds,
  onEvidenceChange,
  evidenceDisabled,
}: {
  detail: ProjectDetail
  onInspect: (reference: RecordReference, element?: HTMLElement) => void
  evidenceIds: string[]
  onEvidenceChange: (ids: string[]) => void
  evidenceDisabled: boolean
}) {
  const [filter, setFilter] = useState("all")
  const [search, setSearch] = useState("")
  type Record =
    | (ProjectDetail["facts"][number] & { type: "fact"; role: string })
    | (ProjectDetail["intents"][number] & { type: "intent" })
  const groups: Array<[string, Record[]]> = [
    [
      "Facts",
      detail.facts.map((r) => ({
        ...r,
        type: "fact" as const,
        role: getFactType(r.id),
      })),
    ],
    [
      "Bootstrap",
      detail.intents
        .filter((r) => r.kind === "bootstrap")
        .map((r) => ({ ...r, type: "intent" as const })),
    ],
    [
      "Explore",
      detail.intents
        .filter((r) => r.kind === "explore")
        .map((r) => ({ ...r, type: "intent" as const })),
    ],
    [
      "Completion",
      detail.intents
        .filter((r) => r.kind === "completion")
        .map((r) => ({ ...r, type: "intent" as const })),
    ],
  ]
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-wrap items-end gap-3 p-4">
        <Field className="flex-1">
          <FieldLabel className="sr-only" htmlFor="records-search">
            Search Records
          </FieldLabel>
          <Input
            id="records-search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search full record content…"
          />
        </Field>
        <Select value={filter} onValueChange={setFilter}>
          <SelectTrigger aria-label="Record kind">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {["all", "Facts", "Bootstrap", "Explore", "Completion"].map(
                (v) => (
                  <SelectItem key={v} value={v}>
                    {v === "all" ? "All records" : v}
                  </SelectItem>
                )
              )}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      <Separator />
      <div className="flex-1 overflow-y-auto p-4">
        <div className="flex flex-col gap-8">
          {groups
            .filter(([name]) => filter === "all" || name === filter)
            .map(([name, records]) => {
              const matches = records.filter((r) =>
                `${r.id} ${r.description}`
                  .toLowerCase()
                  .includes(search.toLowerCase())
              )
              return (
                <section key={name} className="flex flex-col gap-3">
                  <h3 className="flex items-center gap-2 text-base font-semibold">
                    {name}
                    <Badge variant="secondary">{matches.length}</Badge>
                  </h3>
                  {matches.length ? (
                    matches.map((r) => (
                      <div key={r.id} className="flex flex-col gap-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <Button
                            size="default"
                            variant="ghost"
                            aria-label={`Open ${formatLabel(r.type)} ${r.id}`}
                            onClick={(event) =>
                              onInspect(
                                { type: r.type, id: r.id },
                                event.currentTarget
                              )
                            }
                          >
                            {r.type === "fact"
                              ? formatLabel(r.role)
                              : formatLabel(r.kind)}{" "}
                            · {r.id}
                            <IconChevronRight data-icon="inline-end" />
                          </Button>
                          <StatusBadge value={r.status} />
                        </div>
                        <p className="text-sm leading-relaxed wrap-anywhere whitespace-pre-wrap">
                          {r.description}
                        </p>
                        {r.type === "intent" && (
                          <p className="text-xs text-muted-foreground">
                            From {r.from.join(", ")}
                            {r.to ? ` → ${r.to}` : ""} · {r.creator}
                          </p>
                        )}
                        {r.type === "fact" &&
                          r.status === "active" &&
                          r.id !== "goal" && (
                            <Label>
                              <Checkbox
                                aria-label={`Records evidence ${r.id}`}
                                checked={evidenceIds.includes(r.id)}
                                disabled={evidenceDisabled}
                                onCheckedChange={(yes) =>
                                  onEvidenceChange(
                                    yes === true
                                      ? [...evidenceIds, r.id]
                                      : evidenceIds.filter((id) => id !== r.id)
                                  )
                                }
                              />
                              Completion Evidence
                            </Label>
                          )}
                        <Separator />
                      </div>
                    ))
                  ) : (
                    <p className="text-sm text-muted-foreground">
                      No matching {name.toLowerCase()} records.
                    </p>
                  )}
                </section>
              )
            })}
        </div>
      </div>
    </div>
  )
}
