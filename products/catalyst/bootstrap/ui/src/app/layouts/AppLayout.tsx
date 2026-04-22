import { Outlet, Link, useRouterState } from '@tanstack/react-router'
import { LayoutDashboard, Plus, Settings, LogOut, ChevronDown } from 'lucide-react'
import { OOLogo } from '@/shared/ui/OOLogo'
import { cn } from '@/shared/lib/utils'
import { Avatar, AvatarFallback } from '@/shared/ui/avatar'
import { Button } from '@/shared/ui/button'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator, DropdownMenuLabel,
} from '@/shared/ui/dropdown-menu'

const NAV_ITEMS = [
  { to: '/app/dashboard', icon: LayoutDashboard, label: 'Deployments' },
  { to: '/app/settings', icon: Settings, label: 'Settings' },
]

export function AppLayout() {
  const { location } = useRouterState()

  return (
    <div className="flex h-dvh bg-[--color-surface-0]">
      {/* Sidebar */}
      <aside className="flex w-56 shrink-0 flex-col border-r border-[--color-surface-border] bg-[--color-surface-1]">
        {/* Canonical OpenOva mark — see /brand/logo-mark.svg in openova-private */}
        <div className="flex h-14 items-center gap-2.5 px-4 border-b border-[--color-surface-border]">
          <OOLogo h={20} id="app-sidebar-logo" />
          <span className="text-sm font-semibold text-[oklch(92%_0.01_250)] tracking-tight">OpenOva <span className="text-[oklch(60%_0.01_250)] font-normal">Sovereign</span></span>
        </div>

        {/* New deployment CTA */}
        <div className="p-3">
          <Link to="/wizard">
            <Button variant="primary" size="md" className="w-full justify-start gap-2">
              <Plus className="h-4 w-4" />
              New deployment
            </Button>
          </Link>
        </div>

        {/* Navigation */}
        <nav className="flex-1 flex flex-col gap-0.5 px-2 py-1">
          {NAV_ITEMS.map(({ to, icon: Icon, label }) => {
            const active = location.pathname.startsWith(to)
            return (
              <Link
                key={to}
                to={to}
                className={cn(
                  'flex items-center gap-2.5 rounded-[--radius-md] px-2.5 py-2 text-sm font-medium transition-colors duration-150',
                  active
                    ? 'bg-[--color-brand-500]/12 text-[--color-brand-300]'
                    : 'text-[oklch(55%_0.01_250)] hover:bg-[--color-surface-2] hover:text-[oklch(85%_0.01_250)]'
                )}
              >
                <Icon className="h-4 w-4 shrink-0" />
                {label}
              </Link>
            )
          })}
        </nav>

        {/* User menu */}
        <div className="border-t border-[--color-surface-border] p-3">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button className="flex w-full items-center gap-2.5 rounded-[--radius-md] px-2 py-2 text-sm hover:bg-[--color-surface-2] transition-colors">
                <Avatar className="h-7 w-7">
                  <AvatarFallback>EB</AvatarFallback>
                </Avatar>
                <div className="flex-1 text-left min-w-0">
                  <p className="text-xs font-medium text-[oklch(85%_0.01_250)] truncate">Emrah Baysal</p>
                  <p className="text-xs text-[oklch(45%_0.01_250)] truncate">openova.io</p>
                </div>
                <ChevronDown className="h-3.5 w-3.5 text-[oklch(45%_0.01_250)] shrink-0" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent side="top" align="start" className="w-48">
              <DropdownMenuLabel>Account</DropdownMenuLabel>
              <DropdownMenuItem>
                <Settings className="h-3.5 w-3.5" />
                Settings
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem className="text-[--color-error] focus:text-[--color-error]">
                <LogOut className="h-3.5 w-3.5" />
                Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  )
}
