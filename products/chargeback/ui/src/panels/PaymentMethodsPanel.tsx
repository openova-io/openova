import { useState } from 'react'
import { api, asList, errorText } from '../api/client'
import type { PaymentMethod } from '../api/types'
import { Badge, Confirm, Empty, Field, Modal, Notice, Skeleton } from '../components/ui'
import { when } from '../lib/format'
import { activeMethods, expiryLabel, isExpired, methodLabel, needsCompletion } from '../lib/selfservice'
import { useQuery } from '../lib/useQuery'

/**
 * Payment methods (DESIGN.md §16) — what the customer keeps on file.
 *
 * The card is never entered here. `Add a payment method` asks the customer's
 * own gateway to open a setup and hands back the gateway's page; the payer
 * completes it there and returns, and this panel confirms it and shows the
 * brand, the last four digits and the expiry. That is everything this product
 * holds about the instrument — there is no token on this wire to render.
 */
export function PaymentMethodsPanel({
  customerId,
  canManage,
  gatewayName,
}: {
  customerId: string
  canManage: boolean
  gatewayName?: string
}) {
  const q = useQuery<unknown>(customerId ? `/customers/${customerId}/payment-methods` : null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [flash, setFlash] = useState('')
  const [label, setLabel] = useState('')
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<PaymentMethod | null>(null)
  const [pending, setPending] = useState<PaymentMethod | null>(null)

  const methods = activeMethods(asList<PaymentMethod>(q.data, 'payment_methods'))

  const start = async () => {
    setBusy(true)
    setError('')
    setFlash('')
    try {
      const m = await api.post<PaymentMethod>(`/customers/${customerId}/payment-methods`, { label: label.trim() })
      setAdding(false)
      setLabel('')
      await q.reload()
      if (needsCompletion(m)) {
        // The gateway hosts the page the card is entered on; the customer
        // goes there and comes back to confirm.
        setPending(m)
      } else {
        setFlash('payment method saved')
      }
    } catch (e) {
      setError(errorText(e))
      setAdding(false)
    } finally {
      setBusy(false)
    }
  }

  const confirm = async (m: PaymentMethod) => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/customers/${customerId}/payment-methods/${m.id}/confirm`, {})
      setPending(null)
      setFlash('payment method saved')
      await q.reload()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!removing) return
    setBusy(true)
    setError('')
    try {
      await api.del(`/customers/${customerId}/payment-methods/${removing.id}`)
      setRemoving(null)
      setFlash('payment method removed')
      await q.reload()
    } catch (e) {
      setError(errorText(e))
      setRemoving(null)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <div className="card-head">
        <h2>Payment methods</h2>
        {canManage ? (
          <button className="primary" onClick={() => setAdding(true)} disabled={busy}>
            Add a payment method
          </button>
        ) : null}
      </div>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {q.loading && methods.length === 0 ? (
        <Skeleton lines={2} />
      ) : methods.length === 0 ? (
        <Empty>
          Nothing on file. {canManage ? 'Add one and the card is entered on the payment provider’s own page — it is never typed into this console and never stored here.' : 'An owner or billing user of this account can add one.'}
        </Empty>
      ) : (
        <div className="table-wrap">
          <table aria-label="Payment methods">
            <thead>
              <tr>
                <th>Method</th>
                <th>Expires</th>
                <th>Status</th>
                <th>Added</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {methods.map((m) => (
                <tr key={m.id}>
                  <td>
                    {methodLabel(m)}
                    {m.label && (m.brand || m.last4) ? <span className="sub">{m.label}</span> : null}
                  </td>
                  <td>
                    {expiryLabel(m) || <span className="muted">—</span>}
                    {isExpired(m) ? <span className="sub bad">expired</span> : null}
                  </td>
                  <td>
                    <Badge status={m.status} kind={m.status === 'active' ? 'ok' : 'warn'} />
                  </td>
                  <td>{when(m.confirmed_at ?? m.created_at)}</td>
                  <td className="row">
                    {needsCompletion(m) ? (
                      <>
                        <a href={m.setup_url} target="_blank" rel="noreferrer">
                          Finish on the payment page
                        </a>
                        {canManage ? (
                          <button className="link" onClick={() => void confirm(m)} disabled={busy}>
                            I have finished
                          </button>
                        ) : null}
                      </>
                    ) : null}
                    {canManage ? (
                      <button className="link danger" onClick={() => setRemoving(m)} disabled={busy}>
                        Remove
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="hint">
        {gatewayName ? `Collected through ${gatewayName}. ` : ''}
        The card itself is held by the payment provider. This console keeps the brand, the last four digits and the expiry so you can tell which card you saved — nothing that could charge it.
      </p>

      {adding ? (
        <Modal
          title="Add a payment method"
          onClose={() => setAdding(false)}
          footer={
            <>
              <button onClick={() => setAdding(false)} disabled={busy}>
                Cancel
              </button>
              <button className="primary" onClick={() => void start()} disabled={busy}>
                Continue to the payment page
              </button>
            </>
          }
        >
          <div className="stack tight">
            <p>You will be sent to the payment provider’s own page to enter the card. It is not entered here and is not stored here.</p>
            <Field label="Name it (optional)" help="Only for you, so you can tell two cards apart.">
              <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Company card" />
            </Field>
          </div>
        </Modal>
      ) : null}

      {pending ? (
        <Modal
          title="Finish on the payment page"
          onClose={() => setPending(null)}
          footer={
            <>
              <button onClick={() => setPending(null)} disabled={busy}>
                Later
              </button>
              <button className="primary" onClick={() => void confirm(pending)} disabled={busy}>
                I have finished
              </button>
            </>
          }
        >
          <div className="stack tight">
            <p>
              Enter the card on{' '}
              <a href={pending.setup_url} target="_blank" rel="noreferrer">
                the payment provider’s page
              </a>
              , then come back and confirm.
            </p>
          </div>
        </Modal>
      ) : null}

      {removing ? (
        <Confirm
          title="Remove this payment method?"
          danger
          confirmLabel="Remove"
          busy={busy}
          onClose={() => setRemoving(null)}
          onConfirm={remove}
          body={<p>{methodLabel(removing)} will no longer be charged. Invoices already issued stay as they are.</p>}
        />
      ) : null}
    </div>
  )
}
