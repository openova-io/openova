import type { Estimate, EstimateLine, PublicCatalog, PublicCatalogPlan, PublicCatalogRate, PublicCatalogSKU } from '../../api/types'
import { colorForKey } from '../../components/charts/palette'
import { addonLines } from '../../lib/packages'
import { HOURS_PER_MONTH, MAX_HOURS, MAX_LINES, MAX_MONTHS, MAX_QUANTITY } from '../../pages/Estimate'

/**
 * The public calculator's model (DESIGN.md §12.5): the service catalogue by
 * product family, the configurator that turns friendly choices into SKU
 * lines, and the estimate the items become.
 *
 * Two rules hold everything here. The catalogue is BUILT from the facts the
 * API states per SKU (family, service, display name, shape) — nothing in
 * the browser reads a SKU string to decide what it is. And nothing here is
 * money arithmetic: every price on the page comes back from
 * POST /public/estimates?preview=1, priced by the function the invoices
 * use; the only figure this module adds is a SUM of the server's own line
 * amounts, so an item can show what its lines came to.
 */

// ── the catalogue ─────────────────────────────────────────────────────

/** Anything the catalog prices: a list SKU, a pay-per-use meter or a plan. */
export type CatalogEntry = PublicCatalogSKU | PublicCatalogRate | PublicCatalogPlan

export interface CatalogService {
  key: string
  name: string
  family: string
  familyName: string
  /** One line under the name, in a prospect's words. */
  blurb: string
  entries: CatalogEntry[]
  /** The cheapest monthly figure among the entries, as the server priced one unit for 730 h. */
  from: CatalogEntry | null
}

export interface CatalogFamily {
  key: string
  name: string
  /** One line under the family name. */
  blurb: string
  services: CatalogService[]
}

/** The families in the order the page lists them; anything else follows, then Other services last. */
const FAMILY_ORDER = ['compute', 'storage', 'networking', 'databases', 'containers', 'plans']

/** The families in palette order — colour follows the family, never its rank in an estimate. */
export const FAMILY_KEYS: readonly string[] = [...FAMILY_ORDER, 'other']

/** The colour a family is drawn in, from the chart palette. */
export function familyColor(family: string): string {
  return colorForKey(family, FAMILY_KEYS)
}

/** One line under each family name. */
export const FAMILY_BLURBS: Record<string, string> = {
  compute: 'Servers, and the scaling that keeps up with demand.',
  storage: 'Disks, backups and images, priced per GB.',
  networking: 'Addresses, load balancing, gateways and protection.',
  databases: 'Managed relational, document and GaussDB engines, with their storage.',
  containers: 'Kubernetes — a managed cluster, or pay-per-use capacity on the platform.',
  plans: 'A bundled Organization on the platform, billed by the month.',
  other: 'Priced on the list, not yet sorted into a family.',
}

/** Services in the order they read best within a family; unlisted ones follow alphabetically. */
const SERVICE_ORDER = ['ecs', 'as', 'evs', 'cbr', 'ims', 'eip', 'elb', 'nat', 'vpc', 'vpcep', 'dns', 'waf', 'rds-mysql', 'rds-postgresql', 'dds', 'gaussdb', 'cce', 'k8s', 'plan']

/** One line under each service name. A service the API names but this map does not simply shows its name. */
export const BLURBS: Record<string, string> = {
  ecs: 'Virtual servers — general purpose, compute-optimised or memory-optimised, with a disk and an Elastic IP if you want them.',
  as: 'Adds and removes servers with demand.',
  evs: 'Disks for your servers — SSD or HDD, priced per GB.',
  cbr: 'Backups of servers and disks, priced per GB kept.',
  ims: 'Private server images, priced per GB kept.',
  eip: 'A public IP address with the bandwidth you choose.',
  elb: 'Spreads traffic over your servers.',
  nat: 'Outbound internet for servers without a public address.',
  vpc: 'Your private network.',
  vpcep: 'Private access to a service without crossing the internet.',
  dns: 'Hosted zones for your domains.',
  waf: 'Protects web applications from common attacks.',
  'rds-mysql': 'Managed MySQL — single node or primary + standby, with storage.',
  'rds-postgresql': 'Managed PostgreSQL — single node or primary + standby, with storage.',
  'rds-storage': 'Storage for a managed relational database, per GB.',
  dds: 'Managed MongoDB (Document Database Service), with storage.',
  'dds-storage': 'Storage for a managed MongoDB, per GB.',
  gaussdb: 'Huawei GaussDB — centralized or distributed, with storage.',
  'gaussdb-storage': 'Storage for GaussDB, per GB.',
  cce: 'A managed Kubernetes control plane, sized by how many nodes it runs.',
  k8s: 'Pay-per-use vCPU, memory and persistent storage on the platform.',
  plan: 'A bundled Organization on the platform, billed by the month.',
}

