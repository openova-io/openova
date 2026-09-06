import { describe, expect, it } from 'vitest'
import { groupBySource, groupByService, serviceOfSKU } from './sku'

describe('serviceOfSKU', () => {
  it('files a SKU under the service its first token names, with the Go KindLabel text', () => {
    expect(serviceOfSKU('ecs.s6.large.2')).toEqual({ key: 'ecs', label: 'Elastic Cloud Server' })
    expect(serviceOfSKU('evs.ssd.gb')).toEqual({ key: 'evs', label: 'Block storage (EVS)' })
    expect(serviceOfSKU('rds.mysql.large')).toEqual({ key: 'rds', label: 'Relational DB (RDS)' })
    expect(serviceOfSKU('gaussdb.opengauss')).toEqual({ key: 'gaussdb', label: 'GaussDB' })
    expect(serviceOfSKU('cbr.vault.gb')).toEqual({ key: 'cbr', label: 'Backup (CBR)' })
    expect(serviceOfSKU('cce.cluster.small')).toEqual({ key: 'cce', label: 'Kubernetes cluster (CCE)' })
    expect(serviceOfSKU('nat.gateway.small')).toEqual({ key: 'nat', label: 'NAT gateway' })
  })
  it('matches bare and prefixed EIP SKUs, and is case-insensitive', () => {
    expect(serviceOfSKU('eip').label).toBe('Elastic IP')
    expect(serviceOfSKU('eip-bandwidth.mbps').label).toBe('Elastic IP')
    expect(serviceOfSKU('eipbw').label).toBe('Elastic IP')
    expect(serviceOfSKU('ECS.C7.XLARGE.2').key).toBe('ecs')
  })
  // vpcep must not be swallowed by vpc, and vice versa: a whole-token match,
  // not a prefix match, is what keeps two services apart.
  it('keeps vpc and vpcep apart', () => {
    expect(serviceOfSKU('vpc').label).toBe('VPC')
    expect(serviceOfSKU('vpcep.endpoint').label).toBe('VPC endpoint')
    expect(serviceOfSKU('vpc.peering').label).toBe('VPC')
  })
  it('folds every k8s.* / k8s-* meter into one Platform group', () => {
    expect(serviceOfSKU('k8s.pod.vcpu')).toEqual({ key: 'k8s', label: 'Platform (Kubernetes)' })
    expect(serviceOfSKU('k8s-pvc.gb').key).toBe('k8s')
    expect(serviceOfSKU('k8s_pod_mem').key).toBe('k8s')
  })
  it('never guesses: an unknown or empty SKU is Other', () => {
    expect(serviceOfSKU('obs.standard.gb').key).toBe('other')
    expect(serviceOfSKU('')).toEqual({ key: 'other', label: 'Other' })
    expect(serviceOfSKU('   ')).toEqual({ key: 'other', label: 'Other' })
  })
})

describe('groupByService', () => {
  const lines = [
    { sku: 'ecs.s6.large.2', amount: '60' },
    { sku: 'evs.ssd.gb', amount: 10 },
    { sku: 'ecs.c7.xlarge.2', amount: 30 },
    { sku: 'k8s.pod.vcpu', amount: '0' },
  ]
  it('sums amounts per service, largest first, and shares sum to one', () => {
    const g = groupByService(lines)
    expect(g.map((x) => x.key)).toEqual(['ecs', 'evs', 'k8s'])
    expect(g[0].amount).toBe(90)
    expect(g[0].share).toBeCloseTo(0.9)
    expect(g[1].share).toBeCloseTo(0.1)
    expect(g[2].share).toBe(0)
    expect(g.reduce((n, x) => n + x.share, 0)).toBeCloseTo(1)
  })
  it('orders lines inside a group by amount and accepts string amounts', () => {
    const g = groupByService(lines)
    expect(g[0].lines.map((l) => l.sku)).toEqual(['ecs.s6.large.2', 'ecs.c7.xlarge.2'])
  })
  it('gives every group a zero share when nothing was billed, not NaN', () => {
    const g = groupByService([{ sku: 'ecs.x', amount: 0 }])
    expect(g[0].share).toBe(0)
  })
  it('is empty for no lines', () => {
    expect(groupByService([])).toEqual([])
  })
})

describe('groupBySource', () => {
  it('is empty when no line names a source, so the panel is not drawn', () => {
    expect(groupBySource([{ amount: 5 }, { source_id: null, amount: 5 }])).toEqual([])
  })
  it('breaks amounts down per source, largest first', () => {
    const g = groupBySource([
      { source_id: 'a', amount: 10 },
      { source_id: 'b', amount: '30' },
      { source_id: 'a', amount: 20 },
    ])
    expect(g.map((x) => [x.source_id, x.amount, x.lines])).toEqual([
      ['a', 30, 2],
      ['b', 30, 1],
    ])
    expect(g[0].share).toBeCloseTo(0.5)
  })
})
