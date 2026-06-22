/**
 * CodeMirrorEditor — the heavy CodeMirror 6 surface, kept behind a dynamic
 * import (see CodeView.tsx) so the ~200 KB editor never lands in the initial
 * SPA payload. It is the single source of truth for HOW code is rendered in
 * the console: syntax highlighting, line numbers, code folding, in-editor
 * search (Ctrl/Cmd-F), read-only vs edit modes, and console-themed colours
 * (src/widgets/code/codeTheme.ts, bound to the light/dark CSS variables).
 *
 * Do not import this module directly from feature code — always go through
 * <CodeView> / the YamlEditor, which add the chrome (copy button, header,
 * Suspense fallback) and the lazy boundary.
 */
import { useMemo } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { EditorView, lineNumbers } from '@codemirror/view'
import type { EditorState as CMEditorState } from '@codemirror/state'
import { EditorState, type Extension } from '@codemirror/state'
import { foldGutter, codeFolding } from '@codemirror/language'
import { search, highlightSelectionMatches } from '@codemirror/search'
import { yaml as yamlLang } from '@codemirror/lang-yaml'

import { consoleCodeTheme } from './codeTheme'

export type CodeLanguage = 'yaml' | 'plaintext'

export interface CodeMirrorEditorProps {
  value: string
  onChange?: (value: string) => void
  readOnly?: boolean
  language?: CodeLanguage
  /** CSS height (default 24rem). Read-only viewers usually pass a max via
   *  `maxHeight` and let the editor grow to content. */
  height?: string
  maxHeight?: string
  /** Accessible label forwarded to the editor's contenteditable surface. */
  ariaLabel?: string
  /** Stable hook for tests / e2e selectors. */
  testId?: string
  className?: string
  /** Forwarded to CodeMirror — receives the live EditorView on mount.
   *  Used by tests to dispatch programmatic edits. */
  onCreateEditor?: (view: EditorView, state: CMEditorState) => void
}

function languageExtension(language: CodeLanguage): Extension[] {
  // plaintext → no language parser (logs, free-form manifests that aren't
  // strictly YAML still get line numbers + folding + search + theme).
  return language === 'yaml' ? [yamlLang()] : []
}

export default function CodeMirrorEditor({
  value,
  onChange,
  readOnly = false,
  language = 'yaml',
  height = '24rem',
  maxHeight,
  ariaLabel,
  testId,
  className,
  onCreateEditor,
}: CodeMirrorEditorProps) {
  const extensions = useMemo<Extension[]>(() => {
    const exts: Extension[] = [
      lineNumbers(),
      codeFolding(),
      foldGutter(),
      search({ top: true }),
      highlightSelectionMatches(),
      EditorView.lineWrapping,
      consoleCodeTheme,
      ...languageExtension(language),
    ]
    if (readOnly) exts.push(EditorState.readOnly.of(true), EditorView.editable.of(false))
    return exts
  }, [language, readOnly])

  return (
    <div data-testid={testId} className={className}>
      <CodeMirror
        value={value}
        height={height}
        maxHeight={maxHeight}
        readOnly={readOnly}
        editable={!readOnly}
        extensions={extensions}
        onChange={onChange}
        onCreateEditor={onCreateEditor}
        // We supply our own theme via consoleCodeTheme; disable the bundled
        // light/dark presets so they don't override the CSS-variable theme.
        theme="none"
        basicSetup={{
          lineNumbers: false, // provided explicitly above (themed gutter)
          foldGutter: false, // provided explicitly above
          searchKeymap: true,
          highlightActiveLine: !readOnly,
          highlightActiveLineGutter: !readOnly,
          autocompletion: false,
        }}
        aria-label={ariaLabel}
      />
    </div>
  )
}
