"use client"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import type { ValidationProblem } from "@/lib/configuration"
import { settingsSections, sectionOwnsProblem } from "@/lib/settings-sections"
import { projectProblemLabel } from "@/lib/project-settings-form"
import { systemProblemLabel } from "@/lib/system-settings-form"
function recoveryGroups(problems: ValidationProblem[]) {
  const groups = [
    ...settingsSections.map((section) => ({
      title: section.title as string,
      href: `/settings/${section.id}`,
      problems: [] as string[],
      id: section.id,
    })),
    {
      title: "Workers",
      href: "/workers/codex",
      problems: [] as string[],
      id: "workers" as const,
    },
  ]
  for (const problem of problems) {
    if (problem.path.startsWith("workers")) {
      const task =
        /Worker coverage is missing for (bootstrap|reason|explore)$/.exec(
          problem.message
        )?.[1]
      groups[6].problems.push(
        task
          ? `Add a saved Worker that supports ${task[0].toUpperCase()}${task.slice(1)}.`
          : "Check Worker fields, tasks and credentials."
      )
      continue
    }
    const owners = groups.filter(
      (group) =>
        group.id !== "workers" && sectionOwnsProblem(group.id, problem.path)
    )
    const label = problem.path.startsWith("runtime")
      ? systemProblemLabel(problem.path)
      : projectProblemLabel(problem.path)
    for (const group of owners.length ? owners : [groups[3]])
      group.problems.push(`${label} needs attention.`)
  }
  return groups.map((group) => ({
    ...group,
    problems: [...new Set(group.problems)],
  }))
}
export function ConfigurationProblems({
  problems,
}: {
  problems: ValidationProblem[]
}) {
  const groups = recoveryGroups(problems).filter(
    (group) => group.problems.length
  )
  if (!groups.length) return null
  return (
    <div className="grid gap-3 @min-[48rem]/page:grid-cols-2">
      {groups.map((group) => (
        <Alert key={group.title} aria-label={`${group.title} recovery`}>
          <AlertTitle>
            {group.title === "Workers"
              ? "Project creation unavailable"
              : group.title}
          </AlertTitle>
          <AlertDescription>
            <ul>
              {group.problems.map((message) => (
                <li key={message}>{message}</li>
              ))}
            </ul>
          </AlertDescription>
          <Button className="mt-3" variant="outline" asChild>
            <Link href={group.href}>
              Open section<span className="sr-only">: {group.title}</span>
            </Link>
          </Button>
        </Alert>
      ))}
    </div>
  )
}
