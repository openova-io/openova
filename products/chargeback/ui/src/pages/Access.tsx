import { useState, type FormEvent } from 'react'
import { api, asList } from '../api/client'
import type { Customer, GroupRoleMapping, RoleBinding, RoleDoc } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Notice, PageHeader, Skeleton } from '../components/ui'
import { SCOPED_CUSTOMER_ROLES, SOVEREIGN_ROLES, can, roleLabel, scopeKindOf } from '../lib/access'
import { when } from '../lib/format'
import { isEmail } from '../lib/forms'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Configure → Access (DESIGN.md §10.9): who holds which role at which scope.
 * Two tables — explicit role bindings (grant, revoke) with the implicit
 * OPERATOR_EMAILS rows shown read-only, and the directory-group mappings the
 * SSO gate's X-Forwarded-Groups header is read against (saved as a whole
 * set). Every row names the scope: the Sovereign, or one customer.
 */

type BindingsDoc = { bindings?: RoleBinding[]; implicit?: RoleBinding[] }
type MappingsDoc = { mappings?: GroupRoleMapping[]; groups_header?: string }
type RolesDoc = { roles?: RoleDoc[]; permissions?: string[] }

const ALL_ROLES = [...SOVEREIGN_ROLES, ...SCOPED_CUSTOMER_ROLES] as const
type AnyRole = (typeof ALL_ROLES)[number]

function scopeText(row: { scope_kind?: string; customer_name?: string; customer_id?: string | null }): string {
  if (row.scope_kind === 'sovereign') return 'Sovereign'
  return row.customer_name || row.customer_id || '—'
}