/**
 * Which service's configurator asks for a service's companion storage. A
 * companion is listed on its own only when none of the services it belongs
 * to is published.
 */
export const STORAGE_OF: Record<string, string> = {
  'rds-mysql': 'rds-storage',
  'rds-postgresql': 'rds-storage',
  dds: 'dds-storage',
  gaussdb: 'gaussdb-storage',
}

function rank(order: string[], key: string): number {
  const i = order.indexOf(key)
  return i < 0 ? order.length : i
}

function monthlyNumber(e: CatalogEntry): number {
  const n = Number(e.monthly)
  return Number.isFinite(n) ? n : Number.POSITIVE_INFINITY
}

/** Every priced entry of the catalog: the list SKUs, the pay-per-use meters and the plans. */
export function catalogEntries(cat: PublicCatalog): CatalogEntry[] {
  return [...cat.skus, ...cat.payg, ...cat.plans]
}

/**
 * The service catalogue: families in display order, each with its services
 * and their priced entries. Only what the published books price is here —
 * a family with nothing priced does not appear, and a companion storage
 * service hides behind the engines that ask for it.
 */
export function buildCatalogue(cat: PublicCatalog | null): CatalogFamily[] {
  if (!cat) return []
  const services = new Map<string, CatalogService>()
  for (const e of catalogEntries(cat)) {
    const key = e.service || 'other'
    let svc = services.get(key)
    if (!svc) {
      svc = { key, name: e.service_name || key, family: e.family || 'other', familyName: e.family_name || 'Other services', blurb: BLURBS[key] ?? '', entries: [], from: null }
      services.set(key, svc)
    }
    svc.entries.push(e)
    if (!svc.from || monthlyNumber(e) < monthlyNumber(svc.from)) svc.from = e
  }
  // Companion storage folds into its engines when any of them is present.
  const parents = new Map<string, string[]>()
  for (const [engine, storage] of Object.entries(STORAGE_OF)) parents.set(storage, [...(parents.get(storage) ?? []), engine])
  const families = new Map<string, CatalogFamily>()
  for (const svc of services.values()) {
    if ((parents.get(svc.key) ?? []).some((p) => services.has(p))) continue
    let fam = families.get(svc.family)
    if (!fam) {
      fam = { key: svc.family, name: svc.familyName, blurb: FAMILY_BLURBS[svc.family] ?? '', services: [] }
      families.set(svc.family, fam)
    }
    fam.services.push(svc)
  }
  const out = [...families.values()]
  for (const fam of out) {
    fam.services.sort((a, b) => rank(SERVICE_ORDER, a.key) - rank(SERVICE_ORDER, b.key) || a.name.localeCompare(b.name))
  }
  return out.sort((a, b) => {
    if (a.key === 'other' || b.key === 'other') return a.key === 'other' ? 1 : -1
    return rank(FAMILY_ORDER, a.key) - rank(FAMILY_ORDER, b.key) || a.name.localeCompare(b.name)
  })
}

/** The service a key names, whether it is listed or folded into an engine. */
export function findService(cat: PublicCatalog | null, key: string): CatalogService | null {
  if (!cat) return null
  const entries = catalogEntries(cat).filter((e) => e.service === key)
  if (!entries.length) return null
  const [e] = entries
  let from: CatalogEntry | null = null
  for (const x of entries) if (!from || monthlyNumber(x) < monthlyNumber(from)) from = x
  return { key, name: e.service_name || key, family: e.family || 'other', familyName: e.family_name || 'Other services', blurb: BLURBS[key] ?? '', entries, from }
}

