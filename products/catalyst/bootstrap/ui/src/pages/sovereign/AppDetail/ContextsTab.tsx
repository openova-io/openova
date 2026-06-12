/**
 * ContextsTab — #3370. The Contexts of ONE shareable instance.
 *
 * A Context is the first-class child entity inside a shareable instance
 * that one consumer application occupies — for postgres a logical
 * database+role, for kafka a topic-set, for valkey a keyspace, for
 * seaweedfs a bucket. The Blueprint's contextSchema declaration defines
 * what a Context materializes as; this table renders ONLY the generic
 * projection the API serves (`contexts[]` on the application detail) —
 * the SAME component for every shareable blueprint, zero per-service
 * special-casing.
 *
 * Entity-first rows:
 *
 *   | Context   | Occupied by | Credential            | Status |
 *   | db/gitea  | gitea →     | gitea-database-secret | ready  |
 *
 * The kind prefix (db/, topic/, bucket/, keyspace/) comes from the
 * contextSchema via the API's `kind` field. Status derives from the
 * materialized IaC (credential Secret reflected ⇒ ready).
 */

import { Link } from '@tanstack/react-router'
import type { InstanceContext } from '@/lib/catalog.api'

export function ContextsTab({ contexts }: { contexts: InstanceContext[] }) {
  if (!contexts || contexts.length === 0) {
    return (
      <p className="section-hint" data-testid="sov-app-contexts-empty">
        No Contexts declared on this instance yet. Consumers occupy a
        Context when they are provisioned against this instance (Create
        new / Reuse existing in the provisioning journey) or when the
        instance&apos;s IaC declares one.
      </p>
    )
  }
  return (
    <table
      data-testid="sov-contexts-table"
      style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.82rem' }}
    >
      <thead>
        <tr
          style={{
            textAlign: 'left',
            color: 'var(--color-text-dim)',
            borderBottom: '1px solid var(--color-border)',
          }}
        >
          <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Context</th>
          <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Occupied by</th>
          <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Credential</th>
          <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Status</th>
        </tr>
      </thead>
      <tbody>
        {contexts.map((c) => (
          <tr
            key={`${c.kind}-${c.name}`}
            data-testid={`sov-context-row-${c.kind}-${c.name}`}
            style={{ borderBottom: '1px solid var(--color-border)' }}
          >
            <td style={{ padding: '0.45rem 0.5rem' }}>
              <code data-testid={`sov-context-entity-${c.name}`}>
                {c.kind}/{c.name}
              </code>
            </td>
            <td style={{ padding: '0.45rem 0.5rem' }}>
              {c.occupiedBy ? (
                <Link
                  to={'/app/$componentId' as never}
                  params={{ componentId: `bp-${c.occupiedBy}` } as never}
                  data-testid={`sov-context-consumer-${c.name}`}
                  style={{ color: 'var(--color-accent)', fontWeight: 600 }}
                >
                  {c.occupiedBy} →
                </Link>
              ) : (
                <span className="section-hint" style={{ margin: 0 }}>
                  —
                </span>
              )}
            </td>
            <td style={{ padding: '0.45rem 0.5rem' }}>
              {c.credential ? <code>{c.credential}</code> : '—'}
            </td>
            <td style={{ padding: '0.45rem 0.5rem' }}>
              <span
                className={`chip ${
                  c.status === 'ready'
                    ? 'chip-installed'
                    : c.status === 'pending'
                      ? 'chip-pending'
                      : 'chip-cat'
                }`}
                data-testid={`sov-context-status-${c.name}`}
              >
                {c.status}
              </span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
