/**
 * @openova/flow-canvas — FlowLogFeed slot.
 *
 * A *bare* container component for a per-node detail pane (log tail,
 * execution panel, kubectl describe). The host supplies the actual
 * content via the `renderDetail` prop on FlowCanvas; FlowLogFeed only
 * wraps that content with the canonical dock chrome (Esc close,
 * focused-node label, optional toolbar slot).
 *
 * Hosts that want a fancier log pane (xterm tail, search) embed their
 * component INSIDE the renderDetail callback. The canvas does not
 * own that.
 */

import type { ReactNode } from 'react'
import { useEffect } from 'react'

export interface FlowLogFeedProps {
  /** Which node's detail to render. `null` collapses the pane. */
  selectedNodeId: string | null
  /** Render-prop for the actual detail content. */
  renderDetail?: (nodeId: string) => ReactNode
  /** Close handler — host wires this to its selection state. */
  onClose?: () => void
  /** Title slot — defaults to the node id. */
  title?: ReactNode
  /** Optional left-side toolbar slot (actions, status badges). */
  toolbar?: ReactNode
}

export function FlowLogFeed({
  selectedNodeId,
  renderDetail,
  onClose,
  title,
  toolbar,
}: FlowLogFeedProps) {
  useEffect(() => {
    if (!selectedNodeId) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose?.()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [selectedNodeId, onClose])

  if (!selectedNodeId) return null
  return (
    <aside
      className="flow-log-feed"
      data-testid="flow-log-feed"
      data-node-id={selectedNodeId}
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        background: 'var(--flow-canvas-bg)',
        border: '1px solid var(--flow-canvas-border)',
        borderRadius: 12,
      }}
    >
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '8px 12px',
          borderBottom: '1px solid var(--flow-canvas-border)',
          color: 'var(--flow-bubble-label)',
          fontSize: 12,
        }}
      >
        <span data-testid="flow-log-feed-title">{title ?? selectedNodeId}</span>
        <span style={{ display: 'inline-flex', gap: 8, alignItems: 'center' }}>
          {toolbar}
          {onClose ? (
            <button
              type="button"
              onClick={onClose}
              data-testid="flow-log-feed-close"
              style={{
                background: 'transparent',
                color: 'inherit',
                border: '1px solid var(--flow-canvas-border)',
                borderRadius: 4,
                cursor: 'pointer',
                padding: '2px 6px',
                fontSize: 11,
              }}
            >
              Esc
            </button>
          ) : null}
        </span>
      </header>
      <div
        style={{
          flex: 1,
          minHeight: 0,
          overflow: 'auto',
          padding: 12,
          color: 'var(--flow-bubble-label)',
          fontFamily: 'var(--font-mono, ui-monospace, monospace)',
          fontSize: 12,
        }}
      >
        {renderDetail ? renderDetail(selectedNodeId) : null}
      </div>
    </aside>
  )
}
