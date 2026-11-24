import { useState } from 'react'
import {
  CalendarClock,
  Clapperboard,
  Library,
  Languages,
  LogOut,
  Menu,
  Puzzle,
  Radio,
  Scissors,
  Server,
  Smartphone,
  Tv,
  Layers,
  ListOrdered,
} from 'lucide-react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { cn } from '@/lib/utils'
import { api, ApiError } from '@/lib/http'
import { useAuthStore } from '@/store/auth'

// NAV_ITEMS is the only list of destinations: the `md:` sidebar and the
// mobile drawer both render it, so the two can never disagree.
const NAV_ITEMS = [
  { to: '/', label: 'Guide (EPG)', icon: CalendarClock, end: true },
  { to: '/channels', label: 'Channels', icon: Tv, end: false },
  { to: '/blocks', label: 'Blocks', icon: Layers, end: false },
  { to: '/collections', label: 'Collections', icon: Library, end: false },
  { to: '/filler-pools', label: 'Filler pools', icon: Radio, end: false },
  { to: '/filler-rules', label: 'Filler rules', icon: ListOrdered, end: false },
  { to: '/break-templates', label: 'Break templates', icon: Clapperboard, end: false },
  { to: '/cut-rule-sets', label: 'Cut rules', icon: Scissors, end: false },
  { to: '/stream-selector', label: 'Stream selector', icon: Languages, end: false },
  { to: '/media-sources', label: 'Media sources', icon: Server, end: false },
  { to: '/devices', label: 'Devices', icon: Smartphone, end: false },
  { to: '/plugins', label: 'Plugins', icon: Puzzle, end: false },
]

function SidebarNav({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav className="flex flex-1 flex-col gap-1 overflow-y-auto px-2">
      {NAV_ITEMS.map(({ to, label, icon: Icon, end }) => (
        <NavLink
          key={to}
          to={to}
          end={end}
          onClick={onNavigate}
          className={({ isActive }) =>
            cn(
              'flex min-h-11 items-center gap-2 rounded-lg px-3 py-2 text-sm transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 md:min-h-9',
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
  )
}

function SidebarFooter({ onSignOut }: { onSignOut: () => void }) {
  const user = useAuthStore((s) => s.user)
  return (
    <div className="flex flex-col gap-2 border-t border-border px-4 py-3">
      {user && <span className="truncate text-xs text-muted-foreground">{user.email}</span>}
      <Button variant="outline" size="sm" onClick={onSignOut}>
        <LogOut className="size-4" />
        Sign out
      </Button>
    </div>
  )
}

export function Layout() {
  const navigate = useNavigate()
  const logout = useAuthStore((s) => s.logout)
  const [navOpen, setNavOpen] = useState(false)

  async function handleLogout() {
    setNavOpen(false)
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
    // h-svh + a scrolling `main`: the shell owns the viewport, so a long page
    // scrolls its own column and the sidebar never scrolls away with it.
    <div className="flex h-svh bg-background text-foreground">
      <aside className="hidden w-56 shrink-0 flex-col border-r border-border md:flex">
        <div className="px-4 py-4 text-sm font-semibold">Playhead TV — Admin</div>
        <SidebarNav />
        <SidebarFooter onSignOut={handleLogout} />
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-2 border-b border-border px-2 py-1.5 md:hidden">
          <Sheet open={navOpen} onOpenChange={setNavOpen}>
            <SheetTrigger asChild>
              <Button variant="ghost" size="icon" aria-label="Open navigation">
                <Menu className="size-4" />
              </Button>
            </SheetTrigger>
            <SheetContent side="left" className="p-0">
              <SheetHeader className="border-b border-border pr-12">
                <SheetTitle>Playhead TV — Admin</SheetTitle>
              </SheetHeader>
              <div className="flex flex-1 flex-col overflow-y-auto py-2">
                <SidebarNav onNavigate={() => setNavOpen(false)} />
              </div>
              <SidebarFooter onSignOut={handleLogout} />
            </SheetContent>
          </Sheet>
          <span className="truncate text-sm font-semibold">Playhead TV — Admin</span>
        </header>

        <main className="min-w-0 flex-1 overflow-y-auto px-4 py-6 sm:px-6 sm:py-8">
          <div className="mx-auto max-w-5xl">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
