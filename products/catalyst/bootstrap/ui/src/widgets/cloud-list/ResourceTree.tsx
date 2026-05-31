/**
 * ResourceTree — EPIC-4 Slice R2 (#1099) — recursive renderer for the
 * resource tree panel. Walks UP via ownerReferences and DOWN via
 * label-selectors + ownerReferences, both already projected by the
 * server-side handler at /k8s/{kind}/{ns}/{name}/tree.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #3 (event-driven) — initial fetch is one round-trip; live updates
 *      arrive via the existing /k8s/stream SSE shared by CloudPage.
 *   #4 (never hardcode) — every node click navigates via the same
 *      resourceDetailHref helper used by ResourceDetailPage so there's
 *      one source-of-truth for the URL shape.
 */

import { useState } from 'react'

import {
  resourceDetailHref,
  type ResourceTreeNode,
} from '@/pages/sovereign/cloud-list/resource.api'

export interface ResourceTreeProps {
  /** Cloud-list base path so child nodes can deep-link. */
  basePath: string
  tree: ResourceTreeNode | null
  isLoading?: boolean
  isError?: boolean
  /** Test seam: when supplied, called instead of window navigation. */
  onSelect?: (kind: string, ns: string | undefined, name: string) => void
}

interface NodeRowProps {
  node: ResourceTreeNode
  basePath: string
  depth: number
  direction: 'self' | 'owner' | 'child'
  onSelect: (kind: string, ns: string | undefined, name: string) => void
}

function statusBadge(node: ResourceTreeNode): string {
  if (node.ready) return 'Ready'
  if (node.phase) return node.phase
  // G80 #2627: passive K8s kinds (Secret/ConfigMap/PolicyReport/etc.)
  // have no Ready concept. Backend marks them readinessApplicable=false;
  // treat undefined as true for compat with older API responses.
  if (node.readinessApplicable === false) return 'N/A'
  return 'Pending'
}

function statusColorClass(node: ResourceTreeNode): string {
  if (node.ready) return 'text-emerald-300'
  if (node.phase === 'Failed' || node.phase === 'Error') return 'text-rose-300'
  if (node.readinessApplicable === false) return 'text-[var(--color-text-dim)]'
  return 'text-amber-300'
}

function NodeRow({ node, basePath, depth, direction, onSelect }: NodeRowProps) {
  const [expanded, setExpanded] = useState(true)
  const indent = { paddingLeft: `${Math.min(depth, 6) * 16}px` }
  const hasOwners = (node.owners?.length ?? 0) > 0
  const hasChildren = (node.children?.length ?? 0) > 0
  const isExpandable = hasOwners || hasChildren

  return (
    <div
      data-testid={`resource-tree-row-${node.kind}-${node.name}`}
      className="border-b border-[var(--color-border)] last:border-0"
    >
      <div className="flex items-center gap-2 px-3 py-2 text-sm" style={indent}>
        <button
          type="button"
          aria-label={isExpandable ? (expanded ? 'Collapse' : 'Expand') : 'Leaf node'}
          aria-expanded={expanded}
          onClick={() => setExpanded((v) => !v)}
          disabled={!isExpandable}
          className={`flex h-4 w-4 items-center justify-center rounded ${
            isExpandable ? 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]' : 'opacity-30'
          }`}
        >
          {isExpandable ? (expanded ? '▾' : '▸') : '·'}
        </button>
        <button
          type="button"
          onClick={() => onSelect(node.kind, node.ns, node.name)}
          data-testid={`resource-tree-link-${node.kind}-${node.name}`}
          className="font-mono text-[var(--color-text)] hover:underline focus-visible:outline-none"
        >
          {node.kind}/{node.name}
        </button>
        {direction !== 'self' && (
          <span className="text-xs text-[var(--color-text-dim)]">
            ({direction === 'owner' ? 'owner' : 'child'})
          </span>
        )}
        <span className={`ml-auto text-xs ${statusColorClass(node)}`}>{statusBadge(node)}</span>
      </div>
      {expanded && hasOwners && (
        <div data-testid={`resource-tree-owners-${node.name}`}>
          {node.owners!.map((o) => (
            <NodeRow
              key={`o-${o.kind}-${o.ns ?? ''}-${o.name}`}
              node={o}
              basePath={basePath}
              depth={depth + 1}
              direction="owner"
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
      {expanded && hasChildren && (
        <div data-testid={`resource-tree-children-${node.name}`}>
          {node.children!.map((c) => (
            <NodeRow
              key={`c-${c.kind}-${c.ns ?? ''}-${c.name}`}
              node={c}
              basePath={basePath}
              depth={depth + 1}
              direction="child"
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
    </div>
  )
}

export function ResourceTree({ basePath, tree, isLoading, isError, onSelect }: ResourceTreeProps) {
  const handleSelect =
    onSelect ??
    ((kind: string, ns: string | undefined, name: string) => {
      if (typeof window === 'undefined') return
      const href = resourceDetailHref(basePath, kind, ns, name)
      window.location.assign(href)
    })

  if (isLoading) {
    return (
      <div data-testid="resource-tree-loading" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Loading resource tree…
      </div>
    )
  }
  if (isError) {
    return (
      <div data-testid="resource-tree-error" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Resource tree temporarily unreachable.
      </div>
    )
  }
  if (!tree) {
    return (
      <div data-testid="resource-tree-empty" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        No tree data.
      </div>
    )
  }
  return (
    <div data-testid="resource-tree" className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)]">
      <NodeRow node={tree} basePath={basePath} depth={0} direction="self" onSelect={handleSelect} />
    </div>
  )
}
