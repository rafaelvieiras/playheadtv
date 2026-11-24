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

// The SPA is served under /admin/ (vite base, router basename), so a full page
// navigation has to spell that out while the router works in paths below it.
const ADMIN_BASE = '/admin'
const LOGIN_PATH = '/login'

// A 401 is handled with a full page load (this module lives outside the
// component tree, so there is no router to navigate), which means the two
// things the login screen still has to know — why the session ended and which
// URL was asked for — travel in its query string. pages/LoginPage.tsx reads
// both back.
export const SESSION_ENDED_PARAM = 'session'
export const NEXT_PARAM = 'next'

// A fetch that never reached the server rejects with a TypeError ("Failed to
// fetch", "Load failed", "NetworkError when attempting to fetch resource.").
// That is not the server refusing anything, so nothing here may present it as
// a wrong password or repeat the browser's phrase to an operator.
export function isNetworkFailure(error: unknown): boolean {
  return error instanceof TypeError
}

/** Why a request failed, in words someone can act on. */
export function requestFailureReason(error: unknown): string {
  if (isNetworkFailure(error)) {
    return 'Could not reach the server. Check that it is running and that the browser is online, then try again.'
  }
  if (error instanceof ApiError) {
    return error.message
  }
  if (error instanceof Error && error.message.trim() !== '') {
    return error.message
  }
  return 'The request failed for an unknown reason.'
}

/**
 * The login screen's URL, carrying why we left it and where to come back to.
 * `next` is a router path (below /admin); the guide is the default, so it is
 * not worth carrying.
 */
export function loginUrl({ next, sessionEnded }: { next?: string | null; sessionEnded?: boolean } = {}): string {
  const params = new URLSearchParams()
  if (sessionEnded) params.set(SESSION_ENDED_PARAM, 'expired')
  if (next && next !== '/') params.set(NEXT_PARAM, next)
  const query = params.toString()
  return `${ADMIN_BASE}${LOGIN_PATH}${query ? `?${query}` : ''}`
}

/**
 * A path below /admin that is safe to send the browser back to, or null.
 * Accepts both a router path ("/channels") and a full pathname
 * ("/admin/channels?tab=1"), because `state.from` is basename-less while
 * `window.location` is not. Anything that would leave this origin ("//host", a
 * scheme, a backslash trick, a control character) and the login screen itself
 * — which would only bounce — are refused.
 */
export function safeRouterPath(value: string | null | undefined): string | null {
  if (!value) return null
  let path = value.trim()
  if (path === ADMIN_BASE) {
    path = '/'
  } else if (path.startsWith(`${ADMIN_BASE}/`)) {
    path = path.slice(ADMIN_BASE.length)
  }
  if (!path.startsWith('/') || path.startsWith('//')) return null
  if (path.includes('\\')) return null
  // No control character may reach the address bar (a newline is stripped by
  // some browsers, the rest has no business in a path).
  for (const char of path) {
    if (char.charCodeAt(0) < 0x20 || char.charCodeAt(0) === 0x7f) return null
  }
  const [pathname] = path.split(/[?#]/)
  if (pathname === LOGIN_PATH) return null
  return path
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
    // Session expired or was revoked server-side: drop the stale token (this
    // is the only place in the SPA that does) and bounce to the login screen,
    // from anywhere in the app. The destination and the reason ride in the
    // login URL because this is a full page load; `replace` keeps the dead
    // screen out of history, so Back does not land on it and fail again.
    const sessionEnded = token !== null
    useAuthStore.getState().logout()
    if (window.location.pathname !== `${ADMIN_BASE}${LOGIN_PATH}`) {
      const here = `${window.location.pathname}${window.location.search}`
      window.location.replace(loginUrl({ next: safeRouterPath(here), sessionEnded }))
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
