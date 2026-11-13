/**
 * EndpointsTab — Application Endpoints/Ingress surface (issue #2742, G117.3).
 *
 * Read-only. The tab renders the Application's resolved endpoints via
 * GET /catalyst/v1/apps/{id}/endpoints (falls back to the catalyst-api
 * `externalURL` while the list loads / for apps that only surface a
 * single front-door HTTPRoute). An endpoint comes from the Blueprint's
 * declared endpoints; the console has no action that creates, edits or
 * deletes one.
 *
 * Per #4 every URL flows through catalog.api helpers, never a literal.
 * Per memory feedback_ts_field_casing_vs_go_json_tag the wire type
 * (ResolvedEndpoint) matches the Go json tags verbatim.
 */

import { useQuery } from '@tanstack/react-query'
import { listEndpoints, type ResolvedEndpoint } from '@/lib/catalog.api'

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
  // returns none (apps that only expose a front-door HTTPRoute).
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
        </div>
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
    </div>
  )
}
