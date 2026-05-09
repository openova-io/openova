/**
 * PublishPage — EPIC-2 Slice P (#1097): Org owner publishes a Blueprint
 * to `<org>/shared-blueprints` Gitea repo.
 *
 * Form fields:
 *   • Blueprint name (e.g. bp-acme-internal)
 *   • Version (semver)
 *   • Org slug
 *   • blueprint.yaml inline editor (a base64 chartTarball field is
 *     surfaced for future expansion but not required)
 *
 * On publish → POST /api/v1/sovereigns/{id}/blueprints/publish.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from API_BASE
 * via the catalog.api helpers. Per #5 the underlying handler enforces
 * tier-admin or higher; this page renders the form even for viewers
 * but the server is the gate.
 */

import { useState } from 'react'

import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { publishBlueprint, type BlueprintPublishResponse } from '@/lib/catalog.api'

const BLUEPRINT_TEMPLATE = `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-my-app
spec:
  version: 1.0.0
  card:
    title: My App
    summary: Short description
    description: Longer description for the catalog listing.
    category: my-org
  manifests:
    chart: my-app
    source:
      kind: HelmRepository
      ref: bitnami
  configSchema:
    type: object
    required: [domain]
    properties:
      domain:
        type: string
        description: FQDN for the public route.
  placementSchema:
    modes: [single-region, active-active, active-hotstandby]
    default: single-region
    minRegions: 1
    maxRegions: 5
`

export interface PublishPageProps {
  /** Test seam — bypass API call. */
  disableNetwork?: boolean
  /** Test seam — pre-populated YAML. */
  initialYaml?: string
  /** Test seam — pre-populated org. */
  initialOrg?: string
}

export function PublishPage({
  disableNetwork = false,
  initialYaml,
  initialOrg,
}: PublishPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const [org, setOrg] = useState<string>(initialOrg ?? '')
  const [name, setName] = useState<string>('bp-my-app')
  const [version, setVersion] = useState<string>('1.0.0')
  const [yaml, setYaml] = useState<string>(initialYaml ?? BLUEPRINT_TEMPLATE)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [info, setInfo] = useState<BlueprintPublishResponse | null>(null)

  const valid = org.trim() && name.trim().startsWith('bp-') && version.trim() && yaml.trim()

  const onPublish = async () => {
    if (!valid || !deploymentId) return
    if (disableNetwork) {
      setInfo({
        org,
        name,
        version,
        repo: 'shared-blueprints',
        path: `${name}/blueprint.yaml`,
        url: `/${org}/shared-blueprints/src/branch/main/${name}/blueprint.yaml`,
        message: 'Blueprint published (test seam — no network).',
      })
      return
    }
    setBusy(true)
    setError(null)
    setInfo(null)
    try {
      const resp = await publishBlueprint(deploymentId, {
        org: org.trim(),
        name: name.trim(),
        version: version.trim(),
        blueprintYaml: yaml,
      })
      setInfo(resp)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="p-6" data-testid="publish-page">
      <h1 className="mb-4 text-2xl font-semibold text-[var(--color-text)]">Publish Blueprint</h1>
      <p className="mb-4 text-xs text-[var(--color-text-dim)]">
        Push a Blueprint to your Org's <code className="font-mono">shared-blueprints</code>{' '}
        repo. The blueprint-controller will reconcile within ~1 min and the catalog will
        surface it under your Org's private listing. A sovereign-admin can later promote
        it to the cluster-wide catalog via the Curate page.
      </p>

      <div className="mb-4 grid grid-cols-1 gap-3 md:grid-cols-3">
        <label className="flex flex-col text-xs text-[var(--color-text-dim)]">
          Org slug
          <input
            type="text"
            className="mt-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
            data-testid="publish-page-org"
            value={org}
            placeholder="acme"
            onChange={(e) => setOrg(e.target.value)}
          />
        </label>
        <label className="flex flex-col text-xs text-[var(--color-text-dim)]">
          Blueprint name (must start with bp-)
          <input
            type="text"
            className="mt-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
            data-testid="publish-page-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className="flex flex-col text-xs text-[var(--color-text-dim)]">
          Version (semver)
          <input
            type="text"
            className="mt-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
            data-testid="publish-page-version"
            value={version}
            onChange={(e) => setVersion(e.target.value)}
          />
        </label>
      </div>

      <label className="mb-4 block text-xs text-[var(--color-text-dim)]">
        blueprint.yaml
        <textarea
          className="mt-1 h-96 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-elev)] px-3 py-2 text-xs font-mono leading-5"
          data-testid="publish-page-yaml"
          value={yaml}
          onChange={(e) => setYaml(e.target.value)}
        />
      </label>

      {error ? (
        <div
          className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
          data-testid="publish-page-error"
        >
          {error}
        </div>
      ) : null}

      {info ? (
        <div
          className="mb-3 rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2 text-xs text-green-400"
          data-testid="publish-page-success"
        >
          Published <code className="font-mono">{info.name}@{info.version}</code> to{' '}
          <code className="font-mono">{info.org}/{info.repo}/{info.path}</code>. {info.message}
        </div>
      ) : null}

      <div className="flex gap-2">
        <button
          type="button"
          className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
          data-testid="publish-page-submit"
          disabled={!valid || busy}
          onClick={onPublish}
        >
          {busy ? 'Publishing…' : 'Publish'}
        </button>
      </div>
    </div>
  )
}
