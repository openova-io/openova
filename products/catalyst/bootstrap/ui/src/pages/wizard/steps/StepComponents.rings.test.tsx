/**
 * StepComponents.rings.test.tsx — the component step lists the eight kit rings,
 * read-only, from the generated list (tools/guards/cmd/kit-rings), and no entry
 * whose Blueprint has no chart.
 */
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import { StepComponents, RINGS_NOTICE } from './StepComponents'
import { RINGS } from './componentGroups.generated'

// vitest runs from products/catalyst/bootstrap/ui (as AppConsoleRoute.test.tsx reads it).
const ROOT = join(process.cwd(), '..', '..', '..', '..')

// The names that the charts of platform/* and products/* declare.
function chartNames(): Set<string> {
  const names = new Set<string>()
  for (const top of ['platform', 'products']) {
    for (const dir of readdirSync(join(ROOT, top))) {
      const file = join(ROOT, top, dir, 'chart', 'Chart.yaml')
      if (!existsSync(file)) continue
      const m = readFileSync(file, 'utf8').match(/^name:[ \t]*["']?([^"'\s#]+)/m)
      if (m) names.add(m[1])
    }
  }
  return names
}

afterEach(() => cleanup())

describe('StepComponents — the generated kit ring list', () => {
  it('renders the eight rings in their fixed order', () => {
    render(<StepComponents />)
    const sections = within(screen.getByTestId('rings-list')).getAllByRole('region')
    expect(sections.map(s => s.getAttribute('data-testid'))).toEqual([
      'ring-foundation',
      'ring-provider',
      'ring-observability',
      'ring-security-scanning',
      'ring-access',
      'ring-ai',
      'ring-commerce',
      'ring-operations',
    ])
  })

  it('marks foundation and provider required, and no other ring', () => {
    render(<StepComponents />)
    const required = RINGS.filter(r => screen.queryByTestId(`ring-${r.name}-required`)).map(r => r.name)
    expect(required).toEqual(['foundation', 'provider'])
  })

  it('renders every release of the list, and no entry whose Blueprint has no chart', () => {
    render(<StepComponents />)
    const items = screen.getAllByTestId(/^ring-release-/)
    const expected = RINGS.reduce((n, r) => n + r.releases.length, 0)
    expect(expected).toBeGreaterThan(0)
    expect(items).toHaveLength(expected)
    const charts = chartNames()
    const withoutChart = items
      .map(li => (li.getAttribute('data-testid') ?? '').replace(/^ring-release-/, ''))
      .filter(bp => !charts.has(bp))
    expect(withoutChart).toEqual([])
  })

  it('is read-only and says that every ring is installed', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('rings-notice').textContent).toBe(RINGS_NOTICE)
    expect(RINGS_NOTICE).toBe('Every ring is installed. Rings can be switched off after provisioning.')
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0)
    expect(screen.queryAllByRole('switch')).toHaveLength(0)
    expect(screen.queryAllByRole('searchbox')).toHaveLength(0)
  })
})
