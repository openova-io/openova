import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { JobKindDropdown } from './JobKindDropdown'
import { JOB_KINDS } from './jobKinds'

afterEach(cleanup)

const counts = Object.fromEntries(JOB_KINDS.map((k, i) => [k.id, i])) as Record<string, number>

describe('JobKindDropdown', () => {
  it('renders a single <select> with one option per job kind (no chip strip)', () => {
    render(<JobKindDropdown activeKind="install" counts={counts} onChange={() => {}} />)
    const select = screen.getByTestId('jobs-kind-dropdown-select') as HTMLSelectElement
    expect(select.tagName.toLowerCase()).toBe('select')
    expect(select.querySelectorAll('option').length).toBe(JOB_KINDS.length)
    // it is a dropdown, NOT the multi-chip strip
    expect(screen.queryByTestId('jobs-kind-chips')).toBeNull()
  })

  it('shows the engine label + count in each option and reflects the active kind', () => {
    render(<JobKindDropdown activeKind="cron" counts={counts} onChange={() => {}} />)
    const select = screen.getByTestId('jobs-kind-dropdown-select') as HTMLSelectElement
    expect(select.value).toBe('cron')
    const install = JOB_KINDS.find((k) => k.id === 'install')!
    const opt = [...select.options].find((o) => o.value === 'install')!
    expect(opt.textContent).toContain(install.label) // "HelmRelease"
    expect(opt.textContent).toContain(`(${counts.install})`)
  })

  it('fires onChange with the selected kind', () => {
    const onChange = vi.fn()
    render(<JobKindDropdown activeKind="install" counts={counts} onChange={onChange} />)
    fireEvent.change(screen.getByTestId('jobs-kind-dropdown-select'), {
      target: { value: 'cron' },
    })
    expect(onChange).toHaveBeenCalledWith('cron')
  })
})
