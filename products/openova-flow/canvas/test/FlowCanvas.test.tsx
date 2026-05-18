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

/* ────────────────────────────────────────────────────────────────────
 * Agent #9 — fold UX, lane layout, actions menu, cross-flow nav.
 * ──────────────────────────────────────────────────────────────────── */

function group(id: string, meta?: Record<string, unknown>): FlowNode {
  return { id, flowId: FLOW.id, label: id, status: 'pending', meta }
}

describe('FlowCanvas — lane layout (Agent #9)', () => {
  it('renders a lane rectangle for each `contains`-parent with meta.layout', () => {
    const nodes: FlowNode[] = [
      group('fsn1', { layout: 'lane-vertical', isGroup: true }),
      group('hel1', { layout: 'lane-vertical', isGroup: true }),
      leaf('hr-a'),
      leaf('hr-b'),
    ]
    const rels: Relationship[] = [
      { fromId: 'hr-a', toId: 'fsn1', type: 'contains' },
      { fromId: 'hr-b', toId: 'hel1', type: 'contains' },
    ]
    const { container } = render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={rels} folded={new Set()} />,
    )
    expect(container.querySelector('g[data-testid="flow-lane-fsn1"]')).toBeTruthy()
    expect(container.querySelector('g[data-testid="flow-lane-hel1"]')).toBeTruthy()
    expect(
      container.querySelector('g[data-testid="flow-lane-fsn1"]')?.getAttribute('data-lane-axis'),
    ).toBe('vertical')
  })

  it('nests phase lanes inside region lanes (lane-depth surfaced)', () => {
    const nodes: FlowNode[] = [
      group('fsn1', { layout: 'lane-vertical', isGroup: true }),
      group('fsn1/phase-1', { layout: 'lane-horizontal', isGroup: true, sortKey: 1 }),
      leaf('hr-1'),
    ]
    const rels: Relationship[] = [
      { fromId: 'fsn1/phase-1', toId: 'fsn1', type: 'contains' },
      { fromId: 'hr-1', toId: 'fsn1/phase-1', type: 'contains' },
    ]
    const { container } = render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={rels} folded={new Set()} />,
    )
    const region = container.querySelector('g[data-testid="flow-lane-fsn1"]')
    const phase = container.querySelector('g[data-testid="flow-lane-fsn1/phase-1"]')
    expect(region).toBeTruthy()
    expect(phase).toBeTruthy()
    expect(region?.getAttribute('data-lane-depth')).toBe('0')
    expect(phase?.getAttribute('data-lane-depth')).toBe('1')
    expect(phase?.getAttribute('data-lane-axis')).toBe('horizontal')
  })

  it('falls back to organic layout (no lane rect) when no group has meta.layout', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b')]
    const rels: Relationship[] = [
      { fromId: 'a', toId: 'b', type: 'finish-to-start' },
    ]
    const { container } = render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={rels} folded={new Set()} />,
    )
    expect(container.querySelector('g[data-testid="flow-lanes"]')).toBeNull()
  })
})

describe('FlowCanvas — child-count badge (Agent #9)', () => {
  it('renders a recursive descendant-count badge on foldable parents', () => {
    // fsn1 contains phase-1 contains hr-a, hr-b → fsn1.descendantCount=3
    const nodes: FlowNode[] = [
      group('fsn1'),
      group('phase-1'),
      leaf('hr-a'),
      leaf('hr-b'),
    ]
    const rels: Relationship[] = [
      { fromId: 'phase-1', toId: 'fsn1', type: 'contains' },
      { fromId: 'hr-a', toId: 'phase-1', type: 'contains' },
      { fromId: 'hr-b', toId: 'phase-1', type: 'contains' },
    ]
    const { container } = render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={rels}
        folded={new Set(['fsn1'])}
      />,
    )
    const badge = container.querySelector('g[data-testid="flow-node-badge-fsn1"]')
    expect(badge).toBeTruthy()
    expect(badge?.getAttribute('data-descendant-count')).toBe('3')
  })

  it('does NOT render a badge on leaf nodes', () => {
    const nodes: FlowNode[] = [leaf('a')]
    const { container } = render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={[]} folded={new Set()} />,
    )
    expect(container.querySelector('g[data-testid="flow-node-badge-a"]')).toBeNull()
  })
})

