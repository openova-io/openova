/**
 * EndpointsTab — Application Endpoints/Ingress surface (issue #2742, G117.3).
 *
 * Full editable-endpoints CRUD ported from the dead Svelte console
 * (`products/catalyst/console/src/components/EndpointsTab.svelte`) into
 * the production React tree. The app-detail strip had no working
 * Endpoints CRUD — UAT TC-18 + the live hw99 walk (2026-06-07) confirmed
 * it absent / read-only. This tab now renders, per
 * docs/INVIOLABLE-PRINCIPLES.md #1 (target-state shape on first paint):
 *
 *   1. The Application's resolved endpoints via
 *      GET /catalyst/v1/apps/{id}/endpoints (falls back to the
 *      catalyst-api `externalURL` while the list loads / for apps that
 *      only surface a single front-door HTTPRoute).
 *   2. New / Edit / Delete actions. Each mutation opens a GOVERNED
 *      Git-IaC pull request (POST|PATCH|DELETE /apps/{id}/endpoints) —
 *      kyverno-admission / cert-manager-precheck / dns-conflict-precheck
 *      status checks gate the auto-merge. Before every mutation the
 *      operator sees the PR-consequence dialog; after submit, the
 *      raised PR URL surfaces in a banner (`endpoint-pr-banner`).
 *
 * Per #4 every URL flows through catalog.api helpers, never a literal.
 * Per memory feedback_ts_field_casing_vs_go_json_tag the wire types
 * (ResolvedEndpoint / EndpointMutationRequest / EndpointPR) match the Go
 * json tags verbatim.
 */

import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  listEndpoints,
  createAppEndpoint,
  patchAppEndpoint,
  deleteAppEndpoint,
  type ResolvedEndpoint,
  type EndpointMutationRequest,
  type EndpointPR,
  type EndpointProtocol,
  type EndpointVisibility,
} from '@/lib/catalog.api'

const PROTOCOLS: EndpointProtocol[] = ['https', 'http', 'grpc', 'tcp', 'udp']
const VISIBILITIES: EndpointVisibility[] = ['public', 'private', 'internal']

interface EndpointsTabProps {
  applicationName: string
  /** Application CR uid — the {id} path param for the endpoints API. */
  appUID: string
  /** Front-door URL fallback while the list query loads or is empty. */
  externalURL: string
  sovereignFQDN: string | null
}

type DialogMode = 'closed' | 'create' | 'edit' | 'delete'

