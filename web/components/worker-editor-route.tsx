"use client"

import { Suspense, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { WorkerEditor } from "@/components/worker-editor"
import { workerBranches, type AgentType } from "@/lib/worker-catalog"

export function WorkerEditorRoute({ agentType }: { agentType: AgentType }) {
  return (
    <Suspense
      fallback={
        <p role="status" className="p-6">
          Loading Worker...
        </p>
      }
    >
      <SelectedWorkerEditor agentType={agentType} />
    </Suspense>
  )
}

function SelectedWorkerEditor({ agentType }: { agentType: AgentType }) {
  const params = useSearchParams()
  const router = useRouter()
  const workerName = params.get("worker") ?? undefined
  const copy = params.get("copy") === "1"
  const [savedName, setSavedName] = useState<string | null>(null)
  return (
    <WorkerEditor
      key={`${agentType}:${workerName ?? ""}:${copy}`}
      agentType={agentType}
      workerName={workerName}
      copy={copy}
      saved={savedName === workerName && !copy}
      onSaved={(name) => {
        setSavedName(name)
        router.replace(
          `${workerBranches[agentType].href}/editor?worker=${encodeURIComponent(name)}`
        )
      }}
    />
  )
}
