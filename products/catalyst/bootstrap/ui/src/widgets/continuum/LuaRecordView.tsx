/**
 * LuaRecordView — EPIC-6 Slice U-DR-1 (#1101).
 *
 * Read-only display of the lua-record body the Continuum reconciler
 * has written via PDM /v1/commit. Source: Continuum CR's
 * `status.lastLuaRecord` (when surfaced by K-Cont-2). When absent,
 * shows a friendly empty state — flagged as a follow-up in the
 * reporting back to the Coordinator.
 */

import { useState } from 'react'

export interface LuaRecordViewProps {
  /** Continuum CR's status map. */
  status?: Record<string, unknown>
}

export function LuaRecordView({ status }: LuaRecordViewProps) {
  const [open, setOpen] = useState(false)
  const lastLua = status?.['lastLuaRecord'] as Record<string, unknown> | string | undefined

  const lines = parseLuaRecord(lastLua)

  return (
    <div
      className="continuum-lua rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
      data-testid="continuum-lua-view"
    >
      <button
        type="button"
        className="flex w-full items-baseline justify-between text-left text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
        onClick={() => setOpen((v) => !v)}
        data-testid="continuum-lua-toggle"
      >
        <span>Active lua-record (PowerDNS)</span>
        <span className="font-mono">{open ? '▾' : '▸'}</span>
      </button>
      {open ? (
        lines.length === 0 ? (
          <p
            data-testid="continuum-lua-empty"
            className="mt-2 text-xs text-[var(--color-text-dim)]"
          >
            Continuum has not yet written a lua-record (or the controller is not surfacing it on
            status.lastLuaRecord).
          </p>
        ) : (
          <pre
            data-testid="continuum-lua-body"
            className="mt-2 overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-2 text-[11px] leading-5 text-[var(--color-text)]"
          >
            {lines.join('\n')}
          </pre>
        )
      ) : null}
    </div>
  )
}

function parseLuaRecord(raw: unknown): string[] {
  if (raw == null) return []
  if (typeof raw === 'string') return raw.split('\n')
  if (typeof raw === 'object') {
    // Common shapes: { hostname: <str>, body: <str> } or
    // { records: [{hostname, body}, ...] }.
    const r = raw as Record<string, unknown>
    if (typeof r['body'] === 'string') return [`# ${String(r['hostname'] ?? '')}`, String(r['body'])]
    if (Array.isArray(r['records'])) {
      const out: string[] = []
      for (const rec of r['records'] as Array<Record<string, unknown>>) {
        out.push(`# ${String(rec['hostname'] ?? '')}`)
        out.push(String(rec['body'] ?? ''))
      }
      return out
    }
    try {
      return [JSON.stringify(raw, null, 2)]
    } catch {
      return []
    }
  }
  return []
}