/** The services whose name, blurb, family or options match a search. */
export function searchCatalogue(families: CatalogFamily[], query: string): CatalogFamily[] {
  const q = query.trim().toLowerCase()
  if (!q) return families
  return families
    .map((f) => ({
      ...f,
      services: f.services.filter((s) => `${s.name} ${s.blurb} ${f.name} ${s.entries.map((e) => `${e.display_name} ${e.sku} ${'description' in e ? (e.description ?? '') : ''}`).join(' ')}`.toLowerCase().includes(q)),
    }))
    .filter((f) => f.services.length)
}

/** "per GB", "per Mbps" — what one unit of a rate is, for the "from" figure. */
export function unitHint(unit: string): string {
  const u = unit.toLowerCase()
  if (u.startsWith('gb')) return 'per GB'
  if (u.startsWith('gib')) return 'per GiB'
  if (u.startsWith('mbps')) return 'per Mbps'
  if (u.startsWith('vcpu')) return 'per vCPU'
  return ''
}

// ── the configurator ──────────────────────────────────────────────────

/**
 * How a service is configured. The kind is decided by the service key the
 * API states; a service nobody has mapped gets the generic form (pick an
 * option, a quantity and the hours), so a new prefix is priceable at once.
 */
export type Kind = 'server' | 'database' | 'storage' | 'network' | 'eip' | 'sized' | 'capacity' | 'plan' | 'generic'

export const KINDS: Record<string, Kind> = {
  ecs: 'server',
  'rds-mysql': 'database',
  'rds-postgresql': 'database',
  dds: 'database',
  gaussdb: 'database',
  evs: 'storage',
  cbr: 'storage',
  ims: 'storage',
  'rds-storage': 'storage',
  'dds-storage': 'storage',
  'gaussdb-storage': 'storage',
  eip: 'eip',
  nat: 'sized',
  cce: 'sized',
  k8s: 'capacity',
  plan: 'plan',
  elb: 'network',
  vpc: 'network',
  vpcep: 'network',
  dns: 'network',
  waf: 'network',
  as: 'network',
}

export function kindOf(service: string): Kind {
  return KINDS[service] ?? 'generic'
}

export type Usage = 'always' | 'business' | 'custom'

export const USAGES: ReadonlyArray<{ value: Usage; label: string; hours: string }> = [
  { value: 'always', label: 'Always on (730 h/month)', hours: HOURS_PER_MONTH },
  { value: 'business', label: 'Business hours (176 h/month)', hours: '176' },
  { value: 'custom', label: 'Custom hours', hours: '' },
]

/**
 * Everything a configurator asks, as typed. One flat shape serves every
 * kind so an item re-opens with exactly the values it was added with.
 */
export interface ItemConfig {
  service: string
  /** ECS class, EVS media, GaussDB topology — the first dropdown. */
  variant: string
  /** The chosen option's SKU: the server size, the database size, the NAT spec, the storage media. */
  sku: string
  /** single | ha on a database. */
  deployment: string
  quantity: string
  usage: Usage
  /** Hours per month when usage is custom. */
  hours: string
  /** GB: a database's storage, a server's attached disk, a storage service's size. */
  storageGb: string
  /** The attached disk's media on a server; empty = no disk. */
  diskVariant: string
  /** A server takes an Elastic IP. */
  eip: boolean
  /** Mbps on an Elastic IP — the server's, or the EIP service's. */
  bandwidthMbps: string
  /** Kubernetes capacity. */
  vcpu: string
  memGb: string
  pvcGb: string
  /** A platform plan and how many months of it. */
  plan: string
  months: string
  /** The package's optional features taken with the plan (DESIGN.md §22), by feature key; each adds its add-on line. */
  addons?: string[]
}

/** What one unit of a configurator's quantity is, in the words of the form. */
export const QUANTITY_LABEL: Record<Kind, string> = {
  server: 'Servers',
  database: 'Databases',
  storage: 'Quantity',
  network: 'Quantity',
  eip: 'Addresses',
  sized: 'Quantity',
  capacity: 'Quantity',
  plan: 'Organizations',
  generic: 'Quantity',
}

function entriesOf(svc: CatalogService, pred: (e: CatalogEntry) => boolean): CatalogEntry[] {
  return svc.entries.filter(pred)
}

/** The options of a service's first dropdown (variants), in catalog order, deduplicated. */
export function variantsOf(svc: CatalogService): Array<{ value: string; label: string }> {
  const seen = new Map<string, string>()
  for (const e of svc.entries) {
    const v = e.variant ?? ''
    if (!seen.has(v)) seen.set(v, e.variant_name || v || svc.name)
  }
  return [...seen.entries()].map(([value, label]) => ({ value, label }))
}

