/**
 * CatalogInlineField.verdict.test.tsx — regression lock-in for UAT row 132
 * (#5113 facet-a, issue #5223): the catalog card-save must NEVER close
 * silently. The {stored, committed, reason} envelope the catalyst-api
 * returns (honest since #5115) is surfaced as an in-UI toast:
 *
 *   committed:true  → green "Saved to IaC ✓"
 *   committed:false → amber "Saved to store — IaC commit failed: <reason>"
 *   legacy body     → amber "no IaC verdict reported" (never a fabricated green)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import type { CatalogSaveVerdict } from '@/lib/commerce.api'

const verdictHolder: { value: CatalogSaveVerdict } = {
  value: { slug: 'wordpress', stored: true, committed: true },
}
const saveSpy = vi.fn(() => Promise.resolve(verdictHolder.value))
vi.mock('@/lib/commerce.api', () => ({
  saveCatalogEdit: () => saveSpy(),
}))

import { CatalogInlineField } from './CatalogInlineField'

const CURRENT = {
  name: 'WordPress',
  tagline: 'Blog engine',
  supported_topologies: ['singleton'],
  icon_light: '',
  icon_dark: '',
}

function renderField() {
  return render(
    <CatalogInlineField<string>
      blueprintId="bp-wordpress"
      fieldKey="name"
      label="Display name"
      createSeed={CURRENT}
      editable
      initialDraft={CURRENT.name}
      renderDisplay={() => <span>WordPress</span>}
      renderEditor={(draft, setDraft) => (
        <input
          data-testid="verdict-test-input"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
      )}
      toPatch={(draft) => ({ name: draft })}
      onSaved={() => {}}
    />,
  )
}

async function editAndSave() {
  fireEvent.click(screen.getByTestId('cif-name-edit'))
  fireEvent.change(screen.getByTestId('verdict-test-input'), {
    target: { value: 'WordPress Turnkey' },
  })
  fireEvent.click(screen.getByTestId('cif-name-save'))
  await waitFor(() => expect(saveSpy).toHaveBeenCalled())
}

beforeEach(() => {
  saveSpy.mockClear()
})

afterEach(() => {
  cleanup()
})

describe('CatalogInlineField — save-verdict toast (row 132)', () => {
  it('committed:true → green "Saved to IaC ✓" (no more silent close)', async () => {
    verdictHolder.value = { slug: 'wordpress', stored: true, committed: true }
    renderField()
    await editAndSave()
    const toast = await screen.findByTestId('cif-name-save-verdict')
    expect(toast.textContent).toBe('Saved to IaC ✓')
    expect(toast.getAttribute('data-tone')).toBe('ok')
  })

  it('committed:false → amber store-only verdict carrying the server reason', async () => {
    verdictHolder.value = {
      slug: 'wordpress',
      stored: true,
      committed: false,
      reason: 'gitea commit rejected',
    }
    renderField()
    await editAndSave()
    const toast = await screen.findByTestId('cif-name-save-verdict')
    expect(toast.textContent).toContain('IaC commit failed')
    expect(toast.textContent).toContain('gitea commit rejected')
    expect(toast.getAttribute('data-tone')).toBe('warn')
  })

  it('legacy body (no envelope) → amber "no IaC verdict", never a fabricated green', async () => {
    verdictHolder.value = { slug: 'wordpress', stored: true, committed: null }
    renderField()
    await editAndSave()
    const toast = await screen.findByTestId('cif-name-save-verdict')
    expect(toast.textContent).toContain('no IaC verdict')
    expect(toast.getAttribute('data-tone')).toBe('warn')
  })
})
