/**
 * IconPicker tests — #3668 (founder item §5B, the visual icon-picker half).
 *
 * The founder: "why I am not able to see the already uploaded logos? i need to
 * have profile icon picker like approach." These pin the picker: it renders the
 * vendored logos as a clickable thumbnail grid, previews the current selection,
 * highlights the selected tile, lets the operator pick a tile / type a custom
 * URL / clear — the standard profile-icon-picker affordances.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { IconPicker } from './IconPicker'
import { AVAILABLE_ICONS } from '@/shared/lib/catalogIconGallery'

afterEach(() => cleanup())

describe('IconPicker (#3668 visual gallery)', () => {
  let onChange: ReturnType<typeof vi.fn<(src: string) => void>>

  beforeEach(() => {
    onChange = vi.fn<(src: string) => void>()
  })

  it('renders a clickable thumbnail grid of the vendored logos', () => {
    render(<IconPicker which="light" value="" onChange={onChange} />)
    expect(screen.getByTestId('iconpicker-light-grid')).toBeTruthy()
    // A known vendored logo has a tile.
    expect(screen.getByTestId('iconpicker-light-tile-grafana')).toBeTruthy()
    // The grid renders one tile per available icon.
    const grid = screen.getByTestId('iconpicker-light-grid')
    expect(grid.querySelectorAll('button[role=option]').length).toBe(AVAILABLE_ICONS.length)
  })

  it('picking a tile calls onChange with that icon url', () => {
    const grafana = AVAILABLE_ICONS.find((g) => g.id === 'grafana')!
    render(<IconPicker which="light" value="" onChange={onChange} />)
    fireEvent.click(screen.getByTestId('iconpicker-light-tile-grafana'))
    expect(onChange).toHaveBeenCalledWith(grafana.url)
  })

  it('shows the empty state when no icon is set', () => {
    render(<IconPicker which="dark" value="" onChange={onChange} />)
    expect(screen.getByTestId('iconpicker-dark-preview-empty')).toBeTruthy()
    // No clear button when empty.
    expect(screen.queryByTestId('iconpicker-dark-clear')).toBeNull()
  })

  it('previews the current selection and highlights its tile', () => {
    const grafana = AVAILABLE_ICONS.find((g) => g.id === 'grafana')!
    render(<IconPicker which="light" value={grafana.url} onChange={onChange} />)
    const img = screen.getByTestId('iconpicker-light-preview-img') as HTMLImageElement
    expect(img.getAttribute('src')).toBe(grafana.url)
    // The matching tile is aria-selected.
    const tile = screen.getByTestId('iconpicker-light-tile-grafana')
    expect(tile.getAttribute('aria-selected')).toBe('true')
  })

  it('previews a custom URL value (not in the gallery) as the active selection', () => {
    render(<IconPicker which="light" value="https://cdn/custom.svg" onChange={onChange} />)
    const img = screen.getByTestId('iconpicker-light-preview-img') as HTMLImageElement
    expect(img.getAttribute('src')).toBe('https://cdn/custom.svg')
    // The URL input mirrors the value.
    const url = screen.getByTestId('iconpicker-light-url') as HTMLInputElement
    expect(url.value).toBe('https://cdn/custom.svg')
  })

  it('typing a custom URL calls onChange', () => {
    render(<IconPicker which="dark" value="" onChange={onChange} />)
    fireEvent.change(screen.getByTestId('iconpicker-dark-url'), {
      target: { value: 'https://cdn/x.png' },
    })
    expect(onChange).toHaveBeenCalledWith('https://cdn/x.png')
  })

  it('clear resets the value to empty', () => {
    const grafana = AVAILABLE_ICONS.find((g) => g.id === 'grafana')!
    render(<IconPicker which="light" value={grafana.url} onChange={onChange} />)
    fireEvent.click(screen.getByTestId('iconpicker-light-clear'))
    expect(onChange).toHaveBeenCalledWith('')
  })
})