/** The deployments a database service is published in: single first, then ha. */
export function deploymentsOf(svc: CatalogService): Array<{ value: string; label: string }> {
  const set = new Set(svc.entries.map((e) => e.deployment ?? '').filter(Boolean))
  const label = (d: string) => (d === 'ha' ? 'Primary + standby' : d === 'single' ? 'Single node' : d)
  return [...set].sort((a, b) => (a === 'single' ? -1 : b === 'single' ? 1 : a.localeCompare(b))).map((value) => ({ value, label: label(value) }))
}

/**
 * The size options a configurator lists for its current first choices,
 * smallest first. Two entries of the same shape are told apart by their
 * SKU so a choice is never ambiguous.
 */
export function sizeOptions(svc: CatalogService, pick: { variant?: string; deployment?: string }): Array<{ sku: string; label: string; hint?: string; entry: CatalogEntry }> {
  const list = entriesOf(svc, (e) => (pick.variant === undefined || (e.variant ?? '') === pick.variant) && (pick.deployment === undefined || (e.deployment ?? '') === pick.deployment))
  list.sort((a, b) => (a.vcpu ?? 0) - (b.vcpu ?? 0) || (a.memory_gb ?? 0) - (b.memory_gb ?? 0) || (a.size ?? 0) - (b.size ?? 0) || monthlyNumber(a) - monthlyNumber(b))
  const counts = new Map<string, number>()
  for (const e of list) counts.set(e.display_name, (counts.get(e.display_name) ?? 0) + 1)
  return list.map((entry) => {
    const shape = entry.vcpu && entry.memory_gb ? `${entry.vcpu} vCPU · ${entry.memory_gb} GB` : entry.display_name
    const twin = (counts.get(entry.display_name) ?? 0) > 1
    return { sku: entry.sku, label: twin ? `${shape} (${entry.sku})` : shape, hint: twin ? entry.sku : undefined, entry }
  })
}

/** The entry a SKU names in a service, if it is published. */
export function entryBySku(svc: CatalogService | null, sku: string): CatalogEntry | null {
  return svc?.entries.find((e) => e.sku === sku) ?? null
}

/** The storage entry of a database service for a deployment, or the only one it has. */
export function storageFor(cat: PublicCatalog | null, service: string, deployment: string): CatalogEntry | null {
  const storage = findService(cat, STORAGE_OF[service] ?? '')
  if (!storage) return null
  return storage.entries.find((e) => (e.deployment ?? '') === deployment) ?? storage.entries[0] ?? null
}

/** The configurator's opening values for a service — the smallest option, one unit, always on. */
export function defaultConfig(cat: PublicCatalog | null, svc: CatalogService): ItemConfig {
  const kind = kindOf(svc.key)
  const variant = variantsOf(svc)[0]?.value ?? ''
  const deployment = kind === 'database' ? (deploymentsOf(svc)[0]?.value ?? '') : ''
  const first = sizeOptions(svc, kind === 'database' ? { deployment } : kind === 'server' || kind === 'storage' ? { variant } : {})[0]
  const eipService = findService(cat, 'eip')
  return {
    service: svc.key,
    variant,
    sku: first?.sku ?? svc.entries[0]?.sku ?? '',
    deployment,
    quantity: '1',
    usage: 'always',
    hours: HOURS_PER_MONTH,
    storageGb: kind === 'database' || kind === 'storage' ? '100' : '',
    diskVariant: '',
    eip: false,
    bandwidthMbps: kind === 'eip' ? '5' : eipService ? '5' : '',
    vcpu: kind === 'capacity' ? '4' : '',
    memGb: kind === 'capacity' ? '8' : '',
    pvcGb: kind === 'capacity' ? '50' : '',
    plan: kind === 'plan' ? ((svc.entries[0] as PublicCatalogPlan | undefined)?.slug ?? '') : '',
    months: '1',
    addons: [],
  }
}

/** The hours a configuration runs each month. */
export function usageHours(c: Pick<ItemConfig, 'usage' | 'hours'>): string {
  return USAGES.find((u) => u.value === c.usage)?.hours || c.hours.trim()
}

