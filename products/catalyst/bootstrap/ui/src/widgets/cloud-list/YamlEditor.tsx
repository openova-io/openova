/**
 * YamlEditor — EPIC-4 Slice R3 (#1099) — YAML editor with side-by-side
 * diff preview and validate / apply actions.
 *
 * Branching contract per the brief:
 *   - `managed-by: flux` resources → opens a Gitea PR via the unified
 *     /blueprints/edit-pr endpoint (slice T+O+P P1's GiteaBlueprintClient,
 *     extended by slice Z3 with CreatePullRequest).
 *   - `managed-by: manual` resources OR no managed-by annotation → calls
 *     /k8s/{kind}/{ns}/{name}/apply for direct apply.
 *
 * The Monaco editor is intentionally NOT bundled (would add ~3 MB to the
 * SPA payload). Instead the editor uses a styled, monospaced textarea
 * with line numbers + a side-by-side diff that computes per-line LCS at
 * render time. Future slice can drop in @monaco-editor/react if visual
 * editing parity becomes a P0; the validate/apply contract is unchanged.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — every endpoint is composed via resource.api.ts.
 *   #5 (least privilege) — UI hides "Apply" for unauthorized callers,
 *      but the server is the authoritative gate (tier-admin via
 *      applicationInstallCallerAuthorized in k8s_resource_actions.go).
 */

import { useMemo, useState } from 'react'

import { editPRBlueprint, type BlueprintEditPRResponse } from '@/lib/catalog.api'
import {
  applyYAML,
  dryRunYAML,
  isFluxManaged,
} from '@/pages/sovereign/cloud-list/resource.api'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

export interface YamlEditorProps {
  deploymentId: string
  kind: string
  ns: string | undefined
  name: string
  /** Live object — used to seed the editor + branch flux/manual. */
  obj: K8sObject | null
  /** Test seam — when true, "Apply" produces a PR-mode placeholder
   *  instead of a real Gitea call. */
  fluxOverride?: boolean
  /**
   * #3668 §5D — commit-target override. When provided, "Apply" commits the
   * edited YAML through THIS callback instead of the default
   * flux→edit-pr / manual→apply branching. The catalog's "Edit IaC" mode
   * passes a callback that PUTs the full blueprint.yaml to
   * catalog-sovereign/<bp>/blueprint.yaml (the SAME Gitea file the card edit
   * writes), so the same shipping editor drives the catalog single source of
   * truth. The returned string is shown as the success message. A thrown
   * error surfaces as the apply error (so a non-committed write never reads
   * as success). The editor UI (diff / validate / dirty-state) is unchanged.
   */
  onCommit?: (yaml: string) => Promise<string>
  /** Optional label for the apply button when onCommit is set (default
   *  "Commit IaC"). */
  commitLabel?: string
}

/** Convert a JS object to canonical YAML — small subset suitable for
 *  K8s manifests (no anchors, no flow style). Production would wire
 *  js-yaml; for the editor's seed-text use we keep the dependency
 *  surface minimal by stringifying via JSON.stringify + a YAML adapter
 *  that's good enough for `kubectl apply -f` round-trip. */
