/**
 * NodeActionsMenu — per-node right-click action tray surfaced by
 * `FlowCanvas` when an adapter supplies a `NodeAction[]` list.
 *
 * Behaviour (mirrors ProfileMenu in catalyst-ui — canonical pattern
 * for click-outside + ESC dismissal — so we stay consistent without
 * pulling in a radix/headless-ui dependency):
 *
 *   • Positioned absolutely at the right-click coordinates. The
 *     parent renders this inside a `position: relative` host so the
 *     coords are local.
 *   • Dismissed on:
 *       — outside mousedown (anywhere not inside the menu)
 *       — Escape key (captured at document level)
 *       — selecting an action (after the invoke fires)
 *   • Actions disabled via `action.enabled?.(nodeId) === false` are
 *     rendered greyed-out and not clickable. Actions WITHOUT an
 *     `enabled` predicate are always enabled.
 *
 * The component is presentational: it doesn't know what an action
 * does — it only fires `onSelect(actionId)` and lets the canvas/host
 * route it.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode): no fixed
 * strings, no fixed action list, no embedded icons — everything is
 * supplied by the adapter via the NodeAction shape.
 */

import { useEffect, useRef } from 'react'
import type { NodeAction } from '@openova/flow-core'

export interface NodeActionsMenuProps {
  /** The node id this menu is bound to — passed to enabled predicates
   *  and surfaced in data-attrs for tests. */
  nodeId: string
  /** Visible-action list, already filtered by the host if it wants to
   *  hide actions entirely. The component still applies each action's
   *  own `enabled` predicate for the disabled-style. */
  actions: ReadonlyArray<NodeAction>
  /** Canvas-host-local pixel coords (top-left of the menu). */
  x: number
  y: number
  /** Fires once when the operator picks an action. The action's own
   *  `invoke` is NOT called by this component — wiring stays at the
   *  canvas/host boundary. */
  onSelect: (actionId: string) => void
  /** Fires on any dismissal path (outside-click, Esc, action click). */
  onDismiss: () => void
}

export function NodeActionsMenu(props: NodeActionsMenuProps) {
  const { nodeId, actions, x, y, onSelect, onDismiss } = props
  const ref = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    function onMouseDown(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        onDismiss()
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onDismiss()
      }
    }
    document.addEventListener('mousedown', onMouseDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onMouseDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [onDismiss])

  if (actions.length === 0) return null

  return (
    <div
      ref={ref}
      role="menu"
      data-testid="flow-node-actions-menu"
      data-node-id={nodeId}
      style={{
        position: 'absolute',
        top: y,
        left: x,
        minWidth: 180,
        background: 'var(--flow-menu-bg, #0f172a)',
        border: '1px solid var(--flow-menu-border, rgba(255,255,255,0.12))',
        borderRadius: 8,
        boxShadow: '0 12px 28px rgba(0,0,0,0.4)',
        padding: '6px 0',
        zIndex: 80,
        font: 'inherit',
        color: 'var(--flow-menu-text, #e2e8f0)',
        fontSize: 12,
      }}
      onContextMenu={(e) => {
        // Prevent the browser's native menu opening on top of ours.
        e.preventDefault()
      }}
    >
      {actions.map((action) => {
        const disabled = action.enabled ? !action.enabled(nodeId) : false
        return (
          <button
            key={action.id}
            type="button"
            role="menuitem"
            data-testid={`flow-node-action-${action.id}`}
            data-action-id={action.id}
            data-disabled={disabled ? 'true' : 'false'}
            disabled={disabled}
            onClick={() => {
              if (disabled) return
              onSelect(action.id)
            }}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              width: '100%',
              textAlign: 'left',
              padding: '6px 12px',
              background: 'transparent',
              border: 'none',
              color: disabled
                ? 'var(--flow-menu-text-dim, #475569)'
                : 'var(--flow-menu-text, #e2e8f0)',
              fontSize: 12,
              cursor: disabled ? 'not-allowed' : 'pointer',
              opacity: disabled ? 0.5 : 1,
            }}
          >
            {action.icon ? (
              <span
                data-testid={`flow-node-action-icon-${action.id}`}
                style={{ display: 'inline-flex', width: 14, height: 14 }}
              >
                {action.icon}
              </span>
            ) : null}
            <span>{action.label}</span>
          </button>
        )
      })}
    </div>
  )
}
