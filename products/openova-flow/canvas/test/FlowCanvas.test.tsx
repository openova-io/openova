/**
 * @openova/flow-canvas — basic render + interaction tests.
 *
 * Verifies the canvas:
 *   • renders one <g data-testid="flow-node-X"> per visible node
 *   • renders one edge per non-`contains` Relationship
 *   • tags edges with data-rel-type for each of the 6 PMI types
 *   • shows the empty-state message when there are no nodes
 *   • surfaces the host ring via `data-host="true"` on the right node
 *   • surfaces the selection ring via `data-open="true"`
 *   • calls onNodeOpen after the single-click debounce
 *   • calls onFoldToggle on double-click of a group node
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { FlowCanvas } from '../src/FlowCanvas'
import type { FlowInstance, FlowNode, Relationship } from '@openova/flow-core'

const FLOW: FlowInstance = { id: 'f1', status: 'running', startedAt: 0 }

function leaf(id: string, status = 'pending'): FlowNode {
  return { id, flowId: FLOW.id, label: id, status }
}

describe('FlowCanvas — render', () => {
  it('renders an empty-state placeholder when there are no nodes', () => {
    render(
      <FlowCanvas flow={FLOW} nodes={[]} relationships={[]} folded={new Set()} />,
    )
    expect(screen.getByTestId('flow-canvas-empty')).toBeTruthy()
  })

  it('renders one node group per visible FlowNode', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b'), leaf('c')]
    render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={[]} folded={new Set()} />,
    )
    expect(screen.getByTestId('flow-node-a')).toBeTruthy()
    expect(screen.getByTestId('flow-node-b')).toBeTruthy()
    expect(screen.getByTestId('flow-node-c')).toBeTruthy()
  })

  it('tags edges with data-rel-type for each non-contains relationship', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b'), leaf('c'), leaf('d'), leaf('e'), leaf('f')]
    const rels: Relationship[] = [
      { fromId: 'a', toId: 'b', type: 'finish-to-start' },
      { fromId: 'b', toId: 'c', type: 'start-to-start' },
      { fromId: 'c', toId: 'd', type: 'finish-to-finish' },
      { fromId: 'd', toId: 'e', type: 'start-to-finish' },
      { fromId: 'e', toId: 'f', type: 'triggers' },
    ]
    const { container } = render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={rels} folded={new Set()} />,
    )
    const edgeGroups = container.querySelectorAll('g[data-flow-edge]')
    const seenTypes = new Set<string>()
    edgeGroups.forEach((g) => {
      const t = g.getAttribute('data-rel-type')
      if (t) seenTypes.add(t)
    })
    expect(seenTypes.has('finish-to-start')).toBe(true)
    expect(seenTypes.has('start-to-start')).toBe(true)
    expect(seenTypes.has('finish-to-finish')).toBe(true)
    expect(seenTypes.has('start-to-finish')).toBe(true)
    expect(seenTypes.has('triggers')).toBe(true)
  })

  it('does NOT render `contains` edges (hierarchy is grouping, not an edge)', () => {
    const nodes: FlowNode[] = [leaf('group-a'), leaf('child-1')]
    const rels: Relationship[] = [
      { fromId: 'child-1', toId: 'group-a', type: 'contains' },
    ]
    const { container } = render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={rels} folded={new Set(['group-a'])} />,
    )
    const containsEdges = container.querySelectorAll('g[data-flow-edge][data-rel-type="contains"]')
    expect(containsEdges.length).toBe(0)
  })

  it('marks the host node via data-host="true"', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b')]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        hostNodeId="b"
      />,
    )
    const host = screen.getByTestId('flow-node-b')
    expect(host.getAttribute('data-host')).toBe('true')
    const other = screen.getByTestId('flow-node-a')
    expect(other.getAttribute('data-host')).toBe('false')
  })

  it('marks the selected node via data-open="true"', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b')]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        selectedNodeId="a"
      />,
    )
    expect(screen.getByTestId('flow-node-a').getAttribute('data-open')).toBe('true')
    expect(screen.getByTestId('flow-node-b').getAttribute('data-open')).toBe('false')
  })
})

describe('FlowCanvas — interaction', () => {
  it('calls onNodeOpen after the single-click debounce', async () => {
    vi.useFakeTimers()
    const onNodeOpen = vi.fn()
    const nodes: FlowNode[] = [leaf('a')]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        onNodeOpen={onNodeOpen}
      />,
    )
    fireEvent.click(screen.getByTestId('flow-node-a'))
    expect(onNodeOpen).not.toHaveBeenCalled()
    act(() => {
      vi.advanceTimersByTime(250)
    })
    expect(onNodeOpen).toHaveBeenCalledWith('a')
    vi.useRealTimers()
  })

  it('calls onFoldToggle on double-click of a group node', () => {
    // A group node is one that is the `toId` of at least one
    // `contains` edge.
    const nodes: FlowNode[] = [leaf('grp'), leaf('child')]
    const rels: Relationship[] = [
      { fromId: 'child', toId: 'grp', type: 'contains' },
    ]
    const onFoldToggle = vi.fn()
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={rels}
        folded={new Set(['grp'])}
        onFoldToggle={onFoldToggle}
      />,
    )
    const grpNode = screen.getByTestId('flow-node-grp')
    fireEvent.doubleClick(grpNode)
    expect(onFoldToggle).toHaveBeenCalledWith('grp')
  })

  it('calls onNodeNavigate on double-click of a leaf node', () => {
    const onNodeNavigate = vi.fn()
    const nodes: FlowNode[] = [leaf('a')]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        onNodeNavigate={onNodeNavigate}
      />,
    )
    fireEvent.doubleClick(screen.getByTestId('flow-node-a'))
    expect(onNodeNavigate).toHaveBeenCalledWith('a')
  })
})
