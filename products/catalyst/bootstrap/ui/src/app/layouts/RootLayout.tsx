import { Outlet } from '@tanstack/react-router'
import { TooltipProvider } from '@/shared/ui/tooltip'
import { NotificationProvider } from '@/shared/ui/notifications'

export function RootLayout() {
  return (
    <TooltipProvider>
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    </TooltipProvider>
  )
}
