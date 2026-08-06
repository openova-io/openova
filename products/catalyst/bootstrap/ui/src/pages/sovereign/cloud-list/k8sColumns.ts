/**
 * k8sColumns.ts — column definitions, extractors, and the k9s-style status
 * TONE classifiers for the Cloud per-kind list (K8sListPage).
 *
 * Split out of K8sListPage.tsx (#4084) so the page file stays
 * component-only (react-refresh/only-export-components) — mirroring how
 * layout.ts is split from GraphCanvas.tsx. Pure data/helpers, no JSX, so
 * the column catalogue (kindsPages.tsx) and the unit tests import from
 * here while K8sListPage.tsx imports the types + the COL_* primitives it
 * renders.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) — column
 * extractors are passed into K8sListPage by the kind catalogue, not
 * hardcoded per page.
 */

import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

/**
 * Status TONE for a cell (#4084 — k9s-style colour coding). The list must
 * read like k9s: an operator scans the column and sees health by COLOUR
 * instantly. Mapped to the shared status palette:
 *   ok    → green   (Running / Ready / Reconciled / ready==desired / Bound)
 *   warn  → amber   (Pending / Reconciling / partial readiness / restarts>0)
 *   err   → red     (Failed / CrashLoop / Degraded / 0-ready / Error)
 *   info  → blue    (informational positive, e.g. Active)
 *   muted → grey    (Terminating / Unknown / Completed / Succeeded)
 * Returning `undefined` renders the cell as plain text (no chip). Colour
 * is NEVER the only signal — the text label always renders alongside, so
 * the surface stays accessible to colour-blind operators.
 */
export type CellTone = 'ok' | 'warn' | 'err' | 'info' | 'muted'

export interface K8sListColumn {
  /** Column header — short, ≤24 chars. */
  header: string
  /** Pull a string from the object. Returns "—" by convention when
   *  the field is absent. */
  extract: (obj: K8sObject) => string
  /**
   * Optional k9s-style status tone for this cell (#4084). When set, the
   * cell renders as a coloured status chip carrying the extracted text;
   * when omitted (or it returns undefined) the cell renders plain text.
   */
  tone?: (obj: K8sObject) => CellTone | undefined
}

/**
 * #5571: render a k8scache cluster id as an operator-readable region.
 *
 * Cluster ids arrive in two conventions:
 *   - secondaries: `<deploymentID>-<region>`   e.g.
 *     `1c56518035a83e03-me-east-215-b-1` → `me-east-215-b-1`
 *   - the chroot's self-registered primary: `sovereign-<fqdn>` e.g.
 *     `sovereign-t99.omani.works` → `primary`
 *
 * Anything unrecognised renders verbatim — never blank, because a
 * blank region cell is exactly the "partial set reads as complete"
 * failure this column exists to prevent.
 */
export function regionLabel(clusterId: string): string {
  if (!clusterId) return '—'
  if (clusterId.startsWith('sovereign-')) return 'primary'
  // `<32-hex-ish depID>-<region>` — split on the FIRST dash only if the
  // head looks like a deployment id (hex, ≥8 chars).
  const dash = clusterId.indexOf('-')
  if (dash > 0) {
    const head = clusterId.slice(0, dash)
    if (head.length >= 8 && /^[0-9a-f]+$/i.test(head)) {
      return clusterId.slice(dash + 1)
    }
  }
  return clusterId
}

/**
 * #5571: the Region column. Injected by K8sListPage (not declared by
 * each kind in kindsPages.tsx) so EVERY Cloud list page gains region
 * attribution at once and no kind can be forgotten.
 */
export const REGION_COLUMN: K8sListColumn = {
  header: 'Region',
  extract: (obj) => regionLabel(obj.clusterId ?? ''),
}

export interface K8sListPageProps {
  /** Kind name as registered in the k8scache registry (e.g. "pod",
   *  "deployment", "service"). */
  kind: string
  /** H1-style page title. */
  title: string
  /** One-line description rendered under the title. */
  tagline: string
  /** Column definitions. Order is render order (left → right). */
  columns: K8sListColumn[]
  /** When true, items are sorted by namespace then name. Default true. */
  sortByName?: boolean
}

/* ── Common column extractors ──────────────────────────────────── */

export const COL_NAME: K8sListColumn = {
  header: 'Name',
  extract: (o) => o.metadata?.name ?? '—',
}

