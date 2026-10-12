import type { Fact } from "@/modules/workspace/model"

export function isCompletionFact(fact: Pick<Fact, "id" | "status">): boolean {
  return fact.status === "active" && fact.id !== "goal"
}

export function completionEvidenceIds(facts: Fact[], ids: string[]): string[] {
  const eligible = new Set(
    facts.filter(isCompletionFact).map((fact) => fact.id)
  )
  return [...new Set(ids)].filter((id) => eligible.has(id))
}
