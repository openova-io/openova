import { useState, type FormEvent } from 'react'
import { api } from '../api/client'
import type { CostSource } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Modal, Notice, Skeleton } from '../components/ui'
import { when } from '../lib/format'
import { hasErrors, validateSource, type Errors, type SourceForm } from '../lib/forms'
import { useAction } from '../lib/useAction'

export const SCOPE_TOKEN_HELP = 'Bills only resources whose name carries this token (e.g. a deployment id) — empty bills the whole project.'

const KINDS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'huawei-project', label: 'Huawei project', help: 'Metered through the Huawei APIs with an AK/SK of the project.' },
  { value: 'openova-org', label: 'OpenOva Organization', help: 'Usage allocated from this Sovereign to one Organization.' },
  { value: 'k8s-namespace', label: 'Kubernetes namespace', help: 'Pod resource usage of one namespace.' },
  { value: 'file', label: 'File', help: 'Usage uploaded as CSV.' },
]

type Dialog = { kind: 'add' } | { kind: 'edit'; source: CostSource } | { kind: 'rotate'; source: CostSource } | { kind: 'delete'; source: CostSource } | null

/**
 * Cost sources of one customer (#6867). `canManage` = add / edit every
 * field / verify / delete (operator); `canRotate` = credential rotation
 * (operator + customer-admin); `canEditScope` = the customer-admin may edit
 * scope_token only — region and project decide what is billed and stay
 * with the operator. The secret key is write-only and never read back.
 */
