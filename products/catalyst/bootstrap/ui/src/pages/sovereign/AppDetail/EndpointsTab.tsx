/**
 * EndpointsTab — Application Endpoints/Ingress surface (issue #2742).
 *
 * The app-detail strip had no Endpoints tab — UAT TC-18 + the live hw99
 * walk (2026-06-07) confirmed it absent. This tab now renders, per
 * docs/INVIOLABLE-PRINCIPLES.md #1 (target-state shape on first paint):
 *
 *   1. The Application's exposed HTTPRoutes via
 *      GET /catalyst/v1/apps/{id}/endpoints (falls back to the
 *      catalyst-api `externalURL` while the list loads / for apps that
 *      only surface a single front-door).
 *   2. An edit form that mutates an endpoint's hostname / TLS via
 *      POST|PATCH /apps/{id}/endpoints — the backend raises a GOVERNED
 *      Git-IaC pull request (kyverno-admission / cert-manager-precheck /
 *      dns-conflict-precheck status checks gate the merge) and returns
 *      its URL, which we render as a clickable "View PR" link plus the
 *      precheck results. This replaces the prior `pending-api` marker —
 *      the catalyst-api handler (endpoint_handler.go) + giteapr writer
 *      already back this flow.
 *
 * Per #4 every URL flows through catalog.api helpers, never a literal.
 */

import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  listEndpoints,
  mutateEndpoint,
  type EndpointSummary,
  type EndpointMutationResponse,
} from '@/lib/catalog.api'

