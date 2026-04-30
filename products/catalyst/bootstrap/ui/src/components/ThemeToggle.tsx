/**
 * ThemeToggle — sun / moon icon button that flips between dark and
 * light themes on the global `<html data-theme>` attribute.
 *
 * Persistence contract (kept in sync with index.html bootstrap script
 * and src/shared/lib/useTheme.ts):
 *   • localStorage key: 'oo-theme'
 *   • valid values:     'dark' | 'light'
 *   • default:          'dark' (matches the dark-first console
 *                       palette in src/app/globals.css and the
 *                       inline bootstrap script in index.html that
 *                       sets [data-theme] BEFORE first paint to
 *                       avoid a FOUC flash)
 *
 * The component is intentionally chrome-agnostic — it carries no
 * positioning of its own, so callers (PortalShell header, WizardLayout
 * header) decide where it sits. Keeps it usable in both the wizard's
 * `corp-icon-btn` chrome and the Sovereign portal's header.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the colour /
 * size / shape come from CSS variable tokens; a caller can override
 * via className without forking the component.
 */

import { Sun, Moon } from 'lucide-react'
import { useTheme } from '@/shared/lib/useTheme'

export interface ThemeToggleProps {
  /** Optional class override for the host button — default chrome
   *  matches the wizard's `.corp-icon-btn` size + treatment. */
  className?: string
  /** Icon size in px — defaults to 14, matching the wizard header. */
  size?: number
}

export function ThemeToggle({ className, size = 14 }: ThemeToggleProps) {
  const { theme, toggle } = useTheme()
  const isDark = theme === 'dark'
  const cls =
    className ??
    'inline-flex items-center justify-center rounded-md border border-[var(--color-border)] bg-transparent text-[var(--color-text-dim)] hover:text-[var(--color-text-strong)] hover:border-[var(--color-accent)]/50 hover:bg-[var(--color-surface-hover)] transition-colors'

  return (
    <button
      type="button"
      onClick={toggle}
      aria-label={isDark ? 'Switch to light theme' : 'Switch to dark theme'}
      title={isDark ? 'Switch to light theme' : 'Switch to dark theme'}
      data-testid="theme-toggle"
      data-theme-state={theme}
      className={cls}
      style={{ width: 30, height: 30, padding: 0 }}
    >
      {isDark ? <Sun size={size} aria-hidden /> : <Moon size={size} aria-hidden />}
    </button>
  )
}
