/**
 * JobDependenciesGraph.test.tsx — render lock-in for the SVG DAG widget.
 *
 *   • 3 nodes + 2 edges from THREE_NODE_CHAIN render with the expected
 *     data-testids.
 *   • Clicking a node fires the `onNodeClick` callback with the node id.
 *   • Empty input renders the empty-state placeholder.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { JobDependenciesGraph } from './JobDependenciesGraph'
import { THREE_NODE_CHAIN } from '@/test/fixtures/deps-graph.fixture'

afterEach(() => cleanup())

describe('JobDependenciesGraph — render', () => {
  it('renders 3 node groups and 2 edge polylines', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} />)
    expect(screen.getByTestId('jobs-deps-graph')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-node-a')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-node-b')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-node-c')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-edge-a-b')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-edge-b-c')).toBeTruthy()
  })

  it('encodes job status on the node group via data-status', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} />)
    expect(screen.getByTestId('jobs-deps-node-a').getAttribute('data-status')).toBe('succeeded')
    expect(screen.getByTestId('jobs-deps-node-b').getAttribute('data-status')).toBe('running')
    expect(screen.getByTestId('jobs-deps-node-c').getAttribute('data-status')).toBe('pending')
  })

  it('renders the empty state when given no jobs', () => {
    render(<JobDependenciesGraph jobs={[]} />)
    expect(screen.getByTestId('jobs-deps-graph-empty')).toBeTruthy()
    expect(screen.queryByTestId('jobs-deps-graph')).toBeNull()
  })
})

describe('JobDependenciesGraph — interaction', () => {
  it('fires onNodeClick with the node id when a node is clicked', () => {
    const handler = vi.fn()
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} onNodeClick={handler} />)
    fireEvent.click(screen.getByTestId('jobs-deps-node-b'))
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith('b')
  })

  it('fires onNodeClick on Enter key when focused (keyboard a11y)', () => {
    const handler = vi.fn()
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} onNodeClick={handler} />)
    const node = screen.getByTestId('jobs-deps-node-c')
    fireEvent.keyDown(node, { key: 'Enter' })
    expect(handler).toHaveBeenCalledWith('c')
  })

  it('does not crash when no onNodeClick is provided', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} />)
    fireEvent.click(screen.getByTestId('jobs-deps-node-a'))
    // No assertion — just verify no throw.
  })
})

describe('JobDependenciesGraph — dim overlay (P2 highlight)', () => {
  it('emits NO dim attribute when dimNodeIds is absent (default unchanged)', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} />)
    expect(document.querySelectorAll('[data-dimmed]').length).toBe(0)
    // Node opacity attribute is not present in the default render.
    expect(screen.getByTestId('jobs-deps-node-a').getAttribute('opacity')).toBeNull()
  })

  it('dims the nodes in dimNodeIds and leaves the rest bright', () => {
    render(
      <JobDependenciesGraph
        jobs={THREE_NODE_CHAIN}
        dimNodeIds={new Set(['b', 'c'])}
      />,
    )
    expect(screen.getByTestId('jobs-deps-node-a').getAttribute('data-dimmed')).toBe('false')
    expect(screen.getByTestId('jobs-deps-node-b').getAttribute('data-dimmed')).toBe('true')
    expect(screen.getByTestId('jobs-deps-node-c').getAttribute('data-dimmed')).toBe('true')
    // A dimmed node carries a reduced opacity; a bright one does not.
    expect(screen.getByTestId('jobs-deps-node-b').getAttribute('opacity')).toBe('0.25')
    expect(screen.getByTestId('jobs-deps-node-a').getAttribute('opacity')).toBeNull()
  })

  it('dims an edge when either endpoint is dimmed', () => {
    render(
      <JobDependenciesGraph
        jobs={THREE_NODE_CHAIN}
        dimNodeIds={new Set(['c'])}
      />,
    )
    // a → b: neither endpoint dimmed → bright.
    expect(screen.getByTestId('jobs-deps-edge-a-b').getAttribute('data-dimmed')).toBe('false')
    // b → c: c is dimmed → the edge dims too.
    expect(screen.getByTestId('jobs-deps-edge-b-c').getAttribute('data-dimmed')).toBe('true')
  })
})

describe('JobDependenciesGraph — height clamp', () => {
  it('clamps height below 350 to 350', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} height={100} />)
    const wrapper = screen.getByTestId('jobs-deps-graph-wrapper')
    expect(wrapper.style.height).toBe('350px')
  })

  it('clamps height above 450 to 450', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} height={9999} />)
    const wrapper = screen.getByTestId('jobs-deps-graph-wrapper')
    expect(wrapper.style.height).toBe('450px')
  })

  it('uses default 380 when no height is passed', () => {
    render(<JobDependenciesGraph jobs={THREE_NODE_CHAIN} />)
    const wrapper = screen.getByTestId('jobs-deps-graph-wrapper')
    expect(wrapper.style.height).toBe('380px')
  })
})

describe('JobDependenciesGraph — per-instance arrow marker id (P2 multi-graph)', () => {
  it('gives each rendered graph a UNIQUE marker id and references its own (no duplicate DOM ids across sections)', () => {
    // P2 renders one graph per orchestration-unit section on the same page.
    // A hardcoded marker id would repeat → invalid duplicate DOM ids and
    // url(#id) would resolve only to the first. useId() must namespace it.
    const { container } = render(
      <>
        <JobDependenciesGraph jobs={THREE_NODE_CHAIN} />
        <JobDependenciesGraph jobs={THREE_NODE_CHAIN} />
      </>,
    )
    const ids = Array.from(container.querySelectorAll('marker')).map((m) => m.getAttribute('id'))
    expect(ids.length).toBe(2)
    expect(ids[0]).toBeTruthy()
    expect(ids[1]).toBeTruthy()
    expect(ids[0]).not.toBe(ids[1])
    // Every edge references SOME instance's marker, and both markers are used.
    const refs = new Set(
      Array.from(container.querySelectorAll('[data-testid^="jobs-deps-edge-"]')).map((e) =>
        e.getAttribute('marker-end'),
      ),
    )
    expect(refs.has(`url(#${ids[0]})`)).toBe(true)
    expect(refs.has(`url(#${ids[1]})`)).toBe(true)
  })
})
