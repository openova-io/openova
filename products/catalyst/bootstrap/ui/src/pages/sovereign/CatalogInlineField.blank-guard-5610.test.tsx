/**
 * CatalogInlineField.blank-guard-5610.test.tsx — #5610 facet A, the editor half.
 *
 * The reported defect: the summary inline editor opened EMPTY over a non-empty
 * summary. An empty box is one careless Save away from destroying the stored
 * value — and that save really does land (see
 * `commerce.api.blank-guard-5610.test.ts`, which pins the wire behaviour).
 *
 * Two independent properties are pinned here, deliberately not one:
 *
 *   1. PRE-FILL + RE-SYNC — the editor opens on the current value, and the
 *      draft tracks that value when it arrives or changes AFTER mount. The
 *      original bug is the classic stale-initial-state trap: `useState(prop)`
 *      captures the first value forever. Pinning only "opens pre-filled on a
 *      value that was already there at mount" would miss the async case
 *      entirely.
 *
 *   2. THE SEATBELT — a save that would blank a non-empty stored value is
 *      refused and confirmed, not silently performed. This must hold
 *      INDEPENDENTLY of (1): if the pre-fill regresses, the seatbelt is the
 *      only thing between a blank box and data loss.
 *
 * And the direction that keeps the fix honest: deliberately clearing a field
 * must remain possible. A guard that satisfies (2) by making clearing
 * impossible has broken the feature, not fixed it.
 *
 * These exercise CatalogInlineField directly (not through CatalogDetail) so
 * the editor's own contract is what is asserted, with the commerce seam
 * mocked at the module boundary.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { useState } from 'react'
import type { BlankedField, CatalogSaveVerdict } from '@/lib/commerce.api'

/** The mocked commerce seam. Default: an ordinary durable save. */
const saveSpy = vi.fn(
  (
    slug: string,
    patch: Record<string, unknown>,
    seed: Record<string, unknown> | undefined,
    opts: { allowBlank?: readonly string[] } | undefined,
  ): Promise<CatalogSaveVerdict> => {
    void slug
    void patch
    void seed
    void opts
    return Promise.resolve({ slug: 'wordpress', stored: true, committed: true })
  },
)
vi.mock('@/lib/commerce.api', () => ({
  saveCatalogEdit: (
    slug: string,
    patch: Record<string, unknown>,
    seed: Record<string, unknown> | undefined,
    opts: { allowBlank?: readonly string[] } | undefined,
  ) => saveSpy(slug, patch, seed, opts),
}))

import { CatalogInlineField } from './CatalogInlineField'

const SEED = {
  name: 'WordPress',
  tagline: 'Website and blog platform',
  supported_topologies: ['singleton'],
  icon_light: '',
  icon_dark: '',
}

/**
 * Renders the summary field with an EXTERNALLY controllable current value, so
 * a test can make the value arrive after mount the way an async catalog fetch
 * does. `onValue` hands the setter back to the test.
 */
function renderSummaryField(initialValue: string, onValue: (set: (v: string) => void) => void) {
  function Harness() {
    const [value, setValue] = useState(initialValue)
    onValue(setValue)
    return (
      <CatalogInlineField<string>
        blueprintId="bp-wordpress"
        fieldKey="summary"
        label="Summary"
        createSeed={SEED}
        editable
        initialDraft={value}
        renderDisplay={() => <span data-testid="summary-display">{value}</span>}
        renderEditor={(draft, setDraft) => (
          <textarea
            data-testid="cif-summary-input"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
        )}
        toPatch={(draft) => ({ tagline: draft.trim() })}
        onSaved={() => {}}
      />
    )
  }
  return render(<Harness />)
}

/** Make the next save answer with a blank-write refusal for `fields`. */
function refuseOnce(fields: BlankedField[]) {
  saveSpy.mockImplementationOnce((_s, _p, _seed, opts) => {
    const confirmed = opts?.allowBlank ?? []
    const still = fields.filter((f) => !confirmed.includes(f.key))
    if (still.length > 0) {
      return Promise.resolve({
        slug: 'wordpress',
        stored: false,
        committed: false,
        blanked: still,
      })
    }
    return Promise.resolve({ slug: 'wordpress', stored: true, committed: true })
  })
}

beforeEach(() => {
  saveSpy.mockReset()
  saveSpy.mockImplementation(() =>
    Promise.resolve({ slug: 'wordpress', stored: true, committed: true }),
  )
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('#5610 — the inline editor opens on the CURRENT value (facet A)', () => {
  it('opens PRE-FILLED with the value present at mount', () => {
    renderSummaryField('Website and blog platform', () => {})
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    const input = screen.getByTestId('cif-summary-input') as HTMLTextAreaElement
    expect(input.value).toBe('Website and blog platform')
  })

  it('a LATE-ARRIVING value re-syncs into the editor (the stale-initial-state trap)', () => {
    // Mount with nothing — the shape of a detail page whose catalog fetch has
    // not resolved yet. `useState(initialDraft)` captures "" for the lifetime
    // of the component, so the ONLY thing that gets a later value in front of
    // the operator is `open()` re-seeding from the current prop. This holds on
    // the pre-fix tree too; it is pinned because it is an easily-dropped habit
    // of one function and dropping it lands straight back on a blank editor
    // over non-empty content.
    let setValue: (v: string) => void = () => {}
    renderSummaryField('', (s) => {
      setValue = s
    })
    // Non-vacuity: it really is empty before the value lands.
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    expect((screen.getByTestId('cif-summary-input') as HTMLTextAreaElement).value).toBe('')
    fireEvent.click(screen.getByTestId('cif-summary-cancel'))

    // The value arrives after mount …
    act(() => setValue('Website and blog platform'))

    // … and the editor now opens on it.
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    expect((screen.getByTestId('cif-summary-input') as HTMLTextAreaElement).value).toBe(
      'Website and blog platform',
    )
  })

  it('a value arriving while the operator is TYPING never clobbers the draft', async () => {
    // The re-sync must not become a different data-loss bug.
    let setValue: (v: string) => void = () => {}
    renderSummaryField('Old summary', (s) => {
      setValue = s
    })
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    const input = screen.getByTestId('cif-summary-input') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: 'Half-typed replacement' } })
    setValue('A background refetch landed')
    await waitFor(() =>
      expect((screen.getByTestId('cif-summary-input') as HTMLTextAreaElement).value).toBe(
        'Half-typed replacement',
      ),
    )
  })
})

