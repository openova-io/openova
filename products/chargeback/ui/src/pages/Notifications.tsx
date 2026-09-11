import { Fragment, useMemo, useState } from 'react'
import { api } from '../api/client'
import type { NotifyCatalogue, NotifyChannel, NotifyDeliveriesDoc, NotifyDelivery, NotifyEffective, NotifyEvent, NotifyPreferencesDoc, NotifyStatus } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { can, customerIds } from '../lib/access'
import { when } from '../lib/format'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Notifications (DESIGN.md §21) — the one place an operator can answer the
 * three questions this product could not answer before:
 *
 *   WHAT does it send?        the event catalogue, with each event's template
 *   WHO gets it?              the preference rule, and what it resolves to
 *   DID IT ARRIVE?            the delivery log, failures first
 *
 * The same page serves the customer lens as `MyNotifications`: the same
 * preference editor scoped to its own customer, and its own deliveries. The
 * server decides both — the customer routes are scoped by `internal/access`
 * and a customer never reaches the Sovereign catalogue.
 *
 * Reading the catalogue is `metering.read`; editing the Sovereign defaults is
 * `settings.manage`; a customer edits its own with `customer.self.manage`.
 */

const STATUS_TONE: Record<string, 'ok' | 'warn' | 'bad' | 'info'> = {
  sent: 'ok',
  retrying: 'warn',
  failed: 'bad',
  suppressed: 'info',
  unavailable: 'bad',
}

const STATUS_HELP: Record<string, string> = {
  sent: 'the channel accepted it',
  retrying: 'this attempt failed and another followed',
  failed: 'this attempt failed and no more followed',
  suppressed: 'a preference switched the event off for this recipient',
  unavailable: 'the channel is declared but has no transport',
}

const CATEGORY_LABEL: Record<string, string> = {
  access: 'Access',
  billing: 'Billing',
  collections: 'Collections',
  cost: 'Cost',
}

function categoryLabel(c: string): string {
  return CATEGORY_LABEL[c] ?? c
}

function statusTone(s: NotifyStatus): 'ok' | 'warn' | 'bad' | 'info' {
  return STATUS_TONE[s] ?? 'info'
}

/** The first line of a template body, for a one-line preview. */
function firstLine(s: string): string {
  const line = s.split('\n').find((l) => l.trim() !== '')
  return line ?? ''
}

function channelText(names: string[], channels: NotifyChannel[]): string {
  if (!names.length) return '—'
  return names
    .map((n) => {
      const c = channels.find((x) => x.name === n)
      return c && !c.available ? `${n} (no transport)` : n
    })
    .join(' · ')
}

// ---------------------------------------------------------------------------
// the operator page
// ---------------------------------------------------------------------------

