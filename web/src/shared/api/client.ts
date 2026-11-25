const BASE_PATH = '/api'

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

let unauthorizedHandler: (() => void) | null = null
const changeListeners = new Set<() => void>()

export function setUnauthorizedHandler(handler: (() => void) | null) {
  unauthorizedHandler = handler
}

// Runs after every request that changed something, so overviews like the sidebar can refresh.
export function onDataChange(listener: () => void): () => void {
  changeListeners.add(listener)
  return () => {
    changeListeners.delete(listener)
  }
}

export type RequestOptions = {
  method?: string
  body?: unknown
  rawBody?: Blob
  signal?: AbortSignal
  silentUnauthorized?: boolean
}

const OFFLINE_MESSAGE = "Can't reach the panel. Check your connection and try again."

export function fallbackMessage(status: number): string {
  if (status === 401) return 'Please sign in to continue.'
  if (status === 403) return "You're not allowed to do that."
  if (status === 404) return "That doesn't exist (anymore)."
  if (status === 429) return 'Too many attempts. Wait a moment and try again.'
  if (status === 413) return 'That file is too large.'
  if (status >= 500) return 'Something went wrong on the server. Try again in a moment.'
  return `The request didn't go through (status ${status}).`
}

async function readError(response: Response): Promise<ApiError> {
  let message = ''
  try {
    const data: unknown = await response.json()
    if (data && typeof data === 'object' && 'error' in data && typeof data.error === 'string') {
      message = data.error.trim()
    }
  } catch {
    // Body was empty or not JSON; fall back to a generic message.
  }
  return new ApiError(response.status, message || fallbackMessage(response.status))
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET'
  const headers: Record<string, string> = {}
  if (method !== 'GET') headers['X-Ownpanel-Request'] = '1'

  let body: BodyInit | undefined
  if (options.rawBody) {
    headers['Content-Type'] = 'application/octet-stream'
    body = options.rawBody
  } else if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(options.body)
  }

  let response: Response
  try {
    response = await fetch(BASE_PATH + path, {
      method,
      headers,
      credentials: 'same-origin',
      body,
      signal: options.signal,
    })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError(0, OFFLINE_MESSAGE)
  }

  if (method !== 'GET' && (response.ok || response.status === 502)) changeListeners.forEach((listener) => listener())
  if (!response.ok) {
    const error = await readError(response)
    if (error.status === 401 && !options.silentUnauthorized) unauthorizedHandler?.()
    throw error
  }
  if (response.status === 204) return undefined as T
  try {
    return (await response.json()) as T
  } catch {
    throw new ApiError(response.status, fallbackMessage(500))
  }
}

export function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401
}

export function isSavedButNotApplied(error: unknown): boolean {
  return error instanceof ApiError && error.status === 502
}

export function isNotFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  return 'Something went wrong. Try again.'
}
