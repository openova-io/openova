/**
 * ResourcesApplyPage — full target-state YAML-apply surface (qa-loop
 * iter-12 Fix #50). Replaces the iter-6 stub at
 * `pages/sovereign/stubs/ResourcesApplyPage.tsx` with a wired editor
 * that POSTs to `/api/v1/sovereigns/{id}/k8s/apply` (multi-doc YAML).
 *
 * URL: /app/$deploymentId/resources/apply
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall)     — multi-doc YAML support + per-doc result rows
 *                         + Flux-managed PR-link fallback land at first cut.
 *   #2 (quality)       — no "(pending live editor)" placeholder.
 *   #3 (event-driven)  — TanStack Mutation; UI re-renders on settle.
 *   #4 (never hardcode)— endpoint URL derives from API_BASE via
 *                         resources.api.ts.
 *
 * Per `feedback_per_issue_playwright_verification.md` the page surfaces
 * matrix-asserted tokens TC-270: "Apply", "YAML"; TC-271: "created",
 * "ConfigMap" appear after a successful apply.
 */

import { useState } from 'react'
import { useParams } from '@tanstack/react-router'
import { useMutation } from '@tanstack/react-query'

import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { multiApplyYAML, type K8sMultiApplyResponse } from './resources.api'

const PLACEHOLDER_YAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: example-config
  namespace: default
data:
  key1: "value1"
  key2: "value2"
---
# add more documents separated by '---'
`

export function ResourcesApplyPage() {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''

  const [yaml, setYaml] = useState('')
  const [commitMessage, setCommitMessage] = useState('')

  const mutation = useMutation<K8sMultiApplyResponse, Error, void>({
    mutationFn: () =>
      multiApplyYAML(deploymentId, {
        yaml,
        commitMessage: commitMessage || undefined,
      }),
  })

  const result = mutation.data

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="resources-apply-page">
        <div>
          <h2 className="text-xl font-semibold text-[var(--color-text)]">Apply YAML</h2>
          <p className="text-sm text-[oklch(55%_0.01_250)]">
            Apply one or more Kubernetes manifests to <code>{deploymentId}</code>.
            Multi-document YAML (separated by <code>---</code>) is supported. Flux-managed
            namespaces route the change to a Gitea pull request instead of a direct apply.
          </p>
        </div>

        <textarea
          className="h-96 w-full rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-1,#020617)] p-3 font-mono text-xs text-[var(--color-text,#e2e8f0)] placeholder:text-[var(--color-text-dim,#94a3b8)]"
          placeholder={PLACEHOLDER_YAML}
          value={yaml}
          onChange={(e) => setYaml(e.target.value)}
          data-testid="apply-yaml-editor"
          spellCheck={false}
        />

        <div className="flex flex-wrap items-center gap-3">
          <label className="text-xs text-[var(--color-text-dim,#94a3b8)]">
            Commit message (Flux PRs)
            <input
              className="ml-2 w-80 rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] px-2 py-1 text-sm text-[var(--color-text,#e2e8f0)]"
              value={commitMessage}
              onChange={(e) => setCommitMessage(e.target.value)}
              placeholder="Operator: apply ConfigMap example-config"
              data-testid="apply-commit-message"
            />
          </label>
          <button
            type="button"
            className="rounded bg-[var(--color-brand-500,#6366f1)] px-4 py-2 text-sm text-white hover:opacity-90 disabled:opacity-40"
            onClick={() => mutation.mutate()}
            disabled={!yaml.trim() || mutation.isPending || !deploymentId}
            data-testid="apply-submit"
          >
            {mutation.isPending ? 'Applying…' : 'Apply'}
          </button>
          {mutation.isPending && (
            <span className="text-xs text-[var(--color-text-dim,#94a3b8)]">running…</span>
          )}
        </div>

        {mutation.isError && (
          <div
            className="rounded border border-red-700/40 bg-red-900/20 p-3 text-sm text-red-200"
            data-testid="apply-error"
            role="alert"
          >
            Apply failed: {(mutation.error as Error)?.message ?? 'unknown error'}
          </div>
        )}

        {result && (
          <div className="space-y-2" data-testid="apply-results">
            <div className="text-sm text-[var(--color-text,#e2e8f0)]">
              <span className="font-semibold text-emerald-300">{result.created}</span> created ·{' '}
              <span className="font-semibold text-blue-300">{result.updated}</span> updated ·{' '}
              <span className="font-semibold text-red-300">
                {(result.items ?? []).filter((r) => !!r.error).length}
              </span>{' '}
              errors
            </div>
            <ul className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] divide-y divide-[var(--color-border,#1f2937)]">
              {(result.items ?? []).map((r, i) => (
                <li
                  key={`${r.kind}/${r.namespace}/${r.name}/${i}`}
                  className="px-3 py-2 text-sm text-[var(--color-text,#e2e8f0)]"
                  data-testid={`apply-result-row-${r.kind}-${r.namespace ?? '_'}-${r.name}`}
                >
                  <span className="font-mono">{r.kind}</span>
                  {r.namespace && (
                    <span className="ml-2 text-xs text-[var(--color-text-dim,#94a3b8)]">ns=<code>{r.namespace}</code></span>
                  )}
                  <span className="ml-2 font-mono">{r.name}</span>
                  <span
                    className={
                      'ml-3 inline-block rounded px-2 py-0.5 text-[10px] font-semibold uppercase ' +
                      (r.error
                        ? 'bg-red-900/40 text-red-200'
                        : r.flux
                          ? 'bg-amber-900/40 text-amber-200'
                          : r.created
                            ? 'bg-emerald-900/40 text-emerald-200'
                            : 'bg-blue-900/40 text-blue-200')
                    }
                  >
                    {r.error ? 'error' : r.flux ? 'pull request' : r.created ? 'created' : 'updated'}
                  </span>
                  {r.giteaPRUrl && (
                    <a
                      href={r.giteaPRUrl}
                      target="_blank"
                      rel="noreferrer"
                      className="ml-3 text-xs underline text-[var(--color-brand-300,#a5b4fc)]"
                    >
                      open PR
                    </a>
                  )}
                  {r.error && (
                    <div className="mt-1 text-xs text-red-200">{r.error}</div>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </PortalShell>
  )
}
