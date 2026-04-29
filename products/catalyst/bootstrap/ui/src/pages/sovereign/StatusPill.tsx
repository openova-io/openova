/**
 * StatusPill — compact pill rendering an Application or overall
 * Sovereign status. Single source of truth for status → tone mapping
 * across AdminPage, ApplicationPage, and the deep-link top bar.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the colour
 * tones derive from the wizard `--wiz-*` token set + the existing
 * brand palette used elsewhere in the wizard (4ADE80 for ok, F87171
 * for failure, 38BDF8 for in-flight, F59E0B for degraded). New states
 * added to the reducer should add a row to STATUS_TONE here.
 */

import type { ApplicationStatus } from './eventReducer'

export type PillStatus = ApplicationStatus | 'connecting' | 'streaming' | 'completed' | 'unreachable'

interface ToneSpec {
  bg: string
  border: string
  fg: string
  label: string
  pulsing: boolean
}

export const STATUS_TONE: Record<PillStatus, ToneSpec> = {
  pending:    { bg: 'rgba(148,163,184,0.10)', border: 'rgba(148,163,184,0.30)', fg: 'var(--wiz-text-md)', label: 'Pending',    pulsing: false },
  installing: { bg: 'rgba(56,189,248,0.10)',  border: 'rgba(56,189,248,0.35)',  fg: '#38BDF8',            label: 'Installing', pulsing: true  },
  installed:  { bg: 'rgba(74,222,128,0.10)',  border: 'rgba(74,222,128,0.35)',  fg: '#4ADE80',            label: 'Installed',  pulsing: false },
  failed:     { bg: 'rgba(248,113,113,0.10)', border: 'rgba(248,113,113,0.35)', fg: '#F87171',            label: 'Failed',     pulsing: false },
  degraded:   { bg: 'rgba(245,158,11,0.10)',  border: 'rgba(245,158,11,0.35)',  fg: '#F59E0B',            label: 'Degraded',   pulsing: false },
  unknown:    { bg: 'rgba(148,163,184,0.10)', border: 'rgba(148,163,184,0.30)', fg: 'var(--wiz-text-sub)', label: 'Unknown',    pulsing: false },
  connecting: { bg: 'rgba(56,189,248,0.10)',  border: 'rgba(56,189,248,0.35)',  fg: '#38BDF8',            label: 'Connecting', pulsing: true  },
  streaming:  { bg: 'rgba(56,189,248,0.10)',  border: 'rgba(56,189,248,0.35)',  fg: '#38BDF8',            label: 'Provisioning', pulsing: true },
  completed:  { bg: 'rgba(74,222,128,0.10)',  border: 'rgba(74,222,128,0.35)',  fg: '#4ADE80',            label: 'Ready',      pulsing: false },
  unreachable:{ bg: 'rgba(248,113,113,0.10)', border: 'rgba(248,113,113,0.35)', fg: '#F87171',            label: 'Unreachable', pulsing: false },
}

interface StatusPillProps {
  status: PillStatus
  /** Override the default label text from STATUS_TONE. */
  label?: string
  size?: 'sm' | 'md'
  /** Test id seam — defaults to status-pill. */
  testId?: string
}

export function StatusPill({ status, label, size = 'sm', testId = 'status-pill' }: StatusPillProps) {
  const tone = STATUS_TONE[status]
  const finalLabel = label ?? tone.label
  const pad = size === 'md' ? '0.2rem 0.6rem' : '0.12rem 0.5rem'
  const fontSize = size === 'md' ? '0.72rem' : '0.62rem'
  return (
    <span
      data-testid={testId}
      data-status={status}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 5,
        padding: pad,
        borderRadius: 999,
        fontSize,
        fontWeight: 700,
        letterSpacing: '0.04em',
        textTransform: 'uppercase',
        background: tone.bg,
        color: tone.fg,
        border: `1px solid ${tone.border}`,
        whiteSpace: 'nowrap',
      }}
    >
      <span
        aria-hidden
        style={{
          width: 5,
          height: 5,
          borderRadius: '50%',
          background: tone.fg,
          animation: tone.pulsing ? 'sov-pulse 1.6s ease-in-out infinite' : 'none',
        }}
      />
      {finalLabel}
    </span>
  )
}

/** Inject the keyframes once (consumed by AdminLayout). */
export const STATUS_PULSE_KEYFRAMES = `
@keyframes sov-pulse { 0%,100%{opacity:1;transform:scale(1)} 50%{opacity:.4;transform:scale(.7)} }
`