export const COL_NAMESPACE: K8sListColumn = {
  header: 'Namespace',
  extract: (o) => o.metadata?.namespace ?? '—',
}

/**
 * COL_TARGET_NAMESPACE — where a Flux HelmRelease actually installs its
 * workload (`spec.targetNamespace`), as opposed to where the HelmRelease
 * RECORD lives (`metadata.namespace`). For host-shared platform
 * Blueprints every HelmRelease record sits in `flux-system`, which made
 * the list read as if everything is dumped there (#4281); the real
 * workload home is the targetNamespace (catalyst-system, cert-manager,
 * kube-system, …). Helm defaults the install namespace to the release
 * namespace when `spec.targetNamespace` is unset, so an empty value
 * falls back to `metadata.namespace` to match runtime reality.
 */
export const COL_TARGET_NAMESPACE: K8sListColumn = {
  header: 'Target Namespace',
  extract: (o) => {
    const target = (o.spec as Record<string, unknown> | undefined)?.[
      'targetNamespace'
    ] as string | undefined
    if (target && target.trim() !== '') return target
    return o.metadata?.namespace ?? '—'
  },
}

export const COL_AGE: K8sListColumn = {
  header: 'Age',
  extract: (o) => {
    const ts = o.metadata?.creationTimestamp
    if (!ts) return '—'
    const ms = Date.now() - new Date(ts).getTime()
    const s = Math.max(0, Math.floor(ms / 1000))
    if (s < 60) return `${s}s`
    const m = Math.floor(s / 60)
    if (m < 60) return `${m}m`
    const h = Math.floor(m / 60)
    if (h < 24) return `${h}h`
    return `${Math.floor(h / 24)}d`
  },
}

/** Pull a status field from `obj.status.<key>` as a string. Pass
 *  `{ tone: true }` to colour the cell by phase (#4084). */
export function colStatus(
  key: string,
  header = 'Status',
  opts: { tone?: boolean } = {},
): K8sListColumn {
  const value = (o: K8sObject): string => {
    const v = (o.status as Record<string, unknown> | undefined)?.[key]
    return v == null ? '—' : String(v)
  }
  return {
    header,
    extract: value,
    ...(opts.tone ? { tone: (o: K8sObject) => tonePhase(value(o)) } : {}),
  }
}

/** Pull a spec field from `obj.spec.<key>` as a string. */
export function colSpec(key: string, header: string): K8sListColumn {
  return {
    header,
    extract: (o) => {
      const v = (o.spec as Record<string, unknown> | undefined)?.[key]
      return v == null ? '—' : String(v)
    },
  }
}

/* ── k9s-style status TONE helpers (#4084) ────────────────────────── */

/** Classify a free-form K8s phase / state STRING into a status tone. Used
 *  for pod.status.phase, namespace.status.phase, pv.status.phase, etc. */
export function tonePhase(value: string): CellTone | undefined {
  const v = value.trim().toLowerCase()
  if (!v || v === '—') return undefined
  if (
    v === 'running' ||
    v === 'ready' ||
    v === 'active' ||
    v === 'bound' ||
    v === 'available'
  )
    return 'ok'
  if (v === 'pending' || v === 'containercreating' || v === 'progressing')
    return 'warn'
  if (
    v.includes('crashloop') ||
    v.includes('error') ||
    v === 'failed' ||
    v === 'imagepullbackoff' ||
    v === 'errimagepull' ||
    v === 'oomkilled' ||
    v === 'evicted' ||
    v === 'released' ||
    v === 'lost'
  )
    return 'err'
  // Completed / Succeeded / Terminating / Unknown are neutral, not alarming.
  if (
    v === 'succeeded' ||
    v === 'completed' ||
    v === 'terminating' ||
    v === 'unknown'
  )
    return 'muted'
  return undefined
}

/** Tone for a Reconciliation-vocabulary status string (Reconciled /
 *  Reconciling / Degraded), shared by every reconciler column. */
export function toneReconcile(value: string): CellTone | undefined {
  const v = value.trim().toLowerCase()
  if (v === 'reconciled') return 'ok'
  if (v === 'reconciling') return 'warn'
  if (v === 'degraded') return 'err'
  return undefined
}

/**
 * Tone for a "ready vs desired" pair: green when ready==desired (and
 * desired>0), red when ready==0 while desired>0, amber when partial.
 * `desired` 0/undefined → grey (nothing expected). Non-numeric → undefined.
 */
