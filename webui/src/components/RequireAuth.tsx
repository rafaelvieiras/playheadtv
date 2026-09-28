import type { ReactElement } from 'react'
import { Navigate, useLocation } from 'react-router-dom'

import { useAuthStore } from '@/store/auth'

// Route guard: redirects to /admin/login when there's no session token.
// Mirrors the 401 handling in lib/http.ts, but catches the "never logged in"
// case before any request is even made.
export function RequireAuth({ children }: { children: ReactElement }) {
  const token = useAuthStore((s) => s.token)
  const location = useLocation()

  if (!token) {
    return <Navigate to="/login" replace state={{ from: location }} />
  }

  return children
}
