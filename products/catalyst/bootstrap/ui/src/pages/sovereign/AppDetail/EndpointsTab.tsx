/**
 * EndpointsTab — Application Endpoints/Ingress surface (issue #2742).
 *
 * The app-detail strip previously had no Endpoints tab — UAT TC-18 +
 * the live hw99 walk (2026-06-07) confirmed the surface was absent.
 * This tab surfaces, per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state
 * shape on first paint):
 *
 *   1. The app's external front-door endpoint (HTTPRoute hostname) — the
 *      `externalURL` the catalyst-api already resolves from the
 *      Application's exposed HTTPRoute, rendered read-only with a copy +
 *      open affordance.
 *   2. The endpoint-edit → governed Git-IaC PR pipeline (the second half
 *      of #2742) — surfaced but clearly marked `pending-api` so the
 *      operator sees the planned surface without being misled into
 *      thinking it's wired. The edit flow lands once the catalyst-api
 *      endpoint-mutation handler + the Gitea PR pipeline are exposed
 *      (tracked on #2742).
 *
 * Per #4 (never hardcode) the hostname flows from the live
 * `externalURL` prop, never a literal.
 */

interface EndpointsTabProps {
  applicationName: string
  /** The app's external front-door URL (catalyst-api `externalURL`), or '' when the app exposes no HTTPRoute. */
  externalURL: string
  /** Sovereign FQDN, for composing the canonical per-app hostname hint when no externalURL is resolved yet. */
  sovereignFQDN: string | null
}

export function EndpointsTab({ applicationName, externalURL, sovereignFQDN }: EndpointsTabProps) {
  const host = externalURL ? externalURL.replace(/^https?:\/\//, '').replace(/\/$/, '') : ''

  return (
    <div data-testid="app-tab-endpoints-body">
      <section className="section" data-testid="sov-section-endpoints">
        <h2 style={{ margin: '0 0 0.4rem' }}>Endpoints &amp; Ingress</h2>
        <p className="section-hint" style={{ margin: '0 0 0.6rem' }}>
          The external HTTPRoute(s) exposing <code>{applicationName}</code> on this Sovereign.
        </p>

        {externalURL ? (
          <table
            data-testid="sov-endpoints-table"
            style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.82rem' }}
          >
            <thead>
              <tr style={{ textAlign: 'left', color: 'var(--color-text-dim)' }}>
                <th style={{ padding: '0.3rem 0.4rem' }}>Hostname</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>Kind</th>
                <th style={{ padding: '0.3rem 0.4rem' }}>TLS</th>
                <th style={{ padding: '0.3rem 0.4rem' }}></th>
              </tr>
            </thead>
            <tbody>
              <tr data-testid={`sov-endpoint-row-${host}`}>
                <td style={{ padding: '0.3rem 0.4rem' }}>
                  <code>{host}</code>
                </td>
                <td style={{ padding: '0.3rem 0.4rem' }}>HTTPRoute</td>
                <td style={{ padding: '0.3rem 0.4rem' }}>Let&rsquo;s Encrypt (wildcard)</td>
                <td style={{ padding: '0.3rem 0.4rem' }}>
                  <a
                    href={externalURL}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="chip chip-cat"
                    data-testid="sov-endpoint-open"
                  >
                    Open &rarr;
                  </a>
                </td>
              </tr>
            </tbody>
          </table>
        ) : (
          <p className="section-hint" data-testid="sov-endpoints-empty" style={{ margin: 0 }}>
            This Application exposes no external HTTPRoute. Internal-only services are reachable
            cluster-side at their Service DNS{sovereignFQDN ? ` (within ${sovereignFQDN})` : ''}.
          </p>
        )}
      </section>

      <section className="section" data-testid="sov-section-endpoint-edit" style={{ marginTop: '0.8rem' }}>
        <h3 style={{ fontSize: '0.9rem', margin: '0 0 0.3rem' }}>Edit endpoint</h3>
        <p
          className="section-hint"
          data-testid="sov-endpoint-edit-pending-api"
          data-pending-api="true"
          style={{ margin: 0 }}
        >
          Editing the hostname or TLS issuer raises a <strong>governed Git-IaC pull request</strong>{' '}
          against this Sovereign&rsquo;s local Gitea (the kyverno-admission / cert-manager-precheck /
          dns-conflict-precheck status checks gate the merge). This pipeline is{' '}
          <em>pending-api</em> — the catalyst-api endpoint-mutation handler is tracked on{' '}
          <a
            href="https://github.com/openova-io/openova/issues/2742"
            target="_blank"
            rel="noopener noreferrer"
          >
            #2742
          </a>
          .
        </p>
      </section>
    </div>
  )
}