export function SourcesPanel({
  customerId,
  sources,
  canManage,
  canRotate,
  canEditScope,
  onChanged,
  loading,
}: {
  customerId: string
  sources: CostSource[]
  canManage: boolean
  canRotate: boolean
  canEditScope?: boolean
  onChanged: () => void | Promise<void>
  loading?: boolean
}) {
  const act = useAction()
  const [dialog, setDialog] = useState<Dialog>(null)
  const editable: Array<keyof SourceForm> = canManage ? ['region', 'project_id', 'domain_id', 'scope_token'] : canEditScope ? ['scope_token'] : []
  const close = () => setDialog(null)

  const columns: Column<CostSource>[] = [
    { key: 'kind', header: 'Kind', value: (s) => s.kind, render: (s) => KINDS.find((k) => k.value === s.kind)?.label ?? s.kind },
    { key: 'region', header: 'Region', value: (s) => s.region, render: (s) => s.region || <span className="muted">—</span> },
    { key: 'project', header: 'Project', value: (s) => s.project_id, render: (s) => (s.project_id ? <span className="mono">{s.project_id}</span> : <span className="muted">—</span>) },
    {
      key: 'scope',
      header: 'Scope',
      value: (s) => s.scope_token ?? '',
      render: (s) => (s.scope_token ? <span className="mono" title={SCOPE_TOKEN_HELP}>{s.scope_token}</span> : <span className="muted" title={SCOPE_TOKEN_HELP}>whole project</span>),
    },
    {
      key: 'status',
      header: 'Status',
      value: (s) => s.status,
      render: (s) => (
        <>
          <Badge status={s.status} />
          {s.status === 'verified' && s.collecting === false ? <span className="sub">not collecting — customer is not active</span> : null}
        </>
      ),
    },
    { key: 'key', header: 'Access key', value: (s) => s.access_key ?? '', render: (s) => (s.access_key ? <span className="mono small">{s.access_key}</span> : <span className="muted">none</span>) },
    { key: 'verified', header: 'Verified', value: (s) => s.verified_at ?? '', render: (s) => when(s.verified_at) },
    { key: 'collected', header: 'Last collected', value: (s) => s.last_collected_at ?? '', render: (s) => when(s.last_collected_at) },
    { key: 'error', header: 'Last error', value: (s) => s.last_error ?? '', render: (s) => (s.last_error ? <span className="bad small">{s.last_error}</span> : <span className="muted">—</span>) },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap',
      render: (s) => (
        <span className="btn-row">
          {editable.length ? (
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'edit', source: s })}>
              Edit
            </button>
          ) : null}
          {canManage ? (
            <button className="link small" disabled={act.busy} onClick={() => void act.run(`verification requested for ${s.project_id || s.id}`, () => api.post(`/sources/${s.id}/verify`), onChanged)}>
              Verify
            </button>
          ) : null}
          {canRotate ? (
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'rotate', source: s })}>
              Rotate key
            </button>
          ) : null}
          {canManage ? (
            <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete', source: s })}>
              Delete
            </button>
          ) : null}
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      <div className="row between">
        <span className="muted small">
          {sources.length} source{sources.length === 1 ? '' : 's'} · {sources.filter((s) => s.status === 'verified').length} verified
          {sources.some((s) => s.status === 'failed') ? <span className="bad"> · {sources.filter((s) => s.status === 'failed').length} failed</span> : null}
        </span>
        {canManage ? (
          <button className="primary" onClick={() => setDialog({ kind: 'add' })} disabled={act.busy}>
            Add source
          </button>
        ) : null}
      </div>
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {loading && sources.length === 0 ? (
        <Skeleton lines={3} />
      ) : (
        <div className="card pad-0">
          <DataTable
            columns={columns}
            rows={sources}
            rowKey={(s) => s.id}
            emptyTitle="No cost sources"
            emptyBody={canManage ? 'Nothing is collected for this customer until a source is added, given a credential and verified.' : 'Nothing is collected yet — the operator adds and verifies sources.'}
          />
        </div>
      )}
      <p className="muted small">
        {canManage ? (
          <>
            A new source starts <Badge status="pending" />: add its credential with Rotate key, then Verify. A verified source is collected hourly while the customer is active.{' '}
          </>
        ) : (
          <>A verified source is collected hourly while your account is active; the operator adds, verifies and removes sources{canRotate ? ', you may rotate their access keys' : ''}. </>
        )}
        <b>Scope</b>: {SCOPE_TOKEN_HELP}
      </p>

      {dialog?.kind === 'add' ? <SourceFormModal title="Add cost source" customerId={customerId} editable={['kind', 'region', 'project_id', 'scope_token']} onClose={close} onDone={onChanged} /> : null}
      {dialog?.kind === 'edit' ? <SourceFormModal title={`Edit source ${dialog.source.project_id || dialog.source.id}`} customerId={customerId} source={dialog.source} editable={editable} onClose={close} onDone={onChanged} /> : null}
      {dialog?.kind === 'rotate' ? <RotateKeyModal source={dialog.source} onClose={close} onDone={onChanged} /> : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title="Delete cost source"
          danger
          confirmLabel="Delete source"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`source ${dialog.source.project_id || dialog.source.id} deleted`, () => api.del(`/sources/${dialog.source.id}`), onChanged)
            if (ok) close()
          }}
          body={
            <>
              Delete <span className="mono">{dialog.source.project_id || dialog.source.id}</span> ({dialog.source.region || dialog.source.kind})? Collected usage and its credential link are removed from this customer; nothing new is collected from this project. Statements already issued are unchanged.
            </>
          }
        />
      ) : null}
    </div>
  )
}

type SourceField = keyof SourceForm

