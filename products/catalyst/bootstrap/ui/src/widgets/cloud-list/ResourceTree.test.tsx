/**
 * ResourceTree.test.tsx — EPIC-4 Slice R2 (#1099) — owner+selector
 * walk + click navigation.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { ResourceTree } from './ResourceTree'
import type { ResourceTreeNode } from '@/pages/sovereign/cloud-list/resource.api'

afterEach(() => cleanup())

const podLeaf: ResourceTreeNode = {
  kind: 'pod',
  name: 'wp-67-abc',
  ns: 'default',
  ready: true,
}
const replicaSet: ResourceTreeNode = {
  kind: 'replicaset',
  name: 'wp-67',
  ns: 'default',
  ready: true,
  children: [podLeaf],
}
const deployment: ResourceTreeNode = {
  kind: 'deployment',
  name: 'wp',
  ns: 'default',
  ready: true,
  children: [replicaSet],
}

describe('ResourceTree', () => {
  it('renders nothing when tree is null', () => {
    render(<ResourceTree basePath="/cloud" tree={null} />)
    expect(screen.getByTestId('resource-tree-empty')).toBeTruthy()
  })

  it('renders loading + error states', () => {
    const { rerender } = render(<ResourceTree basePath="/cloud" tree={null} isLoading />)
    expect(screen.getByTestId('resource-tree-loading')).toBeTruthy()
    rerender(<ResourceTree basePath="/cloud" tree={null} isError />)
    expect(screen.getByTestId('resource-tree-error')).toBeTruthy()
  })

  it('renders root node + descendants', () => {
    render(<ResourceTree basePath="/cloud" tree={deployment} />)
    expect(screen.getByTestId('resource-tree-row-deployment-wp')).toBeTruthy()
    expect(screen.getByTestId('resource-tree-row-replicaset-wp-67')).toBeTruthy()
    expect(screen.getByTestId('resource-tree-row-pod-wp-67-abc')).toBeTruthy()
  })

  it('clicking a leaf invokes onSelect with the resource tuple', () => {
    const onSelect = vi.fn()
    render(<ResourceTree basePath="/cloud" tree={deployment} onSelect={onSelect} />)
    fireEvent.click(screen.getByTestId('resource-tree-link-pod-wp-67-abc'))
    expect(onSelect).toHaveBeenCalledWith('pod', 'default', 'wp-67-abc')
  })

  it('expand / collapse toggles via the disclosure button', () => {
    render(<ResourceTree basePath="/cloud" tree={deployment} />)
    const collapseBtn = screen.getAllByLabelText('Collapse')[0]
    fireEvent.click(collapseBtn)
    // Children container should be removed from DOM after collapse.
    expect(screen.queryByTestId('resource-tree-children-wp')).toBeNull()
  })
})
