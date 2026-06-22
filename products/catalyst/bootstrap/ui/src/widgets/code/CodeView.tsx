/**
 * CodeView — the ONE reusable code surface for the whole console. Every place
 * that shows or edits YAML / IaC routes through this component so the look,
 * theming, and feature set (syntax highlighting, line numbers, folding,
 * search, copy) are consistent everywhere:
 *
 *   • read-only viewer — install/upgrade/topology manifest previews, any
 *     per-resource YAML display (default `readOnly`).
 *   • editor — the cloud-list per-resource YAML tab and the catalog
 *     "Edit IaC" surface drive it through <YamlEditor>, which composes
 *     CodeView in edit mode.
 *
 * The heavy CodeMirror 6 core is lazy-loaded (React.lazy + Suspense) so it
 * never bloats the initial SPA payload; while it streams in, a themed <pre>
 * shows the same text so content is legible immediately (and a copy of the
 * text is still selectable). This keeps `npm run build`'s main chunk lean.
 */
import { lazy, Suspense, useCallback, useState } from 'react'
import type { EditorView } from '@codemirror/view'
import type { EditorState as CMEditorState } from '@codemirror/state'

import type { CodeLanguage } from './CodeMirrorEditor'

const CodeMirrorEditor = lazy(() => import('./CodeMirrorEditor'))

export interface CodeViewProps {
  value: string
  /** When provided the surface is editable and emits on every keystroke. */
  onChange?: (value: string) => void
  readOnly?: boolean
  language?: CodeLanguage
  /** Optional header label rendered above the editor (e.g. a manifest path). */
  title?: string
  /** Show the copy-to-clipboard button (default true). */
  copyable?: boolean
  height?: string
  maxHeight?: string
  ariaLabel?: string
  testId?: string
  className?: string
  /** Forwarded to CodeMirror — receives the live EditorView on mount. */
  onCreateEditor?: (view: EditorView, state: CMEditorState) => void
}

/** Themed fallback shown while the CodeMirror chunk loads (and as the SSR /
 *  no-JS rendering). Same monospace + surface tokens as the live editor. */
function CodeFallback({
  value,
  maxHeight,
  height,
  testId,
}: {
  value: string
  maxHeight?: string
  height?: string
  testId?: string
}) {
  return (
    <pre
      data-testid={testId ? `${testId}-fallback` : undefined}
      className="overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 font-mono text-[12px] leading-5 text-[var(--color-text)]"
      style={{ maxHeight: maxHeight ?? height, margin: 0 }}
    >
      {value}
    </pre>
  )
}

export function CodeView({
  value,
  onChange,
  readOnly,
  language = 'yaml',
  title,
  copyable = true,
  height,
  maxHeight,
  ariaLabel,
  testId,
  className,
  onCreateEditor,
}: CodeViewProps) {
  const isReadOnly = readOnly ?? onChange === undefined
  const [copied, setCopied] = useState(false)

  const onCopy = useCallback(async () => {
    try {
      await navigator.clipboard?.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard blocked (no permission / insecure context) — leave the
      // button state unchanged; the text remains selectable in the editor.
    }
  }, [value])

  const showHeader = title !== undefined || copyable

  return (
    <div data-testid={testId} className={className}>
      {showHeader && (
        <div className="flex items-center gap-2 px-1 pb-1.5">
          {title !== undefined && (
            <span
              data-testid={testId ? `${testId}-title` : undefined}
              className="truncate font-mono text-[11px] text-[var(--color-text-dim)]"
              title={title}
            >
              {title}
            </span>
          )}
          {copyable && (
            <button
              type="button"
              onClick={onCopy}
              data-testid={testId ? `${testId}-copy` : 'code-view-copy'}
              className="ml-auto rounded border border-[var(--color-border)] px-2 py-0.5 text-[11px] text-[var(--color-text-dim)] hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text)]"
            >
              {copied ? 'Copied ✓' : 'Copy'}
            </button>
          )}
        </div>
      )}
      <Suspense
        fallback={
          <CodeFallback value={value} maxHeight={maxHeight} height={height} testId={testId} />
        }
      >
        <CodeMirrorEditor
          value={value}
          onChange={onChange}
          readOnly={isReadOnly}
          language={language}
          height={height ?? (isReadOnly ? undefined : '24rem')}
          maxHeight={maxHeight}
          ariaLabel={ariaLabel ?? title}
          testId={testId ? `${testId}-cm` : undefined}
          onCreateEditor={onCreateEditor}
        />
      </Suspense>
    </div>
  )
}

export default CodeView
