import { useState, type FormEvent } from 'react'
import { api } from '../api/client'
import type { CustomerUser } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Notice } from '../components/ui'
import { isEmail } from '../lib/forms'
import { useAction } from '../lib/useAction'

/** Who may sign in for this customer, beyond the admin email (#6867). */
export function UsersPanel({ customerId, users, adminEmail, onChanged }: { customerId: string; users: CustomerUser[]; adminEmail: string; onChanged: () => void | Promise<void> }) {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<'admin' | 'viewer'>('viewer')
  const [emailErr, setEmailErr] = useState('')
  const [removing, setRemoving] = useState<CustomerUser | null>(null)
  const act = useAction()

  const add = async (e: FormEvent) => {
    e.preventDefault()
    const v = email.trim().toLowerCase()
    if (!v) return setEmailErr('Email is required.')
    if (!isEmail(v)) return setEmailErr('Not a valid email address.')
    if (v === adminEmail.toLowerCase()) return setEmailErr('That is the admin email; it already signs in.')
    if (users.some((u) => u.email.toLowerCase() === v)) return setEmailErr('Already listed.')
    setEmailErr('')
    const ok = await act.run(`${v} added as ${role}`, () => api.post(`/customers/${customerId}/users`, { email: v, role }), onChanged)
    if (ok) setEmail('')
  }

  const columns: Column<CustomerUser>[] = [
    { key: 'email', header: 'Email', value: (u) => u.email },
    { key: 'role', header: 'Role', value: (u) => u.role, render: (u) => <Badge status={u.role} kind={u.role === 'admin' ? 'info' : undefined} /> },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (u) => (
        <button className="link small danger" disabled={act.busy} onClick={() => setRemoving(u)}>
          Remove
        </button>
      ),
    },
  ]

  return (
    <div className="stack">
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      <p className="muted small">
        The admin email <b>{adminEmail}</b> always signs in as customer-admin (change it under Settings). Users below sign in with a one-time PIN mailed to them; an <b>admin</b> may rotate source credentials and edit scope tokens, a <b>viewer</b> only reads.
      </p>
      <div className="card pad-0">
        <DataTable columns={columns} rows={users} rowKey={(u) => u.email} emptyTitle="No additional users" emptyBody="Only the admin email can sign in. Add a viewer for read-only access to costs and statements." />
      </div>
      <form className="card inline" onSubmit={(e) => void add(e)}>
        <Field label="Email" error={emailErr}>
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@example.com" style={{ minWidth: 260 }} />
        </Field>
        <Field label="Role">
          <select value={role} onChange={(e) => setRole(e.target.value as 'admin' | 'viewer')}>
            <option value="viewer">viewer — read only</option>
            <option value="admin">admin — may rotate keys</option>
          </select>
        </Field>
        <button className="primary" disabled={act.busy}>
          Add user
        </button>
      </form>
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
              Remove <b>{removing.email}</b>? Their session ends at its next request and they can no longer request a sign-in PIN for this customer.
            </>
          }
        />
      ) : null}
    </div>
  )
}
