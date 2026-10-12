export const API_BASE = "/api/v1"

export class ApiError extends Error {
  readonly status: number
  readonly code?: string
  readonly validationProblems?: Array<{ path: string; message: string }>
  readonly currentRevision?: number

  constructor(
    message: string,
    status: number,
    payload?: {
      validation_problems?: unknown
      code?: unknown
      current_revision?: unknown
    }
  ) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = typeof payload?.code === "string" ? payload.code : undefined
    this.validationProblems = Array.isArray(payload?.validation_problems)
      ? payload.validation_problems.filter(isValidationProblem)
      : undefined
    this.currentRevision =
      typeof payload?.current_revision === "number"
        ? payload.current_revision
        : undefined
  }
}

export async function getJSON<T>(
  path: string,
  signal?: AbortSignal
): Promise<T> {
  return requestJSON<T>(path, { signal })
}

export async function deleteJSON<T>(path: string, body: unknown): Promise<T> {
  return requestJSON<T>(path, {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
}

export async function putJSON<T>(path: string, body: unknown): Promise<T> {
  return requestJSON<T>(path, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
}

export async function postJSON<T>(path: string, body: unknown): Promise<T> {
  return requestJSON<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetchStream(path, {
    ...init,
    headers: { Accept: "application/json", ...init?.headers },
  })

  if (!response.ok) {
    let payload: Record<string, unknown> | undefined
    try {
      const decoded: unknown = await response.json()
      if (decoded && typeof decoded === "object")
        payload = decoded as Record<string, unknown>
    } catch {
      // Some infrastructure failures have no JSON body.
    }
    const detail =
      typeof payload?.detail === "string" ? payload.detail : undefined
    throw new ApiError(
      detail ?? `Request ${path} failed with HTTP ${response.status}`,
      response.status,
      payload
    )
  }

  try {
    const result = (await response.json()) as T
    init?.signal?.throwIfAborted()
    return result
  } catch (error) {
    throw readError(error)
  }
}

function isValidationProblem(
  value: unknown
): value is { path: string; message: string } {
  if (!value || typeof value !== "object") return false
  const record = value as Record<string, unknown>
  return typeof record.path === "string" && typeof record.message === "string"
}

/** Keep the response body untouched so callers can consume streaming endpoints. */
export async function fetchStream(path: string, init?: RequestInit) {
  const read = !init?.method || init.method.toUpperCase() === "GET"
  const timeout = read ? AbortSignal.timeout(30_000) : null
  const signal = timeout
    ? init?.signal
      ? AbortSignal.any([init.signal, timeout])
      : timeout
    : init?.signal
  try {
    return await fetch(`${API_BASE}${path}`, { ...init, signal })
  } catch (error) {
    throw readError(error)
  }
}

function readError(error: unknown) {
  return error instanceof DOMException && error.name === "TimeoutError"
    ? new Error(
        "Aether took too long to respond. Check your connection and retry."
      )
    : error
}

export async function getText(path: string): Promise<string> {
  const response = await fetchStream(path, {
    headers: { Accept: "text/plain, application/json" },
  })
  if (!response.ok) {
    let payload: Record<string, unknown> | undefined
    try {
      const decoded: unknown = await response.json()
      if (decoded && typeof decoded === "object")
        payload = decoded as Record<string, unknown>
    } catch {
      // Infrastructure failures may not include a JSON envelope.
    }
    const detail =
      typeof payload?.detail === "string" ? payload.detail : undefined
    throw new ApiError(
      detail ?? `Request ${path} failed with HTTP ${response.status}`,
      response.status,
      payload
    )
  }
  try {
    return await response.text()
  } catch (error) {
    throw readError(error)
  }
}
