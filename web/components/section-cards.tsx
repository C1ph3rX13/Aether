"use client"

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import type { CatalogProject } from "@/lib/project-catalog"
import { formatCount } from "@/lib/format"

export function SectionCards({ projects }: { projects: CatalogProject[] }) {
  const counts = [
    ["Total Projects", projects.length],
    [
      "Active",
      projects.filter((project) => project.status === "active").length,
    ],
    [
      "Paused",
      projects.filter((project) => project.status === "paused").length,
    ],
    [
      "Completed",
      projects.filter((project) => project.status === "completed").length,
    ],
  ] as const

  return (
    <div
      role="group"
      aria-label="Project lifecycle statistics"
      className="grid grid-cols-1 gap-4 px-4 *:data-[slot=card]:bg-linear-to-t *:data-[slot=card]:from-primary/5 *:data-[slot=card]:to-card *:data-[slot=card]:shadow-xs lg:px-6 @min-[20rem]/main:grid-cols-2 @5xl/main:grid-cols-4"
    >
      {counts.map(([label, count]) => (
        <Card
          key={label}
          className="@container/card"
          aria-label={`${label}: ${formatCount(count)}`}
        >
          <CardHeader>
            <CardDescription>{label}</CardDescription>
            <CardTitle className="text-2xl font-semibold tabular-nums @[250px]/card:text-3xl">
              {formatCount(count)}
            </CardTitle>
          </CardHeader>
        </Card>
      ))}
    </div>
  )
}
