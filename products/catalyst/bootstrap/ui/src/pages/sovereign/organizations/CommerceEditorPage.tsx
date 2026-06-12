/**
 * CommerceEditorPage — /organizations/commerce/{plans,addons,bundles,
 * industries,apps} (issue #3378 DoD 7/8).
 *
 * One kind-aware editor over the EXISTING commerce endpoints (§6): a
 * table of the kind's rows + create/edit/delete modals rendering EXACTLY
 * the store.go struct fields (features = list editor; included_quotas =
 * k/v; product_slug = text; Bundle.apps + Industry.suggested_apps =
 * multi-select from GET /catalog/apps; Industry.bundle_id = live-bundle
 * select). Price changes take effect immediately — no redeploy (the
 * defect this kills).
 *
 * Chrome + tokens inherited from the sibling sovereign surfaces.
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { PortalShell } from '../PortalShell'
import {
  createCommerce,
  deleteCommerce,
  listAddOns,
  listApps,
  listBundles,
  listIndustries,
  listPlans,
  updateCommerce,
  type CommerceApp,
  type CommerceBundle,
  type CommerceKind,
} from '@/lib/commerce.api'

const KIND_LABEL: Record<CommerceKind, string> = {
  plans: 'Plans',
  addons: 'Add-ons',
  bundles: 'Bundles',
  industries: 'Industries',
  apps: 'Apps',
}

type Row = Record<string, unknown> & { id?: string }

export interface CommerceEditorPageProps {
  kind: CommerceKind
  /** Test seam — bypass the React Query fetcher. */
  initialRowsOverride?: readonly Row[]
  /** Test seam — supply the apps + bundles option pools synchronously. */
  initialAppsOverride?: readonly CommerceApp[]
  initialBundlesOverride?: readonly CommerceBundle[]
}

const STALE_MS = 30_000

