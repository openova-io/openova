import { describe, it, expect } from 'vitest'
import { flowJobKind } from './flowJobKind'

function n(id: string, label = '') {
  return { id, jobName: label || id, displayName: label || id }
}

describe('flowJobKind', () => {
  it('derives the kind from the flow node id prefix (real hw305 ids)', () => {
    expect(flowJobKind(n('b2b00ce4:install-agwalk305:orgdb-rtz-a'))).toBe('install')
    expect(flowJobKind(n('b2b00ce4:reconcile-catalyst-tenant-x-vcluster'))).toBe('reconcile')
    expect(flowJobKind(n('b2b00ce4:me-east-215-a:cutover-harbor-projects'))).toBe('step')
    expect(flowJobKind(n('b2b00ce4:cron-openbao-snapshot'))).toBe('cron')
    expect(flowJobKind(n('b2b00ce4:task-syft-sbom'))).toBe('task')
    expect(flowJobKind(n('b2b00ce4:tofu-plan'))).toBe('lifecycle')
    expect(flowJobKind(n('b2b00ce4:terraform-apply'))).toBe('lifecycle')
  })

  it('does not mistake "reconciler" for "reconcile"', () => {
    expect(flowJobKind(n('b2b00ce4:reconciler-flux-controllers'))).toBe('reconciler')
    expect(flowJobKind(n('b2b00ce4:reconcilers-host'))).toBe('reconciler')
  })

  it('falls back to the human label when the id has no known prefix', () => {
    expect(flowJobKind(n('b2b00ce4:something-odd', 'Install Orgdb Rtz A'))).toBe('install')
    expect(flowJobKind(n('b2b00ce4:x', 'Reconcile Catalyst Tenant Apps'))).toBe('reconcile')
    expect(flowJobKind(n('b2b00ce4:y', 'Syft SBOM (task)'))).toBe('task')
  })

  it('falls back to task (never silently hidden) when nothing matches', () => {
    expect(flowJobKind(n('b2b00ce4:mystery-node', 'Mystery'))).toBe('task')
  })
})
