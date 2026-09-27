/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { createElement, type ReactElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { Field, FormRow, Tabs } from './ui'

// The stylesheet is read from disk: vitest hands an import of a .css file back
// empty, and the look of these two primitives IS the defect that was reported.
const css = readFileSync(fileURLToPath(new URL('../styles.css', import.meta.url)), 'utf8')

/**
 * The two layout primitives every page shares (#6946): the tab strip and the
 * form row. What is pinned is what the founder review named — exactly ONE tab
 * reads as current and it is told apart by more than an underline, a count is
 * a badge, and a form row exists as one thing rather than twelve local
 * approximations of it.
 */

const render = (el: ReactElement): string => renderToString(el).replace(/<!-- -->/g, '')

describe('the tab strip', () => {
  const strip = (current: string) =>
    render(
      createElement(
        MemoryRouter,
        { initialEntries: [`/customers/c1?tab=${current}`] },
        createElement(Tabs, { base: '/customers/c1', tabs: ['Overview', 'Sources', { key: 'regions', label: 'Regions & zones' }], current, counts: { sources: 3, regions: 0 } }),
      ),
    )

  it('marks exactly the current tab — class and aria-current — and no other', () => {
    const html = strip('sources')
    const tabs = html.match(/<a [^>]*>/g) ?? []
    expect(tabs).toHaveLength(3)
    const current = tabs.filter((a) => a.includes('aria-current="page"'))
    expect(current).toHaveLength(1)
    expect(current[0]).toContain('class="tab active"')
    expect(current[0]).toContain('href="/customers/c1?tab=sources"')
    for (const a of tabs.filter((a) => !a.includes('aria-current'))) {
      expect(a).toContain('class="tab"')
      expect(a).not.toContain('active')
    }
  })

  it('follows `current`, never the pathname: every tab shares one path and differs by ?tab=', () => {
    // The pre-#6946 strip was a NavLink, which matches on the pathname and so
    // marked EVERY tab active — the row of underlined links the founder saw.
    for (const key of ['overview', 'sources', 'regions']) {
      const html = strip(key)
      expect(html.match(/aria-current="page"/g)).toHaveLength(1)
      const marked = (html.match(/<a [^>]*aria-current="page"[^>]*>/g) ?? [])[0]
      expect(marked).toContain(`href="/customers/c1?tab=${key}"`)
    }
  })

  it('renders a count as a badge — zero included — and no badge where there is no count', () => {
    const html = strip('overview')
    expect(html).toContain('<span>Sources</span><span class="count">3</span>')
    expect(html).toContain('<span>Regions &amp; zones</span><span class="count">0</span>')
    expect(html).toMatch(/<span>Overview<\/span><\/a>/)
  })

  it('is styled as a strip: a bordered container, the current segment filled, the count a pill', () => {
    expect(css).toMatch(/\.tabs \{[^}]*border: 1px solid var\(--line\)/)
    expect(css).toMatch(/\.tabs \.tab\.active \{[^}]*background: var\(--accent-2\)/)
    expect(css).toMatch(/\.tabs \.tab\.active \{[^}]*color: var\(--accent\)/)
    expect(css).toMatch(/\.tabs \.tab \.count \{[^}]*border-radius: 999px/)
    expect(css).toMatch(/\.tabs \.tab:focus-visible \{[^}]*outline/)
    expect(css).toMatch(/\.tabs \{[^}]*overflow-x: auto/)
    // The underline-only look is gone.
    expect(css).not.toMatch(/\.tabs a\.active/)
  })
})

describe('the form row', () => {
  it('holds a field, a checkbox and a button as one row', () => {
    const html = render(
      createElement(
        FormRow,
        null,
        createElement(Field, { label: 'Code', help: 'as the ledger names it', children: createElement('input', { defaultValue: 'me-east-215' }) }),
        createElement('label', { className: 'check' }, createElement('input', { type: 'checkbox' }), ' default zone'),
        createElement('button', { type: 'submit' }, 'Add'),
      ),
    )
    expect(html).toMatch(/^<div class="form-row ">/)
    expect(html).toMatch(/<div class="field"><label for="[^"]+">Code<\/label><input id="[^"]+" value="me-east-215"\/><div class="help">as the ledger names it<\/div><\/div>/)
    expect(html).toContain('<label class="check"><input type="checkbox"/> default zone</label>')
    expect(html).toContain('<button type="submit">Add</button>')
  })

  it('aligns on ONE baseline in the stylesheet: controls and buttons share a height, help hangs below, and the old inline form is gone', () => {
    expect(css).toMatch(/\.form-row \{[^}]*align-items: flex-end/)
    expect(css).toMatch(/\.form-row \.field input, \.form-row \.field select \{[^}]*height: 32px/)
    expect(css).toMatch(/\.form-row \.check \{[^}]*height: 32px/)
    expect(css).toMatch(/\.form-row > button[^{]*\{[^}]*height: 32px/)
    expect(css).toMatch(/\.form-row \.field \.help, \.form-row \.field \.err \{[^}]*position: absolute/)
    expect(css).not.toMatch(/form\.inline/)
  })
})
