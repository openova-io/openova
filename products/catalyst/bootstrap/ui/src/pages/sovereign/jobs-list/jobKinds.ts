/**
 * jobs-list/jobKinds.ts — single source of truth for the /jobs parent
 * surface's job-kind chip catalogue (P1b, Refs #6703).
 *
 * The jobs analogue of cloud-list/kinds.ts: the /jobs page mirrors the
 * /cloud (Resources) page's UX — a per-kind chip strip that single-selects
 * the JobKind the list view filters to, plus a List⇄Graph toggle. This
 * catalogue drives the chip strip exactly as `KINDS` drives CloudKindChips.
 *
 * Two hard rules (docs/INVIOLABLE-PRINCIPLES.md #4 — never hardcode):
 *   1. The chip catalogue covers every `JobKind` value EXCEPT `group`
 *      (a synthesised parent row is not an engine an operator filters by).
 *   2. Each chip's LABEL is the REAL engine name, taken verbatim from
 *      `JOB_ENGINE_LABELS` in lib/jobs.types.ts (founder 2026-08-26 — "show
 *      the jobs with their real engine names"). There is no second label
 *      map to drift out of sync: JOB_KIND_LABEL below reads that constant.
 */

import type { JobKind } from '@/lib/jobs.types'
import { JOB_ENGINE_LABELS } from '@/lib/jobs.types'

/**
 * The JobKind ids that get a chip — every `JobKind` EXCEPT `group`.
 * Declared as a typed tuple so the catalogue below can never drift from
 * the canonical `JobKind` union without a compile error.
 */
export type JobChipKind = Exclude<JobKind, 'group'>

export interface JobKindEntry {
  id: JobChipKind
  /** REAL engine label — always sourced from JOB_ENGINE_LABELS. */
  label: string
  /** Free-form one-line tagline (subtitles / hover context). */
  tagline: string
  /** SVG path data on the canonical 24x24 viewBox — Tabler-style. */
  icon: string
  /** Conceptual category (drives the chip-tint colour in JobKindChips). */
  category: 'reconciler' | 'process' | 'mutation' | 'lifecycle'
  /** True → renders inline in the chip strip; false → `+ More` popover. */
  primary: boolean
}

/* Tabler / lucide-style outlines on the 24x24 viewBox. */
const ICON_HELMRELEASE =
  'M12 3a9 9 0 1 0 9 9M12 3v9l6.5 6.5M12 3a9 9 0 0 1 9 9h-9'
const ICON_KUSTOMIZATION =
  'M12 3l8 4 -8 4 -8 -4zM4 11l8 4 8 -4M4 15l8 4 8 -4'
const ICON_DEPLOYMENT =
  'M5 4h6v6H5zM13 4h6v6h-6zM5 14h6v6H5zM13 14h6v6h-6z'
const ICON_CRONJOB =
  'M12 3a9 9 0 1 0 0 18a9 9 0 0 0 0 -18zM12 8v4l3 2'
const ICON_JOB_STEP =
  'M4 5h16v14H4zM9 9l5 3 -5 3z'
const ICON_JOB_TASK =
  'M4 7h9M4 12h9M4 17h6M15 8l2 2 4 -4'
const ICON_CROSSPLANE =
  'M12 3l8 4v10l-8 4 -8 -4V7zM4 7l8 4 8 -4M12 11v10'
const ICON_OPENTOFU =
  'M4 12a8 8 0 0 1 13.7 -5.6L20 8M20 4v4h-4M20 12a8 8 0 0 1 -13.7 5.6L4 16M4 20v-4h4'

/**
 * Canonical job-kind catalogue. Order matters — `primary: true` entries
 * render inline in the chip strip; everything else lives in the `+ More`
 * popover. Labels are pulled from JOB_ENGINE_LABELS so the chip text and
 * the JobsTable Kind column always read the same engine name.
 */
export const JOB_KINDS: readonly JobKindEntry[] = [
  // Primary chips — the most-used finite-work engines stay inline.
  { id: 'install', label: JOB_ENGINE_LABELS.install, tagline: 'Flux HelmRelease install leaves — one per Blueprint.', icon: ICON_HELMRELEASE, category: 'reconciler', primary: true },
  { id: 'step', label: JOB_ENGINE_LABELS.step, tagline: 'One step of a projected activity (provision / cutover).', icon: ICON_JOB_STEP, category: 'process', primary: true },
  { id: 'task', label: JOB_ENGINE_LABELS.task, tagline: 'Standalone / owned batch Kubernetes Jobs.', icon: ICON_JOB_TASK, category: 'process', primary: true },
  { id: 'cron', label: JOB_ENGINE_LABELS.cron, tagline: 'Recurring CronJob runs — each run is an Execution.', icon: ICON_CRONJOB, category: 'process', primary: true },
  { id: 'mutation', label: JOB_ENGINE_LABELS.mutation, tagline: 'Day-2 Crossplane XRC submissions.', icon: ICON_CROSSPLANE, category: 'mutation', primary: true },
  { id: 'lifecycle', label: JOB_ENGINE_LABELS.lifecycle, tagline: 'Phase-0 / lifecycle OpenTofu runs.', icon: ICON_OPENTOFU, category: 'lifecycle', primary: true },

  /* ── Overflow — accessible via the `+ More` popover ───────────── */
  // The open-ended reconcilers are excluded from /jobs by the backend
  // (jobs.FilterFiniteJobs); they surface primarily on the Cloud
  // Reconciliation lens. Kept here so a row that IS present is filterable.
  { id: 'reconcile', label: JOB_ENGINE_LABELS.reconcile, tagline: 'Flux Kustomization reconcile leaves.', icon: ICON_KUSTOMIZATION, category: 'reconciler', primary: false },
  { id: 'reconciler', label: JOB_ENGINE_LABELS.reconciler, tagline: 'Long-running reconciler Deployments (health axis).', icon: ICON_DEPLOYMENT, category: 'reconciler', primary: false },
] as const

export const JOB_KIND_IDS: readonly JobChipKind[] = JOB_KINDS.map((k) => k.id)

/** Default active chip — HelmRelease installs are the bulk of /jobs
 *  (the ~65 install-* leaves, the documented "install lens"). */
export const DEFAULT_JOB_KIND: JobChipKind = 'install'

/** localStorage key — distinct from the Cloud list's `sov-cloud-list-kind`. */
export const JOB_KIND_STORAGE_KEY = 'sov-jobs-list-kind'

export function isValidJobKind(value: unknown): value is JobChipKind {
  return typeof value === 'string' && (JOB_KIND_IDS as readonly string[]).includes(value)
}

export function readPersistedJobKind(): JobChipKind | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(JOB_KIND_STORAGE_KEY)
    return isValidJobKind(raw) ? raw : null
  } catch {
    return null
  }
}

export function writePersistedJobKind(kind: JobChipKind): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(JOB_KIND_STORAGE_KEY, kind)
  } catch {
    /* noop */
  }
}

export function findJobKind(id: JobChipKind): JobKindEntry {
  return JOB_KINDS.find((k) => k.id === id) ?? JOB_KINDS[0]
}
