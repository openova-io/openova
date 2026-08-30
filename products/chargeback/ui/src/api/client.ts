// Same-origin JSON client for /api/v1 (spec §4). The session is the
// HttpOnly `cb_session` cookie the server sets on PIN verify — every call
// sends it with `credentials: 'include'` and nothing is stored in the page.

export const API_BASE = '/api/v1'

export class ApiError extends Error {
  status: number
  body: unknown
  constructor(status: number, message: string, body: unknown) {
    super(message)
    this.status = status
    this.body = body
  }
}

function messageFrom(status: number, body: unknown): string {
  if (body && typeof body === 'object') {
    const b = body as Record<string, unknown>
    for (const k of ['error', 'message', 'detail']) {
      const v = b[k]
      if (typeof v === 'string' && v) return v
    }
  }
  if (typeof body === 'string' && body.trim()) return body.trim().slice(0, 300)
  return `HTTP ${status}`
}

async function parse(res: Response): Promise<unknown> {
  const text = await res.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

async function request<T>(method: string, path: string, body?: unknown, raw?: BodyInit): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  let payload: BodyInit | undefined = raw
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  const res = await fetch(API_BASE + path, {
    method,
    headers,
    body: payload,
    credentials: 'include',
  })
  const parsed = await parse(res)
  if (!res.ok) throw new ApiError(res.status, messageFrom(res.status, parsed), parsed)
  return parsed as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
  /** multipart upload (CSV imports). */
  upload: <T>(path: string, form: FormData) => request<T>('POST', path, undefined, form),
}

/**
 * Lane A may return a bare array or an envelope (`{items: [...]}` /
 * `{rows: [...]}` / `{<name>: [...]}`); the UI accepts both so the two
 * lanes can land in either order.
 */
export function asList<T>(v: unknown, ...keys: string[]): T[] {
  if (Array.isArray(v)) return v as T[]
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>
    for (const k of [...keys, 'items', 'rows', 'data']) {
      if (Array.isArray(o[k])) return o[k] as T[]
    }
  }
  return []
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}