/** One line an item sends: an SKU by the hour, a plan by the month, or a package add-on by the month (an SKU with months and no hours). */
export interface ItemLine {
  sku?: string
  plan?: string
  label: string
  quantity: string
  hours?: string
  months?: number
}

/** "2 × 176 h" / "1 × 3 month(s)" — how a line's quantity reads. */
export function lineUsageText(l: Pick<ItemLine, 'plan' | 'quantity' | 'hours' | 'months'>): string {
  if (l.plan || l.hours === undefined) return `${l.quantity} × ${l.months ?? 1} month(s)`
  return `${l.quantity} × ${l.hours} h`
}

export interface ItemResult {
  lines: ItemLine[]
  /** Field → why, in the words the server would use. Empty when the item can be added. */
  errors: Record<string, string>
}

function positive(v: string, what: string, max = MAX_QUANTITY): string {
  const n = Number(v)
  if (!v.trim() || !Number.isFinite(n) || n <= 0) return `${what} must be more than 0`
  if (n > max) return `${what} must be at most ${max.toLocaleString('en-US')}`
  return ''
}

function nonNegative(v: string, what: string): string {
  if (!v.trim()) return ''
  const n = Number(v)
  if (!Number.isFinite(n) || n < 0) return `${what} must be 0 or more`
  if (n > MAX_QUANTITY) return `${what} must be at most ${MAX_QUANTITY.toLocaleString('en-US')}`
  return ''
}

/** The count a GB field multiplies out to, as a decimal string the API accepts. */
function times(gb: string, count: string): string {
  const n = Number(gb) * Number(count)
  return Number.isInteger(n) ? String(n) : n.toFixed(6).replace(/0+$/, '').replace(/\.$/, '')
}

/**
 * The lines a configuration becomes, or what is still wrong with it. The
 * SKUs come from the entries the prospect chose — a label is never turned
 * back into a SKU — and every rule is the server's, so a form that passes
 * here is not refused there.
 */
