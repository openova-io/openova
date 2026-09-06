import { useEffect, useState } from 'react'
import { api, errorText } from '../api/client'
import { Empty, Notice } from '../components/ui'
import { n2, pct, reconciles } from '../lib/allocation'

/**
 * Allocation — the Sovereign's cost split (#6865, ADR-0014 D3 case 3).
 *
 * Renders what /api/v1/allocation computes: one row per tenant Organization
 * plus the platform-overhead line, each with its share of the window. The
 * endpoint has existed since #6850 with nothing to display it, so the split
 * that makes "Margin = rated Org revenue − cloud cost" computable was
 * invisible to the operator.
 */

type AllocationRow = {
  customer_id: string
  customer_slug: string
  customer_name: string
  tier: string
  vcpu_hours: number
  mem_gib_hours: number
  pvc_gb_hours: number
  weight: number
  share: number
}

type AllocationResp = {
  from?: string
  to?: string
  rows?: AllocationRow[]
  share_total?: number
  organization_rows?: number
  platform_overhead?: number
}

const monthStart = () => {
  const d = new Date()
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1)).toISOString().slice(0, 10)
}
const today = () => new Date().toISOString().slice(0, 10)

export function Allocation() {
  const [from, setFrom] = useState(monthStart())
  const [to, setTo] = useState(today())
  const [data, setData] = useState<AllocationResp | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const load = async () => {
    setBusy(true)
    setError('')
    try {
      const q = new URLSearchParams({ from, to })
      setData(await api.get<AllocationResp>(`/allocation?${q}`))
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const rows = data?.rows ?? []
  const shareTotal = data?.share_total ?? 0
  // A split whose shares do not sum to 1 has lost cost between the cloud total
  // and the rows shown. Saying so on the screen is the point: a silently
  // incomplete allocation looks exactly like a complete one.
  const ok = reconciles(rows.length, shareTotal)

  return (
    <div className="stack">
      <h1>Allocation</h1>
      <p className="muted">
        The Sovereign's consumption split across Organizations, plus its own platform overhead.
        Multiply a share by the collected cloud cost for that row's currency figure.
      </p>

      <form
        className="inline"
        onSubmit={(e) => {
          e.preventDefault()
          void load()
        }}
      >
        <label>
          From <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label>
          To <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <button type="submit" disabled={busy}>
          {busy ? 'Loading…' : 'Apply'}
        </button>
      </form>

      {error && <Notice kind="bad">{error}</Notice>}

      {!error && rows.length === 0 && (
        <Empty>No allocation for this window. Nothing ran, or the platform collector has not completed a pass.</Empty>
      )}

      {rows.length > 0 && (
        <>
          {!ok && (
            <Notice kind="bad">
              Shares total {pct(shareTotal)}, not 100%. Some consumption is missing from this split —
              do not use it as a cost basis until it reconciles.
            </Notice>
          )}
          <table>
            <thead>
              <tr>
                <th>Organization</th>
                <th>Tier</th>
                <th>vCPU-hours</th>
                <th>GiB-hours</th>
                <th>PVC GB-hours</th>
                <th>Share</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.customer_id}-${r.tier}`}>
                  <td>{r.customer_name || r.customer_slug}</td>
                  <td>{r.tier === 'platform-overhead' ? 'Platform overhead' : 'Organization'}</td>
                  <td>{n2(r.vcpu_hours)}</td>
                  <td>{n2(r.mem_gib_hours)}</td>
                  <td>{n2(r.pvc_gb_hours)}</td>
                  <td>{pct(r.share)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr>
                <td colSpan={5}>
                  {data?.organization_rows ?? 0} Organization row(s) · {data?.platform_overhead ?? 0} overhead line(s)
                </td>
                <td>{pct(shareTotal)}</td>
              </tr>
            </tfoot>
          </table>
        </>
      )}
    </div>
  )
}
