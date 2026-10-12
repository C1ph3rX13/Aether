import { ApiError } from "@/lib/api"

/** Convert transport and command failures into actionable operator copy. */
export function commandError(cause: unknown, fallback: string) {
  if (cause instanceof ApiError) {
    if (cause.code === "retry_deferred") {
      return "The previous execution is still being cleaned up. Wait a moment, then retry."
    }
    if (cause.code === "projection_not_live") {
      return "Wait for Live Project data before retrying. Your input is kept."
    }
    if (
      cause.code === "stale_input" ||
      cause.code === "conflict" ||
      cause.status === 409
    ) {
      return "This Project changed elsewhere. Review the refreshed state and retry."
    }
    if (cause.status === 401) {
      return "Your session is no longer valid. Reload the page and try again."
    }
    if (cause.status === 403) {
      return "You do not have permission to perform this action."
    }
    if (cause.status === 404) {
      return "This Project is no longer available. Return to Projects and refresh."
    }
    if (cause.status === 429) {
      return "Too many requests. Wait a moment, then retry."
    }
    if (cause.status >= 500) {
      return cause.message.startsWith("Request ")
        ? "The Server could not complete this action. Retry in a moment."
        : cause.message
    }
  }

  if (cause instanceof TypeError) {
    return "The Server could not be reached. Check the connection and retry."
  }

  if (cause instanceof Error && cause.message) return cause.message
  return fallback
}
