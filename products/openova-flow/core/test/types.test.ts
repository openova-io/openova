/**
 * @openova/flow-core — types snapshot. Compile-time only; the actual
 * assertions are that the test type-checks. Runtime tests assert that
 * the discriminated `FlowMessage` union has the documented set of
 * variants.
 */

import { describe, it, expect } from 'vitest'
import { isBlockingRelationship } from '../src/index'
import type {
  FlowInstance,
  FlowNode,
  Relationship,
  RelationshipType,
  FlowMessage,
  FlowAdapter,
  StatusTone,
} from '../src/index'

describe('@openova/flow-core types — runtime predicates', () => {
  it('classifies relationship types — `contains` is non-blocking, all others are blocking', () => {
    const cases: Array<[RelationshipType, boolean]> = [
      ['contains', false],
      ['finish-to-start', true],
      ['start-to-start', true],
      ['finish-to-finish', true],
      ['start-to-finish', true],
      ['triggers', true],
    ]
    for (const [t, expected] of cases) {
      expect(isBlockingRelationship(t)).toBe(expected)
    }
  })
})

describe('@openova/flow-core types — FlowMessage discriminator coverage', () => {
  function describeMessage(m: FlowMessage): string {
    switch (m.type) {
      case 'snapshot':
        return 'snapshot'
      case 'upsert-flow':
        return 'upsert-flow'
      case 'upsert-nodes':
        return 'upsert-nodes'
      case 'upsert-rels':
        return 'upsert-rels'
      case 'delete-nodes':
        return 'delete-nodes'
      case 'delete-rels':
        return 'delete-rels'
    }
  }

  it('exhaustively handles all 6 wire messages', () => {
    const flow: FlowInstance = { id: 'f', status: 'running', startedAt: 0 }
    const node: FlowNode = { id: 'n', flowId: 'f', label: 'n', status: 'pending' }
    const rel: Relationship = { fromId: 'a', toId: 'b', type: 'finish-to-start' }

    const msgs: FlowMessage[] = [
      { type: 'snapshot', flow, nodes: [node], relationships: [rel] },
      { type: 'upsert-flow', flow },
      { type: 'upsert-nodes', nodes: [node] },
      { type: 'upsert-rels', relationships: [rel] },
      { type: 'delete-nodes', ids: ['n'] },
      { type: 'delete-rels', pairs: [{ fromId: 'a', toId: 'b', type: 'finish-to-start' }] },
    ]
    expect(msgs.map(describeMessage)).toEqual([
      'snapshot',
      'upsert-flow',
      'upsert-nodes',
      'upsert-rels',
      'delete-nodes',
      'delete-rels',
    ])
  })
})

describe('@openova/flow-core types — adapter shape compiles', () => {
  it('accepts a minimum-viable FlowAdapter', () => {
    const tone: StatusTone = {
      fill: '#fff',
      ring: '#000',
      glyph: '#000',
      glow: 'transparent',
      edge: '#000',
      arrow: '#000',
      label: 'Test',
    }
    const adapter: FlowAdapter = {
      schemaVersion: 1,
      subscribe: (_id, _sink) => {
        return () => {
          /* noop */
        }
      },
      statusPalette: { pending: tone },
    }
    expect(adapter.schemaVersion).toBe(1)
  })
})