function SourceFormModal({
  title,
  customerId,
  source,
  editable,
  onClose,
  onDone,
}: {
  title: string
  customerId: string
  source?: CostSource
  editable: SourceField[]
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const [form, setForm] = useState<SourceForm>({
    kind: source?.kind ?? 'huawei-project',
    region: source?.region ?? '',
    project_id: source?.project_id ?? '',
    domain_id: source?.domain_id ?? '',
    scope_token: source?.scope_token ?? '',
  })
  const [errors, setErrors] = useState<Errors<SourceForm>>({})
  const act = useAction()
  const can = (f: SourceField) => editable.includes(f)
  const set = (k: SourceField, v: string) => setForm((f) => ({ ...f, [k]: v }))
  const kindHelp = KINDS.find((k) => k.value === form.kind)?.help

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateSource(form, editable)
    setErrors(errs)
    if (hasErrors(errs)) return
    if (source) {
      const body: Partial<Record<SourceField, string>> = {}
      for (const k of editable) {
        if (k === 'kind') continue
        const v = form[k].trim()
        if (v !== (source[k] ?? '')) body[k] = v
      }
      if (Object.keys(body).length === 0) {
        act.setError('Nothing changed.')
        return
      }
      const resets = 'region' in body || 'project_id' in body
      const ok = await act.run(resets ? 'source updated — it is pending again; verify it' : 'source updated', () => api.patch(`/sources/${source.id}`, body), onDone)
      if (ok) onClose()
      return
    }
    const ok = await act.run('source added', () => api.post(`/customers/${customerId}/sources`, { kind: form.kind, region: form.region.trim(), project_id: form.project_id.trim(), scope_token: form.scope_token.trim() }), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="source-form" disabled={act.busy}>
            {source ? 'Save' : 'Add source'}
          </button>
        </>
      }
    >
      <form id="source-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        {can('kind') ? (
          <Field label="Kind" help={kindHelp}>
            <select value={form.kind} onChange={(e) => set('kind', e.target.value)}>
              {KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </Field>
        ) : (
          <p className="muted small">
            {KINDS.find((k) => k.value === form.kind)?.label ?? form.kind}
            {!can('region') ? (
              <>
                {' '}
                · <span className="mono">{form.region || '—'}</span> / <span className="mono">{form.project_id || '—'}</span> — region and project are set by the operator.
              </>
            ) : null}
          </p>
        )}
        {can('region') || can('project_id') ? (
          <div className="grid2">
            {can('region') ? (
              <Field label="Region" error={errors.region} help={source ? 'Changing it resets the source to pending — verification proved a different project.' : undefined}>
                <input value={form.region} onChange={(e) => set('region', e.target.value)} placeholder="me-east-215-a" autoFocus />
              </Field>
            ) : null}
            {can('project_id') ? (
              <Field label="Project id" error={errors.project_id}>
                <input value={form.project_id} onChange={(e) => set('project_id', e.target.value)} className="mono" />
              </Field>
            ) : null}
          </div>
        ) : null}
        {can('domain_id') ? (
          <Field label="Domain id" error={errors.domain_id} help="The account (domain) the project belongs to; stamped by verification, editable here if it was wrong.">
            <input value={form.domain_id} onChange={(e) => set('domain_id', e.target.value)} className="mono" />
          </Field>
        ) : null}
        {can('scope_token') ? (
          <Field label="Scope token" error={errors.scope_token} help={SCOPE_TOKEN_HELP}>
            <input value={form.scope_token} onChange={(e) => set('scope_token', e.target.value)} className="mono" placeholder="e.g. 1c56518035a83e03" autoFocus={!can('region')} />
          </Field>
        ) : null}
      </form>
    </Modal>
  )
}

function RotateKeyModal({ source, onClose, onDone }: { source: CostSource; onClose: () => void; onDone: () => void | Promise<void> }) {
  const [ak, setAk] = useState('')
  const [sk, setSk] = useState('')
  const [errors, setErrors] = useState<{ ak?: string; sk?: string }>({})
  const act = useAction()
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs: { ak?: string; sk?: string } = {}
    if (!ak.trim()) errs.ak = 'Access key is required.'
    if (!sk) errs.sk = 'Secret key is required.'
    setErrors(errs)
    if (errs.ak || errs.sk) return
    const ok = await act.run('credential saved — run Verify to confirm it works', () => api.post(`/sources/${source.id}/credential`, { access_key: ak.trim(), secret_key: sk }), onDone)
    if (ok) onClose()
  }
  return (
    <Modal
      title={`Rotate access key — ${source.project_id || source.id}`}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="rotate-form" disabled={act.busy}>
            Save new key
          </button>
        </>
      }
    >
      <form id="rotate-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Access key (AK)" error={errors.ak} help={source.access_key ? `Replaces ${source.access_key}.` : undefined}>
          <input value={ak} onChange={(e) => setAk(e.target.value)} autoComplete="off" className="mono" autoFocus />
        </Field>
        <Field label="Secret key (SK)" error={errors.sk} help="Write-only: stored encrypted, never shown again.">
          <input type="password" value={sk} onChange={(e) => setSk(e.target.value)} autoComplete="new-password" />
        </Field>
      </form>
    </Modal>
  )
}
