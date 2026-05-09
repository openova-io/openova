/**
 * CuratePage — EPIC-2 Slice P (#1097): Sovereign-admin promotes a
 * Blueprint from `<org>/shared-blueprints` into `catalog-sovereign`.
 *
 * Lists every per-Org Blueprint reachable via the curatable endpoint.
 * Each row has a "Curate" button that POSTs to /blueprints/curate.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #5 the underlying handler enforces
 * sovereign-admin (admin or owner tier) — the page renders even for
 * other tiers, but the server is the gate.
 */

import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import {
  curateBlueprint,
  listCuratableBlueprints,
  type CuratableBlueprint,
} from '@/lib/catalog.api'

export interface CuratePageProps {
  /** Test seam — bypass network. */
  disableNetwork?: boolean
  /** Test seam — pre-populated curatable list. */
  initialCuratable?: CuratableBlueprint[]
  /** Test seam — pre-populated org list. */
  initialOrgs?: string[]
}

export function CuratePage({
  disableNetwork = false,
  initialCuratable,
  initialOrgs,
}: CuratePageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
  const qc = useQueryClient()

  const [orgsInput, setOrgsInput] = useState<string>((initialOrgs ?? []).join(','))
  const orgs = useMemo(
    () => orgsInput.split(',').map((s) => s.trim()).filter(Boolean),
    [orgsInput],
  )

  const listQ = useQuery({
    queryKey: ['curatable', deploymentId, orgs],
    queryFn: () => listCuratableBlueprints(deploymentId, orgs),
    enabled: !initialCuratable && !disableNetwork && !!deploymentId && orgs.length > 0,
    staleTime: 30_000,
  })

  const items: CuratableBlueprint[] = initialCuratable ?? listQ.data?.items ?? []

  const curateMu = useMutation({
    mutationFn: ({ org, name }: { org: string; name: string }) =>
      curateBlueprint(deploymentId, { sourceOrg: org, blueprintName: name }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['curatable'] })
    },
  })

  return (
    <div className="p-6" data-testid="curate-page">
      <h1 className="mb-4 text-2xl font-semibold text-[var(--color-text)]">
        Curate Blueprints
      </h1>
      <p className="mb-4 text-xs text-[var(--color-text-dim)]">
        Browse Blueprints published by each Org and promote them to{' '}
        <code className="font-mono">catalog-sovereign</code>. Once curated, the catalog
        surfaces them to every Org with `sovereign` source priority.
      </p>

      <label className="mb-4 block text-xs text-[var(--color-text-dim)]">
        Orgs to scan (comma-separated)
        <input
          type="text"
          className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
          data-testid="curate-page-orgs"
          value={orgsInput}
          onChange={(e) => setOrgsInput(e.target.value)}
          placeholder="acme,beta,gamma"
        />
      </label>

      {listQ.isLoading ? (
        <p className="text-xs text-[var(--color-text-dim)]" data-testid="curate-page-loading">
          Loading curatable Blueprints…
        </p>
      ) : null}
      {listQ.isError ? (
        <div
          className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
          data-testid="curate-page-error"
        >
          {(listQ.error as Error).message}
        </div>
      ) : null}

      {items.length === 0 && !listQ.isLoading ? (
        <p className="text-xs text-[var(--color-text-dim)]" data-testid="curate-page-empty">
          No curatable Blueprints found in the named Orgs.
        </p>
      ) : (
        <table className="w-full border-collapse text-xs" data-testid="curate-page-table">
          <thead>
            <tr className="border-b border-[var(--color-border)] text-left text-[var(--color-text-dim)]">
              <th className="px-2 py-1.5 font-medium">Org</th>
              <th className="px-2 py-1.5 font-medium">Blueprint</th>
              <th className="px-2 py-1.5 font-medium">Version</th>
              <th className="px-2 py-1.5 font-medium">Title</th>
              <th className="px-2 py-1.5 font-medium text-right">Action</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item) => (
              <tr
                key={`${item.org}/${item.name}`}
                className="border-b border-[var(--color-border)]/40"
                data-testid={`curate-page-row-${item.org}-${item.name}`}
              >
                <td className="px-2 py-1.5 font-mono">{item.org}</td>
                <td className="px-2 py-1.5 font-mono">{item.name}</td>
                <td className="px-2 py-1.5 font-mono">{item.version || '?'}</td>
                <td className="px-2 py-1.5 text-[var(--color-text)]">{item.title || '—'}</td>
                <td className="px-2 py-1.5 text-right">
                  <button
                    type="button"
                    className="rounded-md border border-[var(--color-accent)] px-2 py-0.5 text-[10px] uppercase tracking-wide text-[var(--color-accent)] hover:bg-[var(--color-accent)]/10 disabled:opacity-40"
                    data-testid={`curate-page-curate-${item.org}-${item.name}`}
                    disabled={curateMu.isPending}
                    onClick={() =>
                      disableNetwork
                        ? undefined
                        : curateMu.mutate({ org: item.org, name: item.name })
                    }
                  >
                    Curate
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {curateMu.isSuccess ? (
        <div
          className="mt-3 rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2 text-xs text-green-400"
          data-testid="curate-page-success"
        >
          Curated {curateMu.data.blueprintName} → {curateMu.data.targetOrg}.
        </div>
      ) : null}
      {curateMu.isError ? (
        <div
          className="mt-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
          data-testid="curate-page-mutation-error"
        >
          {(curateMu.error as Error).message}
        </div>
      ) : null}
    </div>
  )
}