export function itemLines(cat: PublicCatalog | null, c: ItemConfig): ItemResult {
  const svc = findService(cat, c.service)
  const kind = kindOf(c.service)
  const errors: Record<string, string> = {}
  const lines: ItemLine[] = []
  if (!svc) return { lines, errors: { service: 'this service is not in the public price list' } }

  const hours = usageHours(c)
  const hoursErr = (() => {
    const h = Number(hours)
    if (!hours || !Number.isFinite(h) || h <= 0 || h > MAX_HOURS) return `hours must be more than 0 and at most ${MAX_HOURS}`
    return ''
  })()
  if (kind !== 'plan' && hoursErr) errors.hours = hoursErr

  const chosen = entryBySku(svc, c.sku)
  const qtyErr = positive(c.quantity, 'quantity')
  if (qtyErr) errors.quantity = qtyErr
  const qty = c.quantity.trim()

  switch (kind) {
    case 'server': {
      if (!chosen) errors.sku = 'choose a size'
      else lines.push({ sku: chosen.sku, label: chosen.display_name, quantity: qty, hours })
      if (c.diskVariant) {
        const disk = findService(cat, 'evs')?.entries.find((e) => (e.variant ?? '') === c.diskVariant) ?? null
        const err = positive(c.storageGb, 'disk size')
        if (err) errors.storageGb = err
        else if (disk) lines.push({ sku: disk.sku, label: `${disk.display_name} disk · ${c.storageGb.trim()} GB each`, quantity: times(c.storageGb, qty), hours: HOURS_PER_MONTH })
      }
      if (c.eip) {
        const eipSvc = findService(cat, 'eip')
        const address = eipSvc?.entries.find((e) => e.variant === 'address') ?? null
        const bandwidth = eipSvc?.entries.find((e) => e.variant === 'bandwidth') ?? null
        if (address) lines.push({ sku: address.sku, label: address.display_name, quantity: qty, hours })
        if (bandwidth) {
          const err = positive(c.bandwidthMbps, 'bandwidth')
          if (err) errors.bandwidthMbps = err
          else lines.push({ sku: bandwidth.sku, label: `${bandwidth.display_name} · ${c.bandwidthMbps.trim()} Mbps each`, quantity: times(c.bandwidthMbps, qty), hours })
        }
      }
      break
    }
    case 'database': {
      if (!chosen) errors.sku = 'choose a size'
      else lines.push({ sku: chosen.sku, label: chosen.display_name, quantity: qty, hours })
      const storage = storageFor(cat, c.service, c.deployment)
      if (storage) {
        const err = positive(c.storageGb, 'storage')
        if (err) errors.storageGb = err
        else lines.push({ sku: storage.sku, label: `Storage · ${c.storageGb.trim()} GB each`, quantity: times(c.storageGb, qty), hours: HOURS_PER_MONTH })
      }
      break
    }
    case 'storage': {
      if (!chosen) errors.sku = 'choose a type'
      const err = positive(c.storageGb, 'size')
      if (err) errors.storageGb = err
      if (chosen && !err) lines.push({ sku: chosen.sku, label: `${chosen.display_name} · ${c.storageGb.trim()} GB each`, quantity: times(c.storageGb, qty), hours })
      break
    }
    case 'eip': {
      const address = svc.entries.find((e) => e.variant === 'address') ?? null
      const bandwidth = svc.entries.find((e) => e.variant === 'bandwidth') ?? null
      if (address) lines.push({ sku: address.sku, label: address.display_name, quantity: qty, hours })
      if (bandwidth) {
        const err = positive(c.bandwidthMbps, 'bandwidth')
        if (err) errors.bandwidthMbps = err
        else lines.push({ sku: bandwidth.sku, label: `${bandwidth.display_name} · ${c.bandwidthMbps.trim()} Mbps each`, quantity: times(c.bandwidthMbps, qty), hours })
      }
      break
    }
    case 'capacity': {
      const meter = (variant: string) => svc.entries.find((e) => e.variant === variant) ?? null
      const parts: Array<[string, string, string]> = [
        ['vcpu', c.vcpu, 'vCPU'],
        ['memory', c.memGb, 'memory'],
        ['storage', c.pvcGb, 'storage'],
      ]
      for (const [variant, value, what] of parts) {
        const m = meter(variant)
        if (!m) continue
        const err = nonNegative(value, what)
        if (err) errors[variant] = err
        else if (Number(value) > 0) lines.push({ sku: m.sku, label: `${m.display_name} · ${value.trim()}`, quantity: times(value, qty), hours })
      }
      if (!lines.length && !Object.keys(errors).length) errors.vcpu = 'ask for some vCPU, memory or storage'
      break
    }
    case 'plan': {
      const plan = svc.entries.find((e) => 'slug' in e && e.slug === c.plan) as PublicCatalogPlan | undefined
      const months = Number(c.months)
      if (!plan) errors.plan = 'choose a plan'
      if (!Number.isInteger(months) || months < 1 || months > MAX_MONTHS) errors.months = `months must be between 1 and ${MAX_MONTHS}`
      if (plan && !errors.months) {
        lines.push({ plan: plan.slug, label: plan.display_name || `${plan.name} plan`, quantity: qty, months })
        // The package's add-ons (DESIGN.md §22): one line per optional
        // feature taken, by the month like the plan it extends. A feature
        // the chosen package includes adds nothing — it is in the plan.
        for (const a of addonLines(cat?.packages, plan.slug, c.addons ?? [])) lines.push({ sku: a.sku, label: a.label, quantity: qty, months })
      }
      break
    }
    default: {
      // network, sized and generic: one option, a quantity and the hours.
      const entry = chosen ?? (svc.entries.length === 1 ? svc.entries[0] : null)
      if (!entry) errors.sku = 'choose an option'
      else lines.push({ sku: entry.sku, label: entry.display_name, quantity: qty, hours })
    }
  }
  return { lines, errors }
}

