import { ProjectWorkspace } from "@/components/project-workspace"

export function generateStaticParams() {
  return [{ id: "__placeholder__" }]
}

export default function ProjectPage() {
  return <ProjectWorkspace />
}
