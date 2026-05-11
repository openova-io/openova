/**
 * openflow-adapter-sse.test — unit tests for the pure reducer that
 * applies a FlowMessage envelope onto the local FlowStreamState. The
 * SSE transport is exercised end-to-end via the FlowPage smoke test;
 * here we lock the contract-level merge semantics (CONTRACT.md
 * Message variants table).
 */

import { describe, it, expect } from 'vitest'
import type { FlowMessage } from '@openova/flow-core'
import { reduceFlowMessage } from './openflow-adapter-sse'

const EMPTY = {
  flow: null,
  nodes: new Map(),
  relationships: new Map(),
  streamStatus: 'streaming' as const,
  streamError: null,
}

describe('reduceFlowMessage — snapshot', () => {
  it('replaces flow + nodes + relationships', () => {
    const msg: FlowMessage = {
      type: 'snapshot',
      flow: { id: 'f1', status: 'running', startedAt: 1 },
      nodes: [
        { id: 'a', flowId: 'f1', label: 'A', status: 'running' },
        { id: 'b', flowId: 'f1', label: 'B', status: 'pending' },
      ],
      relationships: [
        { fromId: 'a', toId: 'b', type: 'finish-to-start' },
      ],
    }
    const next = reduceFlowMessage(EMPTY, msg)
    expect(next.flow?.id).toBe('f1')
    expect(next.nodes.size).toBe(2)
    expect(next.relationships.size).toBe(1)
    expect(next.relationships.get('a::b::finish-to-start')?.fromId).toBe('a')
  })
})

describe('reduceFlowMessage — upsert-nodes', () => {
  it('merges by id', () => {
    const seeded = reduceFlowMessage(EMPTY, {
      type: 'snapshot',
      flow: { id: 'f1', status: 'running', startedAt: 1 },
      nodes: [{ id: 'a', flowId: 'f1', label: 'A', status: 'pending' }],
      relationships: [],
    })
    const next = reduceFlowMessage(seeded, {
      type: 'upsert-nodes',
      nodes: [
        { id: 'a', flowId: 'f1', label: 'A', status: 'running' },
        { id: 'b', flowId: 'f1', label: 'B', status: 'pending' },
      ],
    })
    expect(next.nodes.size).toBe(2)
    expect(next.nodes.get('a')?.status).toBe('running')
    expect(next.nodes.get('b')?.status).toBe('pending')
  })
})

describe('reduceFlowMessage — upsert-rels', () => {
  it('merges by (from, to, type) natural key', () => {
    const seeded = reduceFlowMessage(EMPTY, {
      type: 'upsert-rels',
      relationships: [
        { fromId: 'a', toId: 'b', type: 'finish-to-start' },
      ],
    })
    const next = reduceFlowMessage(seeded, {
      type: 'upsert-rels',
      relationships: [
        { fromId: 'a', toId: 'b', type: 'finish-to-start', condition: 'on-failure' },
        { fromId: 'b', toId: 'c', type: 'finish-to-start' },
      ],
    })
    expect(next.relationships.size).toBe(2)
    expect(next.relationships.get('a::b::finish-to-start')?.condition).toBe('on-failure')
  })
})

describe('reduceFlowMessage — delete-nodes prunes referencing rels', () => {
  it('removes nodes AND any rel pointing to/from them', () => {
    const seeded = reduceFlowMessage(EMPTY, {
      type: 'snapshot',
      flow: { id: 'f1', status: 'running', startedAt: 1 },
      nodes: [
        { id: 'a', flowId: 'f1', label: 'A', status: 'succeeded' },
        { id: 'b', flowId: 'f1', label: 'B', status: 'running' },
        { id: 'c', flowId: 'f1', label: 'C', status: 'pending' },
      ],
      relationships: [
        { fromId: 'a', toId: 'b', type: 'finish-to-start' },
        { fromId: 'b', toId: 'c', type: 'finish-to-start' },
      ],
    })
    const next = reduceFlowMessage(seeded, {
      type: 'delete-nodes',
      ids: ['b'],
    })
    expect(next.nodes.has('a')).toBe(true)
    expect(next.nodes.has('b')).toBe(false)
    expect(next.nodes.has('c')).toBe(true)
    // Both rels reference b — both pruned.
    expect(next.relationships.size).toBe(0)
  })
})

describe('reduceFlowMessage — delete-rels', () => {
  it('removes by natural key', () => {
    const seeded = reduceFlowMessage(EMPTY, {
      type: 'upsert-rels',
      relationships: [
        { fromId: 'a', toId: 'b', type: 'finish-to-start' },
        { fromId: 'a', toId: 'b', type: 'triggers' },
      ],
    })
    const next = reduceFlowMessage(seeded, {
      type: 'delete-rels',
      pairs: [{ fromId: 'a', toId: 'b', type: 'finish-to-start' }],
    })
    expect(next.relationships.size).toBe(1)
    expect(next.relationships.get('a::b::triggers')).toBeTruthy()
  })
})

describe('reduceFlowMessage — multi-region merge', () => {
  it('keeps both regions when adapter emits per-region FlowNodes', () => {
    // Prov #34 shape: fsn1 + hel1 each install bp-gateway-api; the
    // per-region adapter-flux DaemonSets each emit one FlowNode with
    // their own region tag. The server merges into one flow; the
    // reducer reflects both in the local cache.
    const next = reduceFlowMessage(EMPTY, {
      type: 'snapshot',
      flow: { id: 'd-34', status: 'running', startedAt: 1 },
      nodes: [
        {
          id: 'fsn1/install-bp-gateway-api',
          flowId: 'd-34',
          label: 'bp-gateway-api',
          status: 'running',
          region: 'fsn1',
        },
        {
          id: 'hel1/install-bp-gateway-api',
          flowId: 'd-34',
          label: 'bp-gateway-api',
          status: 'pending',
          region: 'hel1',
        },
      ],
      relationships: [],
    })
    expect(next.nodes.size).toBe(2)
    const regions = new Set(Array.from(next.nodes.values(), (n) => n.region))
    expect(regions.has('fsn1')).toBe(true)
    expect(regions.has('hel1')).toBe(true)
  })
})