export function CommerceEditorPage({
  kind,
  initialRowsOverride,
  initialAppsOverride,
  initialBundlesOverride,
}: CommerceEditorPageProps) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? null
  const qc = useQueryClient()

  const rowsQuery = useQuery<Row[]>({
    queryKey: ['commerce', kind, deploymentId],
    queryFn: () => listRows(kind),
    staleTime: STALE_MS,
    enabled: !initialRowsOverride,
    placeholderData: (prev) => prev,
  })

  // Option pools — apps (for Bundle.apps + Industry.suggested_apps) and
  // bundles (for Industry.bundle_id). Only fetched for the kinds that
  // need them so a plans/addons editor doesn't fan out extra calls.
  const needsApps = kind === 'bundles' || kind === 'industries'
  const needsBundles = kind === 'industries'
  const appsQuery = useQuery<CommerceApp[]>({
    queryKey: ['commerce-apps-pool', deploymentId],
    queryFn: listApps,
    staleTime: STALE_MS,
    enabled: needsApps && !initialAppsOverride,
    placeholderData: (prev) => prev ?? [],
  })
  const bundlesQuery = useQuery<CommerceBundle[]>({
    queryKey: ['commerce-bundles-pool', deploymentId],
    queryFn: listBundles,
    staleTime: STALE_MS,
    enabled: needsBundles && !initialBundlesOverride,
    placeholderData: (prev) => prev ?? [],
  })

  const rows: readonly Row[] = initialRowsOverride ?? rowsQuery.data ?? []
  const appsPool: readonly CommerceApp[] = initialAppsOverride ?? appsQuery.data ?? []
  const bundlesPool: readonly CommerceBundle[] = initialBundlesOverride ?? bundlesQuery.data ?? []
  const loading = !initialRowsOverride && rowsQuery.isLoading
  const error = !initialRowsOverride && rowsQuery.isError ? (rowsQuery.error as Error).message : null

  const fields = FIELD_CONFIG[kind]
  const [editing, setEditing] = useState<Row | null>(null)
  const [creating, setCreating] = useState<boolean>(false)
  const [actionError, setActionError] = useState<string | null>(null)

  function refresh() {
    qc.invalidateQueries({ queryKey: ['commerce', kind] })
  }

  async function onDelete(row: Row) {
    if (!row.id) return
    setActionError(null)
    try {
      await deleteCommerce(kind, String(row.id))
      refresh()
    } catch (e) {
      setActionError((e as Error).message)
    }
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle={`Commerce — ${KIND_LABEL[kind]}`}
      headerSlotLeft={
        <Link
          to={'/organizations' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="commerce-back-link"
        >
          ← Organizations
        </Link>
      }
      headerSlotRight={
        <button
          type="button"
          data-testid="commerce-create-button"
          onClick={() => {
            setCreating(true)
            setEditing(null)
          }}
          className="inline-flex items-center gap-1.5 rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs font-medium text-[var(--color-accent)]"
        >
          + New {KIND_LABEL[kind].replace(/s$/, '')}
        </button>
      }
    >
      <div data-testid={`commerce-${kind}-page`}>
        <p className="mb-4 text-sm text-[var(--color-text-dim)]">
          The {KIND_LABEL[kind].toLowerCase()} the marketplace storefront renders.
          Changes take effect immediately — no redeploy.
        </p>

        {error ? (
          <div data-testid="commerce-error" className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-300">
            {error}
          </div>
        ) : null}
        {actionError ? (
          <div data-testid="commerce-action-error" className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-300">
            {actionError}
          </div>
        ) : null}

        {loading ? (
          <div data-testid="commerce-loading" className="text-sm text-[var(--color-text-dim)]">Loading…</div>
        ) : rows.length === 0 ? (
          <div data-testid="commerce-empty" className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
            No {KIND_LABEL[kind].toLowerCase()} yet. Create one to populate the marketplace.
          </div>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
            <table data-testid={`commerce-${kind}-table`} className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-[var(--color-border)] text-left text-[0.7rem] uppercase tracking-wide text-[var(--color-text-dim)]">
                  {fields.filter((f) => f.inTable).map((f) => (
                    <th key={f.key} className="px-3 py-2 font-semibold">{f.label}</th>
                  ))}
                  <th className="px-3 py-2 font-semibold text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={String(row.id ?? row.slug)} data-testid={`commerce-row-${row.slug}`} className="border-b border-[var(--color-border)] last:border-b-0 hover:bg-[var(--color-surface-hover)]">
                    {fields.filter((f) => f.inTable).map((f) => (
                      <td key={f.key} className="px-3 py-2 align-middle text-[var(--color-text)]" data-testid={`commerce-cell-${f.key}-${row.slug}`}>
                        {renderCell(row[f.key], f.type)}
                      </td>
                    ))}
                    <td className="px-3 py-2 align-middle text-right">
                      <button
                        type="button"
                        data-testid={`commerce-edit-${row.slug}`}
                        onClick={() => { setEditing(row); setCreating(false) }}
                        className="mr-2 text-xs text-[var(--color-accent)] hover:underline"
                      >Edit</button>
                      <button
                        type="button"
                        data-testid={`commerce-delete-${row.slug}`}
                        onClick={() => onDelete(row)}
                        className="text-xs text-rose-400 hover:underline"
                      >Delete</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {(creating || editing) ? (
          <CommerceModal
            kind={kind}
            fields={fields}
            initial={editing ?? undefined}
            appsPool={appsPool}
            bundlesPool={bundlesPool}
            onClose={() => { setCreating(false); setEditing(null) }}
            onSaved={() => { setCreating(false); setEditing(null); refresh() }}
            onError={setActionError}
          />
        ) : null}
      </div>
    </PortalShell>
  )
}

/* ── Field config — mirrors store.go struct fields per kind ──────────── */

type FieldType = 'text' | 'number' | 'bool' | 'list' | 'kv' | 'apps' | 'bundle'
interface FieldDef {
  key: string
  label: string
  type: FieldType
  inTable?: boolean
}

const FIELD_CONFIG: Record<CommerceKind, FieldDef[]> = {
  plans: [
    { key: 'slug', label: 'Slug', type: 'text', inTable: true },
    { key: 'name', label: 'Name', type: 'text', inTable: true },
    { key: 'description', label: 'Description', type: 'text' },
    { key: 'cpu', label: 'CPU', type: 'text', inTable: true },
    { key: 'memory', label: 'Memory', type: 'text', inTable: true },
    { key: 'storage', label: 'Storage', type: 'text' },
    { key: 'price_omr', label: 'Price (OMR)', type: 'number', inTable: true },
    { key: 'popular', label: 'Popular', type: 'bool' },
    { key: 'sort_order', label: 'Sort order', type: 'number' },
    { key: 'features', label: 'Features', type: 'list' },
    { key: 'stripe_price_id', label: 'Stripe price id', type: 'text' },
    { key: 'product_slug', label: 'Product slug', type: 'text' },
    { key: 'included_quotas', label: 'Included quotas', type: 'kv' },
  ],
  addons: [
    { key: 'slug', label: 'Slug', type: 'text', inTable: true },
    { key: 'name', label: 'Name', type: 'text', inTable: true },
    { key: 'description', label: 'Description', type: 'text' },
    { key: 'price_omr', label: 'Price (OMR)', type: 'number', inTable: true },
    { key: 'included', label: 'Included', type: 'bool', inTable: true },
    { key: 'category', label: 'Category', type: 'text', inTable: true },
  ],
  bundles: [
    { key: 'slug', label: 'Slug', type: 'text', inTable: true },
    { key: 'name', label: 'Name', type: 'text', inTable: true },
    { key: 'tagline', label: 'Tagline', type: 'text' },
    { key: 'apps', label: 'Apps', type: 'apps', inTable: true },
    { key: 'discount', label: 'Discount %', type: 'number', inTable: true },
    { key: 'recommended_size', label: 'Recommended size', type: 'text' },
  ],
  industries: [
    { key: 'slug', label: 'Slug', type: 'text', inTable: true },
    { key: 'name', label: 'Name', type: 'text', inTable: true },
    { key: 'emoji', label: 'Emoji', type: 'text', inTable: true },
    { key: 'description', label: 'Description', type: 'text' },
    { key: 'display_order', label: 'Display order', type: 'number' },
    { key: 'suggested_apps', label: 'Suggested apps', type: 'apps' },
    { key: 'bundle_id', label: 'Bundle', type: 'bundle', inTable: true },
  ],
  apps: [
    { key: 'slug', label: 'Slug', type: 'text', inTable: true },
    { key: 'name', label: 'Name', type: 'text', inTable: true },
    { key: 'tagline', label: 'Tagline', type: 'text' },
    { key: 'category', label: 'Category', type: 'text', inTable: true },
  ],
}

function listRows(kind: CommerceKind): Promise<Row[]> {
  switch (kind) {
    case 'plans': return listPlans() as unknown as Promise<Row[]>
    case 'addons': return listAddOns() as unknown as Promise<Row[]>
    case 'bundles': return listBundles() as unknown as Promise<Row[]>
    case 'industries': return listIndustries() as unknown as Promise<Row[]>
    case 'apps': return listApps() as unknown as Promise<Row[]>
  }
}

function renderCell(v: unknown, type: FieldType) {
  if (type === 'bool') return v ? 'yes' : 'no'
  if (type === 'list' || type === 'apps') return Array.isArray(v) ? `${v.length}` : '0'
  if (type === 'kv') return v && typeof v === 'object' ? `${Object.keys(v as object).length}` : '0'
  if (v === undefined || v === null || v === '') return '—'
  return String(v)
}

/* ── Modal ───────────────────────────────────────────────────────────── */

function CommerceModal({
  kind,
  fields,
  initial,
  appsPool,
  bundlesPool,
  onClose,
  onSaved,
  onError,
}: {
  kind: CommerceKind
  fields: FieldDef[]
  initial?: Row
  appsPool: readonly CommerceApp[]
  bundlesPool: readonly CommerceBundle[]
  onClose: () => void
  onSaved: () => void
  onError: (m: string) => void
}) {
  const [form, setForm] = useState<Row>(() => seedForm(fields, initial))
  const [saving, setSaving] = useState(false)
  const isEdit = !!initial?.id

  function set(key: string, value: unknown) {
    setForm((f) => ({ ...f, [key]: value }))
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    onError('')
    try {
      const payload = coerceForm(fields, form)
      if (isEdit) {
        await updateCommerce(kind, String(initial!.id), payload)
      } else {
        await createCommerce(kind, payload)
      }
      onSaved()
    } catch (err) {
      onError((err as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" data-testid="commerce-modal">
      <form
        onSubmit={onSubmit}
        className="grid max-h-[85vh] w-full max-w-lg gap-3 overflow-y-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5"
      >
        <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
          {isEdit ? 'Edit' : 'New'} {KIND_LABEL[kind].replace(/s$/, '')}
        </h2>
        {fields.map((f) => (
          <FieldInput
            key={f.key}
            field={f}
            value={form[f.key]}
            onChange={(v) => set(f.key, v)}
            appsPool={appsPool}
            bundlesPool={bundlesPool}
            disabled={isEdit && f.key === 'slug'}
          />
        ))}
        <div className="mt-2 flex justify-end gap-2">
          <button type="button" onClick={onClose} data-testid="commerce-modal-cancel" className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text-dim)]">Cancel</button>
          <button type="submit" disabled={saving} data-testid="commerce-modal-save" className="rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)]/10 px-3 py-1.5 text-sm font-medium text-[var(--color-accent)] disabled:opacity-50">
            {saving ? 'Saving…' : isEdit ? 'Save' : 'Create'}
          </button>
        </div>
      </form>
    </div>
  )
}

function FieldInput({
  field,
  value,
  onChange,
  appsPool,
  bundlesPool,
  disabled,
}: {
  field: FieldDef
  value: unknown
  onChange: (v: unknown) => void
  appsPool: readonly CommerceApp[]
  bundlesPool: readonly CommerceBundle[]
  disabled?: boolean
}) {
  const id = `commerce-field-${field.key}`
  const baseInput = 'rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-1.5 text-sm'

  if (field.type === 'bool') {
    return (
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" data-testid={id} checked={!!value} onChange={(e) => onChange(e.target.checked)} />
        <span className="text-[var(--color-text-dim)]">{field.label}</span>
      </label>
    )
  }
  if (field.type === 'number') {
    return (
      <label className="grid gap-1 text-sm">
        <span className="text-[var(--color-text-dim)]">{field.label}</span>
        <input type="number" data-testid={id} value={Number(value ?? 0)} onChange={(e) => onChange(Number(e.target.value))} className={baseInput} />
      </label>
    )
  }
  if (field.type === 'list') {
    const arr = Array.isArray(value) ? (value as string[]) : []
    return (
      <label className="grid gap-1 text-sm">
        <span className="text-[var(--color-text-dim)]">{field.label} (one per line)</span>
        <textarea data-testid={id} value={arr.join('\n')} onChange={(e) => onChange(e.target.value.split('\n').map((s) => s.trim()).filter(Boolean))} rows={3} className={baseInput} />
      </label>
    )
  }
  if (field.type === 'kv') {
    const obj = value && typeof value === 'object' ? (value as Record<string, string>) : {}
    const text = Object.entries(obj).map(([k, v]) => `${k}=${v}`).join('\n')
    return (
      <label className="grid gap-1 text-sm">
        <span className="text-[var(--color-text-dim)]">{field.label} (key=value per line)</span>
        <textarea data-testid={id} value={text} onChange={(e) => onChange(parseKV(e.target.value))} rows={3} className={baseInput} />
      </label>
    )
  }
  if (field.type === 'apps') {
    const sel = Array.isArray(value) ? (value as string[]) : []
    return (
      <label className="grid gap-1 text-sm">
        <span className="text-[var(--color-text-dim)]">{field.label}</span>
        <select multiple data-testid={id} value={sel} onChange={(e) => onChange(Array.from(e.target.selectedOptions).map((o) => o.value))} className={`${baseInput} h-28`}>
          {appsPool.map((a) => (<option key={a.slug} value={a.slug}>{a.name || a.slug}</option>))}
        </select>
      </label>
    )
  }
  if (field.type === 'bundle') {
    return (
      <label className="grid gap-1 text-sm">
        <span className="text-[var(--color-text-dim)]">{field.label}</span>
        <select data-testid={id} value={String(value ?? '')} onChange={(e) => onChange(e.target.value)} className={baseInput}>
          <option value="">— none —</option>
          {bundlesPool.map((b) => (<option key={b.id ?? b.slug} value={b.id ?? b.slug}>{b.name || b.slug}</option>))}
        </select>
      </label>
    )
  }
  return (
    <label className="grid gap-1 text-sm">
      <span className="text-[var(--color-text-dim)]">{field.label}</span>
      <input type="text" data-testid={id} value={String(value ?? '')} disabled={disabled} onChange={(e) => onChange(e.target.value)} className={`${baseInput} disabled:opacity-60`} />
    </label>
  )
}

/* ── helpers ─────────────────────────────────────────────────────────── */

function seedForm(fields: FieldDef[], initial?: Row): Row {
  const out: Row = {}
  for (const f of fields) {
    if (initial && f.key in initial) {
      out[f.key] = initial[f.key]
      continue
    }
    out[f.key] =
      f.type === 'bool' ? false :
      f.type === 'number' ? 0 :
      f.type === 'list' || f.type === 'apps' ? [] :
      f.type === 'kv' ? {} : ''
  }
  return out
}

function coerceForm(fields: FieldDef[], form: Row): Row {
  const out: Row = {}
  for (const f of fields) out[f.key] = form[f.key]
  return out
}

function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf('=')
    if (i <= 0) continue
    const k = line.slice(0, i).trim()
    const v = line.slice(i + 1).trim()
    if (k) out[k] = v
  }
  return out
}
