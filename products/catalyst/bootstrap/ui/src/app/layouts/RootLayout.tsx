import { Outlet } from '@tanstack/react-router'
import { TooltipProvider } from '@/shared/ui/tooltip'

export function RootLayout() {
  return (
    <TooltipProvider>
      <Outlet />
    </TooltipProvider>
  )
}
