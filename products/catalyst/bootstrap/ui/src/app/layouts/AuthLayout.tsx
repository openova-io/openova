import { Outlet } from '@tanstack/react-router'
import { motion } from 'framer-motion'
import { OOLogo } from '@/shared/ui/OOLogo'

export function AuthLayout() {
  return <Outlet />
}

export function AuthShell({ children }: { children: React.ReactNode }) {
  // Outer pinned to exactly viewport height (h-dvh, not min-h-dvh)
  // so the right column inherits a bounded height — that's what makes
  // overflow-y-auto on the column actually trigger column-scoped
  // scrolling instead of letting the document grow and scroll as a
  // whole. The card is then centered within the bounded column when
  // it fits and scrolls inside the column when it doesn't.
  // Caught live 2026-05-04: previous min-h-dvh let the outer grow
  // with content, so on small viewport heights (1366×650, mobile
  // landscape, browser with dev tools open) the card visually
  // overflowed the top of the viewport with no scrollable container
  // pinning it.
  return (
    <div className="h-dvh flex items-stretch overflow-hidden">
      {/* Left panel — branding */}
      <div className="hidden lg:flex lg:w-[420px] xl:w-[480px] shrink-0 flex-col justify-between bg-[--color-surface-1] border-r border-[--color-surface-border] p-10 overflow-y-auto">
        <div className="flex items-center gap-2.5">
          {/* Canonical OpenOva mark — see /brand/logo-mark.svg in openova-private */}
          <OOLogo h={22} id="auth-left-logo" />
          <span className="text-sm font-semibold text-[var(--color-text-strong)] tracking-tight">OpenOva <span className="text-[var(--color-text-dim)] font-normal">Sovereign</span></span>
        </div>

        <div className="flex flex-col gap-6">
          <motion.div
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.6, ease: [0.4, 0, 0.2, 1] }}
          >
            <p className="text-2xl font-semibold text-[var(--color-text-strong)] leading-snug text-balance">
              Enterprise Kubernetes.<br />
              Provisioned in minutes.
            </p>
            <p className="mt-3 text-sm text-[var(--color-text-dim)] leading-relaxed max-w-xs">
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
                <span className="text-sm text-[var(--color-text-dimmer)]">{label}</span>
              </div>
            ))}
          </div>
        </div>

        <p className="text-xs text-[var(--color-text-dimmer)]">
          © {new Date().getFullYear()} OpenOva · Platform Edition
        </p>
      </div>

      {/* Right panel — form */}
      {/* The column itself is the scroll container (overflow-y-auto)
          so when the card is taller than the viewport, scrolling
          stays scoped here instead of growing the whole page. The
          inner wrapper uses min-h-full + flex items-center to center
          when the card fits — when it doesn't, items-center degrades
          gracefully (browsers respect overflow start when content
          exceeds container) and py-8 keeps top/bottom breathing room. */}
      <div className="flex-1 overflow-y-auto bg-[--color-surface-0]">
        <div className="min-h-full flex items-center justify-center p-6 sm:p-8">
          <div className="w-full max-w-sm py-8">{children}</div>
        </div>
      </div>
    </div>
  )
}
