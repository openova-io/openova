/**
 * KCUserPicker — reusable Keycloak user-picker widget (EPIC-3 #1098,
 * slice U2).
 *
 * Type-ahead search with 300ms debounce against catalyst-api's
 * `GET /api/v1/sovereigns/{id}/keycloak/users?search=<q>&limit=20`
 * proxy endpoint.
 *
 * Federated users (corporate Sovereigns where Org.spec.identity.federationProvider
 * is set to e.g. azure-sso-acme) surface inline because catalyst-api's
 * proxy maps KC's `federationLink` → `source` discriminator. The picker
 * badges them so the operator can tell native KC users from Azure-AD-
 * federated users at a glance.
 *
 * Per the canonical-seam map (`Frontend (TypeScript) — Compliance UI`):
 * No new state library. TanStack Query + an internal debounce hook
 * is sufficient.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * derives from the central `searchKCUsers()` wrapper which composes
 * API_BASE.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { searchKCUsers, type KCUser } from '@/pages/admin/rbac/rbac.api'

export interface KCUserPickerProps {
  sovereignId: string
  /** Called when the operator clicks a user in the dropdown. */
  onSelect: (user: KCUser) => void
  /** When provided, surfaces the federation badge for users brokered
   *  through this IdP (e.g. "azure-sso-acme"). The picker still calls
   *  the SAME endpoint — this prop only controls the badge tooltip
   *  copy, since KC's federation lookup happens server-side. */
  federatedIdpAlias?: string
  /** Pre-fill input (edit-mode use). */
  initialQuery?: string
  /** When true, the input is read-only — used by the multi-grant
   *  editor on edit views where the user is already pinned. */
  disabled?: boolean
  /** Test seam — bypasses the debounce. */
  noDebounce?: boolean
  /** Placeholder copy for the input. Defaults to "Search by email or name…". */
  placeholder?: string
}

const DEBOUNCE_MS = 300
const MIN_QUERY_LEN = 2
const QUERY_STALE_MS = 30_000

/** Stable query key per (sovereign, query). */
function userQueryKey(sovereignId: string, q: string) {
  return ['kc-user-picker', sovereignId, q] as const
}

export function KCUserPicker({
  sovereignId,
  onSelect,
  federatedIdpAlias,
  initialQuery = '',
  disabled = false,
  noDebounce = false,
  placeholder = 'Search by email or name…',
}: KCUserPickerProps) {
  const [raw, setRaw] = useState(initialQuery)
  const [debounced, setDebounced] = useState(initialQuery)
  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement | null>(null)

  // Debounce the input. The 300ms window matches the brief. The
  // `noDebounce` test seam still routes through setTimeout(0) so the
  // setState happens on the next microtask, not synchronously inside
  // the effect body — keeps lint's "set-state-in-effect" rule happy.
  useEffect(() => {
    const wait = noDebounce ? 0 : DEBOUNCE_MS
    const t = setTimeout(() => setDebounced(raw), wait)
    return () => clearTimeout(t)
  }, [raw, noDebounce])

  // Click-away handler.
  useEffect(() => {
    function onDocMouseDown(e: MouseEvent) {
      if (!containerRef.current) return
      if (!containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDocMouseDown)
    return () => document.removeEventListener('mousedown', onDocMouseDown)
  }, [])

  const enabled = useMemo(
    () => debounced.trim().length >= MIN_QUERY_LEN && !!sovereignId && !disabled,
    [debounced, sovereignId, disabled],
  )

  const { data, isLoading, isError, error } = useQuery({
    queryKey: userQueryKey(sovereignId, debounced.trim()),
    queryFn: () => searchKCUsers(sovereignId, debounced.trim(), 20),
    enabled,
    staleTime: QUERY_STALE_MS,
  })

  const results = data ?? []

  return (
    <div
      ref={containerRef}
      data-testid="kc-user-picker"
      className="relative w-full"
    >
      <input
        type="search"
        autoComplete="off"
        spellCheck={false}
        value={raw}
        disabled={disabled}
        onChange={(e) => {
          setRaw(e.target.value)
          setOpen(true)
        }}
        onFocus={() => setOpen(true)}
        placeholder={placeholder}
        data-testid="kc-user-picker-input"
        className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono disabled:opacity-50"
        aria-label="Search Keycloak users"
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls="kc-user-picker-listbox"
      />
      {open && enabled ? (
        <div
          id="kc-user-picker-listbox"
          role="listbox"
          data-testid="kc-user-picker-listbox"
          className="absolute z-30 mt-1 max-h-72 w-full overflow-y-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] shadow-lg"
        >
          {isLoading ? (
            <div data-testid="kc-user-picker-loading" className="px-3 py-2 text-xs text-[var(--color-text-dim)]">
              Searching…
            </div>
          ) : isError ? (
            <div data-testid="kc-user-picker-error" className="px-3 py-2 text-xs text-red-300">
              {(error as Error)?.message ?? 'search failed'}
            </div>
          ) : results.length === 0 ? (
            <div data-testid="kc-user-picker-empty" className="px-3 py-2 text-xs text-[var(--color-text-dim)]">
              No users match "{debounced}"
            </div>
          ) : (
            <ul>
              {results.map((u) => (
                <li
                  key={u.id}
                  role="option"
                  data-testid={`kc-user-picker-result-${u.id}`}
                  aria-selected={false}
                  className="cursor-pointer border-b border-[var(--color-border)] px-3 py-2 text-sm hover:bg-[var(--color-bg-3)]"
                  onClick={() => {
                    onSelect(u)
                    setRaw(u.email ?? u.username)
                    setOpen(false)
                  }}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-[var(--color-text)]">
                      {u.email ?? u.username}
                    </span>
                    <SourceBadge source={u.source} />
                  </div>
                  {u.firstName || u.lastName ? (
                    <span className="block text-xs text-[var(--color-text-dim)]">
                      {[u.firstName, u.lastName].filter(Boolean).join(' ')}
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
      {federatedIdpAlias ? (
        <p className="mt-1 text-xs text-[var(--color-text-dim)]">
          Federated identities from <code className="font-mono">{federatedIdpAlias}</code> appear
          inline with the source badge.
        </p>
      ) : null}
    </div>
  )
}

interface SourceBadgeProps {
  source: string
}

function SourceBadge({ source }: SourceBadgeProps) {
  if (source === 'keycloak') {
    return (
      <span
        data-testid="kc-user-picker-badge-keycloak"
        className="rounded-full border border-[var(--color-border)] px-2 py-0.5 text-[10px] uppercase text-[var(--color-text-dim)]"
        title="Native Keycloak user"
      >
        keycloak
      </span>
    )
  }
  if (source === 'azure_ad_federated') {
    return (
      <span
        data-testid="kc-user-picker-badge-azure"
        className="rounded-full border border-blue-500/40 bg-blue-500/10 px-2 py-0.5 text-[10px] uppercase text-blue-300"
        title="Federated via Azure AD (Keycloak Identity Provider broker)"
      >
        azure-ad
      </span>
    )
  }
  return (
    <span
      data-testid={`kc-user-picker-badge-${source}`}
      className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] uppercase text-amber-300"
      title={`Federated via ${source}`}
    >
      {source}
    </span>
  )
}
