/**
 * flow-bridge.test — lock the catalyst-ui ↔ openova-flow shim:
 *   • status palette covers all 4 statuses the openova-flow-emitter emits
 *   • flowStateToArrays materialises Map → Array in insertion order
 *   • regionDescriptorsFromFlow surfaces unique region tags + falls back
 *     gracefully
 *   • rollupFlowStatus mirrors the legacy provisioning-status rollup on
 *     the new contract
 */

import { describe, it, expect } from 'vitest'
import type { FlowNode, Relationship } from '@openova/flow-core'
import {
  CATALYST_STATUS_PALETTE,
  flowStateToArrays,
  regionDescriptorsFromFlow,
  rollupFlowStatus,
} from './flow-bridge'

function node(over: Partial<FlowNode> & Pick<FlowNode, 'id'>): FlowNode {
  return {
    flowId: 'f1',
    label: over.id,
    status: 'running',
    ...over,
  }
}

describe('CATALYST_STATUS_PALETTE', () => {
  it('covers pending/running/succeeded/failed', () => {
    for (const k of ['pending', 'running', 'succeeded', 'failed']) {
      expect(CATALYST_STATUS_PALETTE[k]).toBeTruthy()
      expect(CATALYST_STATUS_PALETTE[k].arrow).toMatch(/^#[0-9A-F]{6}$/i)
    }
  })
})

describe('flowStateToArrays', () => {
  it('materialises Maps to arrays preserving insertion order', () => {
    const nodes = new Map<string, FlowNode>()
    nodes.set('b', node({ id: 'b' }))
    nodes.set('a', node({ id: 'a' }))
    const relationships = new Map<string, Relationship>()
    relationships.set('a::b::finish-to-start', {
      fromId: 'a',
      toId: 'b',
      type: 'finish-to-start',
    })
    const out = flowStateToArrays({
      flow: null,
      nodes,
      relationships,
      streamStatus: 'streaming',
      streamError: null,
    })
    expect(out.nodes.map((n) => n.id)).toEqual(['b', 'a'])
    expect(out.relationships).toHaveLength(1)
  })
})

describe('regionDescriptorsFromFlow', () => {
  it('returns a single fallback descriptor when nodes carry no region', () => {
    const nodes = new Map<string, FlowNode>()
    nodes.set('a', node({ id: 'a' }))
    const out = regionDescriptorsFromFlow(nodes)
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe('')
    expect(out[0].label).toBe('Primary Region')
  })

  it('surfaces unique region tags from the stream, sorted', () => {
    const nodes = new Map<string, FlowNode>()
    nodes.set('a', node({ id: 'a', region: 'hel1' }))
    nodes.set('b', node({ id: 'b', region: 'fsn1' }))
    nodes.set('c', node({ id: 'c', region: 'fsn1' }))
    const out = regionDescriptorsFromFlow(nodes)
    expect(out.map((r) => r.id)).toEqual(['fsn1', 'hel1'])
  })

  it('augments labels with wizard-store region metadata when available', () => {
    const nodes = new Map<string, FlowNode>()
    nodes.set('a', node({ id: 'a', region: 'fsn1' }))
    const out = regionDescriptorsFromFlow(nodes, [
      { id: 'fsn1', code: 'fsn1', location: 'Falkenstein', name: 'Region 1' },
    ])
    expect(out[0]).toMatchObject({
      id: 'fsn1',
      label: 'FSN1 · Falkenstein',
      meta: 'Region 1',
    })
  })
})

describe('rollupFlowStatus', () => {
  function withGroupAndChildren(): {
    nodes: Map<string, FlowNode>
    relationships: Map<string, Relationship>
  } {
    const nodes = new Map<string, FlowNode>()
    nodes.set('grp', node({ id: 'grp', status: 'running' }))
    nodes.set('a', node({ id: 'a', status: 'succeeded', startedAt: 100 }))
    nodes.set('b', node({ id: 'b', status: 'running', startedAt: 200 }))
    const relationships = new Map<string, Relationship>()
    relationships.set('a::grp::contains', { fromId: 'a', toId: 'grp', type: 'contains' })
    relationships.set('b::grp::contains', { fromId: 'b', toId: 'grp', type: 'contains' })
    return { nodes, relationships }
  }

  it('excludes group nodes from the total/finished counts', () => {
    const { nodes, relationships } = withGroupAndChildren()
    const r = rollupFlowStatus({ nodes, relationships })
    expect(r.total).toBe(2)
    expect(r.finished).toBe(1)
    expect(r.status).toBe('running')
    expect(r.earliestStartedMs).toBe(100)
  })

  it('returns failed when every leaf is terminal and at least one failed', () => {
    const nodes = new Map<string, FlowNode>()
    nodes.set('a', node({ id: 'a', status: 'succeeded' }))
    nodes.set('b', node({ id: 'b', status: 'failed' }))
    const relationships = new Map<string, Relationship>()
    expect(rollupFlowStatus({ nodes, relationships }).status).toBe('failed')
  })

  it('returns succeeded when every leaf succeeded', () => {
    const nodes = new Map<string, FlowNode>()
    nodes.set('a', node({ id: 'a', status: 'succeeded' }))
    nodes.set('b', node({ id: 'b', status: 'succeeded' }))
    const relationships = new Map<string, Relationship>()
    expect(rollupFlowStatus({ nodes, relationships }).status).toBe('succeeded')
  })

  it('returns pending when there are no leaf nodes', () => {
    const nodes = new Map<string, FlowNode>()
    const relationships = new Map<string, Relationship>()
    expect(rollupFlowStatus({ nodes, relationships }).status).toBe('pending')
  })
})
