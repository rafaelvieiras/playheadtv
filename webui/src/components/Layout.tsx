import {
  CalendarClock,
  Library,
  Languages,
  LogOut,
  Tv,
  Layers,
  ListOrdered,
  Radio,
  Server,
  Smartphone,
} from 'lucide-react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { api, ApiError } from '@/lib/http'
import { useAuthStore } from '@/store/auth'

const NAV_ITEMS = [
  { to: '/', label: 'Guide (EPG)', icon: CalendarClock, end: true },
  { to: '/channels', label: 'Channels', icon: Tv, end: false },
  { to: '/blocks', label: 'Blocks', icon: Layers, end: false },
  { to: '/collections', label: 'Collections', icon: Library, end: false },
  { to: '/filler-pools', label: 'Filler pools', icon: Radio, end: false },
  { to: '/filler-rules', label: 'Filler rules', icon: ListOrdered, end: false },
  { to: '/stream-selector', label: 'Stream selector', icon: Languages, end: false },
  { to: '/media-sources', label: 'Media sources', icon: Server, end: false },
  { to: '/devices', label: 'Devices', icon: Smartphone, end: false },
]

export function Layout() {
  const navigate = useNavigate()
  const logout = useAuthStore((s) => s.logout)
  const user = useAuthStore((s) => s.user)

  async function handleLogout() {
    try {
      await api.delete('/sessions')
    } catch (err) {
      // A 401 already cleared the store and redirected via lib/http.ts; any
      // other error shouldn't block the user from leaving the (now
      // presumably broken) session behind.
      if (!(err instanceof ApiError && err.status === 401)) {
        console.error('logout request failed', err)
      }
    } finally {
      logout()
      navigate('/login', { replace: true })
    }
  }

  return (
    <div className="flex min-h-svh bg-background text-foreground">
      <aside className="flex w-56 shrink-0 flex-col border-r border-border">
        <div className="px-4 py-4 text-sm font-semibold">Playhead TV — Admin</div>
        <nav className="flex flex-1 flex-col gap-1 px-2">
          {NAV_ITEMS.map(({ to, label, icon: Icon, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2 rounded-lg px-3 py-2 text-sm transition-colors',
                  isActive
                    ? 'bg-primary/10 font-medium text-primary'
                    : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                )
              }
            >
              <Icon className="size-4" />
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="flex flex-col gap-2 border-t border-border px-4 py-3">
          {user && <span className="truncate text-xs text-muted-foreground">{user.email}</span>}
          <Button variant="outline" size="sm" onClick={handleLogout}>
            <LogOut className="size-4" />
            Sign out
          </Button>
        </div>
      </aside>
      <main className="flex-1 overflow-y-auto px-6 py-8">
        <div className="mx-auto max-w-5xl">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
