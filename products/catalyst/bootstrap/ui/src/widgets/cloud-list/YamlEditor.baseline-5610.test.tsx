/**
 * YamlEditor.baseline-5610.test.tsx — #5610 facet B, PART 2.
 *
 * PART 1 (`pages/sovereign/CatalogDetail.iac-baseline-5610.test.tsx`) pins that
 * CatalogDetail feeds the just-committed YAML back as `seedYaml` after a
 * successful "Commit IaC". On its own that asserts a prop nobody proved
 * matters. This file closes the chain against the REAL widget: the seed is
 * exactly what the "• unsaved changes" pill and the Commit button read.
 *
 * The reported symptom was the pill still reading "• unsaved changes" beside
 * the editor's own "Committed to IaC ✓", with Commit still enabled and
 * inviting a redundant re-commit of identical bytes — clearing only on a full
 * page reload.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import type { EditorView } from '@codemirror/view'

import { YamlEditor } from './YamlEditor'

const COMMITTED_YAML = [
  'apiVersion: catalyst.openova.io/v1',
  'kind: Blueprint',
  'metadata:',
  '  name: bp-alloy',
  'spec:',
  '  version: 1.0.2',
  '',
].join('\n')

/** The issue's exact walk: one line, 1.0.2 → 1.0.3. */
const EDITED_YAML = COMMITTED_YAML.replace('version: 1.0.2', 'version: 1.0.3')

beforeEach(() => {
  global.fetch = vi.fn()
})
afterEach(() => cleanup())

describe('#5610 facet B — the seed IS the dirty baseline', () => {
  it('pill goes dirty on an edit and clears once the seed catches up to the buffer', async () => {
    let view: EditorView | null = null
    const props = {
      deploymentId: 'dep',
      kind: 'Blueprint',
      ns: undefined,
      name: 'bp-alloy',
      obj: null,
      commitLabel: 'Commit IaC',
      onCommit: async () => 'Committed to IaC ✓',
      onCreateEditor: (v: EditorView) => {
        view = v
      },
    }
    const { rerender } = render(<YamlEditor {...props} seedYaml={COMMITTED_YAML} />)

    // Untouched: clean, and Commit disabled (nothing to send).
    expect(screen.getByText('• no unsaved edits')).toBeTruthy()
    expect((screen.getByTestId('yaml-editor-apply') as HTMLButtonElement).disabled).toBe(true)

    // The operator edits one line — dirty, Commit enabled. (Non-vacuity: the
    // clean assertion below cannot pass by the pill never having lit up.)
    await waitFor(() => expect(view).not.toBeNull())
    view!.dispatch({ changes: { from: 0, to: view!.state.doc.length, insert: EDITED_YAML } })
    await waitFor(() => expect(screen.getByText('• unsaved changes')).toBeTruthy())
    expect((screen.getByTestId('yaml-editor-apply') as HTMLButtonElement).disabled).toBe(false)

    // The commit lands and the owner re-baselines the seed to those bytes
    // (PART 1). THAT is what clears the pill and disables the button.
    rerender(<YamlEditor {...props} seedYaml={EDITED_YAML} />)
    await waitFor(() => expect(screen.getByText('• no unsaved edits')).toBeTruthy())
    expect((screen.getByTestId('yaml-editor-apply') as HTMLButtonElement).disabled).toBe(true)
  })

  it('a STALE seed keeps the pill lit — the exact reported symptom', async () => {
    // The pre-fix shape: the buffer holds the committed bytes, but the seed
    // was never moved off the pre-commit file. `dirty` can never go false.
    let view: EditorView | null = null
    render(
      <YamlEditor
        deploymentId="dep"
        kind="Blueprint"
        ns={undefined}
        name="bp-alloy"
        obj={null}
        commitLabel="Commit IaC"
        seedYaml={COMMITTED_YAML}
        onCommit={async () => 'Committed to IaC ✓'}
        onCreateEditor={(v) => {
          view = v
        }}
      />,
    )
    await waitFor(() => expect(view).not.toBeNull())
    view!.dispatch({ changes: { from: 0, to: view!.state.doc.length, insert: EDITED_YAML } })

    // No re-baseline → still "unsaved changes", and Commit still enabled,
    // inviting the redundant re-commit the issue calls out.
    await waitFor(() => expect(screen.getByText('• unsaved changes')).toBeTruthy())
    expect((screen.getByTestId('yaml-editor-apply') as HTMLButtonElement).disabled).toBe(false)
  })

  it('whitespace-only drift does not read as unsaved (the baseline is trimmed)', async () => {
    let view: EditorView | null = null
    const props = {
      deploymentId: 'dep',
      kind: 'Blueprint',
      ns: undefined,
      name: 'bp-alloy',
      obj: null,
      commitLabel: 'Commit IaC',
      onCommit: async () => 'ok',
      onCreateEditor: (v: EditorView) => {
        view = v
      },
    }
    render(<YamlEditor {...props} seedYaml={COMMITTED_YAML} />)
    await waitFor(() => expect(view).not.toBeNull())
    view!.dispatch({
      changes: { from: 0, to: view!.state.doc.length, insert: `${COMMITTED_YAML}\n\n` },
    })
    await waitFor(() => expect(screen.getByText('• no unsaved edits')).toBeTruthy())
  })
})