describe('#5610 — the blank-write seatbelt (independent of the pre-fill)', () => {
  it('a save that would blank a non-empty stored summary asks first — and says what is at stake', async () => {
    refuseOnce([{ key: 'tagline', current: 'Website and blog platform' }])
    renderSummaryField('Website and blog platform', () => {})
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), { target: { value: '' } })
    fireEvent.click(screen.getByTestId('cif-summary-save'))

    // The confirmation is raised…
    await screen.findByTestId('cif-summary-blank-confirm')
    expect(screen.getByTestId('cif-summary-blank-current').textContent).toBe(
      'Website and blog platform',
    )
    // …the editor stayed open on the operator's draft…
    expect(screen.getByTestId('cif-summary-input')).toBeTruthy()
    // …and no durable-save toast was fabricated.
    expect(screen.queryByTestId('cif-summary-save-verdict')).toBeNull()
    // The first attempt went out WITHOUT a blanket allowBlank — the refusal is
    // the API's to make, not something the editor opted out of.
    expect(saveSpy).toHaveBeenCalledTimes(1)
    expect(saveSpy.mock.calls[0][3]).toEqual({ allowBlank: [] })
  })

  it('backing out of the confirmation leaves the value alone — no second save', async () => {
    refuseOnce([{ key: 'tagline', current: 'Website and blog platform' }])
    renderSummaryField('Website and blog platform', () => {})
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), { target: { value: '' } })
    fireEvent.click(screen.getByTestId('cif-summary-save'))
    await screen.findByTestId('cif-summary-blank-confirm')

    fireEvent.click(screen.getByTestId('cif-summary-blank-keep'))
    await waitFor(() => expect(screen.queryByTestId('cif-summary-blank-confirm')).toBeNull())
    expect(saveSpy).toHaveBeenCalledTimes(1) // still just the refused attempt
  })

  it('the seatbelt holds even when the editor opened BLANK (pre-fill regression)', async () => {
    // The exact #5610 shape: the editor believes the current value is empty,
    // so it cannot know a save is destructive. The refusal comes from the
    // stored row, so the operator is still asked.
    refuseOnce([{ key: 'tagline', current: 'Website and blog platform' }])
    renderSummaryField('', () => {})
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    expect((screen.getByTestId('cif-summary-input') as HTMLTextAreaElement).value).toBe('')
    fireEvent.click(screen.getByTestId('cif-summary-save'))
    await screen.findByTestId('cif-summary-blank-confirm')
    expect(screen.getByTestId('cif-summary-blank-current').textContent).toBe(
      'Website and blog platform',
    )
  })

  /* ── The direction that keeps the fix honest ─────────────────────────── */

  it('NEGATIVE CASE — a deliberate clear still goes through on confirmation', async () => {
    // If this fails, the seatbelt is a blanket block and clearing a field the
    // product says is clearable has been broken.
    refuseOnce([{ key: 'tagline', current: 'Website and blog platform' }])
    renderSummaryField('Website and blog platform', () => {})
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), { target: { value: '' } })
    fireEvent.click(screen.getByTestId('cif-summary-save'))
    await screen.findByTestId('cif-summary-blank-confirm')

    fireEvent.click(screen.getByTestId('cif-summary-blank-confirm-clear'))

    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(2))
    // The confirmed retry carries the explicit opt-in for THAT column…
    expect(saveSpy.mock.calls[1][3]).toEqual({ allowBlank: ['tagline'] })
    // …the empty value is genuinely what is being written…
    expect(saveSpy.mock.calls[1][1]).toEqual({ tagline: '' })
    // …and the editor closes with a real verdict, as any successful save does.
    await waitFor(() => expect(screen.queryByTestId('cif-summary-input')).toBeNull())
    expect((await screen.findByTestId('cif-summary-save-verdict')).getAttribute('data-tone')).toBe(
      'ok',
    )
  })

  it('NEGATIVE CASE — an ordinary non-empty edit saves on ONE click, no confirmation', async () => {
    renderSummaryField('Website and blog platform', () => {})
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), {
      target: { value: 'Website, blog and store platform' },
    })
    fireEvent.click(screen.getByTestId('cif-summary-save'))

    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    expect(screen.queryByTestId('cif-summary-blank-confirm')).toBeNull()
    expect(saveSpy.mock.calls[0][1]).toEqual({ tagline: 'Website, blog and store platform' })
    await waitFor(() => expect(screen.queryByTestId('cif-summary-input')).toBeNull())
  })
})
