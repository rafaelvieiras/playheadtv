import { useAuthStore } from '@/store/auth'

// Thin fetch wrapper for the admin SPA. The SPA is served under /admin/ but
// the API lives at the root of the same origin/port (no CORS, no configurable
// base URL needed) — every call here uses an absolute path like "/channels".
export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const token = useAuthStore.getState().token

  const headers = new Headers(options.headers)
  if (options.body !== undefined) {
    headers.set('Content-Type', 'application/json')
  }
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }

  const res = await fetch(path, { ...options, headers })

  if (res.status === 401) {
    // Session expired or was revoked server-side: drop the stale token and
    // bounce to the login screen, from anywhere in the app.
    useAuthStore.getState().logout()
    if (window.location.pathname !== '/admin/login') {
      window.location.href = '/admin/login'
    }
    throw new ApiError(401, 'unauthorized')
  }

  if (res.status === 204) {
    return undefined as T
  }

  const contentType = res.headers.get('content-type') ?? ''
  const body = contentType.includes('application/json') ? await res.json() : undefined

  if (!res.ok) {
    const message = (body && typeof body === 'object' && 'error' in body && String(body.error)) || res.statusText || 'request failed'
    throw new ApiError(res.status, message)
  }

  return body as T
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, data?: unknown) =>
    request<T>(path, { method: 'POST', body: data !== undefined ? JSON.stringify(data) : undefined }),
  put: <T>(path: string, data?: unknown) =>
    request<T>(path, { method: 'PUT', body: data !== undefined ? JSON.stringify(data) : undefined }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
}
