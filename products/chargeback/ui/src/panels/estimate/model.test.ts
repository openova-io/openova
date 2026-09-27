import { describe, expect, it } from 'vitest'
import type { Estimate } from '../../api/types'
import { catalog } from './fixture'
import {
  buildCatalogue,
  defaultConfig,
  deploymentsOf,
  describeItem,
  estimateReady,
  familyBreakdown,
  familyColor,
  findService,
  groupItems,
  itemLines,
  kindOf,
  makeItem,
  pricedByItem,
  removeItem,
  requestBody,
  searchCatalogue,
  sizeOptions,
  sumAmounts,
  unitHint,
  upsertItem,
  variantsOf,
  type ItemConfig,
} from './model'

const ecs = () => findService(catalog, 'ecs')!
const config = (patch: Partial<ItemConfig>): ItemConfig => ({ ...defaultConfig(catalog, findService(catalog, patch.service ?? 'ecs')!), ...patch })

describe('the service catalogue', () => {
  it('lists the families in product order, their services in reading order, and Other services last', () => {
    const fams = buildCatalogue(catalog)
    expect(fams.map((f) => f.name)).toEqual(['Compute', 'Storage', 'Networking', 'Databases', 'Containers', 'Platform plans', 'Other services'])
    expect(fams[0].services.map((s) => s.name)).toEqual(['Elastic Cloud Server', 'Auto Scaling'])
    expect(fams[2].services.map((s) => s.key)).toEqual(['eip', 'elb', 'nat', 'vpc'])
    expect(fams[4].services.map((s) => s.key)).toEqual(['cce', 'k8s'])
    expect(fams[6].services.map((s) => [s.key, s.name])).toEqual([['obs', 'OBS']])
    expect(fams.map((f) => f.blurb.length > 0)).not.toContain(false)
    expect(fams[3].blurb).toMatch(/GaussDB/)
    expect(buildCatalogue(null)).toEqual([])
  })

  it('folds a companion storage service into the engines that ask for it, and shows it alone otherwise', () => {
    const databases = buildCatalogue(catalog).find((f) => f.key === 'databases')!
    expect(databases.services.map((s) => s.key)).toEqual(['rds-mysql'])
    const alone = buildCatalogue({ ...catalog, skus: catalog.skus.filter((s) => s.service !== 'rds-mysql') })
    expect(alone.find((f) => f.key === 'databases')!.services.map((s) => s.key)).toEqual(['rds-storage'])
    // findService still reaches a folded service, for the configurator.
    expect(findService(catalog, 'rds-storage')?.entries).toHaveLength(2)
    expect(findService(catalog, 'nothing')).toBeNull()
  })

  it('shows each service from its cheapest monthly figure, with the unit that figure is per', () => {
    const svc = ecs()
    expect(svc.from?.sku).toBe('ecs.s7n.small.1')
    expect(findService(catalog, 'evs')!.from?.sku).toBe('evs.hdd.gb')
    expect(unitHint('gb-hour')).toBe('per GB')
    expect(unitHint('mbps-hour')).toBe('per Mbps')
    expect(unitHint('instance-hour')).toBe('')
    expect(svc.blurb).toMatch(/Virtual servers/)
  })

  it('searches services by name, description, family and option', () => {
    const fams = buildCatalogue(catalog)
    expect(searchCatalogue(fams, 'mysql').flatMap((f) => f.services.map((s) => s.key))).toEqual(['rds-mysql'])
    expect(searchCatalogue(fams, 'Networking').map((f) => f.key)).toEqual(['networking'])
    expect(searchCatalogue(fams, 'memory-optimised').flatMap((f) => f.services.map((s) => s.key))).toEqual(['ecs'])
    expect(searchCatalogue(fams, 'object storage').flatMap((f) => f.services.map((s) => s.key))).toEqual(['obs'])
    expect(searchCatalogue(fams, 'nothing here')).toEqual([])
    expect(searchCatalogue(fams, '  ')).toBe(fams)
  })
})

