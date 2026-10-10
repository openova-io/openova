import type { PackagesDoc, PublicCatalog, PublicCatalogPlan, PublicCatalogRate, PublicCatalogSKU } from '../../api/types'

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

/** The two larger plans, for a catalog that publishes the whole S / M / L / XL ladder. */
export const morePlans: PublicCatalogPlan[] = [
  { slug: 'l', name: 'L', sku: 'plan.l', family: 'plans', family_name: 'Platform plans', service: 'plan', service_name: 'Platform plans', display_name: 'L plan · 8 vCPU · 16 GB', variant: 'l', variant_name: 'L', vcpu: 8, memory_gib: 16, memory_gb: 16, unit: 'plan-hour', unit_price: '0.02191781', monthly: '16.000001' },
  { slug: 'xl', name: 'XL', sku: 'plan.xl', family: 'plans', family_name: 'Platform plans', service: 'plan', service_name: 'Platform plans', display_name: 'XL plan · 16 vCPU · 32 GB', variant: 'xl', variant_name: 'XL', vcpu: 16, memory_gib: 32, memory_gb: 32, unit: 'plan-hour', unit_price: '0.04109589', monthly: '30.000000' },
]

/**
 * The packages document as GET /public/packages answers it for the showcase
 * book (DESIGN.md §22): the four packages with their price and included
 * quantities, and a cut of the feature matrix with all three states.
 */
/**
 * The packages document as GET /public/packages answers it for the showcase
 * book (DESIGN.md §22.4), priced at the catalog's constants the plans above
 * carry: the groups, the floor, the four packages with their settings, shape
 * and step-up (holding on S, failing on L), and a cut of the ladder with
 * every kind and state — a quantity with its overage, a boolean add-on, a
 * teaser, an access door, a level with a purchasable next level.
 */
