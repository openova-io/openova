// Service grouping for statement lines (DESIGN.md §2.9). A statement carries
// SKUs, not resource kinds; the SKU's first token is the kind, and the labels
// are the Go KindLabel strings (internal/store/cost.go) so a statement reads
// like the explorer it was rated from.
import { toNumber } from './num'

export interface Service {
  key: string
  label: string
}

export const PLATFORM_SERVICE: Service = { key: 'k8s', label: 'Platform (Kubernetes)' }
export const OTHER_SERVICE: Service = { key: 'other', label: 'Other' }

const SERVICES: ReadonlyArray<Service> = [
  { key: 'ecs', label: 'Elastic Cloud Server' },
  { key: 'evs', label: 'Block storage (EVS)' },
  { key: 'eip', label: 'Elastic IP' },
  { key: 'elb', label: 'Load balancer' },
  { key: 'nat', label: 'NAT gateway' },
  { key: 'vpc', label: 'VPC' },
  { key: 'rds', label: 'Relational DB (RDS)' },
  { key: 'dds', label: 'Document DB (DDS)' },
  { key: 'gaussdb', label: 'GaussDB' },
  { key: 'cbr', label: 'Backup (CBR)' },
  { key: 'cce', label: 'Kubernetes cluster (CCE)' },
  { key: 'ims', label: 'Images (IMS)' },
  { key: 'dns', label: 'DNS' },
  { key: 'waf', label: 'Web application firewall' },
  { key: 'as', label: 'Auto scaling' },
  { key: 'vpcep', label: 'VPC endpoint' },
]
const BY_KEY = new Map(SERVICES.map((s) => [s.key, s]))

/**
 * The service a SKU belongs to. `ecs.s6.large.2` → ECS, `evs.ssd.gb` → EVS,
 * `eip`/`eip-bandwidth` → Elastic IP, `k8s.pod.vcpu` / `k8s-pvc.gb` →
 * Platform (Kubernetes). Anything unrecognised is "Other" rather than a
 * guess: a statement must never file a line under the wrong service.
 */
export function serviceOfSKU(sku: string): Service {
  const s = sku.trim().toLowerCase()
  if (!s) return OTHER_SERVICE
  const head = s.split(/[.\-_/:]/, 1)[0]
  if (head === 'k8s') return PLATFORM_SERVICE
  const svc = BY_KEY.get(head)
  if (svc) return svc
  if (s.startsWith('eip')) return BY_KEY.get('eip')!
  return OTHER_SERVICE
}

export interface ServiceGroup<L> {
  key: string
  label: string
  lines: L[]
  amount: number
  share: number
}

/** Lines grouped by service, largest group first, each group's lines largest first. */
export function groupByService<L extends { sku: string; amount: number | string }>(lines: L[]): ServiceGroup<L>[] {
  const map = new Map<string, ServiceGroup<L>>()
  for (const l of lines) {
    const svc = serviceOfSKU(l.sku)
    let g = map.get(svc.key)
    if (!g) {
      g = { key: svc.key, label: svc.label, lines: [], amount: 0, share: 0 }
      map.set(svc.key, g)
    }
    g.lines.push(l)
    g.amount += toNumber(l.amount)
  }
  const total = [...map.values()].reduce((n, g) => n + g.amount, 0)
  return [...map.values()]
    .map((g) => ({ ...g, share: total > 0 ? g.amount / total : 0, lines: [...g.lines].sort((a, b) => toNumber(b.amount) - toNumber(a.amount)) }))
    .sort((a, b) => b.amount - a.amount)
}

export interface SourceGroup {
  source_id: string
  amount: number
  share: number
  lines: number
}

/** Per-source breakdown; empty when no line names a source (older statements). */
export function groupBySource(lines: Array<{ source_id?: string | null; amount: number | string }>): SourceGroup[] {
  const map = new Map<string, SourceGroup>()
  let any = false
  for (const l of lines) {
    const id = l.source_id ?? ''
    if (id) any = true
    let g = map.get(id)
    if (!g) {
      g = { source_id: id, amount: 0, share: 0, lines: 0 }
      map.set(id, g)
    }
    g.amount += toNumber(l.amount)
    g.lines += 1
  }
  if (!any) return []
  const total = [...map.values()].reduce((n, g) => n + g.amount, 0)
  return [...map.values()].map((g) => ({ ...g, share: total > 0 ? g.amount / total : 0 })).sort((a, b) => b.amount - a.amount)
}