describe('FlowCanvas — hover dim (Agent #9)', () => {
  it('dims non-neighbor nodes when one is hovered, restores on mouseleave', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b'), leaf('c')]
    const rels: Relationship[] = [
      { fromId: 'a', toId: 'b', type: 'finish-to-start' },
    ]
    render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={rels} folded={new Set()} />,
    )
    fireEvent.mouseEnter(screen.getByTestId('flow-node-a'))
    // c is NOT a neighbor of a → dimmed.
    expect(screen.getByTestId('flow-node-c').getAttribute('data-dimmed')).toBe('true')
    // b IS a neighbor → not dimmed.
    expect(screen.getByTestId('flow-node-b').getAttribute('data-dimmed')).toBe('false')
    // a itself is hovered → not dimmed.
    expect(screen.getByTestId('flow-node-a').getAttribute('data-dimmed')).toBe('false')
    fireEvent.mouseLeave(screen.getByTestId('flow-node-a'))
    expect(screen.getByTestId('flow-node-c').getAttribute('data-dimmed')).toBe('false')
  })

  it('selection ring keeps full opacity even while hover is active', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b')]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        selectedNodeId="b"
      />,
    )
    fireEvent.mouseEnter(screen.getByTestId('flow-node-a'))
    // b is selected — selection-dim already calculated based on
    // neighborhood; hover should NOT downgrade it further.
    expect(screen.getByTestId('flow-node-b').getAttribute('data-open')).toBe('true')
  })
})

describe('FlowCanvas — right-click actions menu (Agent #9)', () => {
  it('opens a context menu on right-click when actions are supplied', () => {
    const onNodeAction = vi.fn()
    const invoke = vi.fn()
    const actions = [
      { id: 'retry', label: 'Retry', invoke },
      { id: 'logs', label: 'View logs', invoke },
    ]
    const nodes: FlowNode[] = [leaf('a')]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        actions={actions}
        onNodeAction={onNodeAction}
      />,
    )
    fireEvent.contextMenu(screen.getByTestId('flow-node-a'))
    const menu = screen.getByTestId('flow-node-actions-menu')
    expect(menu).toBeTruthy()
    expect(menu.getAttribute('data-node-id')).toBe('a')
    fireEvent.click(screen.getByTestId('flow-node-action-retry'))
    expect(onNodeAction).toHaveBeenCalledWith('a', 'retry')
  })

  it('does NOT open a menu when no actions are supplied', () => {
    const nodes: FlowNode[] = [leaf('a')]
    render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={[]} folded={new Set()} />,
    )
    fireEvent.contextMenu(screen.getByTestId('flow-node-a'))
    expect(screen.queryByTestId('flow-node-actions-menu')).toBeNull()
  })

  it('filters actions whose `enabled` predicate returns false', () => {
    const nodes: FlowNode[] = [leaf('a')]
    const actions = [
      { id: 'visible', label: 'Visible', invoke: vi.fn() },
      { id: 'hidden', label: 'Hidden', invoke: vi.fn(), enabled: (_id: string) => false },
    ]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        actions={actions}
      />,
    )
    fireEvent.contextMenu(screen.getByTestId('flow-node-a'))
    expect(screen.getByTestId('flow-node-action-visible')).toBeTruthy()
    expect(screen.queryByTestId('flow-node-action-hidden')).toBeNull()
  })
})

