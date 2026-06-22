/**
 * codeTheme — a CodeMirror 6 theme + highlight style that binds to the
 * console's CSS custom properties (defined in src/app/globals.css and
 * flipped by `<html data-theme="light">`). Because CodeMirror applies its
 * theme as inline CSS-in-JS that ends up as real stylesheet rules, `var(--…)`
 * references resolve against whichever theme is active at paint time — so a
 * single theme object tracks both the dark default and the light peer with
 * zero JS theme-switching.
 *
 * Token colours intentionally reuse the existing accent / status palette so
 * the editor reads as "part of the console" rather than a default Monaco /
 * VS-Code drop-in. Syntax hues degrade gracefully: if a `--code-*` token is
 * absent the `var()` fallback keeps the editor legible.
 */
import { EditorView } from '@codemirror/view'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'
import type { Extension } from '@codemirror/state'

/** Base editor chrome — gutters, selection, cursor, active line, search. */
const baseTheme = EditorView.theme({
  '&': {
    color: 'var(--color-text, #e5e7eb)',
    backgroundColor: 'var(--color-bg-2, #111827)',
    fontSize: '13px',
    borderRadius: 'var(--radius-md, 0.5rem)',
    border: '1px solid var(--color-border, #1f2937)',
  },
  '&.cm-focused': {
    outline: 'none',
    borderColor: 'var(--color-accent, #3b82f6)',
  },
  '.cm-content': {
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
    caretColor: 'var(--color-accent, #3b82f6)',
    padding: '8px 0',
  },
  '.cm-scroller': {
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
    lineHeight: '1.5',
  },
  '.cm-cursor, .cm-dropCursor': {
    borderLeftColor: 'var(--color-accent, #3b82f6)',
  },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': {
    backgroundColor: 'color-mix(in oklab, var(--color-accent, #3b82f6) 28%, transparent)',
  },
  '.cm-gutters': {
    backgroundColor: 'var(--color-bg, #0b1220)',
    color: 'var(--color-text-dimmer, #6b7280)',
    border: 'none',
    borderRight: '1px solid var(--color-border, #1f2937)',
  },
  '.cm-activeLineGutter': {
    backgroundColor: 'color-mix(in oklab, var(--color-accent, #3b82f6) 10%, transparent)',
    color: 'var(--color-text-dim, #9ca3af)',
  },
  '.cm-activeLine': {
    backgroundColor: 'color-mix(in oklab, var(--color-accent, #3b82f6) 6%, transparent)',
  },
  '.cm-foldGutter span': {
    cursor: 'pointer',
    color: 'var(--color-text-dimmer, #6b7280)',
  },
  '.cm-foldPlaceholder': {
    backgroundColor: 'color-mix(in oklab, var(--color-accent, #3b82f6) 16%, transparent)',
    border: 'none',
    color: 'var(--color-text-dim, #9ca3af)',
    padding: '0 6px',
    borderRadius: '4px',
  },
  '.cm-panels': {
    backgroundColor: 'var(--color-bg, #0b1220)',
    color: 'var(--color-text, #e5e7eb)',
    borderTop: '1px solid var(--color-border, #1f2937)',
  },
  '.cm-panels .cm-textfield': {
    backgroundColor: 'var(--color-bg-2, #111827)',
    color: 'var(--color-text, #e5e7eb)',
    border: '1px solid var(--color-border, #1f2937)',
    borderRadius: 'var(--radius-sm, 0.375rem)',
  },
  '.cm-panels button, .cm-panels .cm-button': {
    backgroundColor: 'var(--color-bg-2, #111827)',
    backgroundImage: 'none',
    color: 'var(--color-text, #e5e7eb)',
    border: '1px solid var(--color-border, #1f2937)',
    borderRadius: 'var(--radius-sm, 0.375rem)',
  },
  '.cm-searchMatch': {
    backgroundColor: 'color-mix(in oklab, var(--color-warn, #f59e0b) 35%, transparent)',
    outline: '1px solid color-mix(in oklab, var(--color-warn, #f59e0b) 60%, transparent)',
  },
  '.cm-searchMatch.cm-searchMatch-selected': {
    backgroundColor: 'color-mix(in oklab, var(--color-accent, #3b82f6) 45%, transparent)',
  },
  '.cm-tooltip': {
    backgroundColor: 'var(--color-bg-2, #111827)',
    color: 'var(--color-text, #e5e7eb)',
    border: '1px solid var(--color-border, #1f2937)',
    borderRadius: 'var(--radius-sm, 0.375rem)',
  },
})

/** Syntax colours — keyed off the console palette so YAML keys / strings /
 *  numbers / comments read in the same accent family as the rest of the UI. */
const highlight = HighlightStyle.define([
  { tag: [t.propertyName, t.definition(t.propertyName)], color: 'var(--color-accent, #60a5fa)' },
  { tag: [t.string, t.special(t.string)], color: 'var(--color-success, #34d399)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--color-warn, #fbbf24)' },
  { tag: [t.keyword, t.operator], color: 'var(--color-accent-hover, #818cf8)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--color-text-dimmer, #6b7280)', fontStyle: 'italic' },
  { tag: [t.meta, t.documentMeta], color: 'var(--color-text-dim, #9ca3af)' },
  { tag: t.invalid, color: 'var(--color-danger, #f87171)' },
])

/** The complete theming extension bundle — chrome + syntax highlighting. */
export const consoleCodeTheme: Extension = [baseTheme, syntaxHighlighting(highlight)]
