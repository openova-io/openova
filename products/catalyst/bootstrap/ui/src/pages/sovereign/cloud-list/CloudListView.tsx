/**
 * CloudListView — list-mode body for the Cloud parent surface.
 *
 * Per issue #366 item 1, the legacy 12-tile card grid + dropdown
 * switcher are gone — both are replaced by the compact chip strip
 * (`CloudKindChips`) that lives inline in the CloudPage toolbar so
 * the active list table sits directly under the toolbar without a
 * vertical-space penalty.
 *
 * What's left in this file: resolve the active kind from URL / local
 * storage / default, persist on change, and render the active per-kind
 * list page. The kind catalogue itself (icons, labels, primary/overflow
 * split, validation helpers, the per-kind column sets) lives in
 * `./kinds.ts` + `./kindsPages.tsx`. EACH kind renders its OWN attribute
 * columns (Pods → Phase/Node/Age, HelmReleases → Chart/Revision/Status,
 * Certificates → Issuer/NotAfter/Ready, …) and drills into the
 * ResourceDetailPage (logs / exec / metrics / events / YAML / SBOM).
 *
 * #3978 RESTORE: #3970 had retired this rich per-kind dispatch from the
 * list path and replaced it with a flat NAME·KIND·FAMILY·STATUS table
 * (CloudUnifiedList). That table dropped every kind's own attributes and
 * the drill-in. We bring the rich per-kind catalogue back AND add the
 * reconciler kinds (HelmRelease / Kustomization / GitRepository /
 * Certificate / ExternalSecret / CNPG Cluster + Database / the Catalyst
 * CRs) as NEW per-kind pages with their own columns — the ONLY thing
 * #3970 should have done to the list. The graph-side #3970 wins (lens =
 * named chip-set, default Cloud, 100% density, even layout, reconcilers
 * in the graph) are untouched — this is a LIST-only restore.
 */

import { useCallback, useEffect, useMemo } from 'react'
import { useNavigate, useRouterState, useSearch } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useCloud } from '../CloudPage'
import { CLOUD_LIST_CSS } from './cloudListCss'
import {
  DEFAULT_KIND,
  findKind,
  isValidKind,
  readPersistedKind,
  writePersistedKind,
  type CloudListKind,
} from './kinds'

// Re-export so existing call sites that import the type from here keep
// compiling without churn.
export type { CloudListKind } from './kinds'

interface CloudListViewProps {
  /** Optional explicit kind override — used by tests. When omitted the
   *  active kind comes from the URL query, the persisted value, or
   *  the default in that order. */
  kindOverride?: CloudListKind
}

export function CloudListView({ kindOverride }: CloudListViewProps = {}) {
  const { deploymentId, data, isLoading } = useCloud()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { view?: string; kind?: string }
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  // Resolve the active kind: explicit override > URL query > persisted
  // localStorage > default. The first time the operator lands on the
  // list view without a `kind` query, we update the URL so the state
  // is shareable.
  const activeKind: CloudListKind = useMemo(() => {
    if (kindOverride) return kindOverride
    if (isValidKind(search.kind)) return search.kind
    const persisted = readPersistedKind()
    if (persisted) return persisted
    return DEFAULT_KIND
  }, [kindOverride, search.kind])

  // Persist the active kind whenever it changes.
  useEffect(() => {
    writePersistedKind(activeKind)
  }, [activeKind])

  // Make sure the URL carries the explicit kind so deep links work.
  useEffect(() => {
    if (kindOverride) return
    if (search.kind === activeKind) return
    if (!pathname.endsWith('/cloud')) return
    navigate({
      to: (DETECTED_MODE.mode === 'sovereign' || !deploymentId
        ? '/cloud'
        : `/provision/${deploymentId}/cloud`) as never,
      params: { deploymentId } as never,
      search: { view: 'list', kind: activeKind } as never,
      replace: true,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeKind, search.kind, pathname, deploymentId])

  // Setter retained for completeness (test-seam consumers); no
  // visible UI here calls it directly anymore — the chip strip in the
  // CloudPage toolbar drives the navigation.
  const setActiveKind = useCallback(
    (next: CloudListKind) => {
      navigate({
        to: '/cloud' as never,
        params: { deploymentId } as never,
        search: { view: 'list', kind: next } as never,
        replace: false,
      })
    },
    [navigate, deploymentId],
  )
  // Reference setActiveKind so eslint doesn't flag it; it's an exposed
  // capability of the view module even though the chip strip in the
  // toolbar is the primary affordance.
  void setActiveKind

  const ActiveListComponent = useMemo(
    () => findKind(activeKind).Component,
    [activeKind],
  )

  return (
    <div data-testid="cloud-list-view" data-kind={activeKind}>
      <style>{CLOUD_LIST_CSS}</style>
      <div className="cloud-list-view-active" data-testid={`cloud-list-view-active-${activeKind}`}>
        {isLoading && !data ? (
          <div
            className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
            data-testid="cloud-list-view-loading"
          >
            Loading {findKind(activeKind).label.toLowerCase()}…
          </div>
        ) : (
          <ActiveListComponent />
        )}
      </div>
    </div>
  )
}