export function EndpointsTab({
  applicationName,
  appUID,
  externalURL,
  sovereignFQDN,
}: EndpointsTabProps) {
  const qc = useQueryClient()
  const endpointsQuery = useQuery({
    queryKey: ['app-endpoints', appUID],
    queryFn: () => listEndpoints(appUID),
    enabled: !!appUID,
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: 1,
  })

  const items: ResolvedEndpoint[] = endpointsQuery.data?.items ?? []
  const fallbackHost = externalURL
    ? externalURL.replace(/^https?:\/\//, '').replace(/\/$/, '')
    : ''
  // Synthesize a single read-only row from externalURL when the API
  // returns none (apps that only expose a front-door HTTPRoute). Such a
  // synthesized row has no backing endpoint manifest, so it is NOT
  // editable/deletable — only "New endpoint" applies.
  const synthesized = items.length === 0 && !!fallbackHost
  const rows: ResolvedEndpoint[] =
    items.length > 0
      ? items
      : synthesized
        ? [
            {
              name: applicationName,
              hostnameTemplate: fallbackHost,
              hostname: fallbackHost,
              protocol: 'https',
              tls: true,
              status: 'Ready',
            },
          ]
        : []

  const [dialogMode, setDialogMode] = useState<DialogMode>('closed')
  const [working, setWorking] = useState<EndpointMutationRequest>({ name: '' })
  const [editingName, setEditingName] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [lastPR, setLastPR] = useState<EndpointPR | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  function refresh() {
    qc.invalidateQueries({ queryKey: ['app-endpoints', appUID] })
  }

  function openCreate() {
    setWorking({
      name: '',
      hostname: '',
      port: 443,
      protocol: 'https',
      tls: true,
      visibility: 'public',
      ssoEnabled: true,
    })
    setEditingName(null)
    setError(null)
    setDialogMode('create')
  }

  function openEdit(ep: ResolvedEndpoint) {
    setWorking({
      name: ep.name,
      hostname: ep.hostname,
      port: ep.port,
      protocol: ep.protocol,
      tls: ep.tls,
      visibility: ep.visibility,
      ssoEnabled: ep.ssoEnabled,
    })
    setEditingName(ep.name)
    setError(null)
    setDialogMode('edit')
  }

  function openDelete(name: string) {
    setDeleteTarget(name)
    setError(null)
    setDialogMode('delete')
  }

  function cancel() {
    setDialogMode('closed')
    setEditingName(null)
    setDeleteTarget(null)
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (submitting) return
    setError(null)
    setSubmitting(true)
    try {
      const pr =
        dialogMode === 'edit' && editingName
          ? await patchAppEndpoint(appUID, editingName, working)
          : await createAppEndpoint(appUID, working)
      setLastPR(pr)
      setDialogMode('closed')
      setEditingName(null)
      refresh()
    } catch (err) {
      setError((err as Error)?.message ?? 'mutation failed')
    } finally {
      setSubmitting(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget || submitting) return
    setError(null)
    setSubmitting(true)
    try {
      const pr = await deleteAppEndpoint(appUID, deleteTarget)
      setLastPR(pr)
      setDialogMode('closed')
      setDeleteTarget(null)
      refresh()
    } catch (err) {
      setError((err as Error)?.message ?? 'delete failed')
    } finally {
      setSubmitting(false)
    }
  }

  const canEdit = !!appUID && !synthesized

  return (
    <div data-testid="app-tab-endpoints-body">
      <section className="section" data-testid="sov-section-endpoints">
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            gap: '0.5rem',
            marginBottom: '0.4rem',
          }}
        >
          <h2 style={{ margin: 0 }}>Endpoints &amp; Ingress ({rows.length})</h2>
          {appUID ? (
            <button
              type="button"
              data-testid="endpoint-new"
              onClick={openCreate}
              className="btn btn-primary"
              style={{ padding: '0.35rem 0.7rem', fontSize: '0.82rem', fontWeight: 600 }}
            >
              + New endpoint
            </button>
          ) : null}
        </div>
        <p className="section-hint" style={{ margin: '0 0 0.6rem' }}>
          External HTTPRoute(s) exposing <code>{applicationName}</code> on this Sovereign.
          Changes raise a governed Git-IaC pull request.
        </p>

        {lastPR ? (
          <p
            data-testid="endpoint-pr-banner"
            className="section-hint"
            style={{
              margin: '0 0 0.6rem',
              padding: '0.4rem 0.6rem',
              borderRadius: '4px',
              background: 'var(--color-surface, #1e1b4b)',
            }}
          >
            Change submitted as PR{' '}
            {lastPR.prURL ? (
              <a
                href={lastPR.prURL}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="endpoint-pr-banner-link"
              >
                {lastPR.prURL}
              </a>
            ) : (
              <strong>{lastPR.status}</strong>
            )}{' '}
            — auto-merges on green Kyverno + cert-manager + DNS-conflict checks.
          </p>
        ) : null}

        {endpointsQuery.isPending && !!appUID ? (
          <p className="section-hint" data-testid="sov-endpoints-loading" style={{ margin: 0 }}>
            Loading endpoints…
          </p>
        ) : rows.length > 0 ? (
          <table
            data-testid="sov-endpoints-table"
            style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.82rem' }}
          >
            <thead>
              <tr style={{ textAlign: 'left', color: 'var(--color-text-dim)' }}>
                <th style={{ padding: '0.3rem 0.4rem' }}>Name</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>Hostname</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>Proto</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>TLS</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>SSO</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>Status</th>
                <th style={{ padding: '0.3rem 0.4rem' }}></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((ep) => (
                <tr key={ep.name || ep.hostname} data-testid={`sov-endpoint-row-${ep.hostname}`}>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    <strong>{ep.name}</strong>
                    {ep.launchDefault ? (
                      <span className="chip" style={{ marginLeft: '0.3rem' }}>
                        default
                      </span>
                    ) : null}
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    <code>{ep.hostname}</code>
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    {ep.protocol ?? 'https'}
                    {ep.port ? `:${ep.port}` : ''}
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    {ep.tls === false ? 'none' : "Let's Encrypt"}
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>{ep.ssoEnabled ? 'yes' : 'no'}</td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    <span className="chip chip-cat">{ep.status || 'Ready'}</span>
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem', whiteSpace: 'nowrap' }}>
                    <a
                      href={`${ep.tls === false ? 'http' : 'https'}://${ep.hostname}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="chip chip-cat"
                      data-testid="sov-endpoint-open"
                    >
                      Open &rarr;
                    </a>
                    {canEdit ? (
                      <>
                        <button
                          type="button"
                          data-testid={`endpoint-edit-${ep.name}`}
                          onClick={() => openEdit(ep)}
                          className="chip"
                          style={{ marginLeft: '0.3rem', cursor: 'pointer' }}
                        >
                          Edit
                        </button>
                        <button
                          type="button"
                          data-testid={`endpoint-delete-${ep.name}`}
                          onClick={() => openDelete(ep.name)}
                          className="chip"
                          style={{ marginLeft: '0.3rem', cursor: 'pointer', color: 'var(--color-danger)' }}
                        >
                          Delete
                        </button>
                      </>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="section-hint" data-testid="sov-endpoints-empty" style={{ margin: 0 }}>
            This Application exposes no external HTTPRoute. Internal-only services are reachable
            cluster-side at their Service DNS{sovereignFQDN ? ` (within ${sovereignFQDN})` : ''}.
          </p>
        )}

        {endpointsQuery.isError ? (
          <p
            className="section-hint"
            data-testid="sov-endpoints-error"
            style={{ margin: '0.4rem 0 0', color: 'var(--color-danger)' }}
          >
            Failed to load endpoints: {(endpointsQuery.error as Error)?.message ?? 'unknown error'}
          </p>
        ) : null}

        {!appUID ? (
          <p
            className="section-hint"
            data-testid="sov-section-endpoint-edit-unavailable"
            style={{ margin: '0.6rem 0 0' }}
          >
            Endpoint editing via a governed Git-IaC PR is available for catalog-installed
            Applications. This is a bootstrap-kit component (no catalog instance), so its endpoints
            are managed directly in the Sovereign&rsquo;s GitOps repo rather than from this tab.
          </p>
        ) : null}
      </section>

      {/* ─── Create / Edit dialog ─────────────────────────────────────── */}
      {dialogMode === 'create' || dialogMode === 'edit' ? (
        <EndpointDialog
          mode={dialogMode}
          editingName={editingName}
          working={working}
          setWorking={setWorking}
          submitting={submitting}
          error={error}
          onSubmit={submit}
          onCancel={cancel}
        />
      ) : dialogMode === 'delete' ? (
        <section
          className="section"
          role="dialog"
          aria-modal="true"
          data-testid="endpoint-delete-dialog"
          style={{ marginTop: '0.8rem' }}
        >
          <h3 style={{ fontSize: '0.95rem', margin: '0 0 0.4rem' }}>
            Delete endpoint {deleteTarget}?
          </h3>
          <p
            className="section-hint"
            data-testid="endpoint-consequence"
            style={{
              margin: '0 0 0.6rem',
              padding: '0.4rem 0.6rem',
              borderLeft: '3px solid var(--color-warning, #b45309)',
              borderRadius: '3px',
              background: 'var(--color-surface, #2a2440)',
            }}
          >
            This opens a PR against <code>gitea.&lt;sov&gt;/&lt;org&gt;/iac</code> removing the
            endpoint manifest. The cert-manager Certificate + Gateway HTTPRoute are removed on merge.
          </p>
          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <button
              type="button"
              data-testid="endpoint-delete-confirm"
              onClick={confirmDelete}
              disabled={submitting}
              className="btn"
              style={{
                padding: '0.4rem 0.8rem',
                fontSize: '0.82rem',
                fontWeight: 600,
                color: '#fff',
                background: 'var(--color-danger, #b00020)',
              }}
            >
              {submitting ? 'Raising PR…' : 'Delete — open PR'}
            </button>
            <button
              type="button"
              data-testid="endpoint-dialog-cancel"
              onClick={cancel}
              disabled={submitting}
              className="btn"
              style={{ padding: '0.4rem 0.8rem', fontSize: '0.82rem' }}
            >
              Cancel
            </button>
          </div>
          {error ? (
            <p
              data-testid="endpoint-dialog-error"
              style={{ margin: '0.5rem 0 0', fontSize: '0.82rem', color: 'var(--color-danger)' }}
            >
              {error}
            </p>
          ) : null}
        </section>
      ) : null}
    </div>
  )
}

/* ─── Create / Edit endpoint dialog → governed Git-IaC PR ─────────────── */

function EndpointDialog({
  mode,
  editingName,
  working,
  setWorking,
  submitting,
  error,
  onSubmit,
  onCancel,
}: {
  mode: 'create' | 'edit'
  editingName: string | null
  working: EndpointMutationRequest
  setWorking: React.Dispatch<React.SetStateAction<EndpointMutationRequest>>
  submitting: boolean
  error: string | null
  onSubmit: (e: React.FormEvent) => void
  onCancel: () => void
}) {
  const inputStyle = useMemo<React.CSSProperties>(
    () => ({
      width: '100%',
      padding: '0.4rem 0.6rem',
      background: 'var(--color-bg)',
      color: 'var(--color-text)',
      border: '1px solid var(--color-border)',
      borderRadius: '4px',
      fontSize: '0.82rem',
      marginTop: '0.2rem',
    }),
    [],
  )

  return (
    <section
      className="section"
      role="dialog"
      aria-modal="true"
      data-testid="endpoint-dialog"
      style={{ marginTop: '0.8rem' }}
    >
      <h3 style={{ fontSize: '0.95rem', margin: '0 0 0.4rem' }}>
        {mode === 'create' ? 'New endpoint' : `Edit endpoint ${editingName}`}
      </h3>
      <p
        className="section-hint"
        data-testid="endpoint-consequence"
        style={{
          margin: '0 0 0.6rem',
          padding: '0.4rem 0.6rem',
          borderLeft: '3px solid var(--color-warning, #b45309)',
          borderRadius: '3px',
          background: 'var(--color-surface, #2a2440)',
        }}
      >
        Submitting this opens a <strong>governed Git-IaC pull request</strong> against{' '}
        <code>gitea.&lt;sov&gt;/&lt;org&gt;/iac</code>. Kyverno + cert-manager + DNS-conflict checks
        gate the auto-merge; if a check fails the PR stays open for operator review.
      </p>
      <form onSubmit={onSubmit} style={{ display: 'grid', gap: '0.5rem', maxWidth: '460px' }}>
        <label style={{ fontSize: '0.82rem' }}>
          Name
          <input
            type="text"
            data-testid="endpoint-input-name"
            required
            pattern="^[a-z][a-z0-9-]{0,30}[a-z0-9]$"
            value={working.name}
            disabled={mode === 'edit'}
            onChange={(e) => setWorking((w) => ({ ...w, name: e.target.value }))}
            placeholder="web"
            style={inputStyle}
          />
        </label>
        <label style={{ fontSize: '0.82rem' }}>
          Hostname
          <input
            type="text"
            data-testid="endpoint-input-hostname"
            required
            value={working.hostname ?? ''}
            onChange={(e) => setWorking((w) => ({ ...w, hostname: e.target.value }))}
            placeholder="app.example.omani.homes"
            style={inputStyle}
          />
        </label>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <label style={{ fontSize: '0.82rem', flex: 1 }}>
            Protocol
            <select
              data-testid="endpoint-input-protocol"
              value={working.protocol ?? 'https'}
              onChange={(e) =>
                setWorking((w) => ({ ...w, protocol: e.target.value as EndpointProtocol }))
              }
              style={inputStyle}
            >
              {PROTOCOLS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </label>
          <label style={{ fontSize: '0.82rem', flex: 1 }}>
            Port
            <input
              type="number"
              data-testid="endpoint-input-port"
              min={1}
              max={65535}
              value={working.port ?? ''}
              onChange={(e) =>
                setWorking((w) => ({
                  ...w,
                  port: e.target.value === '' ? undefined : Number(e.target.value),
                }))
              }
              style={inputStyle}
            />
          </label>
          <label style={{ fontSize: '0.82rem', flex: 1 }}>
            Visibility
            <select
              data-testid="endpoint-input-visibility"
              value={working.visibility ?? 'public'}
              onChange={(e) =>
                setWorking((w) => ({ ...w, visibility: e.target.value as EndpointVisibility }))
              }
              style={inputStyle}
            >
              {VISIBILITIES.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div style={{ display: 'flex', gap: '1rem', alignItems: 'center' }}>
          <label style={{ fontSize: '0.82rem', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
            <input
              type="checkbox"
              data-testid="endpoint-input-tls"
              checked={working.tls ?? true}
              onChange={(e) => setWorking((w) => ({ ...w, tls: e.target.checked }))}
            />
            TLS
          </label>
          <label style={{ fontSize: '0.82rem', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
            <input
              type="checkbox"
              data-testid="endpoint-input-sso"
              checked={working.ssoEnabled ?? false}
              onChange={(e) => setWorking((w) => ({ ...w, ssoEnabled: e.target.checked }))}
            />
            SSO enabled
          </label>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.2rem' }}>
          <button
            type="submit"
            data-testid="endpoint-save"
            disabled={submitting}
            className="btn btn-primary"
            style={{ padding: '0.4rem 0.8rem', fontSize: '0.82rem', fontWeight: 600 }}
          >
            {submitting ? 'Raising PR…' : 'Save — open PR'}
          </button>
          <button
            type="button"
            data-testid="endpoint-dialog-cancel"
            onClick={onCancel}
            disabled={submitting}
            className="btn"
            style={{ padding: '0.4rem 0.8rem', fontSize: '0.82rem' }}
          >
            Cancel
          </button>
        </div>
      </form>
      {error ? (
        <p
          data-testid="endpoint-dialog-error"
          style={{ margin: '0.5rem 0 0', fontSize: '0.82rem', color: 'var(--color-danger)' }}
        >
          {error}
        </p>
      ) : null}
    </section>
  )
}
