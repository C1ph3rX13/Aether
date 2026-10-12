import { ApiError, getJSON } from "@/lib/api"
import { normalizeDynamicDraft, type DynamicDraft } from "@/lib/configuration"

/** Revision identity and Secret references stay outside the presentation model. */
export async function readAppliedSettings(
  signal?: AbortSignal
): Promise<DynamicDraft | null> {
  try {
    const response = await getJSON<{ configuration: DynamicDraft }>(
      "/configuration/revisions/active",
      signal
    )
    const draft = response?.configuration
    if (
      !draft?.runtime ||
      !draft.tasks?.bootstrap ||
      !draft.tasks.reason ||
      !draft.tasks.explore ||
      !draft.retry?.bootstrap ||
      !draft.retry.reason ||
      !draft.retry.explore ||
      !draft.container ||
      !(draft.workers === null || Array.isArray(draft.workers))
    ) {
      throw new Error("Saved settings are unreadable")
    }
    try {
      return normalizeDynamicDraft(draft)
    } catch {
      throw new Error("Saved settings are unreadable")
    }
  } catch (error) {
    if (
      error instanceof ApiError &&
      error.status === 404 &&
      error.code === "not_found"
    )
      return null
    throw error
  }
}