export function Notifications() {
  const { me } = useSession()
  const canManage = can(me, 'settings.manage')
  const canReadLog = can(me, 'audit.read')
  const cat = useQuery<NotifyCatalogue>('/notifications/events')
  const prefs = useQuery<NotifyPreferencesDoc>('/notifications/preferences')
  const [onlyProblems, setOnlyProblems] = useState<'problems' | 'all'>('problems')
  const log = useQuery<NotifyDeliveriesDoc>(canReadLog ? `/notifications/deliveries?limit=200${onlyProblems === 'problems' ? '&status=problems' : ''}` : null, [onlyProblems])

  const events = useMemo(() => cat.data?.events ?? [], [cat.data])
  const channels = useMemo(() => cat.data?.channels ?? [], [cat.data])
  const unavailable = channels.filter((c) => !c.available)
  const mandatory = events.filter((e) => e.mandatory)
  const stats = log.data?.stats ?? []
  const countOf = (s: string) => stats.find((x) => x.status === s)?.count ?? 0
  const failed = countOf('failed') + countOf('unavailable')

  return (
    <div className="stack">
      <PageHeader
        title="Notifications"
        sub="Every message this product can send, who receives it, and what happened to each one. A notice the operator marks mandatory — an invoice, a dunning reminder — cannot be switched off by anyone."
      />
      {cat.error ? <Notice kind="bad">{cat.error}</Notice> : null}
      {prefs.error ? <Notice kind="bad">{prefs.error}</Notice> : null}
      {log.error ? <Notice kind="bad">{log.error}</Notice> : null}

      <div className="kpis">
        <KPI label="Events" value={events.length} note={`${mandatory.length} mandatory · ${events.length - mandatory.length} a recipient may switch off`} />
        <KPI label="Channels" value={channels.length} note={unavailable.length ? `${unavailable.length} declared without a transport` : 'every declared channel can deliver'} tone={unavailable.length ? 'warn' : undefined} />
        <KPI label="Delivered" value={countOf('sent')} note="attempts accepted by a channel in the last 7 days" />
        <KPI label="Not delivered" value={failed} note={failed ? 'failed or refused in the last 7 days — listed below' : 'nothing failed in the last 7 days'} tone={failed ? 'bad' : undefined} />
      </div>

      {/* A DECLARED channel with no transport says so, in its own words. It
          is not an error state to be hidden: it is what an operator needs to
          read before offering anyone that channel. */}
      {unavailable.map((c) => (
        <Notice kind="warn" key={c.name}>
          <b>{c.name}</b> is declared and cannot deliver. {c.reason}
        </Notice>
      ))}

      {!canManage ? (
        <Notice kind="info">
          Read-only: the Sovereign-wide defaults are edited with <code>settings.manage</code>. A customer&apos;s own preferences are edited on its Notifications tab.
        </Notice>
      ) : null}

      <div className="card">
        <div className="card-head">
          <h2>Events</h2>
          <span className="hint">what the product sends, and the template it sends it from</span>
        </div>
        {cat.loading && !events.length ? (
          <Skeleton lines={5} />
        ) : (
          <EventTable catalogue={cat.data} prefs={prefs.data} canManage={canManage} onDone={() => void prefs.reload()} />
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Deliveries</h2>
          <Segmented
            value={onlyProblems}
            ariaLabel="Delivery filter"
            options={[
              { value: 'problems', label: 'Problems' },
              { value: 'all', label: 'All attempts' },
            ]}
            onChange={setOnlyProblems}
          />
        </div>
        <p className="muted small">
          One row per <b>attempt</b>, newest first. A transient failure followed by a success is two rows, which is what tells you the mail took two goes. Kept for 180 days.
        </p>
        {!canReadLog ? (
          <Notice kind="info">
            The delivery log is an audit trail and needs <code>audit.read</code>.
          </Notice>
        ) : log.loading && !log.data ? (
          <Skeleton lines={4} />
        ) : (
          <DeliveryTable rows={log.data?.deliveries ?? []} emptyTitle={onlyProblems === 'problems' ? 'Nothing failed' : 'No deliveries recorded'} showCustomer />
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// the customer lens
// ---------------------------------------------------------------------------

export function MyNotifications() {
  const { me } = useSession()
  const id = customerIds(me)[0] ?? null
  const canManage = can(me, 'customer.self.manage', id)
  const prefs = useQuery<NotifyPreferencesDoc>(id ? `/customers/${id}/notifications/preferences` : null)
  const log = useQuery<NotifyDeliveriesDoc>(id ? `/customers/${id}/notifications/deliveries?limit=100` : null)

  if (!id) return <Notice kind="bad">This sign-in is not bound to a customer.</Notice>

  return (
    <div className="stack">
      <PageHeader title="Notifications" sub="Which messages this account receives, and what happened to each one. Invoices and payment reminders are always sent." />
      {prefs.error ? <Notice kind="bad">{prefs.error}</Notice> : null}
      {log.error ? <Notice kind="bad">{log.error}</Notice> : null}

      <div className="card">
        <div className="card-head">
          <h2>What we send you</h2>
          <span className="hint">{canManage ? 'switch off what you do not need' : 'read-only'}</span>
        </div>
        {prefs.loading && !prefs.data ? <Skeleton lines={5} /> : <PreferenceTable doc={prefs.data} customerID={id} canManage={canManage} onDone={() => void prefs.reload()} />}
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Recent messages</h2>
          <span className="hint">one row per delivery attempt</span>
        </div>
        {log.loading && !log.data ? <Skeleton lines={4} /> : <DeliveryTable rows={log.data?.deliveries ?? []} emptyTitle="Nothing has been sent yet" />}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// the tables
// ---------------------------------------------------------------------------

type Dialog = { kind: 'edit'; event: NotifyEvent; effective?: NotifyEffective } | { kind: 'reset'; event: NotifyEvent } | { kind: 'template'; event: NotifyEvent } | null

function EventTable({ catalogue, prefs, canManage, onDone }: { catalogue: NotifyCatalogue | null; prefs: NotifyPreferencesDoc | null; canManage: boolean; onDone: () => void }) {
  const [dialog, setDialog] = useState<Dialog>(null)
  const act = useAction()
  const events = catalogue?.events ?? []
  const channels = catalogue?.channels ?? []
  const defaultLocale = catalogue?.default_locale ?? 'en'
  const effective = prefs?.effective ?? []
  const close = () => setDialog(null)

  const effectiveFor = (key: string) => effective.find((e) => e.event === key)
  const templateFor = (key: string) => (catalogue?.templates ?? []).find((t) => t.event === key && t.locale === defaultLocale)

  const columns: Column<NotifyEvent>[] = [
    {
      key: 'event',
      header: 'Event',
      value: (e) => e.title,
      render: (e) => (
        <span>
          {e.title}
          <span className="sub mono">{e.key}</span>
        </span>
      ),
    },
    { key: 'category', header: 'Group', value: (e) => categoryLabel(e.category), render: (e) => <Badge status={categoryLabel(e.category)} kind="info" /> },
    {
      key: 'mandatory',
      header: 'Switchable',
      value: (e) => (e.mandatory ? 'no' : 'yes'),
      render: (e) =>
        e.mandatory ? (
          <span className="nowrap">
            <Badge status="Mandatory" kind="warn" />
            <span className="sub">cannot be switched off</span>
          </span>
        ) : (
          <span className="nowrap muted">a recipient may switch it off</span>
        ),
    },
    { key: 'channels', header: 'Channels', value: (e) => e.channels.join(' '), render: (e) => <span className="nowrap">{channelText(e.channels, channels)}</span> },
    {
      key: 'effective',
      header: 'Sovereign default',
      value: (e) => (effectiveFor(e.key)?.enabled === false ? 'off' : 'on'),
      render: (e) => {
        const eff = effectiveFor(e.key)
        if (!eff) return <span className="muted">—</span>
        return (
          <span className="nowrap">
            <Badge status={eff.enabled ? 'On' : 'Off'} kind={eff.enabled ? 'ok' : 'bad'} />
            <span className="sub">{eff.source === 'catalogue default' ? 'no preference set' : `set at ${eff.source}`}</span>
          </span>
        )
      },
    },
    {
      key: 'subject',
      header: 'Subject',
      value: (e) => templateFor(e.key)?.subject ?? '',
      render: (e) => {
        const t = templateFor(e.key)
        if (!t) return <span className="warn">no template</span>
        return (
          <button className="link small" onClick={() => setDialog({ kind: 'template', event: e })} title="Show the template">
            <span className="mono">{firstLine(t.subject).slice(0, 60)}</span>
          </button>
        )
      },
      sortable: false,
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (e) =>
        canManage ? (
          <span className="btn-row">
            <button className="link small" disabled={act.busy || e.mandatory} title={e.mandatory ? 'A mandatory notice cannot be switched off' : undefined} onClick={() => setDialog({ kind: 'edit', event: e, effective: effectiveFor(e.key) })}>
              Edit
            </button>
            <button className="link small" disabled={act.busy || effectiveFor(e.key)?.source === 'catalogue default'} onClick={() => setDialog({ kind: 'reset', event: e })}>
              Reset
            </button>
          </span>
        ) : null,
    },
  ]

  return (
    <>
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      <DataTable label="Notification events" columns={columns} rows={events} rowKey={(e) => e.key} defaultSort={{ key: 'category', dir: 'asc' }} pageSize={25} csvName="notification-events" emptyTitle="No events" />
      {dialog?.kind === 'template' ? <TemplateModal event={dialog.event} catalogue={catalogue} onClose={close} /> : null}
      {dialog?.kind === 'edit' ? <PreferenceModal event={dialog.event} effective={dialog.effective} channels={channels} locales={catalogue?.locales ?? []} path="/notifications/preferences" onClose={close} onDone={onDone} /> : null}
      {dialog?.kind === 'reset' ? (
        <Confirm
          title={`Reset ${dialog.event.title}`}
          confirmLabel="Reset to the default"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`${dialog.event.title} reset`, () => api.del(`/notifications/preferences/${encodeURIComponent(dialog.event.key)}`), onDone)
            if (ok) close()
          }}
          body={
            <p>
              The Sovereign-wide preference for <b>{dialog.event.title}</b> is removed, so it returns to the catalogue default ({dialog.event.default_on ? 'sent' : 'not sent'}). Preferences set on a
              customer or on one person are untouched.
            </p>
          }
        />
      ) : null}
    </>
  )
}

/** The preference list a CUSTOMER sees and edits for itself. */
function PreferenceTable({ doc, customerID, canManage, onDone }: { doc: NotifyPreferencesDoc | null; customerID: string; canManage: boolean; onDone: () => void }) {
  const [editing, setEditing] = useState<NotifyEffective | null>(null)
  const act = useAction()
  const rows = doc?.effective ?? []
  const events = doc?.events ?? []
  const channels = doc?.channels ?? []
  const eventFor = (key: string) => events.find((e) => e.key === key)

  const columns: Column<NotifyEffective>[] = [
    {
      key: 'event',
      header: 'Message',
      value: (e) => e.title,
      render: (e) => (
        <span>
          {e.title}
          <span className="sub">{eventFor(e.event)?.description ?? ''}</span>
        </span>
      ),
    },
    { key: 'category', header: 'Group', value: (e) => categoryLabel(e.category), render: (e) => <Badge status={categoryLabel(e.category)} kind="info" /> },
    { key: 'channels', header: 'Channel', value: (e) => e.channels.join(' '), render: (e) => <span className="nowrap">{channelText(e.channels, channels)}</span> },
    {
      key: 'state',
      header: 'Receiving',
      value: (e) => (e.enabled ? 'yes' : 'no'),
      render: (e) =>
        e.mandatory ? (
          <span className="nowrap">
            <Badge status="Always" kind="ok" />
            <span className="sub">required — cannot be switched off</span>
          </span>
        ) : (
          <span className="nowrap">
            <Badge status={e.enabled ? 'Yes' : 'No'} kind={e.enabled ? 'ok' : 'bad'} />
            <span className="sub">{e.source === 'catalogue default' ? 'the default' : `set at ${e.source}`}</span>
          </span>
        ),
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (e) =>
        canManage && !e.mandatory ? (
          <button className="link small" disabled={act.busy} onClick={() => setEditing(e)}>
            Change
          </button>
        ) : null,
    },
  ]

  const ev = editing ? eventFor(editing.event) : undefined

  return (
    <>
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {rows.length === 0 ? (
        <EmptyState title="No messages">Nothing is configured to be sent to this account.</EmptyState>
      ) : (
        <DataTable label="Notification preferences" columns={columns} rows={rows} rowKey={(e) => e.event} defaultSort={{ key: 'category', dir: 'asc' }} pageSize={25} emptyTitle="No messages" />
      )}
      {editing && ev ? (
        <PreferenceModal
          event={ev}
          effective={editing}
          channels={channels}
          locales={doc?.locales ?? []}
          path={`/customers/${customerID}/notifications/preferences`}
          onClose={() => setEditing(null)}
          onDone={onDone}
        />
      ) : null}
    </>
  )
}

function DeliveryTable({ rows, emptyTitle, showCustomer }: { rows: NotifyDelivery[]; emptyTitle: string; showCustomer?: boolean }) {
  const columns: Column<NotifyDelivery>[] = [
    { key: 'at', header: 'When', value: (d) => d.at, render: (d) => <span className="nowrap">{when(d.at)}</span> },
    { key: 'event', header: 'Event', value: (d) => d.event, render: (d) => <span className="mono">{d.event}</span> },
    {
      key: 'recipient',
      header: 'Recipient',
      value: (d) => d.recipient,
      render: (d) => (
        <span>
          {d.recipient}
          {d.subject ? <span className="sub">{d.subject}</span> : null}
        </span>
      ),
    },
    { key: 'channel', header: 'Channel', value: (d) => d.channel, render: (d) => <span className="nowrap">{d.channel}</span> },
    { key: 'attempt', header: 'Attempt', value: (d) => d.attempt, numeric: true },
    {
      key: 'status',
      header: 'Outcome',
      value: (d) => d.status,
      render: (d) => (
        <span className="nowrap" title={STATUS_HELP[d.status] ?? ''}>
          <Badge status={d.status} kind={statusTone(d.status)} />
        </span>
      ),
    },
    {
      key: 'reason',
      header: 'Detail',
      value: (d) => d.reason ?? '',
      sortable: false,
      render: (d) => (d.reason ? <span className="small">{d.reason}</span> : <span className="muted">—</span>),
    },
  ]
  if (showCustomer) {
    columns.splice(3, 0, {
      key: 'customer',
      header: 'Customer',
      value: (d) => d.customer_id ?? '',
      render: (d) => (d.customer_id ? <span className="mono small">{d.customer_id.slice(0, 8)}</span> : <span className="muted">—</span>),
    })
  }
  if (rows.length === 0) {
    return <EmptyState title={emptyTitle}>Every attempt this product makes is recorded here, whether it succeeded, was retried, failed or was suppressed by a preference.</EmptyState>
  }
  return <DataTable label="Notification deliveries" columns={columns} rows={rows} rowKey={(d) => d.id} pageSize={25} csvName="notification-deliveries" emptyTitle={emptyTitle} />
}

// ---------------------------------------------------------------------------
// the dialogs
// ---------------------------------------------------------------------------

/** The template an event renders from, as the operator can read it. */
export function TemplateModal({ event, catalogue, onClose }: { event: NotifyEvent; catalogue: NotifyCatalogue | null; onClose: () => void }) {
  const templates = (catalogue?.templates ?? []).filter((t) => t.event === event.key)
  return (
    <Modal title={`Template — ${event.title}`} wide onClose={onClose} footer={<button onClick={onClose}>Close</button>}>
      <div className="stack tight">
        <p className="muted small">{event.description}</p>
        <p className="muted small">
          Emitted by <span className="mono">{event.source}</span>. A second language is one more template file for these same event keys; nothing else changes.
        </p>
        {templates.map((t) => (
          <div key={t.locale} className="stack tight">
            <h3>
              Locale <span className="mono">{t.locale}</span>
            </h3>
            <div>
              <div className="muted small">Subject</div>
              <pre className="details">{t.subject}</pre>
            </div>
            <div>
              <div className="muted small">Body</div>
              <pre className="details">{t.body}</pre>
            </div>
          </div>
        ))}
        {event.payload?.length ? (
          <div>
            <h3>Payload</h3>
            <dl className="kv">
              {event.payload.map((f) => (
                <Fragment key={f.name}>
                  <dt className="mono">{f.name}</dt>
                  <dd>{f.description}</dd>
                </Fragment>
              ))}
            </dl>
          </div>
        ) : null}
      </div>
    </Modal>
  )
}

/** Edit one scope's preference for one event. */
export function PreferenceModal({
  event,
  effective,
  channels,
  locales,
  path,
  onClose,
  onDone,
}: {
  event: NotifyEvent
  effective?: NotifyEffective
  channels: NotifyChannel[]
  locales: string[]
  path: string
  onClose: () => void
  onDone: () => void
}) {
  const [enabled, setEnabled] = useState(effective?.enabled ?? event.default_on)
  const [selected, setSelected] = useState<string[]>(effective?.channels ?? event.channels)
  const [locale, setLocale] = useState(effective?.locale ?? '')
  const act = useAction()

  const toggle = (name: string) => setSelected((cur) => (cur.includes(name) ? cur.filter((c) => c !== name) : [...cur, name]))

  const submit = async () => {
    const ok = await act.run(`${event.title} saved`, () => api.put(path, { event: event.key, enabled, channels: selected, locale }), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={`${event.title}`}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" onClick={() => void submit()} disabled={act.busy}>
            Save
          </button>
        </>
      }
    >
      <div className="stack tight">
        <p className="muted small">{event.description}</p>
        {event.mandatory ? (
          <Notice kind="warn">
            This is a mandatory notice. It cannot be switched off and it always keeps its {event.channels.join(', ')} channel; you may add a channel to it.
          </Notice>
        ) : null}
        <Field label="Receive this message" help={event.mandatory ? 'A mandatory notice is always sent.' : 'Off means nobody in this scope is sent it.'}>
          <input type="checkbox" aria-label="Receive this message" checked={enabled} disabled={event.mandatory} onChange={(e) => setEnabled(e.target.checked)} />
        </Field>
        <Field label="Channels" help="A channel with no transport refuses at send time and records the refusal — it never silently drops the message.">
          <div className="stack tight">
            {channels.map((c) => (
              <label key={c.name} className="row">
                <input type="checkbox" aria-label={`Channel ${c.name}`} checked={selected.includes(c.name)} onChange={() => toggle(c.name)} />
                <span className="mono">{c.name}</span>
                {c.available ? null : <span className="warn small">no transport — {c.reason}</span>}
              </label>
            ))}
          </div>
        </Field>
        <Field label="Language" help="Empty uses the default language. An event with no template in the chosen language falls back to the default.">
          <select aria-label="Language" value={locale} onChange={(e) => setLocale(e.target.value)}>
            <option value="">default</option>
            {locales.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </select>
        </Field>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      </div>
    </Modal>
  )
}