export function toneReadyVsDesired(
  ready: number | undefined,
  desired: number | undefined,
): CellTone | undefined {
  if (desired == null || Number.isNaN(desired)) return undefined
  const r = ready ?? 0
  if (desired === 0) return 'muted'
  if (r >= desired) return 'ok'
  if (r === 0) return 'err'
  return 'warn'
}

/** Read a numeric `obj.status.<key>` (or 0 when absent). */
function statusNum(o: K8sObject, key: string): number | undefined {
  const v = (o.status as Record<string, unknown> | undefined)?.[key]
  if (v == null) return undefined
  const n = Number(v)
  return Number.isNaN(n) ? undefined : n
}

/** Read a numeric `obj.spec.<key>` (or undefined when absent). */
function specNum(o: K8sObject, key: string): number | undefined {
  const v = (o.spec as Record<string, unknown> | undefined)?.[key]
  if (v == null) return undefined
  const n = Number(v)
  return Number.isNaN(n) ? undefined : n
}

/**
 * colReadyCount — a k9s-style "Ready" column that renders `<ready>/<desired>`
 * and colours by readiness. `readyKey` is the status field with the ready
 * replica count; `desiredKey` (default same as readyKey's spec sibling)
 * names where the desired count lives. `desiredFrom` chooses spec vs status.
 */
export function colReadyCount(
  readyStatusKey: string,
  desiredKey: string,
  opts: { header?: string; desiredFrom?: 'spec' | 'status' } = {},
): K8sListColumn {
  const { header = 'Ready', desiredFrom = 'spec' } = opts
  return {
    header,
    extract: (o) => {
      const ready = statusNum(o, readyStatusKey) ?? 0
      const desired =
        desiredFrom === 'spec' ? specNum(o, desiredKey) : statusNum(o, desiredKey)
      if (desired == null) return String(ready)
      return `${ready}/${desired}`
    },
    tone: (o) => {
      const ready = statusNum(o, readyStatusKey)
      const desired =
        desiredFrom === 'spec' ? specNum(o, desiredKey) : statusNum(o, desiredKey)
      return toneReadyVsDesired(ready, desired)
    },
  }
}

/**
 * colPodRestarts — sum container restartCount across the pod's
 * status.containerStatuses[]. Any restart >0 is amber; a high count (≥5)
 * is red (something is flapping). 0 restarts renders muted "0".
 */
export function colPodRestarts(header = 'Restarts'): K8sListColumn {
  const count = (o: K8sObject): number => {
    const cs = (o.status as Record<string, unknown> | undefined)?.[
      'containerStatuses'
    ] as Array<Record<string, unknown>> | undefined
    if (!Array.isArray(cs)) return 0
    return cs.reduce((sum, c) => sum + (Number(c?.['restartCount']) || 0), 0)
  }
  return {
    header,
    extract: (o) => String(count(o)),
    tone: (o) => {
      const n = count(o)
      if (n === 0) return 'muted'
      return n >= 5 ? 'err' : 'warn'
    },
  }
}

/**
 * colPodPhase — the pod phase, but upgraded the k9s way: a pod whose phase
 * is Running yet has a not-Ready / waiting container (CrashLoopBackOff,
 * ImagePullBackOff, …) reads as the WAITING reason, coloured by it. This
 * is what makes the list surface a crash-looping pod at a glance instead
 * of a misleading green "Running".
 */
export function colPodPhase(header = 'Phase'): K8sListColumn {
  const effective = (o: K8sObject): string => {
    const phase =
      ((o.status as Record<string, unknown> | undefined)?.['phase'] as
        | string
        | undefined) ?? '—'
    const cs = (o.status as Record<string, unknown> | undefined)?.[
      'containerStatuses'
    ] as Array<Record<string, unknown>> | undefined
    if (Array.isArray(cs)) {
      for (const c of cs) {
        const waiting = (c?.['state'] as Record<string, unknown> | undefined)?.[
          'waiting'
        ] as Record<string, unknown> | undefined
        const reason = waiting?.['reason'] as string | undefined
        if (reason && reason !== 'ContainerCreating') return reason
      }
    }
    return phase
  }
  return {
    header,
    extract: effective,
    tone: (o) => tonePhase(effective(o)),
  }
}