describe('the configurator model', () => {
  it('offers friendly first choices and sizes, smallest first, never a raw SKU', () => {
    expect(variantsOf(ecs())).toEqual([
      { value: 'general', label: 'General purpose' },
      { value: 'compute', label: 'Compute-optimised' },
      { value: 'memory', label: 'Memory-optimised' },
    ])
    expect(sizeOptions(ecs(), { variant: 'compute' }).map((o) => o.label)).toEqual(['2 vCPU · 4 GB', '8 vCPU · 16 GB'])
    expect(deploymentsOf(findService(catalog, 'rds-mysql')!)).toEqual([
      { value: 'single', label: 'Single node' },
      { value: 'ha', label: 'Primary + standby' },
    ])
    expect(sizeOptions(findService(catalog, 'rds-mysql')!, { deployment: 'ha' }).map((o) => o.label)).toEqual(['2 vCPU · 4 GB', '4 vCPU · 16 GB'])
    expect(sizeOptions(findService(catalog, 'nat')!, {}).map((o) => o.label)).toEqual(['Small', 'Medium'])
    // Two entries of one shape are told apart by their SKU.
    const twin = { ...catalog.skus[1], sku: 'ecs.s6.xlarge.4' }
    const twins = findService({ ...catalog, skus: [...catalog.skus, twin] }, 'ecs')!
    expect(sizeOptions(twins, { variant: 'general' }).map((o) => o.label)).toEqual(['1 vCPU · 1 GB', '4 vCPU · 16 GB (ecs.s7n.xlarge.4)', '4 vCPU · 16 GB (ecs.s6.xlarge.4)'])
  })

  it('opens on the smallest option, one unit, always on', () => {
    expect(defaultConfig(catalog, ecs())).toMatchObject({ service: 'ecs', variant: 'general', sku: 'ecs.s7n.small.1', quantity: '1', usage: 'always', hours: '730', diskVariant: '', eip: false })
    expect(defaultConfig(catalog, findService(catalog, 'rds-mysql')!)).toMatchObject({ deployment: 'single', sku: 'rds.mysql.c7.large.2.single', storageGb: '100' })
    expect(defaultConfig(catalog, findService(catalog, 'plan')!)).toMatchObject({ plan: 's', months: '1' })
    expect(defaultConfig(catalog, findService(catalog, 'k8s')!)).toMatchObject({ vcpu: '4', memGb: '8', pvcGb: '50' })
    expect(kindOf('ecs')).toBe('server')
    expect(kindOf('obs')).toBe('generic')
  })

  it('turns a server with a disk and an Elastic IP into four lines, quantities multiplied out per server', () => {
    const r = itemLines(catalog, config({ variant: 'compute', sku: 'ecs.c7n.2xlarge.2', quantity: '2', usage: 'business', diskVariant: 'ssd', storageGb: '100', eip: true, bandwidthMbps: '10' }))
    expect(r.errors).toEqual({})
    expect(r.lines).toEqual([
      { sku: 'ecs.c7n.2xlarge.2', label: 'Compute-optimised · 8 vCPU · 16 GB', quantity: '2', hours: '176' },
      { sku: 'evs.ssd.gb', label: 'SSD disk · 100 GB each', quantity: '200', hours: '730' },
      { sku: 'eip', label: 'Elastic IP address', quantity: '2', hours: '176' },
      { sku: 'eip.bandwidth_mbps', label: 'Bandwidth per Mbps · 10 Mbps each', quantity: '20', hours: '176' },
    ])
    // Custom hours ride on the server and the address, never on the disk.
    const custom = itemLines(catalog, config({ usage: 'custom', hours: '100.5', diskVariant: 'hdd', storageGb: '40' }))
    expect(custom.lines.map((l) => l.hours)).toEqual(['100.5', '730'])
    expect(custom.lines[1].quantity).toBe('40')
  })

  it('names what is wrong with a form in the words the server would use, and adds nothing meanwhile', () => {
    expect(itemLines(catalog, config({ quantity: '0' })).errors.quantity).toMatch(/quantity must be more than 0/)
    expect(itemLines(catalog, config({ quantity: '1000000001' })).errors.quantity).toMatch(/at most 1,000,000,000/)
    expect(itemLines(catalog, config({ usage: 'custom', hours: '745' })).errors.hours).toMatch(/at most 744/)
    expect(itemLines(catalog, config({ usage: 'custom', hours: '' })).errors.hours).toMatch(/hours must be more than 0/)
    expect(itemLines(catalog, config({ diskVariant: 'ssd', storageGb: '' })).errors.storageGb).toMatch(/disk size must be more than 0/)
    expect(itemLines(catalog, config({ eip: true, bandwidthMbps: '0' })).errors.bandwidthMbps).toMatch(/bandwidth must be more than 0/)
    expect(itemLines(catalog, config({ sku: 'ecs.gone' })).errors.sku).toBe('choose a size')
    expect(itemLines(catalog, { ...config({}), service: 'nothing' }).errors.service).toMatch(/not in the public price list/)
    expect(itemLines(null, config({})).errors.service).toBeTruthy()
  })

  it('configures a database as its size plus the storage of the same deployment', () => {
    const r = itemLines(catalog, config({ service: 'rds-mysql', deployment: 'ha', sku: 'rds.mysql.c7.xlarge.4.ha', storageGb: '200', quantity: '2' }))
    expect(r.errors).toEqual({})
    expect(r.lines).toEqual([
      { sku: 'rds.mysql.c7.xlarge.4.ha', label: '4 vCPU · 16 GB · Primary + standby', quantity: '2', hours: '730' },
      { sku: 'rds.storage.ha.gb', label: 'Storage · 200 GB each', quantity: '400', hours: '730' },
    ])
    expect(itemLines(catalog, config({ service: 'rds-mysql', storageGb: '0' })).errors.storageGb).toMatch(/storage must be more than 0/)
  })

  it('configures storage, an Elastic IP, a sized service, capacity, a plan and an unclassified service', () => {
    expect(itemLines(catalog, config({ service: 'evs', variant: 'hdd', sku: 'evs.hdd.gb', storageGb: '500', quantity: '2' })).lines).toEqual([{ sku: 'evs.hdd.gb', label: 'HDD · 500 GB each', quantity: '1000', hours: '730' }])
    expect(itemLines(catalog, config({ service: 'eip', quantity: '3', bandwidthMbps: '5' })).lines).toEqual([
      { sku: 'eip', label: 'Elastic IP address', quantity: '3', hours: '730' },
      { sku: 'eip.bandwidth_mbps', label: 'Bandwidth per Mbps · 5 Mbps each', quantity: '15', hours: '730' },
    ])
    expect(itemLines(catalog, config({ service: 'nat', sku: 'nat.2' })).lines).toEqual([{ sku: 'nat.2', label: 'Medium', quantity: '1', hours: '730' }])
    expect(itemLines(catalog, config({ service: 'elb', quantity: '2' })).lines).toEqual([{ sku: 'elb', label: 'Load balancer', quantity: '2', hours: '730' }])
    const cap = itemLines(catalog, config({ service: 'k8s', vcpu: '8', memGb: '16', pvcGb: '0' }))
    expect(cap.lines.map((l) => [l.sku, l.quantity])).toEqual([
      ['k8s.vcpu', '8'],
      ['k8s.mem_gb', '16'],
    ])
    expect(itemLines(catalog, config({ service: 'k8s', vcpu: '0', memGb: '0', pvcGb: '0' })).errors.vcpu).toMatch(/ask for some/)
    expect(itemLines(catalog, config({ service: 'k8s', vcpu: '-1' })).errors.vcpu).toMatch(/0 or more/)
    const plan = itemLines(catalog, config({ service: 'plan', plan: 'm', months: '3', quantity: '2' }))
    expect(plan.lines).toEqual([{ plan: 'm', label: 'M plan · 4 vCPU · 8 GB', quantity: '2', months: 3 }])
    expect(itemLines(catalog, config({ service: 'plan', plan: 'm', months: '13' })).errors.months).toMatch(/between 1 and 12/)
    expect(itemLines(catalog, config({ service: 'plan', plan: 'xxl' })).errors.plan).toBe('choose a plan')
    expect(itemLines(catalog, config({ service: 'obs', quantity: '10' })).lines).toEqual([{ sku: 'obs.standard.gb', label: 'obs.standard.gb', quantity: '10', hours: '730' }])
  })

  it('summarises an item in one line', () => {
    expect(describeItem(catalog, config({ variant: 'compute', sku: 'ecs.c7n.2xlarge.2', quantity: '2', usage: 'business' }))).toBe('2 × Compute-optimised · 8 vCPU · 16 GB · business hours')
    expect(describeItem(catalog, config({ service: 'rds-mysql', deployment: 'ha', sku: 'rds.mysql.c7.xlarge.4.ha', storageGb: '200' }))).toBe('1 × 4 vCPU · 16 GB · Primary + standby · 200 GB · always on')
    expect(describeItem(catalog, config({ service: 'evs', sku: 'evs.ssd.gb', storageGb: '100' }))).toBe('1 × 100 GB SSD')
    expect(describeItem(catalog, config({ service: 'eip', quantity: '2', bandwidthMbps: '10', usage: 'custom', hours: '300' }))).toBe('2 × 10 Mbps · 300 h/month')
    expect(describeItem(catalog, config({ service: 'k8s', vcpu: '8', memGb: '16', pvcGb: '' }))).toBe('8 vCPU · 16 GiB · always on')
    expect(describeItem(catalog, config({ service: 'plan', plan: 'm', months: '3' }))).toBe('1 × M plan · 4 vCPU · 8 GB · 3 month(s)')
    expect(describeItem(catalog, config({ service: 'elb' }))).toBe('1 × Load balancer · always on')
  })
})