interface EndpointsTabProps {
  applicationName: string
  /** Application CR uid — the {id} path param for the endpoints API. */
  appUID: string
  /** Front-door URL fallback while the list query loads or is empty. */
  externalURL: string
  sovereignFQDN: string | null
}

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

  const items: EndpointSummary[] = endpointsQuery.data?.items ?? []
  const fallbackHost = externalURL
    ? externalURL.replace(/^https?:\/\//, '').replace(/\/$/, '')
    : ''
  // Synthesize a single row from externalURL when the API returns none
  // (apps that only expose a front-door HTTPRoute).
  const rows: EndpointSummary[] =
    items.length > 0
      ? items
      : fallbackHost
        ? [{ name: applicationName, hostname: fallbackHost, protocol: 'https', tls: true, status: 'Ready' }]
        : []

  return (
    <div data-testid="app-tab-endpoints-body">
      <section className="section" data-testid="sov-section-endpoints">
        <h2 style={{ margin: '0 0 0.4rem' }}>Endpoints &amp; Ingress</h2>
        <p className="section-hint" style={{ margin: '0 0 0.6rem' }}>
          External HTTPRoute(s) exposing <code>{applicationName}</code> on this Sovereign.
        </p>

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
                <th style={{ padding: '0.3rem 0.4rem' }}>Hostname</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>Kind</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>TLS</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>Status</th>
                <th style={{ padding: '0.3rem 0.4rem' }}></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((ep) => (
                <tr key={ep.name || ep.hostname} data-testid={`sov-endpoint-row-${ep.hostname}`}>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    <code>{ep.hostname}</code>
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>HTTPRoute</td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    {ep.tls === false ? 'none' : "Let's Encrypt"}
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    <span className="chip chip-cat">{ep.status || 'Ready'}</span>
                  </td>
                  <td style={{ padding: '0.3rem 0.4rem' }}>
                    <a
                      href={`https://${ep.hostname}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="chip chip-cat"
                      data-testid="sov-endpoint-open"
                    >
                      Open &rarr;
                    </a>
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
      </section>

      <EditEndpointForm
        appUID={appUID}
        defaultHostname={rows[0]?.hostname ?? fallbackHost}
        defaultName={rows[0]?.name ?? applicationName}
        defaultTls={rows[0]?.tls !== false}
        isExisting={items.length > 0}
        disabled={!appUID}
        onMutated={() => qc.invalidateQueries({ queryKey: ['app-endpoints', appUID] })}
      />
    </div>
  )
}

/* ─── Edit endpoint → governed Git-IaC PR ───────────────────────────── */

function EditEndpointForm({
  appUID,
  defaultHostname,
  defaultName,
  defaultTls,
  isExisting,
  disabled,
  onMutated,
}: {
  appUID: string
  defaultHostname: string
  defaultName: string
  defaultTls: boolean
  isExisting: boolean
  disabled: boolean
  onMutated: () => void
}) {
  const [hostname, setHostname] = useState(defaultHostname)
  const [tls, setTls] = useState(defaultTls)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<EndpointMutationResponse | null>(null)

  // Re-sync from props once the endpoints query resolves — the row's
  // real hostname/TLS arrive after the initial '' fallback render, and
  // useState only reads the prop on first mount (reviewer note, PR #3071).
  useEffect(() => {
    setHostname(defaultHostname)
  }, [defaultHostname])
  useEffect(() => {
    setTls(defaultTls)
  }, [defaultTls])

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (submitting || disabled) return
    setError(null)
    setResult(null)
    setSubmitting(true)
    try {
      // PATCH an existing endpoint by name; POST a new one when the row
      // is the synthesized externalURL fallback (no real endpoint file
      // yet) so we create rather than write a file named after the app
      // (reviewer note #2, PR #3071).
      const resp = await mutateEndpoint(
        appUID,
        { hostname: hostname.trim(), tls },
        isExisting ? { name: defaultName } : {},
      )
      setResult(resp)
      onMutated()
    } catch (err) {
      setError((err as Error)?.message ?? 'mutation failed')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="section" data-testid="sov-section-endpoint-edit" style={{ marginTop: '0.8rem' }}>
      <h3 style={{ fontSize: '0.9rem', margin: '0 0 0.3rem' }}>Edit endpoint</h3>
      <p className="section-hint" style={{ margin: '0 0 0.5rem' }}>
        Changing the hostname or TLS raises a <strong>governed Git-IaC pull request</strong> against
        this Sovereign&rsquo;s local Gitea (kyverno-admission / cert-manager-precheck /
        dns-conflict-precheck status checks gate the merge).
      </p>
      <form onSubmit={onSubmit} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <input
          type="text"
          data-testid="input-endpoint-hostname"
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
          placeholder="app.example.omani.homes"
          required
          style={{
            flex: '1 1 280px',
            padding: '0.4rem 0.6rem',
            background: 'var(--color-bg)',
            color: 'var(--color-text)',
            border: '1px solid var(--color-border)',
            borderRadius: '4px',
            fontSize: '0.82rem',
          }}
        />
        <label style={{ fontSize: '0.8rem', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
          <input
            type="checkbox"
            data-testid="input-endpoint-tls"
            checked={tls}
            onChange={(e) => setTls(e.target.checked)}
          />
          TLS
        </label>
        <button
          type="submit"
          data-testid="btn-endpoint-raise-pr"
          disabled={submitting || disabled}
          className="btn btn-primary"
          style={{ padding: '0.4rem 0.8rem', fontSize: '0.82rem', fontWeight: 600 }}
        >
          {submitting ? 'Raising PR…' : 'Raise PR'}
        </button>
      </form>

      {result ? (
        <p data-testid="sov-endpoint-pr-result" style={{ margin: '0.5rem 0 0', fontSize: '0.82rem' }}>
          PR raised ({result.status}):{' '}
          {result.prURL ? (
            <a href={result.prURL} target="_blank" rel="noopener noreferrer" data-testid="sov-endpoint-pr-link">
              View PR &rarr;
            </a>
          ) : (
            'pending'
          )}
          {result.preCheckResults && Object.keys(result.preCheckResults).length > 0 ? (
            <span style={{ marginLeft: '0.5rem', color: 'var(--color-text-dim)' }}>
              · prechecks: {Object.keys(result.preCheckResults).length}
            </span>
          ) : null}
        </p>
      ) : null}

      {error ? (
        <p
          data-testid="sov-endpoint-edit-error"
          style={{ margin: '0.5rem 0 0', fontSize: '0.82rem', color: 'var(--color-danger)' }}
        >
          {error}
        </p>
      ) : null}
    </section>
  )
}
