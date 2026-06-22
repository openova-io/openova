/**
 * CodeView.test.tsx — the shared YAML/IaC code surface (#sophisticated-editor).
 *
 * Covers the contract the call-sites rely on:
 *   • read-only viewer renders the content + a Copy button + optional title.
 *   • the lazy CodeMirror core mounts (`.cm-editor`) in both modes.
 *   • edit mode emits onChange when the live EditorView dispatches an edit.
 *   • Copy writes the value to the clipboard.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import type { EditorView } from '@codemirror/view'

import { CodeView } from './CodeView'

afterEach(() => cleanup())

const SAMPLE = 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n'

describe('CodeView — read-only viewer', () => {
  it('renders the content via the lazy CodeMirror surface', async () => {
    const { container } = render(
      <CodeView value={SAMPLE} readOnly title="manifests/demo.yaml" testId="cv" />,
    )
    // Header title surfaces the manifest path.
    expect(screen.getByTestId('cv-title').textContent).toBe('manifests/demo.yaml')
    // CodeMirror mounts (lazy → wait for the editor DOM).
    await waitFor(() => expect(container.querySelector('.cm-editor')).toBeTruthy())
    expect(container.querySelector('.cm-content')?.textContent).toContain('ConfigMap')
  })

  it('shows a Copy button by default and hides it when copyable=false', () => {
    const { rerender } = render(<CodeView value={SAMPLE} readOnly testId="cv" />)
    expect(screen.getByTestId('cv-copy')).toBeTruthy()
    rerender(<CodeView value={SAMPLE} readOnly copyable={false} title="t" testId="cv" />)
    expect(screen.queryByTestId('cv-copy')).toBeNull()
  })
})

describe('CodeView — copy', () => {
  beforeEach(() => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('copies the value to the clipboard and flips the label', async () => {
    render(<CodeView value={SAMPLE} readOnly testId="cv" />)
    fireEvent.click(screen.getByTestId('cv-copy'))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(SAMPLE)
    await waitFor(() => expect(screen.getByTestId('cv-copy').textContent).toContain('Copied'))
  })
})

describe('CodeView — edit mode', () => {
  it('emits onChange when the editor document is edited', async () => {
    const onChange = vi.fn()
    let view: EditorView | null = null
    render(
      <CodeView
        value={SAMPLE}
        onChange={onChange}
        testId="cv"
        onCreateEditor={(v) => {
          view = v
        }}
      />,
    )
    await waitFor(() => expect(view).not.toBeNull())
    view!.dispatch({ changes: { from: view!.state.doc.length, insert: '  extra: 1\n' } })
    await waitFor(() => expect(onChange).toHaveBeenCalled())
    expect(onChange.mock.calls.at(-1)![0]).toContain('extra: 1')
  })
})
