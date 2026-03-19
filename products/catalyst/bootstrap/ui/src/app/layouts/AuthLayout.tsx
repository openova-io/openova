import { Outlet } from '@tanstack/react-router'
import { motion } from 'framer-motion'
import { OctagonAlert } from 'lucide-react'

export function AuthLayout() {
  return <Outlet />
}

export function AuthShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-dvh flex">
      {/* Left panel — branding */}
      <div className="hidden lg:flex lg:w-[420px] xl:w-[480px] shrink-0 flex-col justify-between bg-[--color-surface-1] border-r border-[--color-surface-border] p-10">
        <div className="flex items-center gap-2.5">
          <div className="flex h-8 w-8 items-center justify-center rounded-[--radius-md] bg-[--color-brand-500]">
            <OctagonAlert className="h-4.5 w-4.5 text-white" />
          </div>
          <span className="text-sm font-semibold text-[oklch(92%_0.01_250)] tracking-tight">OpenOva Catalyst</span>
        </div>

        <div className="flex flex-col gap-6">
          <motion.div
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.6, ease: [0.4, 0, 0.2, 1] }}
          >
            <p className="text-2xl font-semibold text-[oklch(92%_0.01_250)] leading-snug text-balance">
              Enterprise Kubernetes.<br />
              Provisioned in minutes.
            </p>
            <p className="mt-3 text-sm text-[oklch(50%_0.01_250)] leading-relaxed max-w-xs">
              52 open-source components. AI-native operations. Multi-region out of the box.
              Production-grade from day one.
            </p>
          </motion.div>

          <div className="flex flex-col gap-3">
            {[
              { stat: '52', label: 'platform components' },
              { stat: '6', label: 'cloud regions on Hetzner' },
              { stat: '<5 min', label: 'to a running cluster' },
            ].map(({ stat, label }) => (
              <div key={label} className="flex items-center gap-3">
                <span className="text-lg font-bold text-[--color-brand-400] tabular-nums">{stat}</span>
                <span className="text-sm text-[oklch(45%_0.01_250)]">{label}</span>
              </div>
            ))}
          </div>
        </div>

        <p className="text-xs text-[oklch(35%_0.01_250)]">
          © {new Date().getFullYear()} OpenOva · Platform Edition
        </p>
      </div>

      {/* Right panel — form */}
      <div className="flex flex-1 items-center justify-center p-6 bg-[--color-surface-0]">
        <div className="w-full max-w-sm">{children}</div>
      </div>
    </div>
  )
}
