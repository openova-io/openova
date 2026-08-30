import { useState, type FormEvent } from 'react'
import { api, errorText } from '../api/client'
import type { CostSource } from '../api/types'
import { Badge, Empty, Field, Notice } from '../components/ui'
import { when } from '../lib/format'

/**
 * Cost sources of one customer. `canManage` = add/verify/delete (operator);
 * `canRotate` = credential rotation (operator + customer-admin). The secret
 * key field is write-only: it is posted once and never read back — the API
 * never returns it (spec §1 credentials).
 */
export function SourcesPanel({
  customerId,
  sources,
  canManage,
  canRotate,
  onChanged,
}: {
  customerId: string
  sources: CostSource[]
  canManage: boolean
  canRotate: boolean
  onChanged: () => void | Promise<void>
}) {
  const [error, setError] = useState('')
  const [ok, setOk] = useState('')
  const [rotating, setRotating] = useState<string | null>(null)
  const [ak, setAk] = useState('')
  const [sk, setSk] = useState('')
  const [kind, setKind] = useState('huawei-project')
  const [region, setRegion] = useState('')
  const [projectId, setProjectId] = useState('')

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setError('')
    setOk('')
    try {
      await fn()
      setOk(label)
      await onChanged()
    } catch (e) {
      setError(errorText(e))
    }
  }

  const add = (e: FormEvent) => {
    e.preventDefault()
    void run('source added', () =>
      api.post(`/customers/${customerId}/sources`, { kind, region: region.trim(), project_id: projectId.trim() }),
    ).then(() => {
      setProjectId('')
    })
  }

  const rotate = (e: FormEvent) => {
    e.preventDefault()
    if (!rotating) return
    const id = rotating
    void run('credential rotated — verify to confirm', () =>
      api.post(`/sources/${id}/credential`, { access_key: ak.trim(), secret_key: sk }),
    ).then(() => {
      setAk('')
      setSk('')
      setRotating(null)
    })
  }

  return (
    <div className="stack">
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {ok ? <Notice kind="ok">{ok}</Notice> : null}
      {sources.length === 0 ? (
        <Empty>No cost sources.</Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Kind</th>
                <th>Region</th>
                <th>Project</th>
                <th>Status</th>
                <th>Verified</th>
                <th>Last collected</th>
                <th>Last error</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {sources.map((s) => (
                <tr key={s.id}>
                  <td>{s.kind}</td>
                  <td>{s.region}</td>
                  <td className="mono">{s.project_id}</td>
                  <td>
                    <Badge status={s.status} />
                  </td>
                  <td>{when(s.verified_at)}</td>
                  <td>{when(s.last_collected_at)}</td>
                  <td className="small">{s.last_error ?? '—'}</td>
                  <td className="row">
                    {canManage ? (
                      <button className="link" onClick={() => void run('verification requested', () => api.post(`/sources/${s.id}/verify`))}>
                        Verify
                      </button>
                    ) : null}
                    {canRotate ? (
                      <button className="link" onClick={() => setRotating(rotating === s.id ? null : s.id)}>
                        Rotate key
                      </button>
                    ) : null}
                    {canManage ? (
                      <button
                        className="link"
                        onClick={() => {
                          if (window.confirm(`Delete source ${s.project_id}? Collected usage is kept.`)) {
                            void run('source deleted', () => api.del(`/sources/${s.id}`))
                          }
                        }}
                      >
                        Delete
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {rotating ? (
        <form className="card stack" onSubmit={rotate}>
          <h3>Rotate access key for {sources.find((s) => s.id === rotating)?.project_id ?? rotating}</h3>
          <div className="grid2">
            <Field label="Access key (AK)">
              <input value={ak} onChange={(e) => setAk(e.target.value)} autoComplete="off" required />
            </Field>
            <Field label="Secret key (SK) — write-only, never shown again">
              <input type="password" value={sk} onChange={(e) => setSk(e.target.value)} autoComplete="new-password" required />
            </Field>
          </div>
          <div className="row">
            <button className="primary">Save new key</button>
            <button type="button" onClick={() => setRotating(null)}>
              Cancel
            </button>
          </div>
        </form>
      ) : null}

      {canManage ? (
        <form className="card" onSubmit={add}>
          <h3>Add source</h3>
          <div className="inline row" style={{ alignItems: 'end' }}>
            <Field label="Kind">
              <select value={kind} onChange={(e) => setKind(e.target.value)}>
                <option value="huawei-project">huawei-project</option>
                <option value="openova-org">openova-org</option>
                <option value="k8s-namespace">k8s-namespace</option>
                <option value="file">file</option>
              </select>
            </Field>
            <Field label="Region">
              <input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="om-east-1" required />
            </Field>
            <Field label="Project id">
              <input value={projectId} onChange={(e) => setProjectId(e.target.value)} required />
            </Field>
            <button className="primary">Add</button>
          </div>
          <p className="muted small">A new source starts pending; add its credential with Rotate key, then Verify.</p>
        </form>
      ) : null}
    </div>
  )
}
