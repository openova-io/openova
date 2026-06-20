/**
 * ParentDomainsPage — admin "Parent Domains" surface (issue #829, parent
 * epic #825).
 *
 * Mounted at /console/parent-domains in the SovereignConsoleLayout.
 * Visible only to operator-admin role (gating handled at route level
 * via the same RequireSession guard the rest of /console/* uses).
 *
 * Layout:
 *   • Header with title + "+ Add another domain" CTA
 *   • Table: Name | Role | Status | Registrar | Added | (Delete)
 *   • Each row expands inline into a PropagationPanel
 *   • Add modal: name, role, registrarKind, registrarToken
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target-state shape): the page renders the empty
 *       state on first paint while the fetch runs in the background; the
 *       table replaces the empty state once the response lands.
 *   #4 (never hardcode): every URL flows through the
 *       parentDomains.api.ts wrappers; no inline `/api/...` strings.
 *   #10 (credential hygiene): the registrarToken field uses input
 *       type="password" so the operator's screen recording / shoulder
 *       surfing risk is minimised. The token is sent once on POST and
 *       never returned in the GET list response.
 */

import { useEffect, useState } from 'react'
import {
  addParentDomain,
  deleteParentDomain,
  flipStatusLabel,
  flipStatusTone,
  listParentDomains,
  type AddParentDomainRequest,
  type ParentDomain,
  type ParentDomainRole,
} from './parentDomains.api'
import { PropagationPanel } from './PropagationPanel'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface ParentDomainsPageProps {
  /** Test seam — supplies the initial list synchronously. */
  initialItems?: ParentDomain[]
  /** Test seam — disables fetch + propagation polling. */
  disableFetch?: boolean
}

export function ParentDomainsPage({
  initialItems,
  disableFetch = false,
}: ParentDomainsPageProps = {}) {
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''
  const { deploymentId } = useResolvedDeploymentId()
  const [items, setItems] = useState<ParentDomain[]>(initialItems ?? [])
  const [loading, setLoading] = useState<boolean>(!initialItems && !disableFetch)
  const [error, setError] = useState<string | null>(null)
  const [showModal, setShowModal] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)

  async function refresh() {
    try {
      setLoading(true)
      const rows = await listParentDomains()
      setItems(rows)
      setError(null)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (disableFetch) return
    void refresh()
    // refresh is intentionally re-created on every render; we only want
    // to refire on disableFetch changes (i.e. test seam toggle).
  }, [disableFetch])

  async function onDelete(item: ParentDomain) {
    if (item.role === 'primary') {
      alert('Cannot remove the Sovereign primary domain.')
      return
    }
    if (
      !confirm(
        `Remove parent domain "${item.name}"? SMEs already using subdomains under this zone will continue to work; only NEW SME signups stop being offered this domain.`,
      )
    ) {
      return
    }
    try {
      await deleteParentDomain(item.name)
      setItems((rows) => rows.filter((r) => r.name !== item.name))
    } catch (err) {
      setError((err as Error).message)
    }
  }

  return (
    <PortalShell
      deploymentId={deploymentId ?? ''}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Parent Domains"
    >
    <div data-testid="parent-domains-page">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <p className="text-sm text-[var(--color-text-dim)]">
            Domains served by this Sovereign's PowerDNS. The primary hosts your console + API; pool domains are offered to Organizations for free subdomain allocation.
          </p>
        </div>
        <button
          type="button"
          data-testid="parent-domains-add-cta"
          onClick={() => setShowModal(true)}
          className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-white hover:opacity-90"
        >
          + Add another domain
        </button>
      </div>

      {error ? (
        <div
          data-testid="parent-domains-error"
          className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300"
        >
          {error}
        </div>
      ) : null}

      {loading ? (
        <div data-testid="parent-domains-loading" className="text-sm text-[var(--color-text-dim)]">
          Loading…
        </div>
      ) : items.length === 0 ? (
        <div
          data-testid="parent-domains-empty"
          className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
        >
          No parent domains in the pool yet. Click "+ Add another domain" to flip a domain's NS records to this Sovereign.
        </div>
      ) : (
        <table
          data-testid="parent-domains-table"
          className="w-full border-collapse text-sm"
        >
          <thead>
            <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
              <th className="py-2 pr-3">Domain</th>
              <th className="py-2 pr-3">Role</th>
              <th className="py-2 pr-3">Status</th>
              <th className="py-2 pr-3">Registrar</th>
              <th className="py-2 pr-3">Added</th>
              <th className="py-2 pr-3 text-right" aria-label="actions" />
            </tr>
          </thead>
          <tbody>
            {items.map((item) => {
              const isExpanded = expanded === item.name
              return (
                <ParentDomainRow
                  key={item.name}
                  item={item}
                  expanded={isExpanded}
                  disablePolling={disableFetch}
                  onToggle={() => setExpanded(isExpanded ? null : item.name)}
                  onDelete={() => onDelete(item)}
                />
              )
            })}
          </tbody>
        </table>
      )}

      {showModal && (
        <AddDomainModal
          onClose={() => setShowModal(false)}
          onCreated={async (created) => {
            setShowModal(false)
            setItems((rows) => [...rows.filter((r) => r.name !== created.name), created])
            setExpanded(created.name)
            // Background refresh so any stub rows produced server-side
            // are reconciled.
            void refresh()
          }}
        />
      )}
    </div>
    </PortalShell>
  )
}

interface RowProps {
  item: ParentDomain
  expanded: boolean
  disablePolling: boolean
  onToggle: () => void
  onDelete: () => void
}