/** The one-line summary an item shows: "2 × General purpose · 4 vCPU · 16 GB · business hours". */
export function describeItem(cat: PublicCatalog | null, c: ItemConfig): string {
  const svc = findService(cat, c.service)
  const kind = kindOf(c.service)
  const chosen = entryBySku(svc, c.sku)
  const usage = c.usage === 'custom' ? `${c.hours.trim()} h/month` : c.usage === 'business' ? 'business hours' : 'always on'
  const n = c.quantity.trim()
  switch (kind) {
    case 'server':
      return `${n} × ${chosen?.display_name ?? 'server'} · ${usage}`
    case 'database': {
      const dep = deploymentsOf(svc ?? { key: '', name: '', family: '', familyName: '', blurb: '', entries: [], from: null }).find((d) => d.value === c.deployment)?.label
      return `${n} × ${chosen ? `${chosen.vcpu} vCPU · ${chosen.memory_gb} GB` : 'database'}${dep ? ` · ${dep}` : ''} · ${c.storageGb.trim()} GB · ${usage}`
    }
    case 'storage':
      return `${n} × ${c.storageGb.trim()} GB ${chosen?.display_name ?? ''}`.trim() + (c.usage === 'always' ? '' : ` · ${usage}`)
    case 'eip':
      return `${n} × ${c.bandwidthMbps.trim()} Mbps · ${usage}`
    case 'capacity':
      return [c.vcpu.trim() && `${c.vcpu.trim()} vCPU`, c.memGb.trim() && `${c.memGb.trim()} GiB`, c.pvcGb.trim() && `${c.pvcGb.trim()} GB storage`].filter(Boolean).join(' · ') + (n !== '1' ? ` × ${n}` : '') + ` · ${usage}`
    case 'plan': {
      const addons = addonLines(cat?.packages, c.plan, c.addons ?? []).map((a) => a.label.replace(/ add-on$/, ''))
      return `${n} × ${(svc?.entries.find((e) => 'slug' in e && e.slug === c.plan) as PublicCatalogPlan | undefined)?.display_name ?? c.plan.toUpperCase()} · ${c.months.trim()} month(s)${addons.length ? ` + ${addons.join(', ')}` : ''}`
    }
    default:
      return `${n} × ${chosen?.display_name ?? svc?.name ?? c.service} · ${usage}`
  }
}

// ── the estimate ──────────────────────────────────────────────────────

/** One configured service in the estimate: its choices and the lines they became. */
export interface EstimateItem {
  id: string
  service: string
  serviceName: string
  family: string
  familyName: string
  summary: string
  config: ItemConfig
  lines: ItemLine[]
}

/** Builds an item from a configuration the configurator accepted. */
export function makeItem(cat: PublicCatalog | null, id: string, c: ItemConfig): EstimateItem {
  const svc = findService(cat, c.service)
  return { id, service: c.service, serviceName: svc?.name ?? c.service, family: svc?.family ?? 'other', familyName: svc?.familyName ?? 'Other services', summary: describeItem(cat, c), config: c, lines: itemLines(cat, c).lines }
}

export function upsertItem(items: EstimateItem[], item: EstimateItem): EstimateItem[] {
  const at = items.findIndex((i) => i.id === item.id)
  if (at < 0) return [...items, item]
  const copy = [...items]
  copy[at] = item
  return copy
}

export function removeItem(items: EstimateItem[], id: string): EstimateItem[] {
  return items.filter((i) => i.id !== id)
}

/** Items grouped under their service, services in the order they were first added. */
export function groupItems(items: EstimateItem[]): Array<{ service: string; serviceName: string; familyName: string; items: EstimateItem[] }> {
  const groups = new Map<string, { service: string; serviceName: string; familyName: string; items: EstimateItem[] }>()
  for (const it of items) {
    const g = groups.get(it.service) ?? { service: it.service, serviceName: it.serviceName, familyName: it.familyName, items: [] }
    g.items.push(it)
    groups.set(it.service, g)
  }
  return [...groups.values()]
}

export interface EstimateRequest {
  region?: string
  contact_email?: string
  lines: Array<{ sku?: string; plan?: string; quantity: string; hours_per_month?: string; months?: number }>
}

/**
 * The request body for POST /public/estimates, and where each item's lines
 * sit in it — the priced lines come back in the same order, so an item's
 * amounts are read back by position.
 */
export function requestBody(items: EstimateItem[], region?: string, contactEmail?: string): { body: EstimateRequest; spans: Array<{ id: string; start: number; count: number }> } {
  const body: EstimateRequest = { lines: [] }
  const spans: Array<{ id: string; start: number; count: number }> = []
  for (const it of items) {
    spans.push({ id: it.id, start: body.lines.length, count: it.lines.length })
    for (const l of it.lines) {
      if (l.plan) body.lines.push({ plan: l.plan, quantity: l.quantity, months: l.months ?? 1 })
      // A package add-on is an SKU by the month, like the plan (DESIGN.md §22).
      else if (l.hours === undefined && l.months !== undefined) body.lines.push({ sku: l.sku, quantity: l.quantity, months: l.months })
      else body.lines.push({ sku: l.sku, quantity: l.quantity, hours_per_month: l.hours ?? HOURS_PER_MONTH })
    }
  }
  if (region) body.region = region
  const email = (contactEmail ?? '').trim()
  if (email) body.contact_email = email
  return { body, spans }
}