describe('the estimate', () => {
  const server = makeItem(catalog, 'i1', config({ variant: 'compute', sku: 'ecs.c7n.2xlarge.2', quantity: '2', usage: 'business', diskVariant: 'ssd', storageGb: '100' }))
  const plan = makeItem(catalog, 'i2', config({ service: 'plan', plan: 'm', months: '3' }))

  it('builds the request the API expects and remembers where each item sits in it', () => {
    const { body, spans } = requestBody([server, plan], 'me-east-215-a', ' buyer@example.com ')
    expect(body).toEqual({
      region: 'me-east-215-a',
      contact_email: 'buyer@example.com',
      lines: [
        { sku: 'ecs.c7n.2xlarge.2', quantity: '2', hours_per_month: '176' },
        { sku: 'evs.ssd.gb', quantity: '200', hours_per_month: '730' },
        { plan: 'm', quantity: '1', months: 3 },
      ],
    })
    expect(spans).toEqual([
      { id: 'i1', start: 0, count: 2 },
      { id: 'i2', start: 2, count: 1 },
    ])
    expect(body.lines[0]).not.toHaveProperty('months')
    expect(body.lines[2]).not.toHaveProperty('hours_per_month')
    expect(requestBody([server]).body).not.toHaveProperty('region')
    expect(requestBody([server], '', '  ').body).not.toHaveProperty('contact_email')
    expect(estimateReady([])).toBe(false)
    expect(estimateReady([server])).toBe(true)
  })

  it('reads the priced lines back by position and sums each item exactly, or prices nothing from a stale answer', () => {
    const priced = {
      lines: [
        { sku: 'ecs.c7n.2xlarge.2', unit: 'instance-hour', quantity: '2', hours: '176', months: 1, rated_quantity: '352.000000', unit_price: '0.19495082', amount: '68.622689' },
        { sku: 'evs.ssd.gb', unit: 'gb-hour', quantity: '200', hours: '730', months: 1, rated_quantity: '146000.000000', unit_price: '0.00022831', amount: '33.333260' },
        { sku: 'plan.m', plan: 'm', unit: 'plan-hour', quantity: '1', hours: '730', months: 3, rated_quantity: '2190.000000', unit_price: '0.01232877', amount: '27.000006' },
      ],
    } as unknown as Estimate
    const by = pricedByItem([server, plan], priced)
    expect(by.get('i1')).toMatchObject({ amount: '101.955949', monthly: true })
    expect(by.get('i1')!.lines.map((l) => l.priced?.amount)).toEqual(['68.622689', '33.333260'])
    expect(by.get('i2')).toMatchObject({ amount: '27.000006', monthly: false })
    // The server has not answered, or answered an earlier shape of the estimate.
    expect(pricedByItem([server, plan], null).get('i1')!.amount).toBeNull()
    expect(pricedByItem([server], priced).get('i1')!.amount).toBeNull()
    expect(pricedByItem([server], priced).get('i1')!.lines).toHaveLength(2)
  })

  it('breaks the estimate down by family, largest first, only once every item is priced', () => {
    const second = makeItem(catalog, 'i3', config({ service: 'evs', sku: 'evs.ssd.gb', storageGb: '100' }))
    const priced = new Map([
      ['i1', { lines: [], amount: '101.955949', monthly: true }],
      ['i2', { lines: [], amount: '27.000006', monthly: false }],
      ['i3', { lines: [], amount: '16.666630', monthly: true }],
    ])
    const shares = familyBreakdown([server, plan, second], priced)
    expect(shares.map((s) => [s.family, s.familyName, s.amount, Math.round(s.share * 100)])).toEqual([
      ['compute', 'Compute', '101.955949', 70],
      ['plans', 'Platform plans', '27.000006', 19],
      ['storage', 'Storage', '16.666630', 11],
    ])
    expect(shares.reduce((n, s) => n + s.share, 0)).toBeCloseTo(1, 9)
    expect(new Set(shares.map((s) => s.color)).size).toBe(3)
    expect(familyColor('other')).toBe('#94a3b8')
    // Two items of one family sum exactly into one share.
    const twice = familyBreakdown([server, second, makeItem(catalog, 'i4', config({ quantity: '1' }))], new Map([...priced, ['i4', { lines: [], amount: '0.000051', monthly: true }]]))
    expect(twice.map((s) => [s.family, s.amount])).toEqual([
      ['compute', '101.956000'],
      ['storage', '16.666630'],
    ])
    // Not every item priced yet: nothing to show, rather than a partial whole.
    expect(familyBreakdown([server, plan], new Map([['i1', { lines: [], amount: '1.000000', monthly: true }]]))).toEqual([])
    expect(familyBreakdown([], new Map())).toEqual([])
  })

  it('sums six-decimal amounts without a float', () => {
    expect(sumAmounts(['0.1', '0.2'])).toBe('0.300000')
    expect(sumAmounts(['68.622689', '33.333260'])).toBe('101.955949')
    expect(sumAmounts([1234567.891011, '0.000001'])).toBe('1234567.891012')
    expect(sumAmounts(['5', '-2.5'])).toBe('2.500000')
    expect(sumAmounts([])).toBe('0.000000')
  })

  it('groups items under their service, edits in place and removes by id', () => {
    const second = makeItem(catalog, 'i3', config({ quantity: '4' }))
    let items = upsertItem(upsertItem(upsertItem([], server), plan), second)
    expect(groupItems(items).map((g) => [g.serviceName, g.items.map((i) => i.id)])).toEqual([
      ['Elastic Cloud Server', ['i1', 'i3']],
      ['Platform plans', ['i2']],
    ])
    items = upsertItem(items, makeItem(catalog, 'i1', config({ quantity: '5' })))
    expect(items.map((i) => i.id)).toEqual(['i1', 'i2', 'i3'])
    expect(items[0].summary).toBe('5 × General purpose · 1 vCPU · 1 GB · always on')
    expect(removeItem(items, 'i2').map((i) => i.id)).toEqual(['i1', 'i3'])
    expect(server.familyName).toBe('Compute')
  })
})
