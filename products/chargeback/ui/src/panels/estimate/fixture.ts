import type { PublicCatalog, PublicCatalogPlan, PublicCatalogRate, PublicCatalogSKU } from '../../api/types'

/**
 * Test data: a cut of the National Cloud 2026 list as GET /public/catalog
 * returns it, with the facts internal/api/publiccatalog.go states per SKU.
 * `monthly` is one unit for 730 h at the unit price, as the server prices it.
 */

function m(price: number): string {
  return (price * 730).toFixed(6)
}

function sku(id: string, family: string, familyName: string, service: string, serviceName: string, displayName: string, unit: string, price: number, extra: Partial<PublicCatalogSKU> = {}): PublicCatalogSKU {
  return { sku: id, family, family_name: familyName, service, service_name: serviceName, display_name: displayName, unit, unit_price: price.toFixed(8), monthly: m(price), ...extra }
}

const compute = (id: string, name: string, unit: string, price: number, extra: Partial<PublicCatalogSKU> = {}) => sku(id, 'compute', 'Compute', 'ecs', 'Elastic Cloud Server', name, unit, price, extra)
const net = (id: string, service: string, serviceName: string, name: string, price: number, extra: Partial<PublicCatalogSKU> = {}) => sku(id, 'networking', 'Networking', service, serviceName, name, 'hour', price, extra)
const db = (id: string, service: string, serviceName: string, name: string, price: number, extra: Partial<PublicCatalogSKU> = {}) => sku(id, 'databases', 'Databases', service, serviceName, name, 'instance-hour', price, extra)

export const skus: PublicCatalogSKU[] = [
  compute('ecs.s7n.small.1', 'General purpose · 1 vCPU · 1 GB', 'instance-hour', 0.01599491, { variant: 'general', variant_name: 'General purpose', vcpu: 1, memory_gb: 1 }),
  compute('ecs.s7n.xlarge.4', 'General purpose · 4 vCPU · 16 GB', 'instance-hour', 0.09138567, { variant: 'general', variant_name: 'General purpose', vcpu: 4, memory_gb: 16 }),
  compute('ecs.c7n.2xlarge.2', 'Compute-optimised · 8 vCPU · 16 GB', 'instance-hour', 0.19495082, { variant: 'compute', variant_name: 'Compute-optimised', vcpu: 8, memory_gb: 16 }),
  compute('ecs.c7n.large.2', 'Compute-optimised · 2 vCPU · 4 GB', 'instance-hour', 0.04873551, { variant: 'compute', variant_name: 'Compute-optimised', vcpu: 2, memory_gb: 4 }),
  compute('ecs.m7n.large.8', 'Memory-optimised · 2 vCPU · 16 GB', 'instance-hour', 0.07829879, { variant: 'memory', variant_name: 'Memory-optimised', vcpu: 2, memory_gb: 16 }),
  sku('as', 'compute', 'Compute', 'as', 'Auto Scaling', 'Auto Scaling group', 'hour', 0),
  sku('evs.ssd.gb', 'storage', 'Storage', 'evs', 'Block storage', 'SSD', 'gb-hour', 0.00022831, { variant: 'ssd', variant_name: 'SSD' }),
  sku('evs.hdd.gb', 'storage', 'Storage', 'evs', 'Block storage', 'HDD', 'gb-hour', 0.0000483, { variant: 'hdd', variant_name: 'HDD' }),
  sku('cbr.gb', 'storage', 'Storage', 'cbr', 'Backup', 'Backup storage', 'gb-hour', 0.00006761),
  net('eip', 'eip', 'Elastic IP', 'Elastic IP address', 0.02853881, { variant: 'address', variant_name: 'Address' }),
  { ...net('eip.bandwidth_mbps', 'eip', 'Elastic IP', 'Bandwidth per Mbps', 0.01716719, { variant: 'bandwidth', variant_name: 'Bandwidth' }), unit: 'mbps-hour' },
  net('elb', 'elb', 'Load balancer', 'Load balancer', 0.01923076),
  net('nat.1', 'nat', 'NAT gateway', 'Small', 0.06037935, { size: 1 }),
  net('nat.2', 'nat', 'NAT gateway', 'Medium', 0.11322445, { size: 2 }),
  net('vpc', 'vpc', 'Virtual private cloud', 'Virtual private cloud', 0),
  db('rds.mysql.c7.large.2.single', 'rds-mysql', 'RDS for MySQL', '2 vCPU · 4 GB · Single node', 0.07415042, { vcpu: 2, memory_gb: 4, deployment: 'single' }),
  db('rds.mysql.c7.large.2.ha', 'rds-mysql', 'RDS for MySQL', '2 vCPU · 4 GB · Primary + standby', 0.14834035, { vcpu: 2, memory_gb: 4, deployment: 'ha' }),
  db('rds.mysql.c7.xlarge.4.ha', 'rds-mysql', 'RDS for MySQL', '4 vCPU · 16 GB · Primary + standby', 0.32964524, { vcpu: 4, memory_gb: 16, deployment: 'ha' }),
  { ...db('rds.storage.single.gb', 'rds-storage', 'RDS storage', 'Single node', 0.00047208, { deployment: 'single' }), unit: 'gb-hour' },
  { ...db('rds.storage.ha.gb', 'rds-storage', 'RDS storage', 'Primary + standby', 0.00075527, { deployment: 'ha' }), unit: 'gb-hour' },
  sku('cce.cloud-container-engine-cce-excluding-vms-50-', 'containers', 'Containers', 'cce', 'CCE cluster', 'Up to 50 nodes', 'cluster-hour', 0.34380927, { size: 50 }),
  sku('cce.cloud-container-engine-cce-excluding-vms-100', 'containers', 'Containers', 'cce', 'CCE cluster', 'Up to 100 nodes', 'cluster-hour', 1.29420443, { size: 100 }),
  sku('obs.standard.gb', 'other', 'Other services', 'obs', 'OBS', 'obs.standard.gb', 'gb-hour', 0.00003, { description: 'Object storage, standard class' }),
]