/** An estimate is sendable when it has at least one line and no more than the cap. */
export function estimateReady(items: EstimateItem[]): boolean {
  const n = items.reduce((sum, it) => sum + it.lines.length, 0)
  return n > 0 && n <= MAX_LINES
}

/** One of an item's lines as the server priced it. */
export interface PricedLine {
  line: ItemLine
  priced: EstimateLine | null
}

export interface PricedItem {
  lines: PricedLine[]
  /** The sum of the server's line amounts — a total of prices, never a price. Null until the server has answered. */
  amount: string | null
  /** True when every line is one month, so the amount is a monthly figure. */
  monthly: boolean
}

/**
 * The server's priced lines, split back onto the items that asked for them.
 * A stale answer (a different line count than the items now have) prices
 * nothing rather than pricing the wrong item.
 */
export function pricedByItem(items: EstimateItem[], estimate: Estimate | null): Map<string, PricedItem> {
  const { spans } = requestBody(items)
  const total = spans.reduce((n, s) => n + s.count, 0)
  const fresh = estimate !== null && estimate.lines.length === total
  const out = new Map<string, PricedItem>()
  for (const it of items) {
    const span = spans.find((s) => s.id === it.id)
    const lines: PricedLine[] = it.lines.map((line, i) => ({ line, priced: fresh && span ? (estimate.lines[span.start + i] ?? null) : null }))
    const amounts = lines.map((l) => l.priced?.amount).filter((a): a is string | number => a !== undefined && a !== null)
    out.set(it.id, { lines, amount: fresh && amounts.length === lines.length ? sumAmounts(amounts) : null, monthly: it.lines.every((l) => (l.months ?? 1) === 1) })
  }
  return out
}

/** What one family came to, and its share of the whole estimate. */
export interface FamilyShare {
  family: string
  familyName: string
  /** The exact sum of the family's item amounts, as the server priced them. */
  amount: string
  /** The family's proportion of the estimate, 0..1 — geometry for the bar, never a figure shown as money. */
  share: number
  color: string
}

/**
 * The estimate by family, largest first: each family's item amounts summed
 * exactly, then proportioned for the breakdown bar and the gauge. Empty
 * until every item has been priced, so the bar never shows a partial whole.
 */
export function familyBreakdown(items: EstimateItem[], priced: Map<string, PricedItem>): FamilyShare[] {
  const sums = new Map<string, { familyName: string; amounts: string[] }>()
  for (const it of items) {
    const p = priced.get(it.id)
    if (!p || p.amount === null) return []
    const s = sums.get(it.family) ?? { familyName: it.familyName, amounts: [] }
    s.amounts.push(p.amount)
    sums.set(it.family, s)
  }
  const rows = [...sums.entries()].map(([family, s]) => ({ family, familyName: s.familyName, amount: sumAmounts(s.amounts), color: familyColor(family) }))
  const total = rows.reduce((n, r) => n + Number(r.amount), 0)
  return rows.map((r) => ({ ...r, share: total > 0 ? Number(r.amount) / total : 0 })).sort((a, b) => b.share - a.share || a.familyName.localeCompare(b.familyName))
}

const MICRO = BigInt(1000000)

function toMicro(v: string | number): bigint {
  const s = typeof v === 'number' ? v.toFixed(6) : String(v).trim()
  const neg = s.startsWith('-')
  const [int, frac = ''] = (neg ? s.slice(1) : s).split('.')
  const micro = BigInt(int || '0') * MICRO + BigInt((frac + '000000').slice(0, 6))
  return neg ? -micro : micro
}

/** The exact sum of six-decimal amounts, as a six-decimal string — no float ever touches a figure. */
export function sumAmounts(values: Array<string | number>): string {
  let total = BigInt(0)
  for (const v of values) total += toMicro(v)
  const neg = total < BigInt(0)
  const abs = neg ? -total : total
  return `${neg ? '-' : ''}${abs / MICRO}.${(abs % MICRO).toString().padStart(6, '0')}`
}