export function Access() {
  const { me } = useSession()
  const bindings = useQuery<BindingsDoc>('/access/bindings')
  const mappings = useQuery<MappingsDoc>('/access/group-mappings')
  const roles = useQuery<RolesDoc>('/access/roles')
  const customers = useQuery<unknown>('/customers')
  const act = useAction()
  const [revoking, setRevoking] = useState<RoleBinding | null>(null)
  const customerRows = asList<Customer>(customers.data, 'customers')

  if (!can(me, 'settings.manage')) {
    return (
      <div className="stack">
        <PageHeader title="Access" />
        <Notice kind="warn">Access is managed by a sovereign-admin (permission settings.manage). You can see the roles below but not change who holds them.</Notice>
        <RolesCard roles={roles.data?.roles ?? []} />
      </div>
    )
  }
  if (bindings.error && !bindings.data) {
    return (
      <div className="stack">
        <PageHeader title="Access" />
        <Notice kind="bad">{bindings.error}</Notice>
      </div>
    )
  }
  if (!bindings.data) return <Skeleton lines={6} />

  const explicit = bindings.data.bindings ?? []
  const implicit = bindings.data.implicit ?? []
  const rows: RoleBinding[] = [...implicit.map((b) => ({ ...b, id: `config:${b.subject_email}` })), ...explicit]

  const columns: Column<RoleBinding>[] = [
    { key: 'email', header: 'Email', value: (b) => b.subject_email, render: (b) => <span className="mono">{b.subject_email}</span> },
    { key: 'role', header: 'Role', value: (b) => b.role, render: (b) => <Badge status={roleLabel(String(b.role))} kind={b.scope_kind === 'sovereign' ? 'info' : undefined} /> },
    { key: 'scope', header: 'Scope', value: (b) => scopeText(b) },
    {
      key: 'granted',
      header: 'Granted',
      value: (b) => b.granted_at ?? '',
      render: (b) => (b.source === 'config' ? <span className="muted">OPERATOR_EMAILS</span> : <span className="muted">{b.granted_by ? `${b.granted_by} · ` : ''}{b.granted_at ? when(b.granted_at) : ''}</span>),
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (b) =>
        b.source === 'config' ? (
          <span className="muted small" title="Set in the deployment's OPERATOR_EMAILS; not revocable here">
            configured
          </span>
        ) : (
          <button className="link small danger" disabled={act.busy} onClick={() => setRevoking(b)}>
            Revoke
          </button>
        ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader title="Access" sub={`${explicit.length} binding${explicit.length === 1 ? '' : 's'} · ${implicit.length} configured sovereign-admin${implicit.length === 1 ? '' : 's'} · ${(mappings.data?.mappings ?? []).length} group mapping${(mappings.data?.mappings ?? []).length === 1 ? '' : 's'}`} />
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Role bindings</h2>
          <span className="hint">who holds which role, at the Sovereign or on one customer</span>
        </div>
        <DataTable columns={columns} rows={rows} rowKey={(b) => b.id ?? `${b.subject_email}|${b.role}|${b.customer_id ?? ''}`} label="Role bindings" emptyTitle="No bindings yet" emptyBody="Grant a role below. Addresses in OPERATOR_EMAILS are sovereign-admins without a binding." />
      </div>
      <GrantForm customers={customerRows} busy={act.busy} onGrant={(body) => act.run(`${body.subject_email} granted ${roleLabel(body.role)}`, () => api.post('/access/bindings', body), bindings.reload)} />

      <GroupMappings doc={mappings.data} error={mappings.error} customers={customerRows} onSaved={mappings.reload} />

      <RolesCard roles={roles.data?.roles ?? []} />

      {revoking ? (
        <Confirm
          title="Revoke binding"
          danger
          confirmLabel="Revoke"
          busy={act.busy}
          onClose={() => setRevoking(null)}
          onConfirm={async () => {
            const ok = await act.run(`${revoking.subject_email} no longer ${roleLabel(String(revoking.role))}${revoking.scope_kind === 'customer' ? ` on ${scopeText(revoking)}` : ''}`, () => api.del(`/access/bindings/${revoking.id}`), bindings.reload)
            if (ok) setRevoking(null)
          }}
          body={
            <>
              Revoke <b>{roleLabel(String(revoking.role))}</b> from <b>{revoking.subject_email}</b> {revoking.scope_kind === 'sovereign' ? 'at the Sovereign' : <>on <b>{scopeText(revoking)}</b></>}? It takes effect at their next request; a live session ends there.
            </>
          }
        />
      ) : null}
    </div>
  )
}

function GrantForm({ customers, busy, onGrant }: { customers: Customer[]; busy: boolean; onGrant: (body: { subject_email: string; role: string; customer_id?: string }) => Promise<boolean> }) {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<AnyRole>('finance-viewer')
  const [customerId, setCustomerId] = useState('')
  const [err, setErr] = useState('')
  const needsCustomer = scopeKindOf(role) === 'customer'

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const v = email.trim().toLowerCase()
    if (!isEmail(v)) return setErr('Not a valid email address.')
    if (needsCustomer && !customerId) return setErr('A customer role needs a customer.')
    setErr('')
    const ok = await onGrant(needsCustomer ? { subject_email: v, role, customer_id: customerId } : { subject_email: v, role })
    if (ok) setEmail('')
  }

  return (
    <form className="card inline" onSubmit={(e) => void submit(e)} aria-label="Grant a role">
      <Field label="Email" error={err}>
        <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@example.com" style={{ minWidth: 240 }} />
      </Field>
      <Field label="Role" help={needsCustomer ? 'on one customer' : 'Sovereign-wide'}>
        <select value={role} onChange={(e) => setRole(e.target.value as AnyRole)} aria-label="Role">
          <optgroup label="Sovereign">
            {SOVEREIGN_ROLES.map((r) => (
              <option key={r} value={r}>
                {roleLabel(r)}
              </option>
            ))}
          </optgroup>
          <optgroup label="Customer">
            {SCOPED_CUSTOMER_ROLES.map((r) => (
              <option key={r} value={r}>
                {roleLabel(r)}
              </option>
            ))}
          </optgroup>
        </select>
      </Field>
      {needsCustomer ? (
        <Field label="Customer">
          <select value={customerId} onChange={(e) => setCustomerId(e.target.value)} aria-label="Customer">
            <option value="">— choose —</option>
            {customers.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>
      ) : null}
      <button className="primary" disabled={busy}>
        Grant
      </button>
    </form>
  )
}

/**
 * The directory-group mappings, edited as a set and saved with one PUT: the
 * given set becomes THE set, so a removed row is a revocation for every
 * member of that group at their next request.
 */
function GroupMappings({ doc, error, customers, onSaved }: { doc: MappingsDoc | null; error: string; customers: Customer[]; onSaved: () => void | Promise<void> }) {
  const act = useAction()
  const [edits, setEdits] = useState<GroupRoleMapping[] | null>(null)
  const [group, setGroup] = useState('')
  const [role, setRole] = useState<AnyRole>('finance-viewer')
  const [customerId, setCustomerId] = useState('')
  const [err, setErr] = useState('')
  const saved = doc?.mappings ?? []
  const rows = edits ?? saved
  const dirty = edits !== null
  const needsCustomer = scopeKindOf(role) === 'customer'
  const header = doc?.groups_header

  const add = (e: FormEvent) => {
    e.preventDefault()
    const g = group.trim()
    if (!g) return setErr('Group name is required.')
    if (needsCustomer && !customerId) return setErr('A customer role needs a customer.')
    if (rows.some((m) => m.group_name === g && m.role === role && (m.customer_id ?? '') === (needsCustomer ? customerId : ''))) return setErr('Already mapped.')
    setErr('')
    const cust = customers.find((c) => c.id === customerId)
    setEdits([...rows, { group_name: g, role, scope_kind: scopeKindOf(role), customer_id: needsCustomer ? customerId : null, customer_name: needsCustomer ? cust?.name : undefined }])
    setGroup('')
  }
  const remove = (i: number) => setEdits(rows.filter((_, j) => j !== i))
  const save = () =>
    act.run(`${rows.length} group mapping${rows.length === 1 ? '' : 's'} saved`, () => api.put('/access/group-mappings', { mappings: rows.map((m) => ({ group_name: m.group_name, role: m.role, customer_id: m.customer_id || undefined })) }), async () => {
      await onSaved()
      setEdits(null)
    })

  const columns: Column<GroupRoleMapping & { i: number }>[] = [
    { key: 'group', header: 'Directory group', value: (m) => m.group_name, render: (m) => <span className="mono">{m.group_name}</span> },
    { key: 'role', header: 'Role', value: (m) => m.role, render: (m) => <Badge status={roleLabel(String(m.role))} kind={m.scope_kind === 'sovereign' ? 'info' : undefined} /> },
    { key: 'scope', header: 'Scope', value: (m) => scopeText(m) },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (m) => (
        <button className="link small danger" disabled={act.busy} onClick={() => remove(m.i)}>
          Remove
        </button>
      ),
    },
  ]

  return (
    <div className="stack">
      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Directory group mappings</h2>
          <span className="btn-row">
            <span className="hint">{header ? `read from the ${header} header the SSO gate forwards` : 'inert until the SSO gate is configured (TRUSTED_FORWARD_AUTH_HEADER)'}</span>
            {dirty ? (
              <>
                <button className="small" onClick={() => setEdits(null)} disabled={act.busy}>
                  Discard
                </button>
                <button className="small primary" onClick={() => void save()} disabled={act.busy}>
                  Save mappings
                </button>
              </>
            ) : null}
          </span>
        </div>
        {error ? <Notice kind="bad">{error}</Notice> : null}
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
        <DataTable columns={columns} rows={rows.map((m, i) => ({ ...m, i }))} rowKey={(m) => `${m.group_name}|${m.role}|${m.customer_id ?? ''}`} label="Group mappings" emptyTitle="No group mapped" emptyBody="Map a directory group to a role: every member the SSO gate forwards in that group holds it, with no per-person binding." />
      </div>
      <form className="card inline" onSubmit={add} aria-label="Map a group">
        <Field label="Directory group" error={err}>
          <input value={group} onChange={(e) => setGroup(e.target.value)} placeholder="finance" style={{ minWidth: 200 }} />
        </Field>
        <Field label="Role" help={needsCustomer ? 'on one customer' : 'Sovereign-wide'}>
          <select value={role} onChange={(e) => setRole(e.target.value as AnyRole)} aria-label="Mapping role">
            <optgroup label="Sovereign">
              {SOVEREIGN_ROLES.map((r) => (
                <option key={r} value={r}>
                  {roleLabel(r)}
                </option>
              ))}
            </optgroup>
            <optgroup label="Customer">
              {SCOPED_CUSTOMER_ROLES.map((r) => (
                <option key={r} value={r}>
                  {roleLabel(r)}
                </option>
              ))}
            </optgroup>
          </select>
        </Field>
        {needsCustomer ? (
          <Field label="Customer">
            <select value={customerId} onChange={(e) => setCustomerId(e.target.value)} aria-label="Mapping customer">
              <option value="">— choose —</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
        ) : null}
        <button disabled={act.busy}>Add mapping</button>
      </form>
    </div>
  )
}

/** The six roles and what each may do — the same document the server decides by. */
function RolesCard({ roles }: { roles: RoleDoc[] }) {
  if (!roles.length) return null
  return (
    <div className="card">
      <h2>Roles</h2>
      <p className="muted small">Each role is a fixed bundle of permissions at one scope. A Sovereign role covers every customer; a customer role covers one customer only. There is no custom role.</p>
      <table aria-label="Roles">
        <thead>
          <tr>
            <th>Role</th>
            <th>Scope</th>
            <th>Permissions</th>
            <th>What it means</th>
          </tr>
        </thead>
        <tbody>
          {roles.map((r) => (
            <tr key={r.role}>
              <td>
                <Badge status={roleLabel(String(r.role))} kind={r.scope_kind === 'sovereign' ? 'info' : undefined} />
              </td>
              <td>{r.scope_kind === 'sovereign' ? 'Sovereign' : 'one customer'}</td>
              <td className="mono small">{r.permissions.join(', ')}</td>
              <td className="muted small">{r.description}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
