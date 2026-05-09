/**
 * RoleBrowserPage — Keycloak realm-role + per-OIDC-client-role browser
 * for sovereign-admin (EPIC-3 #1098 slice U4).
 *
 * Layout:
 *   • Top tabs: "Realm roles" | "Client roles" (per OIDC client)
 *   • Realm roles tab: list w/ tier-level sort, click row → members panel
 *   • Client roles tab: select client → list of client roles
 *
 * Sovereign-admin only. The catalyst-api enforces the gate; the UI
 * surfaces 403 explicitly so the operator knows their session lacks
 * the privilege.
 */

import { useMemo, useState } from 'react'
import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import {
  listKCClientRoles,
  listKCRoleMembers,
  listKCRoles,
} from './rbac.api'
import { readTierLevel, sortRolesByTierLevel } from './roleHelpers'

interface RoleBrowserPageProps {
  initialDeploymentId?: string
  /** Test seam — start on a specific tab without TanStack Router. */
  initialTab?: 'realm' | 'client'
}

export function RoleBrowserPage({
  initialDeploymentId,
  initialTab = 'realm',
}: RoleBrowserPageProps = {}) {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = initialDeploymentId ?? params.deploymentId ?? resolvedId ?? ''

  const [tab, setTab] = useState<'realm' | 'client'>(initialTab)
  const [clientUUID, setClientUUID] = useState('')

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Keycloak Roles">
      <div data-testid="role-browser-page" className="px-6 py-4">
        <div className="mb-4">
          <h1 className="text-xl font-semibold text-[var(--color-text-strong)]">Keycloak Roles</h1>
          <p className="text-sm text-[var(--color-text-dim)]">
            Realm-scoped + per-OIDC-client roles in the Sovereign realm. Sovereign-admin only.
          </p>
        </div>

        <div className="mb-3 flex items-center gap-2 border-b border-[var(--color-border)]">
          <button
            type="button"
            data-testid="role-browser-tab-realm"
            onClick={() => setTab('realm')}
            className={`-mb-px px-3 py-2 text-sm transition-colors ${
              tab === 'realm'
                ? 'border-b-2 border-[var(--color-accent)] text-[var(--color-text-strong)]'
                : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
            }`}
          >
            Realm roles
          </button>
          <button
            type="button"
            data-testid="role-browser-tab-client"
            onClick={() => setTab('client')}
            className={`-mb-px px-3 py-2 text-sm transition-colors ${
              tab === 'client'
                ? 'border-b-2 border-[var(--color-accent)] text-[var(--color-text-strong)]'
                : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
            }`}
          >
            Client roles
          </button>
        </div>

        {tab === 'realm' ? (
          <RealmRolesPanel deploymentId={deploymentId} />
        ) : (
          <ClientRolesPanel
            deploymentId={deploymentId}
            clientUUID={clientUUID}
            onClientUUIDChange={setClientUUID}
          />
        )}
      </div>
    </PortalShell>
  )
}

/* ── Realm-roles tab ──────────────────────────────────────────────── */

