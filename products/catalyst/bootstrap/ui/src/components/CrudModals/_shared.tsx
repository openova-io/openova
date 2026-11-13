/**
 * _shared.tsx — the modal shell of the deployment delete and wipe
 * dialogs (DeleteDeploymentModal, WipeDeploymentModal).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (no compromise) — every dialog speaks the same vocabulary
 *      (title bar, body, footer, primary/secondary buttons).
 *   #4 (never hardcode) — colours flow from the canonical CSS vars.
 */

import type { ReactNode } from 'react'
import { useEffect } from 'react'

export interface ModalShellProps {
  /** Stable testid suffix — `infrastructure-modal-<id>` is the root. */
  id: string
  open: boolean
  title: string
  /** Optional sub-heading shown beneath the title (e.g. "Step 1 of 3"). */
  subtitle?: string
  onClose: () => void
  primary?: {
    label: string
    onClick: () => void
    disabled?: boolean
    loading?: boolean
    danger?: boolean
  }
  secondary?: {
    label: string
    onClick: () => void
  }
  children: ReactNode
}

export function ModalShell({
  id,
  open,
  title,
  subtitle,
  onClose,
  primary,
  secondary,
  children,
}: ModalShellProps) {
  useEffect(() => {
    if (!open) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={title}
      data-testid={`infrastructure-modal-${id}`}
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 100,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'color-mix(in srgb, var(--color-bg) 70%, transparent)',
        backdropFilter: 'blur(4px)',
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        style={{
          width: 'min(560px, 90vw)',
          maxHeight: '85vh',
          background: 'var(--color-bg-2)',
          border: '1px solid var(--color-border)',
          borderRadius: 12,
          display: 'flex',
          flexDirection: 'column',
          boxShadow: '0 20px 50px rgba(0, 0, 0, 0.35)',
          overflow: 'hidden',
        }}
      >
        <header
          style={{
            padding: '14px 18px',
            borderBottom: '1px solid var(--color-border)',
            display: 'flex',
            alignItems: 'flex-start',
            justifyContent: 'space-between',
            gap: 12,
          }}
        >
          <div>
            <h2
              data-testid={`infrastructure-modal-${id}-title`}
              style={{
                fontSize: '1rem',
                fontWeight: 600,
                color: 'var(--color-text-strong)',
                margin: 0,
              }}
            >
              {title}
            </h2>
            {subtitle && (
              <p
                style={{
                  margin: '3px 0 0',
                  fontSize: '0.78rem',
                  color: 'var(--color-text-dim)',
                }}
              >
                {subtitle}
              </p>
            )}
          </div>
          <button
            type="button"
            data-testid={`infrastructure-modal-${id}-close`}
            onClick={onClose}
            aria-label="Close"
            style={{
              border: '1px solid var(--color-border)',
              borderRadius: 6,
              background: 'transparent',
              color: 'var(--color-text-dim)',
              padding: '3px 8px',
              cursor: 'pointer',
              fontSize: '0.85rem',
            }}
          >
            ×
          </button>
        </header>

        <div
          style={{
            padding: '16px 18px',
            overflowY: 'auto',
            flex: 1,
            display: 'flex',
            flexDirection: 'column',
            gap: 12,
          }}
        >
          {children}
        </div>

        {(primary || secondary) && (
          <footer
            style={{
              padding: '12px 18px',
              borderTop: '1px solid var(--color-border)',
              display: 'flex',
              justifyContent: 'flex-end',
              gap: 8,
            }}
          >
            {secondary && (
              <button
                type="button"
                data-testid={`infrastructure-modal-${id}-secondary`}
                onClick={secondary.onClick}
                style={{
                  padding: '6px 14px',
                  borderRadius: 6,
                  border: '1px solid var(--color-border)',
                  background: 'transparent',
                  color: 'var(--color-text)',
                  fontSize: '0.82rem',
                  cursor: 'pointer',
                }}
              >
                {secondary.label}
              </button>
            )}
            {primary && (
              <button
                type="button"
                data-testid={`infrastructure-modal-${id}-primary`}
                onClick={primary.onClick}
                disabled={primary.disabled || primary.loading}
                style={{
                  padding: '6px 14px',
                  borderRadius: 6,
                  border: 'none',
                  background: primary.danger
                    ? 'var(--color-danger)'
                    : 'var(--color-accent)',
                  color: '#fff',
                  fontSize: '0.82rem',
                  fontWeight: 600,
                  cursor: primary.disabled ? 'not-allowed' : 'pointer',
                  opacity: primary.disabled || primary.loading ? 0.55 : 1,
                }}
              >
                {primary.loading ? 'Working…' : primary.label}
              </button>
            )}
          </footer>
        )}
      </div>
    </div>
  )
}