export const payg: PublicCatalogRate[] = [
  { sku: 'k8s.vcpu', family: 'containers', family_name: 'Containers', service: 'k8s', service_name: 'Kubernetes capacity', display_name: 'vCPU', variant: 'vcpu', variant_name: 'vCPU', unit: 'vcpu-hour', unit_price: '0.00273973', monthly: '2.000003' },
  { sku: 'k8s.mem_gb', family: 'containers', family_name: 'Containers', service: 'k8s', service_name: 'Kubernetes capacity', display_name: 'Memory per GiB', variant: 'memory', variant_name: 'Memory', unit: 'gib-hour', unit_price: '0.00068493', monthly: '0.499999' },
  { sku: 'k8s.pvc_gb', family: 'containers', family_name: 'Containers', service: 'k8s', service_name: 'Kubernetes capacity', display_name: 'Persistent storage per GB', variant: 'storage', variant_name: 'Persistent storage', unit: 'gb-hour', unit_price: '0.00013699', monthly: '0.100003' },
]

export const plans: PublicCatalogPlan[] = [
  { slug: 's', name: 'S', sku: 'plan.s', family: 'plans', family_name: 'Platform plans', service: 'plan', service_name: 'Platform plans', display_name: 'S plan · 2 vCPU · 4 GB', variant: 's', variant_name: 'S', vcpu: 2, memory_gib: 4, memory_gb: 4, unit: 'plan-hour', unit_price: '0.00684932', monthly: '5.000004' },
  { slug: 'm', name: 'M', sku: 'plan.m', family: 'plans', family_name: 'Platform plans', service: 'plan', service_name: 'Platform plans', display_name: 'M plan · 4 vCPU · 8 GB', variant: 'm', variant_name: 'M', vcpu: 4, memory_gib: 8, memory_gb: 8, unit: 'plan-hour', unit_price: '0.01232877', monthly: '9.000002' },
]

export const catalog: PublicCatalog = {
  price_book: { id: 'pb1', name: 'NC list 2026', updated_at: '2026-09-01T00:00:00Z' },
  currency: 'OMR',
  tax_rate: '0.0500',
  regions: ['me-east-215-a', 'me-east-215-b'],
  skus,
  plans,
  payg,
  hours_per_month: 730,
  list_prices: true,
  notice: 'List prices. Taxes are shown separately. A negotiated price, a discount or a partner rate is never part of this estimate; contact us for a proposal.',
  generated_at: '2026-09-11T10:00:00Z',
}

/** The unit price of every priced entry, for a fake server in a test. */
export const unitPrices: Record<string, number> = Object.fromEntries([...skus, ...payg, ...plans].map((e) => [e.sku, Number(e.unit_price)]))