function RealmRolesPanel({ deploymentId }: { deploymentId: string }) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['kc-roles', deploymentId],
    queryFn: () => listKCRoles(deploymentId),
    enabled: !!deploymentId,
    staleTime: 30_000,
  })

  const sorted = useMemo(() => sortRolesByTierLevel(data ?? []), [data])
  const [selectedName, setSelectedName] = useState<string | null>(null)

  const isForbidden = isError && /HTTP 403/.test((error as Error)?.message ?? '')

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <div>
        {isForbidden ? (
          <div data-testid="role-browser-forbidden" className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            Forbidden — sovereign-admin tier (admin or owner) required.
          </div>
        ) : isError ? (
          <div data-testid="role-browser-error" className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {(error as Error)?.message ?? 'Failed to load roles'}
          </div>
        ) : null}
        {isLoading ? (
          <div data-testid="role-browser-loading" className="text-sm text-[var(--color-text-dim)]">
            Loading roles…
          </div>
        ) : sorted.length === 0 && !isError ? (
          <div data-testid="role-browser-empty" className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
            No realm roles yet — the T2 bootstrap may not have run.
          </div>
        ) : (
          <table data-testid="role-browser-table" className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
                <th className="py-2 pr-3">Name</th>
                <th className="py-2 pr-3">Tier-level</th>
                <th className="py-2 pr-3">Composite</th>
                <th className="py-2 pr-3">Description</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((r) => (
                <tr
                  key={r.id ?? r.name}
                  data-testid={`role-browser-row-${r.name}`}
                  onClick={() => setSelectedName(r.name)}
                  className={`cursor-pointer border-b border-[var(--color-border)] hover:bg-[var(--color-bg-2)] ${
                    selectedName === r.name ? 'bg-[var(--color-bg-2)]' : ''
                  }`}
                >
                  <td className="py-2 pr-3 font-mono">{r.name}</td>
                  <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
                    {readTierLevel(r) ?? '—'}
                  </td>
                  <td className="py-2 pr-3 text-xs">{r.composite ? '✓' : '—'}</td>
                  <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
                    {r.description ?? '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div>
        {selectedName ? (
          <RoleMembersPanel deploymentId={deploymentId} roleName={selectedName} />
        ) : (
          <div data-testid="role-browser-members-placeholder" className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
            Click a role to see its members.
          </div>
        )}
      </div>
    </div>
  )
}

function RoleMembersPanel({
  deploymentId,
  roleName,
}: {
  deploymentId: string
  roleName: string
}) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['kc-role-members', deploymentId, roleName],
    queryFn: () => listKCRoleMembers(deploymentId, roleName),
    enabled: !!deploymentId && !!roleName,
    staleTime: 30_000,
  })

  return (
    <div
      data-testid={`role-browser-members-${roleName}`}
      className="rounded-md border border-[var(--color-border)] p-3"
    >
      <h2 className="text-xs uppercase text-[var(--color-text-dim)]">
        Members of <code className="font-mono text-[var(--color-text)]">{roleName}</code>
      </h2>
      {isLoading ? (
        <p className="mt-2 text-xs text-[var(--color-text-dim)]">Loading members…</p>
      ) : isError ? (
        <p className="mt-2 text-xs text-red-300">{(error as Error)?.message}</p>
      ) : !data || data.items.length === 0 ? (
        <p className="mt-2 text-xs text-[var(--color-text-dim)]">
          No direct members. (Users may still inherit this role via group membership — see the
          Access Matrix view.)
        </p>
      ) : (
        <ul className="mt-2 space-y-1">
          {data.items.map((u) => (
            <li
              key={u.id}
              data-testid={`role-browser-member-${u.id}`}
              className="text-xs font-mono text-[var(--color-text)]"
            >
              {u.email ?? u.username}
              <span className="ml-2 text-[var(--color-text-dim)]">({u.source})</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/* ── Client-roles tab ─────────────────────────────────────────────── */

function ClientRolesPanel({
  deploymentId,
  clientUUID,
  onClientUUIDChange,
}: {
  deploymentId: string
  clientUUID: string
  onClientUUIDChange: (v: string) => void
}) {
  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['kc-client-roles', deploymentId, clientUUID],
    queryFn: () => listKCClientRoles(deploymentId, clientUUID),
    enabled: !!deploymentId && !!clientUUID,
    staleTime: 30_000,
  })

  return (
    <div data-testid="role-browser-client-tab">
      <div className="mb-3 flex items-center gap-2">
        <label className="text-xs text-[var(--color-text-dim)]">
          Client UUID:
          <input
            data-testid="role-browser-client-uuid"
            type="text"
            value={clientUUID}
            onChange={(e) => onClientUUIDChange(e.target.value)}
            placeholder="paste a Keycloak client UUID"
            className="ml-2 rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs font-mono"
          />
        </label>
        {isFetching ? <span className="text-xs text-[var(--color-text-dim)]">loading…</span> : null}
      </div>
      {!clientUUID ? (
        <div data-testid="role-browser-client-empty" className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
          Paste a client UUID above to list its roles. (Realm roles are on the other tab.)
        </div>
      ) : isLoading ? (
        <div className="text-sm text-[var(--color-text-dim)]">Loading client roles…</div>
      ) : isError ? (
        <div data-testid="role-browser-client-err" className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {(error as Error)?.message ?? 'Failed to load client roles'}
        </div>
      ) : !data || data.length === 0 ? (
        <div data-testid="role-browser-client-no-roles" className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
          No roles on this client.
        </div>
      ) : (
        <table data-testid="role-browser-client-table" className="w-full border-collapse text-sm">
          <thead>
            <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
              <th className="py-2 pr-3">Name</th>
              <th className="py-2 pr-3">Description</th>
            </tr>
          </thead>
          <tbody>
            {data.map((r) => (
              <tr
                key={r.id ?? r.name}
                data-testid={`role-browser-client-row-${r.name}`}
                className="border-b border-[var(--color-border)]"
              >
                <td className="py-2 pr-3 font-mono">{r.name}</td>
                <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
                  {r.description ?? '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

/* ── Helpers — see roleHelpers.ts ─────────────────────────────────── */
