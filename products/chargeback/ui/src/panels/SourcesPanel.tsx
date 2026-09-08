import { useState, type FormEvent } from 'react'
import { api, errorText } from '../api/client'
import type { CostSource, PriceBook } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Modal, Notice, Skeleton } from '../components/ui'
import { when } from '../lib/format'
import { hasErrors, validateSource, type Errors, type SourceForm } from '../lib/forms'
import { CLOUD_SOURCE_KINDS, bookCellText, booksForSource, layerLabel, layerOf, sourceKindLabel } from '../lib/layers'
import { useAction } from '../lib/useAction'

export const SCOPE_TOKEN_HELP = 'Bills only resources whose name carries this token (e.g. a deployment id) — empty bills the whole project.'

export const BOOK_HELP = 'The rate card that prices THIS source. A cloud source takes a cloud book, a platform source a platform book — the two layers are never priced by the same card.'

type Dialog = { kind: 'add' } | { kind: 'edit'; source: CostSource } | { kind: 'rotate'; source: CostSource } | { kind: 'delete'; source: CostSource } | { kind: 'purge'; source: CostSource } | null

/**
 * Cost sources of one customer (#6867, DESIGN.md §2). Each source carries
 * its LAYER (cloud or platform) and the price book that rates it — the book
 * is a property of the source, never of the customer — so the book select
 * offers only the books of the matching scope.
 *
 * `canManage` = add / edit every field / assign the book / verify / delete
 * (operator); `canRotate` = credential rotation (operator + customer-admin);
 * `canEditScope` = the customer-admin may edit scope_token only — region,
 * project and the price book decide what is billed and stay with the
 * operator. The secret key is write-only and never read back.
 */
