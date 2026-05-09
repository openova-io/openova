/**
 * ResourcesTab — EPIC-4 Slice R (#1099) — live resource view for the
 * focused Application. Replaces the EPIC-2 placeholder. Surfaces a
 * quick-navigation grid into the cloud-list / resource detail flow
 * that ships with slice R.
 *
 * The full per-application filter wiring (e.g. only Pods owned by a
 * specific Deployment that this Application installed) lands when the
 * cloud-list gains Application-aware label filters; for now this tab
 * routes the user into the live cloud-list view by kind.
 */

import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

const QUICK_LINKS: ReadonlyArray<{ kind: string; label: string }> = [
  { kind: 'pods', label: 'Pods' },
  { kind: 'deployments', label: 'Deployments' },
  { kind: 'services', label: 'Services' },
  { kind: 'ingresses', label: 'Ingresses' },
  { kind: 'configmaps', label: 'ConfigMaps' },
  { kind: 'secrets', label: 'Secrets' },
  { kind: 'pvcs', label: 'PVCs' },
] as const

export interface ResourcesTabProps {
  applicationName: string
}

export function ResourcesTab({ applicationName }: ResourcesTabProps) {
  const { deploymentId: chrootDepId } = useResolvedDeploymentId()
  const deploymentId = chrootDepId ?? ''
  const cloudPath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/cloud'
      : `/provision/${deploymentId}/cloud`

  return (
    <div className="resources-tab space-y-3" data-testid="app-resources-tab">
      <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Live Resources</h3>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          Browse the live K8s objects backing{' '}
          <code className="font-mono text-[var(--color-text)]">{applicationName}</code>. Click any
          row to open the detail page (Overview / YAML / Events / Metrics / Tree).
        </p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          {QUICK_LINKS.map((q) => (
            <a
              key={q.kind}
              href={`${cloudPath}?view=list&kind=${q.kind}`}
              data-testid={`app-resources-link-${q.kind}`}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg-1)] px-3 py-2 text-sm text-[var(--color-text)] hover:bg-[var(--color-bg-3)]"
            >
              {q.label}
            </a>
          ))}
        </div>
      </div>
    </div>
  )
}