describe('FlowCanvas — triggeredBy banner + cross-flow nav (Agent #9)', () => {
  it('renders a triggeredBy banner when flow.triggeredBy is non-empty', () => {
    const onNavigateFlow = vi.fn()
    const triggeringFlow: FlowInstance = {
      ...FLOW,
      triggeredBy: [{ flowId: 'parent-flow-1', when: 'success' }],
    }
    const nodes: FlowNode[] = [leaf('a')]
    render(
      <FlowCanvas
        flow={triggeringFlow}
        nodes={nodes}
        relationships={[]}
        folded={new Set()}
        onNavigateFlow={onNavigateFlow}
      />,
    )
    const badge = screen.getByTestId('flow-triggered-by-parent-flow-1')
    expect(badge).toBeTruthy()
    fireEvent.click(badge)
    expect(onNavigateFlow).toHaveBeenCalledWith('parent-flow-1')
  })

  it('does NOT render the banner when triggeredBy is missing/empty', () => {
    const nodes: FlowNode[] = [leaf('a')]
    render(
      <FlowCanvas flow={FLOW} nodes={nodes} relationships={[]} folded={new Set()} />,
    )
    expect(screen.queryByTestId('flow-triggered-by-banner')).toBeNull()
  })

  it('renders a Sandbox terminal-glyph SVG when meta.kind = Sandbox', () => {
    // FlowNodes carry kind via `meta.kind` per the post-2026-05-18
    // sandbox-node contract. The canvas swaps the bubble glyph from
    // the legacy ○/◐/✓/✗ text-based status pip to a tabler
    // terminal-monitor SVG so the operator can pick out sandbox
    // bubbles at a glance — same icon as the Sovereign sidebar.
    const sandbox: FlowNode = {
      id: 'fsn1:sandbox:emrah-at-acme',
      flowId: FLOW.id,
      label: 'emrah@acme.io',
      status: 'succeeded',
      meta: { kind: 'Sandbox', ownerEmail: 'emrah@acme.io' },
    }
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={[sandbox]}
        relationships={[]}
        folded={new Set()}
      />,
    )
    const glyph = screen.getByTestId(`flow-node-glyph-${sandbox.id}`)
    expect(glyph).toBeTruthy()
    expect(glyph.getAttribute('data-kind')).toBe('Sandbox')
    // The bubble itself surfaces the kind via data-meta-kind for any
    // downstream e2e selector.
    const node = screen.getByTestId(`flow-node-${sandbox.id}`)
    expect(node.getAttribute('data-meta-kind')).toBe('Sandbox')
  })

  it('renders a SandboxPod prompt-glyph when meta.kind = SandboxPod', () => {
    const pod: FlowNode = {
      id: 'fsn1:sandbox-pod:sb-emrah/pty-server-abc',
      flowId: FLOW.id,
      label: 'pty-server (abc)',
      status: 'running',
      meta: { kind: 'SandboxPod', component: 'pty-server' },
    }
    render(
      <FlowCanvas flow={FLOW} nodes={[pod]} relationships={[]} folded={new Set()} />,
    )
    const glyph = screen.getByTestId(`flow-node-glyph-${pod.id}`)
    expect(glyph.getAttribute('data-kind')).toBe('SandboxPod')
  })

  it('falls back to the legacy status glyph when meta.kind is absent', () => {
    const plain: FlowNode = leaf('plain', 'succeeded')
    render(
      <FlowCanvas flow={FLOW} nodes={[plain]} relationships={[]} folded={new Set()} />,
    )
    // No glyph wrapper → the bubble renders the legacy ✓ text node.
    expect(screen.queryByTestId(`flow-node-glyph-${plain.id}`)).toBeNull()
    const node = screen.getByTestId(`flow-node-${plain.id}`)
    expect(node.getAttribute('data-meta-kind')).toBe('')
  })

  it('renders a cross-flow "→ flow" tag on a node whose Relationship targets another flow', () => {
    const onNavigateFlow = vi.fn()
    const nodes: FlowNode[] = [leaf('a')]
    const rels: Relationship[] = [
      {
        fromId: 'a',
        toId: 'remote-node',
        toFlowId: 'remote-flow',
        type: 'triggers',
      },
    ]
    render(
      <FlowCanvas
        flow={FLOW}
        nodes={nodes}
        relationships={rels}
        folded={new Set()}
        onNavigateFlow={onNavigateFlow}
      />,
    )
    const tag = screen.getByTestId('flow-node-crossflow-a')
    expect(tag).toBeTruthy()
    expect(tag.getAttribute('data-cross-flow-target')).toBe('remote-flow')
    fireEvent.click(tag)
    expect(onNavigateFlow).toHaveBeenCalledWith('remote-flow')
  })
})