export function SourcesPanel({
  customerId,
  sources,
  books,
  canManage,
  canRotate,
  canEditScope,
  onChanged,
  loading,
  autoAdd,
}: {
  customerId: string
  sources: CostSource[]
  books?: PriceBook[]
  canManage: boolean
  canRotate: boolean
  canEditScope?: boolean
  onChanged: () => void | Promise<void>
  loading?: boolean
  /** Open the add-source modal immediately (the new-customer flow). */
  autoAdd?: boolean
}) {
  const act = useAction()
  const [dialog, setDialog] = useState<Dialog>(autoAdd ? { kind: 'add' } : null)
  const [purge, setPurge] = useState<{ ok?: string; err?: string }>({})
  const [bookDraft, setBookDraft] = useState<Record<string, string>>({})
  const [bookErr, setBookErr] = useState<Record<string, string>>({})
  const [bookBusy, setBookBusy] = useState<string | null>(null)
  const catalogue = books ?? []
  const editable: Array<keyof SourceForm> = canManage ? ['region', 'project_id', 'domain_id', 'scope_token'] : canEditScope ? ['scope_token'] : []
  const close = () => setDialog(null)

  const saveBook = async (s: CostSource) => {
    const next = bookDraft[s.id] ?? ''
    setBookBusy(s.id)
    setBookErr((p) => ({ ...p, [s.id]: '' }))
    try {
      await api.patch(`/sources/${s.id}`, { price_book_id: next })
      setBookDraft((p) => {
        const { [s.id]: _gone, ...rest } = p
        return rest
      })
      await onChanged()
    } catch (e) {
      setBookErr((p) => ({ ...p, [s.id]: errorText(e) }))
    } finally {
      setBookBusy(null)
    }
  }

  const columns: Column<CostSource>[] = [
    { key: 'kind', header: 'Kind', value: (s) => s.kind, render: (s) => sourceKindLabel(s.kind) },
    {
      key: 'layer',
      header: 'Layer',
      value: (s) => layerOf(s),
      render: (s) => <Badge status={layerLabel(layerOf(s))} kind={layerOf(s) === 'cloud' ? 'info' : undefined} />,
    },
    { key: 'region', header: 'Region', value: (s) => s.region, render: (s) => s.region || <span className="muted">—</span> },
    { key: 'project', header: 'Project', value: (s) => s.project_id, render: (s) => (s.project_id ? <span className="mono">{s.project_id}</span> : <span className="muted">—</span>) },
    {
      key: 'book',
      header: 'Price book',
      value: (s) => s.price_book_name ?? '',
      render: (s) => {
        const offered = booksForSource(catalogue, s)
        const current = s.price_book_id ?? ''
        const draft = bookDraft[s.id]
        const dirty = draft !== undefined && draft !== current
        if (!canManage || s.internal) {
          return s.price_book_id ? <span>{bookCellText(s)}</span> : <span className="muted warn">{bookCellText(s)}</span>
        }
        return (
          <span className="btn-row">
            <select
              value={draft ?? current}
              aria-label={`Price book for ${s.project_id || s.kind}`}
              disabled={bookBusy === s.id}
              onChange={(e) => setBookDraft((p) => ({ ...p, [s.id]: e.target.value }))}
            >
              <option value="">— none —</option>
              {offered.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name} ({b.currency})
                </option>
              ))}
            </select>
            {dirty ? (
              <button className="primary small" disabled={bookBusy === s.id} onClick={() => void saveBook(s)}>
                Save
              </button>
            ) : null}
            {!s.price_book_id && !dirty ? <span className="sub warn">its usage rates to 0</span> : null}
            {bookErr[s.id] ? <span className="sub bad">{bookErr[s.id]}</span> : null}
          </span>
        )
      },
    },
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
          {s.status === 'disabled' ? <span className="sub">decommissioned — collects nothing new; its history still bills</span> : null}
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
      className: 'nowrap actions',
      render: (s) => (
        <span className="btn-row">
          {editable.length ? (
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'edit', source: s })}>
              Edit
            </button>
          ) : null}
          {canManage && s.status !== 'disabled' ? (
            <button className="link small" disabled={act.busy} onClick={() => void act.run(`verification requested for ${s.project_id || s.id}`, () => api.post(`/sources/${s.id}/verify`), onChanged)}>
              Verify
            </button>
          ) : null}
          {canManage && !s.internal ? (
            <button
              className="link small"
              disabled={act.busy}
              title={s.status === 'disabled' ? 'Collect from this source again' : 'Stop collecting from this source; its history keeps billing'}
              onClick={() =>
                void act.run(
                  s.status === 'disabled' ? `${s.project_id || s.id} enabled` : `${s.project_id || s.id} disabled — its history still bills`,
                  () => api.patch(`/sources/${s.id}`, { disabled: s.status !== 'disabled' }),
                  onChanged,
                )
              }
            >
              {s.status === 'disabled' ? 'Enable' : 'Disable'}
            </button>
          ) : null}
          {canRotate ? (
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'rotate', source: s })}>
              Rotate key
            </button>
          ) : null}
          {canManage && s.scope_token ? (
            <button className="link small" disabled={act.busy} title="Remove the hours collected before the scope token existed for resources it excludes" onClick={() => setDialog({ kind: 'purge', source: s })}>
              Purge excluded
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
      {purge.ok ? <Notice kind="ok">{purge.ok}</Notice> : null}
      {purge.err ? <Notice kind="bad">{purge.err}</Notice> : null}
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
        <b>Scope</b>: {SCOPE_TOKEN_HELP} <b>Price book</b>: {BOOK_HELP} <b>Disable</b>: a decommissioned source collects nothing new and counts as neither verified nor live, but its collected history still rates — in the explorer and on every statement already issued from it.
      </p>

      {dialog?.kind === 'add' ? <SourceFormModal title="Add cost source" customerId={customerId} books={catalogue} editable={['kind', 'region', 'project_id', 'scope_token']} onClose={close} onDone={onChanged} /> : null}
      {dialog?.kind === 'edit' ? <SourceFormModal title={`Edit source ${dialog.source.project_id || dialog.source.id}`} customerId={customerId} books={catalogue} source={dialog.source} editable={editable} onClose={close} onDone={onChanged} /> : null}
      {dialog?.kind === 'rotate' ? <RotateKeyModal source={dialog.source} onClose={close} onDone={onChanged} /> : null}
      {dialog?.kind === 'purge' ? (
        <Confirm
          title="Purge usage of excluded resources"
          danger
          confirmLabel="Purge"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            setPurge({})
            try {
              const r = await api.post<{ usage_records_deleted: number; excluded_resources: string[] }>(`/sources/${dialog.source.id}/purge-excluded`)
              setPurge({ ok: `${r.usage_records_deleted.toLocaleString()} usage record(s) of ${r.excluded_resources.length} excluded resource(s) removed from ${dialog.source.project_id || dialog.source.id}` })
              close()
              await onChanged()
            } catch (e) {
              setPurge({ err: errorText(e) })
            }
          }}
          body={
            <>
              Scope token <span className="mono">{dialog.source.scope_token}</span> stops NEW collection for resources outside it, but the hours collected before the token was set stay on every explorer view and draft statement. This removes those hours for every resource of{' '}
              <span className="mono">{dialog.source.project_id || dialog.source.id}</span> that neither carries the token nor is attached to one that does, and marks them deleted in the inventory. Issued statements are unchanged. Cannot be undone.
            </>
          }
        />
      ) : null}
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
  books,
  editable,
  onClose,
  onDone,
}: {
  title: string
  customerId: string
  source?: CostSource
  books: PriceBook[]
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
  // A new source is always a CLOUD source (platform sources are created by
  // the Organization sync), so the book offered here is a cloud book.
  const [bookID, setBookID] = useState(source?.price_book_id ?? '')
  const [errors, setErrors] = useState<Errors<SourceForm>>({})
  const act = useAction()
  const can = (f: SourceField) => editable.includes(f)
  const set = (k: SourceField, v: string) => setForm((f) => ({ ...f, [k]: v }))
  const kindHelp = CLOUD_SOURCE_KINDS.find((k) => k.value === form.kind)?.help
  const offered = booksForSource(books, { layer: layerOf({ layer: '', kind: form.kind }), kind: form.kind })

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
    const ok = await act.run(
      'source added',
      () => api.post(`/customers/${customerId}/sources`, { kind: form.kind, region: form.region.trim(), project_id: form.project_id.trim(), scope_token: form.scope_token.trim(), price_book_id: bookID }),
      onDone,
    )
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
              {CLOUD_SOURCE_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </Field>
        ) : (
          <p className="muted small">
            {sourceKindLabel(form.kind)}
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
        {!source && can('kind') ? (
          <>
            <Field label="Price book" help={BOOK_HELP}>
              <select value={bookID} onChange={(e) => setBookID(e.target.value)} aria-label="Price book">
                <option value="">— none yet —</option>
                {offered.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.name} ({b.currency})
                  </option>
                ))}
              </select>
            </Field>
            <p className="muted small" style={{ marginTop: 0 }}>
              This is a <b>cloud</b> source. The platform source of an Organization on this Sovereign is created automatically by the Organization sync and priced by the OpenOva plans book.
            </p>
          </>
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