export const packages: PackagesDoc = {
  currency: 'OMR',
  price_book: 'OpenOva plans',
  prices_as_of: '2026-10-10',
  groups: [
    { key: 'capacity', name: 'Capacity' },
    { key: 'features', name: 'Features' },
    { key: 'access', name: 'Access' },
    { key: 'ops', name: 'Managed operations' },
    { key: 'scope', name: 'Scope' },
    { key: 'resilience', name: 'Resilience' },
    { key: 'service', name: 'Service level' },
  ],
  floor: [
    { key: 'ssl', name: 'Unlimited free SSL', blurb: 'Certificates for every site, renewed for you' },
    { key: 'ddos', name: 'Standard DDoS protection', blurb: 'Volumetric attacks absorbed at the edge' },
  ],
  packages: [
    {
      sku: 'plan.s',
      name: 'S',
      tagline: '',
      price_month: '5.000',
      recommended: false,
      annual_months_free: 0,
      shape: { vcpu: 1, memory_gb: 2, vcpu_guaranteed: 0.17, memory_gb_guaranteed: 0.67, disk_gb: 25 },
      step_up: { next_sku: 'plan.m', next_name: 'M', gap_month: '4.000', bundled_addon_keys: ['dr_topology'], bundled_addons_sum_month: '8.000', rule_holds: true },
      includes: { vcpu: 1, memory_gb: 2, bandwidth_mbps: 50, disk_gb: 25 },
    },
    {
      sku: 'plan.m',
      name: 'M',
      tagline: '',
      price_month: '9.000',
      recommended: true,
      annual_months_free: 0,
      shape: { vcpu: 2, memory_gb: 4, vcpu_guaranteed: 0.33, memory_gb_guaranteed: 1.33, disk_gb: 50 },
      step_up: { next_sku: 'plan.l', next_name: 'L', gap_month: '7.000', bundled_addon_keys: [], bundled_addons_sum_month: '0.000', rule_holds: true },
      includes: { vcpu: 2, memory_gb: 4, bandwidth_mbps: 100, disk_gb: 50 },
    },
    {
      sku: 'plan.l',
      name: 'L',
      tagline: '',
      price_month: '16.000',
      recommended: false,
      annual_months_free: 0,
      shape: { vcpu: 4, memory_gb: 8, vcpu_guaranteed: 0.67, memory_gb_guaranteed: 2.67, disk_gb: 100 },
      step_up: { next_sku: 'plan.xl', next_name: 'XL', gap_month: '14.000', bundled_addon_keys: ['ai_seo', 'backup'], bundled_addons_sum_month: '3.500', rule_holds: false },
      includes: { vcpu: 4, memory_gb: 8, bandwidth_mbps: 250, disk_gb: 100 },
    },
    {
      sku: 'plan.xl',
      name: 'XL',
      tagline: '',
      price_month: '30.000',
      recommended: false,
      annual_months_free: 0,
      shape: { vcpu: 8, memory_gb: 16, vcpu_guaranteed: 1.33, memory_gb_guaranteed: 5.33, disk_gb: 250 },
      includes: { vcpu: 8, memory_gb: 16, bandwidth_mbps: 1000, disk_gb: 250 },
    },
  ],
  features: [
    {
      key: 'bandwidth',
      name: 'Bandwidth',
      blurb: 'Included bandwidth; above it the package’s overage rule applies',
      group: 'capacity',
      kind: 'quantity',
      unit: 'Mbps',
      addon_sku: 'eip.bandwidth_mbps',
      teaser: false,
      cells: {
        'plan.s': { state: 'included', quantity: 50, overage: 'hard_cap' },
        'plan.m': { state: 'included', quantity: 100, overage: 'hard_cap' },
        'plan.l': { state: 'included', quantity: 250, overage: 'metered' },
        'plan.xl': { state: 'included', quantity: 1000, overage: 'metered' },
      },
    },
    {
      key: 'disk',
      name: 'Disk',
      blurb: 'Persistent storage for apps and databases',
      group: 'capacity',
      kind: 'quantity',
      unit: 'GB',
      addon_sku: 'k8s.pvc_gb',
      teaser: false,
      cells: {
        'plan.s': { state: 'included', quantity: 25, overage: 'hard_cap' },
        'plan.m': { state: 'included', quantity: 50, overage: 'hard_cap' },
        'plan.l': { state: 'included', quantity: 100, overage: 'metered' },
        'plan.xl': { state: 'included', quantity: 250, overage: 'metered' },
      },
    },
    {
      key: 'ai_seo',
      name: 'AI SEO ready',
      blurb: 'Search-engine readiness checked and tuned by AI',
      group: 'features',
      kind: 'boolean',
      addon_sku: 'addon.ai_seo',
      teaser: false,
      cells: {
        'plan.s': { state: 'optional', addon_sku: 'addon.ai_seo', price_month: '2.000', included_from: 'plan.xl' },
        'plan.m': { state: 'optional', addon_sku: 'addon.ai_seo', price_month: '2.000', included_from: 'plan.xl' },
        'plan.l': { state: 'optional', addon_sku: 'addon.ai_seo', price_month: '2.000', included_from: 'plan.xl' },
        'plan.xl': { state: 'included' },
      },
    },
    {
      key: 'gitea_iac',
      name: 'Gitea + IaC',
      blurb: 'Your Organization’s Git and the infrastructure-as-code behind every application',
      group: 'access',
      kind: 'access',
      teaser: false,
      cells: {
        'plan.s': { state: 'not_offered', included_from: 'plan.m' },
        'plan.m': { state: 'included', note: 'read' },
        'plan.l': { state: 'included' },
        'plan.xl': { state: 'included' },
      },
    },
    {
      key: 'vuln_dashboard',
      name: 'Vulnerability dashboard',
      blurb: 'Every image and dependency scanned, the findings in one place',
      group: 'ops',
      kind: 'boolean',
      teaser: true,
      cells: {
        'plan.s': { state: 'teaser', included_from: 'plan.m' },
        'plan.m': { state: 'included' },
        'plan.l': { state: 'included' },
        'plan.xl': { state: 'included' },
      },
    },
    {
      key: 'dedicated_ip',
      name: 'Dedicated IP address',
      blurb: 'A public IPv4 reserved for your Organization',
      group: 'scope',
      kind: 'boolean',
      addon_sku: 'addon.dedicated_ip',
      teaser: false,
      cells: {
        'plan.s': { state: 'optional', addon_sku: 'addon.dedicated_ip', price_month: '2.000' },
        'plan.m': { state: 'optional', addon_sku: 'addon.dedicated_ip', price_month: '2.000' },
        'plan.l': { state: 'not_offered' },
        'plan.xl': { state: 'optional', addon_sku: 'addon.dedicated_ip', price_month: '2.000' },
      },
    },
    {
      key: 'backup',
      name: 'Backup',
      blurb: 'Scheduled backups of your sites and databases',
      group: 'resilience',
      kind: 'boolean',
      addon_sku: 'addon.backup',
      teaser: false,
      cells: {
        'plan.s': { state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' },
        'plan.m': { state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' },
        'plan.l': { state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' },
        'plan.xl': { state: 'included' },
      },
    },
    {
      key: 'dr_topology',
      name: 'DR topology',
      blurb: 'Where your applications run, and where they fail over to',
      group: 'resilience',
      kind: 'level',
      levels: ['single region', 'active-passive'],
      addon_sku: 'addon.dr',
      teaser: false,
      cells: {
        'plan.s': { state: 'included', level: 0, included_from: 'plan.m', next_level_addon: { addon_sku: 'addon.dr', price_month: '8.000' } },
        'plan.m': { state: 'included', level: 1 },
        'plan.l': { state: 'included', level: 1 },
        'plan.xl': { state: 'included', level: 1 },
      },
    },
  ],
}

/** Content addresses of the icons the branded document names (DESIGN.md §22.10). */
export const ICON_BACKUP = '1'.repeat(64)
export const ICON_SSL = '2'.repeat(64)
export const ICON_RESILIENCE = '3'.repeat(64)
export const ICON_PLAN_M = '4'.repeat(64)

/**
 * The same document with icons and branding, as BSS publishes them once an
 * operator has set them: Backup with an icon on a tile, the SSL floor item
 * with an icon and no tile, the Resilience group with an icon, M with an
 * icon, an accent and a badge, S with an accent only. Everything else carries
 * none of it — every key absent, the document's rule.
 */
export const brandedPackages: PackagesDoc = {
  ...packages,
  groups: packages.groups!.map((g) => (g.key === 'resilience' ? { ...g, icon: { src: `/api/v1/public/icons/${ICON_RESILIENCE}`, alt: g.name } } : g)),
  floor: packages.floor!.map((f) => (f.key === 'ssl' ? { ...f, icon: { src: `/api/v1/public/icons/${ICON_SSL}`, alt: f.name } } : f)),
  packages: packages.packages.map((p) =>
    p.sku === 'plan.m' ? { ...p, icon: { src: `/api/v1/public/icons/${ICON_PLAN_M}`, alt: p.name }, accent: '#3B82F6', badge: 'Most popular' } : p.sku === 'plan.s' ? { ...p, accent: '#93C5FD' } : p,
  ),
  features: packages.features.map((f) => (f.key === 'backup' ? { ...f, icon: { src: `/api/v1/public/icons/${ICON_BACKUP}`, alt: f.name, bg: '#FFE4E6' } } : f)),
}

/** The add-on SKUs per plan-hour: monthly ÷ 730. */
export const addonUnitPrices: Record<string, number> = { 'addon.backup': 0.00205479, 'addon.dedicated_ip': 0.0027397, 'addon.ai_seo': 0.00273973, 'addon.dr': 0.0109589 }

/** The unit price of every priced entry, for a fake server in a test. */
export const unitPrices: Record<string, number> = { ...Object.fromEntries([...skus, ...payg, ...plans, ...morePlans].map((e) => [e.sku, Number(e.unit_price)])), ...addonUnitPrices }