function objectToYAML(obj: unknown, indent = 0): string {
  const pad = '  '.repeat(indent)
  if (obj === null || obj === undefined) return 'null'
  if (typeof obj === 'string') {
    if (obj === '' || /[:#&*?{}[\],"'\n]|^[\s-]/.test(obj)) {
      return JSON.stringify(obj)
    }
    return obj
  }
  if (typeof obj === 'number' || typeof obj === 'boolean') return String(obj)
  if (Array.isArray(obj)) {
    if (obj.length === 0) return '[]'
    return obj
      .map((v) => {
        if (v && typeof v === 'object' && !Array.isArray(v)) {
          const inner = objectToYAML(v, indent + 1).replace(new RegExp(`^${pad}  `), '')
          return `${pad}- ${inner.trimStart()}`
        }
        return `${pad}- ${objectToYAML(v, indent)}`
      })
      .join('\n')
  }
  if (typeof obj === 'object') {
    const entries = Object.entries(obj as Record<string, unknown>)
    if (entries.length === 0) return '{}'
    return entries
      .map(([k, v]) => {
        if (v && typeof v === 'object' && (!Array.isArray(v) || v.length > 0)) {
          return `${pad}${k}:\n${objectToYAML(v, indent + 1)}`
        }
        return `${pad}${k}: ${objectToYAML(v, indent)}`
      })
      .join('\n')
  }
  return String(obj)
}

interface DiffLine {
  type: '=' | '+' | '-' | '~'
  lhs: string
  rhs: string
}

/** Tiny line-by-line diff renderer (no LCS — paired by index, with `~`
 *  flagging changed lines). Adequate for the typical small-edit case. */
function computeDiff(left: string, right: string): DiffLine[] {
  const a = left.split('\n')
  const b = right.split('\n')
  const out: DiffLine[] = []
  const max = Math.max(a.length, b.length)
  for (let i = 0; i < max; i++) {
    const lhs = a[i] ?? ''
    const rhs = b[i] ?? ''
    if (i >= a.length) out.push({ type: '+', lhs: '', rhs })
    else if (i >= b.length) out.push({ type: '-', lhs, rhs: '' })
    else if (lhs === rhs) out.push({ type: '=', lhs, rhs })
    else out.push({ type: '~', lhs, rhs })
  }
  return out
}

export function YamlEditor({ deploymentId, kind, ns, name, obj, fluxOverride, onCommit, commitLabel }: YamlEditorProps) {
  const initial = useMemo(() => (obj ? objectToYAML(obj) : ''), [obj])
  const [yaml, setYaml] = useState<string>(initial)
  const [showDiff, setShowDiff] = useState(false)
  const [validateMsg, setValidateMsg] = useState<string | null>(null)
  const [validateError, setValidateError] = useState<string | null>(null)
  const [applyMsg, setApplyMsg] = useState<string | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [prInfo, setPRInfo] = useState<BlueprintEditPRResponse | null>(null)
  const [busy, setBusy] = useState<'validate' | 'apply' | null>(null)

  const flux = fluxOverride ?? isFluxManaged(obj)
  const dirty = yaml.trim() !== initial.trim()
  const diff = useMemo(() => computeDiff(initial, yaml), [initial, yaml])

  async function onValidate() {
    setValidateMsg(null)
    setValidateError(null)
    setBusy('validate')
    try {
      const resp = await dryRunYAML(deploymentId, kind, ns, name, yaml)
      setValidateMsg(`Dry-run accepted (resourceVersion ${resp.resourceVersion}).`)
    } catch (err) {
      setValidateError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function onApply() {
    setApplyMsg(null)
    setApplyError(null)
    setPRInfo(null)
    setBusy('apply')
    try {
      if (onCommit) {
        // #3668 §5D — catalog "Edit IaC" mode: commit through the provided
        // catalog-sovereign writer instead of the cloud-list edit-pr/apply
        // branching. A thrown error surfaces below (no false success).
        const msg = await onCommit(yaml)
        setApplyMsg(msg)
      } else if (flux) {
        // Slice Z3 — flux-managed resources route through the unified
        // /blueprints/edit-pr endpoint: branch + commit + PR on the
        // per-Org shared-blueprints repo. The org slug is derived from
        // the K8s object's `catalyst.openova.io/organization` label
        // (per slice I labels) with the namespace as fallback.
        const org = orgFromObject(obj, ns)
        const path = editPathFromObject(obj, kind, ns, name)
        const title = `Edit ${kind}/${name} via Catalyst UI`
        const message = `Edit ${kind}/${name} (ns=${ns ?? '(cluster)'}) via Catalyst YamlEditor`
        const resp = await editPRBlueprint(deploymentId, {
          org,
          path,
          content: yaml,
          message,
          title,
        })
        setPRInfo(resp)
        setApplyMsg(`PR #${resp.prNumber} opened — ${resp.prURL}`)
      } else {
        const resp = await applyYAML(deploymentId, kind, ns, name, yaml)
        setApplyMsg(`Applied (resourceVersion ${resp.resourceVersion}).`)
      }
    } catch (err) {
      setApplyError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div data-testid="yaml-editor" className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-text-dim)]">
        <span data-testid="yaml-editor-managed-by">
          managed-by: <span className={flux ? 'text-emerald-300' : 'text-amber-300'}>{flux ? 'flux' : 'manual'}</span>
        </span>
        <span className="text-[var(--color-text-dim)]">
          {dirty ? '• unsaved changes' : '• in sync'}
        </span>
        <button
          type="button"
          onClick={() => setShowDiff((v) => !v)}
          data-testid="yaml-editor-toggle-diff"
          className="ml-auto rounded border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg-3)]"
        >
          {showDiff ? 'Hide diff' : 'Show diff'}
        </button>
      </div>

      {showDiff ? (
        <div
          data-testid="yaml-editor-diff"
          className="grid grid-cols-2 gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-2 font-mono text-xs"
        >
          <div data-testid="yaml-editor-diff-left">
            <div className="mb-1 font-semibold text-[var(--color-text-dim)]">Current</div>
            <ol className="space-y-0">
              {diff.map((l, i) => (
                <li
                  key={`l-${i}`}
                  className={
                    l.type === '-' || l.type === '~'
                      ? 'bg-rose-950/40 text-rose-200'
                      : 'text-[var(--color-text)]'
                  }
                >
                  {l.lhs || ' '}
                </li>
              ))}
            </ol>
          </div>
          <div data-testid="yaml-editor-diff-right">
            <div className="mb-1 font-semibold text-[var(--color-text-dim)]">Proposed</div>
            <ol className="space-y-0">
              {diff.map((l, i) => (
                <li
                  key={`r-${i}`}
                  className={
                    l.type === '+' || l.type === '~'
                      ? 'bg-emerald-950/40 text-emerald-200'
                      : 'text-[var(--color-text)]'
                  }
                >
                  {l.rhs || ' '}
                </li>
              ))}
            </ol>
          </div>
        </div>
      ) : (
        <textarea
          aria-label="YAML editor"
          data-testid="yaml-editor-textarea"
          value={yaml}
          onChange={(e) => setYaml(e.target.value)}
          spellCheck={false}
          className="h-96 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-1)] p-3 font-mono text-sm text-[var(--color-text)] focus:outline-none"
        />
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={onValidate}
          disabled={busy !== null}
          data-testid="yaml-editor-validate"
          className="rounded border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-bg-3)] disabled:opacity-60"
        >
          {busy === 'validate' ? 'Validating…' : 'Validate (dry-run)'}
        </button>
        <button
          type="button"
          onClick={onApply}
          disabled={busy !== null || !dirty}
          data-testid="yaml-editor-apply"
          className="rounded border border-emerald-500 bg-emerald-600 px-3 py-1.5 text-sm text-white hover:bg-emerald-500 disabled:opacity-60"
        >
          {busy === 'apply'
            ? 'Applying…'
            : onCommit
              ? (commitLabel ?? 'Commit IaC')
              : flux
                ? 'Open PR'
                : 'Apply'}
        </button>
        {validateMsg && (
          <span data-testid="yaml-editor-validate-ok" className="text-xs text-emerald-300">
            {validateMsg}
          </span>
        )}
        {validateError && (
          <span data-testid="yaml-editor-validate-err" className="text-xs text-rose-300">
            {validateError}
          </span>
        )}
        {applyMsg && (
          <span data-testid="yaml-editor-apply-ok" className="text-xs text-emerald-300">
            {applyMsg}
          </span>
        )}
        {prInfo && prInfo.prURL && (
          <a
            data-testid="yaml-editor-pr-link"
            href={prInfo.prURL}
            target="_blank"
            rel="noreferrer noopener"
            className="text-xs text-emerald-200 underline"
          >
            Open PR #{prInfo.prNumber}
          </a>
        )}
        {applyError && (
          <span data-testid="yaml-editor-apply-err" className="text-xs text-rose-300">
            {applyError}
          </span>
        )}
      </div>
    </div>
  )
}

/** orgFromObject — extract the canonical Org slug from a K8s object's
 *  labels (per slice I labels) with the namespace as fallback. Empty
 *  namespace (cluster-scoped) → "" (the server rejects with 400). */
function orgFromObject(obj: K8sObject | null | undefined, ns: string | undefined): string {
  const labels = (obj?.metadata as { labels?: Record<string, string> } | undefined)?.labels
  const fromLabel = labels?.['catalyst.openova.io/organization']
  if (fromLabel && fromLabel.trim() !== '') return fromLabel.trim()
  return (ns ?? '').trim()
}

/** editPathFromObject — repo-relative path the edit-pr handler writes
 *  to. Mirrors the Flux convention `<kind>/<ns>/<name>.yaml` so the
 *  per-Org shared-blueprints repo organises edits by resource. The
 *  blueprint-controller's reconciler ignores cross-bp edits. */
function editPathFromObject(
  obj: K8sObject | null | undefined,
  kind: string,
  ns: string | undefined,
  name: string,
): string {
  const segments = ['edits', kind, ns && ns.trim() !== '' ? ns : '_cluster', `${name}.yaml`]
  return segments.join('/')
  // obj is reserved for future namespace-or-label-derived path overrides.
  void obj
}
