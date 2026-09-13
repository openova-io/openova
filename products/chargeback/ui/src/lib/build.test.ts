import { beforeEach, describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import { buildSnapshot, buildsDiffer, noteServerBuild, pageBuild, resetBuilds, serverBuild, setPageBuild, staleBuild, subscribeBuild } from './build'
import { failureText } from './useAction'

/**
 * A tab left open across a deploy runs the previous bundle against the new
 * server, and until this existed the only symptom was that a save quietly did
 * nothing — the founder's report on 0.1.42. These tests hold the three
 * properties that make the notice trustworthy:
 *
 *	IT FIRES when the two builds are known and differ.
 *	IT STAYS SILENT otherwise — and "otherwise" includes every kind of NOT
 *	KNOWING, because a notice that cannot be wrong about a matching pair is
 *	the same defect as one that never fires.
 *	A FAILED WRITE while it is firing says WHY, and keeps what the server said.
 */

beforeEach(() => {
  resetBuilds()
})

describe('the stale-build rule', () => {
  it('is true only when both builds are known and they differ', () => {
    expect(buildsDiffer('0.1.40', '0.1.42')).toBe(true)
    // THE VACUITY CASE: the same build must never raise the notice.
    expect(buildsDiffer('0.1.42', '0.1.42')).toBe(false)
    // Neither half alone is enough to accuse anyone of anything.
    expect(buildsDiffer('', '0.1.42')).toBe(false)
    expect(buildsDiffer('0.1.40', '')).toBe(false)
    expect(buildsDiffer('', '')).toBe(false)
  })

  it('knows neither build until it is told', () => {
    // vitest renders in node: there is no document carrying the shell's meta,
    // which is exactly the "unknown" the rule has to survive.
    expect(pageBuild()).toBe('')
    expect(serverBuild()).toBe('')
    expect(staleBuild()).toBe(false)
  })

  it('goes stale when the server moves on, and quiet again when the page catches up', () => {
    setPageBuild('0.1.40')
    noteServerBuild('0.1.40')
    expect(staleBuild()).toBe(false)

    noteServerBuild('0.1.42')
    expect(staleBuild()).toBe(true)
    expect(pageBuild()).toBe('0.1.40')
    expect(serverBuild()).toBe('0.1.42')

    // What a reload does: the new shell stamps the new build.
    setPageBuild('0.1.42')
    expect(staleBuild()).toBe(false)
  })

  it('treats an unstamped shell and an unversioned server as unknown, never as stale', () => {
    // A dev server, or a build older than the stamping, leaves the token in
    // place. It must not produce a notice naming a build called
    // "__CHARGEBACK_BUILD__".
    setPageBuild('__CHARGEBACK_BUILD__')
    noteServerBuild('0.1.42')
    expect(pageBuild()).toBe('')
    expect(staleBuild()).toBe(false)

    // And a server that reports nothing leaves the last thing it did report.
    resetBuilds()
    setPageBuild('0.1.40')
    noteServerBuild(null)
    noteServerBuild('')
    noteServerBuild('   ')
    noteServerBuild(undefined)
    expect(serverBuild()).toBe('')
    expect(staleBuild()).toBe(false)
  })

  it('notifies subscribers when a build changes, and not when a response repeats one', () => {
    let fired = 0
    const stop = subscribeBuild(() => {
      fired++
    })
    setPageBuild('0.1.40')
    const quiet = fired
    noteServerBuild('0.1.42')
    expect(fired).toBe(quiet + 1)
    const snapshot = buildSnapshot()
    // Every subsequent response carries the same header; none of them may
    // re-render the console.
    noteServerBuild('0.1.42')
    noteServerBuild('0.1.42')
    expect(fired).toBe(quiet + 1)
    expect(buildSnapshot()).toBe(snapshot)
    stop()
    noteServerBuild('0.1.43')
    expect(fired).toBe(quiet + 1)
  })
})

describe('a write that fails while the page is stale', () => {
  const refused = new ApiError(400, 'unknown field "machines"', null)

  it('says the server`s message verbatim when the builds match', () => {
    setPageBuild('0.1.42')
    noteServerBuild('0.1.42')
    expect(failureText(refused)).toBe('unknown field "machines"')
  })

  it('names both builds, says to reload, and still carries what the server said', () => {
    setPageBuild('0.1.40')
    noteServerBuild('0.1.42')
    const text = failureText(refused)
    expect(text).toContain('0.1.40')
    expect(text).toContain('0.1.42')
    expect(text).toMatch(/reload/i)
    // NEVER a bare failure: the server's own words survive, because the cause
    // is a guess until somebody reads them.
    expect(text).toContain('unknown field "machines"')
  })
})