function ParentDomainRow({ item, expanded, disablePolling, onToggle, onDelete }: RowProps) {
  return (
    <>
      <tr
        data-testid={`parent-domain-row-${item.name}`}
        className="border-b border-[var(--color-border)] hover:bg-[var(--color-bg-2)]"
      >
        <td className="py-2 pr-3">
          <button
            type="button"
            data-testid={`parent-domain-toggle-${item.name}`}
            onClick={onToggle}
            className="flex items-center gap-1 font-mono text-[var(--color-text)] hover:underline"
          >
            <span aria-hidden>{expanded ? '▾' : '▸'}</span>
            {item.name}
          </button>
        </td>
        <td className="py-2 pr-3">
          <span
            data-testid={`parent-domain-role-${item.name}`}
            className={`rounded-full px-2 py-0.5 text-xs ${
              item.role === 'primary'
                ? 'bg-blue-500/15 text-blue-300'
                : 'bg-purple-500/15 text-purple-300'
            }`}
          >
            {item.role}
          </span>
        </td>
        <td className="py-2 pr-3">
          <FlipStatusBadge item={item} />
        </td>
        <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
          {item.registrarKind ?? '—'}
        </td>
        <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
          {item.addedAt && item.addedAt.startsWith('0001')
            ? '—'
            : new Date(item.addedAt).toLocaleDateString()}
        </td>
        <td className="py-2 pr-3 text-right">
          {item.role === 'primary' ? (
            <span className="text-xs text-[var(--color-text-dim)]">locked</span>
          ) : (
            <button
              type="button"
              data-testid={`parent-domain-delete-${item.name}`}
              onClick={onDelete}
              className="text-xs text-red-400 hover:underline"
            >
              Remove
            </button>
          )}
        </td>
      </tr>
      {expanded && (
        <tr data-testid={`parent-domain-drawer-${item.name}`}>
          <td
            colSpan={6}
            className="bg-[var(--color-bg-2)] border-b border-[var(--color-border)]"
          >
            <PropagationPanel domainName={item.name} disablePolling={disablePolling} />
          </td>
        </tr>
      )}
    </>
  )
}

function FlipStatusBadge({ item }: { item: ParentDomain }) {
  const tone = flipStatusTone(item.flipStatus)
  const label = flipStatusLabel(item.flipStatus)
  return (
    <span
      data-testid={`parent-domain-status-${item.name}`}
      title={item.flipMessage}
      className={`inline-block rounded-full px-2 py-0.5 text-[11px] font-semibold ${
        tone === 'green'
          ? 'bg-emerald-500/15 text-emerald-300'
          : tone === 'amber'
            ? 'bg-amber-500/15 text-amber-300'
            : tone === 'red'
              ? 'bg-red-500/15 text-red-300'
              : 'bg-blue-500/15 text-blue-300'
      }`}
    >
      {label}
    </span>
  )
}

interface AddDomainModalProps {
  onClose: () => void
  onCreated: (item: ParentDomain) => void
}

function AddDomainModal({ onClose, onCreated }: AddDomainModalProps) {
  const [name, setName] = useState('')
  const [role, setRole] = useState<ParentDomainRole>('sme-pool')
  const [registrarKind, setRegistrarKind] = useState('dynadot')
  const [token, setToken] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!name.trim() || !token.trim()) {
      setError('Name and registrar token are required.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const req: AddParentDomainRequest = {
        name: name.trim(),
        role,
        registrarKind,
        registrarToken: token,
      }
      const created = await addParentDomain(req)
      onCreated(created)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div
      data-testid="add-domain-modal"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onClose}
      role="presentation"
    >
      <form
        onSubmit={onSubmit}
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-6 shadow-xl"
      >
        <h2 className="mb-1 text-lg font-semibold text-[var(--color-text-strong)]">
          Add another parent domain
        </h2>
        <p className="mb-4 text-xs text-[var(--color-text-dim)]">
          OpenOva flips this domain's NS records via the registrar API, bootstraps the PowerDNS zone
          on this Sovereign, and issues a wildcard cert via cert-manager. Propagation across public
          resolvers takes up to 48h (gTLD NS TTL).
        </p>

        {error && (
          <div
            data-testid="add-domain-error"
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300"
          >
            {error}
          </div>
        )}

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">Domain</div>
          <input
            data-testid="add-domain-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="omani.trade"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            autoFocus
          />
        </label>

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">Role</div>
          <select
            data-testid="add-domain-role"
            value={role}
            onChange={(e) => setRole(e.target.value as ParentDomainRole)}
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          >
            <option value="sme-pool">pool — offered to Organizations</option>
            <option value="primary">primary — operator's own domain</option>
          </select>
        </label>

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">Registrar</div>
          <select
            data-testid="add-domain-registrar"
            value={registrarKind}
            onChange={(e) => setRegistrarKind(e.target.value)}
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          >
            <option value="dynadot">Dynadot</option>
            <option value="cloudflare">Cloudflare</option>
            <option value="namecheap">Namecheap</option>
            <option value="godaddy">GoDaddy</option>
            <option value="ovh">OVH</option>
          </select>
        </label>

        <label className="mb-4 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Registrar API token
          </div>
          <input
            data-testid="add-domain-token"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="••••••••••••••••"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm font-mono text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            autoComplete="off"
          />
          <div className="mt-1 text-[10px] text-[var(--color-text-dim)]">
            Used once to flip the NS records. Never logged or persisted in plaintext.
          </div>
        </label>

        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md px-3 py-1.5 text-sm text-[var(--color-text-dim)] hover:bg-[var(--color-bg-2)]"
          >
            Cancel
          </button>
          <button
            type="submit"
            data-testid="add-domain-submit"
            disabled={submitting}
            className="rounded-md bg-[var(--color-accent)] px-4 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {submitting ? 'Adding…' : 'Add domain'}
          </button>
        </div>
      </form>
    </div>
  )
}