/* ── Reconciler-status helpers (#3978 / Refs #3970) ───────────────── */

/**
 * Find a status condition by `type` on a K8s object. Flux/cert-manager/
 * ESO/CNPG + the Catalyst CRs all expose `status.conditions[]` with the
 * canonical {type,status,reason,message} shape.
 */
function findCondition(
  o: K8sObject,
  type: string,
): { status?: string; reason?: string; message?: string } | undefined {
  const conds = (o.status as Record<string, unknown> | undefined)?.[
    'conditions'
  ] as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(conds)) return undefined
  const c = conds.find((x) => x?.['type'] === type)
  if (!c) return undefined
  return {
    status: c['status'] as string | undefined,
    reason: c['reason'] as string | undefined,
    message: c['message'] as string | undefined,
  }
}

/**
 * colReconcileStatus — render the canonical Ready condition of a
 * continuous reconciler in the Reconciliation vocabulary
 * (Reconciled / Reconciling / Degraded — NEVER Success/Failed). Mirrors
 * the backend reconciliation_dag.go sticky state machine:
 *   Ready=True              → Reconciled
 *   Ready=False (Stalled)   → Degraded   (reason contains "stall"/"retries exhausted")
 *   Ready=False (otherwise) → Reconciling (Flux is still retrying)
 *   no Ready condition yet   → Reconciling
 *
 * `conditionType` defaults to "Ready"; CNPG Clusters carry no Ready
 * condition so callers pass a phase-based extractor instead.
 */
export function colReconcileStatus(
  header = 'Status',
  conditionType = 'Ready',
): K8sListColumn {
  const value = (o: K8sObject): string => {
    const c = findCondition(o, conditionType)
    if (!c || c.status == null) return 'Reconciling'
    if (c.status === 'True') return 'Reconciled'
    // Ready=False: terminal-Degraded only when Flux has given up
    // (Stalled), otherwise it's still self-healing → Reconciling.
    const reason = (c.reason ?? '').toLowerCase()
    const stalled =
      reason.includes('stall') ||
      reason.includes('retriesexhausted') ||
      reason.includes('exhausted')
    return stalled ? 'Degraded' : 'Reconciling'
  }
  return { header, extract: value, tone: (o) => toneReconcile(value(o)) }
}

/**
 * colCnpgStatus — CNPG Cluster has NO Ready condition; its health lives
 * in `status.phase` ("Cluster in healthy state", "Setting up primary",
 * "Failing over", …). Map onto the recon vocabulary so the column reads
 * the same as every other reconciler.
 */
export function colCnpgStatus(header = 'Status'): K8sListColumn {
  const value = (o: K8sObject): string => {
    const phase = (o.status as Record<string, unknown> | undefined)?.[
      'phase'
    ] as string | undefined
    if (!phase) return 'Reconciling'
    const p = phase.toLowerCase()
    if (p.includes('healthy')) return 'Reconciled'
    if (p.includes('fail') || p.includes('unrecoverable')) return 'Degraded'
    return 'Reconciling'
  }
  return { header, extract: value, tone: (o) => toneReconcile(value(o)) }
}

/**
 * colCatalystCrStatus — the Catalyst control-plane CRs (Application /
 * Environment / Organization / Continuum / UserAccess) expose a
 * `status.phase` and/or a Ready condition. Prefer the Ready condition;
 * fall back to phase. Recon vocabulary.
 */
export function colCatalystCrStatus(header = 'Status'): K8sListColumn {
  const value = (o: K8sObject): string => {
    const c = findCondition(o, 'Ready')
    if (c && c.status != null) {
      if (c.status === 'True') return 'Reconciled'
      const reason = (c.reason ?? '').toLowerCase()
      if (reason.includes('degraded') || reason.includes('error'))
        return 'Degraded'
      return 'Reconciling'
    }
    const phase = (o.status as Record<string, unknown> | undefined)?.[
      'phase'
    ] as string | undefined
    if (!phase) return 'Reconciling'
    const p = phase.toLowerCase()
    if (p.includes('ready') || p.includes('reconciled') || p.includes('active'))
      return 'Reconciled'
    if (p.includes('fail') || p.includes('error') || p.includes('degraded'))
      return 'Degraded'
    return 'Reconciling'
  }
  return { header, extract: value, tone: (o) => toneReconcile(value(o)) }
}
