/**
 * auditHelpers.ts — pure helpers used by AuditPage (slice U8).
 *
 * Extracted so AuditPage.tsx's exports stay component-only
 * (lint react-refresh/only-export-components).
 */

import type { AuditEvent } from './rbac.api'

/** mergeAuditEvents prepends streamed events on top of the REST page,
 *  de-duped by (auditType, ts, userAccessRef). Streamed wins on
 *  collision. Newest first. */
export function mergeAuditEvents(
  streamed: AuditEvent[],
  baseline: AuditEvent[],
): AuditEvent[] {
  const seen = new Set<string>()
  const out: AuditEvent[] = []
  for (const ev of [...streamed, ...baseline]) {
    const k = `${ev.auditType}|${ev.ts}|${ev.userAccessRef ?? ''}`
    if (seen.has(k)) continue
    seen.add(k)
    out.push(ev)
  }
  // Newest first by ts (ISO strings sort lexicographically when in UTC Z form).
  out.sort((a, b) => (a.ts > b.ts ? -1 : a.ts < b.ts ? 1 : 0))
  return out
}

/** formatAuditTimestamp — short relative + absolute hybrid that
 *  reads well in a table without taking too much horizontal room.
 *  `now` is injectable so unit tests run deterministically. */
export function formatAuditTimestamp(iso: string, now: number = Date.now()): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const sec = Math.max(0, Math.round((now - d.getTime()) / 1000))
  if (sec < 60) return `${sec}s ago`
  if (sec < 3600) return `${Math.round(sec / 60)}m ago`
  if (sec < 86400) return `${Math.round(sec / 3600)}h ago`
  return d.toISOString().slice(0, 19).replace('T', ' ')
}

/** actionPalette — returns the bg/fg/border colors for a given audit
 *  type. Pure function — exported for snapshot tests. */
export function actionPalette(auditType: string): {
  bg: string
  fg: string
  border: string
} {
  switch (auditType) {
    case 'rbac-grant-created':
      return {
        bg: 'rgba(34, 197, 94, 0.10)',
        fg: '#86efac',
        border: 'rgba(34, 197, 94, 0.40)',
      }
    case 'rbac-grant-updated':
      return {
        bg: 'rgba(56, 189, 248, 0.10)',
        fg: '#7dd3fc',
        border: 'rgba(56, 189, 248, 0.40)',
      }
    case 'rbac-grant-deleted':
      return {
        bg: 'rgba(239, 68, 68, 0.10)',
        fg: '#fca5a5',
        border: 'rgba(239, 68, 68, 0.40)',
      }
    case 'rbac-tier-changed':
      return {
        bg: 'rgba(245, 158, 11, 0.10)',
        fg: '#fcd34d',
        border: 'rgba(245, 158, 11, 0.40)',
      }
    default:
      return {
        bg: 'rgba(125, 125, 125, 0.10)',
        fg: '#cbd5e1',
        border: 'rgba(125, 125, 125, 0.30)',
      }
  }
}

/** resultPalette — returns colors for the result pill column. Pure. */
export function resultPalette(result: string): { bg: string; fg: string; border: string } {
  if (result === 'denied') {
    return { bg: 'rgba(239, 68, 68, 0.10)', fg: '#fca5a5', border: 'rgba(239, 68, 68, 0.40)' }
  }
  if (result === 'error') {
    return { bg: 'rgba(245, 158, 11, 0.10)', fg: '#fcd34d', border: 'rgba(245, 158, 11, 0.40)' }
  }
  return { bg: 'rgba(34, 197, 94, 0.10)', fg: '#86efac', border: 'rgba(34, 197, 94, 0.40)' }
}
