import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { beforeEach, describe, expect, it } from 'vitest'
import { noteServerBuild, resetBuilds, setPageBuild } from '../lib/build'
import { BuildNotice } from './BuildNotice'

/**
 * What the user actually sees when their tab has been open across a deploy —
 * and, just as important, what they see when it has not.
 */

beforeEach(() => {
  resetBuilds()
})

const render = () => renderToString(createElement(BuildNotice)).replace(/<!-- -->/g, '')

describe('the stale-build notice', () => {
  it('renders NOTHING while the page and the server agree', () => {
    setPageBuild('0.1.42')
    noteServerBuild('0.1.42')
    expect(render()).toBe('')
  })

  it('renders nothing while either build is unknown', () => {
    setPageBuild('0.1.40')
    expect(render()).toBe('')
    resetBuilds()
    noteServerBuild('0.1.42')
    expect(render()).toBe('')
  })

  it('names both builds and offers a reload once they differ', () => {
    setPageBuild('0.1.40')
    noteServerBuild('0.1.42')
    const html = render()
    expect(html).toContain('This page is out of date.')
    expect(html).toContain('0.1.40')
    expect(html).toContain('0.1.42')
    expect(html).toContain('Reload the page')
    expect(html).toContain('data-stale-build="0.1.40→0.1.42"')
    // It says the form can be finished first — the reason it is a strip and
    // not a modal.
    expect(html).toContain('you can finish what you are typing first')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  })

  it('is a strip, not a modal, and does not fade', () => {
    setPageBuild('0.1.40')
    noteServerBuild('0.1.42')
    const html = render()
    // .modal-back is the console's overlay; a notice that used it would cover
    // the half-typed pool this whole change exists to protect.
    expect(html).not.toContain('modal')
    expect(html).toContain('class="banner build"')
    // Announced, never focus-stealing.
    expect(html).toContain('role="status"')
  })
})
