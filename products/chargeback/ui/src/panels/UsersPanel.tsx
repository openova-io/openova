import { useState, type FormEvent } from 'react'
import { api } from '../api/client'
import type { CustomerUser } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Notice } from '../components/ui'
import { CUSTOMER_ROLES, roleLabel } from '../lib/access'
import { isEmail } from '../lib/forms'
import { useAction } from '../lib/useAction'

type CustomerRole = (typeof CUSTOMER_ROLES)[number]['role']

/** The role actually bound, falling back to the legacy vocabulary of an older document. */
function boundRole(u: CustomerUser): string {
  return u.binding_role ?? (u.role === 'admin' ? 'customer-owner' : 'customer-viewer')
}

/**
 * Who may sign in for this customer (DESIGN.md §10.9): its users, each with
 * ONE customer role — owner, billing or viewer. `canManage` is
 * customer.self.manage on this customer (the owner) or customers.manage (the
 * operator); without it the list is read-only.
 */
export function UsersPanel({ customerId, users, adminEmail, canManage = true, onChanged }: { customerId: string; users: CustomerUser[]; adminEmail: string; canManage?: boolean; onChanged: () => void | Promise<void> }) {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<CustomerRole>('customer-viewer')
  const [emailErr, setEmailErr] = useState('')
  const [removing, setRemoving] = useState<CustomerUser | null>(null)
  const act = useAction()

  const add = async (e: FormEvent) => {
    e.preventDefault()
    const v = email.trim().toLowerCase()
    if (!v) return setEmailErr('Email is required.')
    if (!isEmail(v)) return setEmailErr('Not a valid email address.')
    if (users.some((u) => u.email.toLowerCase() === v && boundRole(u) === role)) return setEmailErr(`Already ${roleLabel(role).toLowerCase()}.`)
    setEmailErr('')
    const ok = await act.run(`${v} is now ${roleLabel(role).toLowerCase()}`, () => api.post(`/customers/${customerId}/users`, { email: v, role }), onChanged)
    if (ok) setEmail('')
  }

  const columns: Column<CustomerUser>[] = [
    { key: 'email', header: 'Email', value: (u) => u.email, render: (u) => <span className="mono">{u.email}</span> },
    { key: 'role', header: 'Role', value: (u) => boundRole(u), render: (u) => <Badge status={roleLabel(boundRole(u))} kind={boundRole(u) === 'customer-owner' ? 'info' : undefined} /> },
    { key: 'may', header: 'May', value: (u) => CUSTOMER_ROLES.find((r) => r.role === boundRole(u))?.help ?? '', className: 'muted small' },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            value: () => '',
            sortable: false,
            className: 'nowrap actions',
            render: (u: CustomerUser) => (
              <button className="link small danger" disabled={act.busy} onClick={() => setRemoving(u)}>
                Remove
              </button>
            ),
          } satisfies Column<CustomerUser>,
        ]
      : []),
  ]

  return (
    <div className="stack">
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      <p className="muted small">
        The admin email <b>{adminEmail}</b> is always an owner. Users sign in at the Sovereign SSO or with a one-time PIN mailed to them; an <b>owner</b> reads, tops up the account and manages users, PO reference and tax registration; <b>billing</b> reads and tops up; a <b>viewer</b> only reads. Recording a payment, issuing, credit notes and suspension stay with the operator.
      </p>
      <div className="card pad-0">
        <DataTable columns={columns} rows={users} rowKey={(u) => `${u.email}|${boundRole(u)}`} label="Users" emptyTitle="No users yet" emptyBody="Only the admin email can sign in. Add a viewer for read-only access, billing for top-ups, or another owner." />
      </div>
      {canManage ? (
        <form className="card inline" onSubmit={(e) => void add(e)} aria-label="Add a user">
          <Field label="Email" error={emailErr}>
            <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@example.com" style={{ minWidth: 260 }} />
          </Field>
          <Field label="Role">
            <select value={role} onChange={(e) => setRole(e.target.value as CustomerRole)} aria-label="User role">
              {CUSTOMER_ROLES.map((r) => (
                <option key={r.role} value={r.role}>
                  {r.label} — {r.help}
                </option>
              ))}
            </select>
          </Field>
          <button className="primary" disabled={act.busy}>
            Add user
          </button>
        </form>
      ) : (
        <p className="muted small">Only an owner of this customer, or the operator, can add or remove users.</p>
      )}
      {removing ? (
        <Confirm
          title="Remove user"
          danger
          confirmLabel="Remove"
          busy={act.busy}
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            const ok = await act.run(`${removing.email} removed`, () => api.del(`/customers/${customerId}/users/${encodeURIComponent(removing.email)}`), onChanged)
            if (ok) setRemoving(null)
          }}
          body={
            <>
              Remove <b>{removing.email}</b>? Their session ends at its next request and they can no longer sign in for this customer.
              {removing.email.toLowerCase() === adminEmail.toLowerCase() ? <> This is the admin email: it is granted owner again the next time the customer is saved or synced.</> : null}
            </>
          }
        />
      ) : null}
    </div>
  )
}
