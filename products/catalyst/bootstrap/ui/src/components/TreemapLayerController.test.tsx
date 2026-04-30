/**
 * TreemapLayerController.test.tsx — toolbar behaviour lock-in.
 *
 * Coverage:
 *   1. Renders Size + Color + Layer 1 selects with default values.
 *   2. Add layer button appends a layer (capped at MAX_LAYERS).
 *   3. Remove layer button removes the last layer (floor MIN_LAYERS).
 *   4. Each layer select excludes dimensions taken by other layers.
 *   5. Picking a capacity size metric forces colorBy → utilization
 *      and disables the colour select.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { useState } from 'react'
import {
  TreemapLayerController,
  MAX_LAYERS,
  MIN_LAYERS,
} from './TreemapLayerController'
import type {
  TreemapColorBy,
  TreemapDimension,
  TreemapSizeBy,
} from '@/lib/treemap.types'

interface HarnessProps {
  initialLayers?: TreemapDimension[]
  initialColorBy?: TreemapColorBy
  initialSizeBy?: TreemapSizeBy
}

function Harness({
  initialLayers = ['family'],
  initialColorBy = 'utilization',
  initialSizeBy = 'replica_count',
}: HarnessProps) {
  const [layers, setLayers] = useState<readonly TreemapDimension[]>(initialLayers)
  const [colorBy, setColorBy] = useState<TreemapColorBy>(initialColorBy)
  const [sizeBy, setSizeBy] = useState<TreemapSizeBy>(initialSizeBy)
  return (
    <TreemapLayerController
      layers={layers}
      setLayers={setLayers}
      colorBy={colorBy}
      setColorBy={setColorBy}
      sizeBy={sizeBy}
      setSizeBy={setSizeBy}
    />
  )
}

afterEach(() => cleanup())

describe('TreemapLayerController — initial render', () => {
  it('renders Size, Color, Layer 1 selects', () => {
    render(<Harness />)
    expect(screen.getByTestId('treemap-size-select')).toBeTruthy()
    expect(screen.getByTestId('treemap-color-select')).toBeTruthy()
    expect(screen.getByTestId('treemap-layer-0-select')).toBeTruthy()
  })

  it('Add layer button is enabled, Remove is disabled at MIN_LAYERS', () => {
    render(<Harness />)
    const add = screen.getByTestId('treemap-add-layer') as HTMLButtonElement
    const remove = screen.getByTestId('treemap-remove-layer') as HTMLButtonElement
    expect(add.disabled).toBe(false)
    expect(remove.disabled).toBe(true)
  })
})

describe('TreemapLayerController — add / remove layers', () => {
  it('adding a layer appends a select for layer 2', () => {
    render(<Harness />)
    const add = screen.getByTestId('treemap-add-layer')
    fireEvent.click(add)
    expect(screen.getByTestId('treemap-layer-1-select')).toBeTruthy()
  })

  it('cannot exceed MAX_LAYERS', () => {
    render(<Harness initialLayers={['sovereign', 'cluster', 'family', 'application']} />)
    expect(MAX_LAYERS).toBe(4)
    const add = screen.getByTestId('treemap-add-layer') as HTMLButtonElement
    expect(add.disabled).toBe(true)
  })

  it('removing brings the layer count back to MIN_LAYERS minimum', () => {
    render(<Harness initialLayers={['family', 'application']} />)
    const remove = screen.getByTestId('treemap-remove-layer')
    fireEvent.click(remove)
    expect(MIN_LAYERS).toBe(1)
    expect(screen.queryByTestId('treemap-layer-1-select')).toBeNull()
  })
})

describe('TreemapLayerController — capacity auto-lock', () => {
  it('disables the colour select when sizing by cpu_limit', () => {
    render(<Harness initialSizeBy="cpu_limit" />)
    const colour = screen.getByTestId('treemap-color-select') as HTMLSelectElement
    expect(colour.disabled).toBe(true)
    expect(colour.value).toBe('utilization')
  })

  it('flipping to a capacity metric forces utilisation', () => {
    render(<Harness initialSizeBy="replica_count" initialColorBy="health" />)
    const size = screen.getByTestId('treemap-size-select') as HTMLSelectElement
    const colour = screen.getByTestId('treemap-color-select') as HTMLSelectElement
    expect(colour.value).toBe('health')
    fireEvent.change(size, { target: { value: 'memory_limit' } })
    // Colour state updates synchronously through the harness.
    expect(colour.value).toBe('utilization')
    expect(colour.disabled).toBe(true)
  })

  it('keeps colour select enabled for replica_count', () => {
    render(<Harness initialSizeBy="replica_count" initialColorBy="health" />)
    const colour = screen.getByTestId('treemap-color-select') as HTMLSelectElement
    expect(colour.disabled).toBe(false)
  })
})

describe('TreemapLayerController — dimension exclusion', () => {
  it('layer 2 select hides dimensions already picked in layer 1', () => {
    render(<Harness initialLayers={['family', 'application']} />)
    const layer2 = screen.getByTestId('treemap-layer-1-select') as HTMLSelectElement
    const values = Array.from(layer2.querySelectorAll('option')).map((o) => (o as HTMLOptionElement).value)
    // 'family' is already picked in layer 0; layer 2's options should
    // include 'application' (current value) but not 'family'.
    expect(values).toContain('application')
    expect(values).not.toContain('family')
  })
})
