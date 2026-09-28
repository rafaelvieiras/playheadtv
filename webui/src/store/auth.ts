import { create } from 'zustand'
import { persist } from 'zustand/middleware'

import type { User } from '@/lib/types'

interface AuthState {
  token: string | null
  user: User | null
  login: (token: string, user: User) => void
  logout: () => void
}

// Persisted to localStorage: an opaque session token, same trust model the
// backend already uses for device tokens (see AGENTS.md / auth.go). This is
// a self-hosted admin tool, not a bank — that's an accepted tradeoff here.
export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      user: null,
      login: (token, user) => set({ token, user }),
      logout: () => set({ token: null, user: null }),
    }),
    { name: 'playheadtv-auth' }
  )
)
