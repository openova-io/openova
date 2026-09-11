import { afterEach, describe, expect, it } from 'vitest'
import { DEFAULT_LOCALE, activeLocale, entryOf, keys, locales, registerLocale, resolveLocale, setLocale, t, type Catalogue, type Key, type PluralCategory } from './index'
import { EN } from './locales/en'

/**
 * The locale seam (DESIGN.md §21.3 applied to the screen). These tests hold
 * three properties: English renders what the console always rendered, a
 * SECOND locale is purely additive, and a key or a value that is missing or
 * malformed fails loudly instead of rendering a brace, a blank or a raw key.
 *
 * The tag "zz" is a TEST-ONLY fixture locale, exactly as notify's template
 * tests use. No translation is invented anywhere in this repository.
 */

afterEach(() => {
  setLocale(DEFAULT_LOCALE)
})

describe('the English catalogue is what renders', () => {
  it('defaults to English and looks a plain entry up', () => {
    expect(activeLocale()).toBe(DEFAULT_LOCALE)
    expect(t('common.cancel')).toBe('Cancel')
    expect(t('nav.pricebooks')).toBe('Price books')
  })

  it('interpolates named values', () => {
    expect(t('signin.pinSent', { email: 'ops@acme.example' })).toBe('A PIN was sent to ops@acme.example.')
    expect(t('collections.escalateAfter', { days: 45, action: 'suspend' })).toBe('escalate 45 days after, suspend')
  })

  it('selects the plural form from the count, and prints the formatted number', () => {
    expect(t('table.rows', { count: 1, n: '1' })).toBe('1 row')
    expect(t('table.rows', { count: 0, n: '0' })).toBe('0 rows')
    expect(t('table.rows', { count: 1204, n: '1,204' })).toBe('1,204 rows')
    expect(t('collections.openInvoices', { count: 1 })).toBe('1 open invoice')
    expect(t('collections.openInvoices', { count: 2 })).toBe('2 open invoices')
  })
})

describe('a missing key or value fails loudly', () => {
  // The FIRST gate is the compiler: `Key` is derived from the English
  // catalogue, so `t('nope')` does not build. This is the second gate, for
  // anything that reaches the lookup with the types cast away.
  it('refuses a key no locale has', () => {
    expect(() => (t as (k: string) => string)('nope.not.a.key')).toThrow(/unknown key "nope\.not\.a\.key"/)
  })

  it('refuses an entry rendered without the value it interpolates', () => {
    expect(() => (t as (k: Key) => string)('signin.pinSent')).toThrow(/needs a value for \{email\}/)
  })

  it('refuses a counted entry with no numeric count', () => {
    expect(() => (t as (k: Key, v: object) => string)('table.rows', { n: '3' })).toThrow(/needs a numeric count/)
  })

  it('refuses a locale that declares a key English does not have', () => {
    expect(() => registerLocale('zz', { 'not.a.key': 'x' } as Catalogue)).toThrow(/which the English catalogue does not have/)
  })

  it('refuses an empty entry, a plural entry with no other, and a malformed placeholder', () => {
    expect(() => registerLocale('zz', { 'common.cancel': '' })).toThrow(/empty entry/)
    expect(() => registerLocale('zz', { 'table.rows': { one: '{n} row' } } as Catalogue)).toThrow(/needs a non-empty "other"/)
    expect(() => registerLocale('zz', { 'common.cancel': 'hello {}' })).toThrow(/malformed placeholder/)
    expect(() => registerLocale('zz', { 'common.cancel': 'hello {two words}' })).toThrow(/malformed placeholder/)
    expect(() => registerLocale('', {})).toThrow(/empty locale/)
  })
})

describe('a second locale is purely additive', () => {
  // THE PROPERTY THIS WHOLE MODULE EXISTS FOR: a new language is one new file
  // under locales/, translating as many or as few keys as it has, and every
  // untranslated key falls back to English. Not one line of ./locale, of a
  // component or of a test changes for it — which is what this proves.
  it('registers, renders and falls back key by key', () => {
    const before = locales().length
    registerLocale('zz', { 'common.cancel': 'ZZ cancel', 'signin.pinSent': 'ZZ {email}' })
    expect(locales().length).toBe(before + 1)
    expect(locales()).toContain('zz')

    expect(setLocale('zz')).toBe('zz')
    expect(t('common.cancel')).toBe('ZZ cancel')
    expect(t('signin.pinSent', { email: 'a@b' })).toBe('ZZ a@b')
    // Everything it did not translate is still the English console, not a
    // hole: an operator sees a half-translated product, never a broken one.
    expect(t('nav.pricebooks')).toBe('Price books')
    expect(t('table.rows', { count: 2, n: '2' })).toBe('2 rows')
  })

  it('takes a region tag down to its language, and an unknown tag to English', () => {
    registerLocale('zz', { 'common.cancel': 'ZZ cancel' })
    expect(setLocale('ZZ-OM')).toBe('zz')
    expect(t('common.cancel')).toBe('ZZ cancel')
    expect(setLocale('qq')).toBe(DEFAULT_LOCALE)
    expect(t('common.cancel')).toBe('Cancel')
  })

  it('brings its own plural rules, and falls back to other for a category it did not spell out', () => {
    const select = (n: number): PluralCategory => (n === 0 ? 'zero' : n === 1 ? 'one' : n < 5 ? 'few' : 'many')
    registerLocale('zz', { 'collections.openInvoices': { zero: 'ZZ none', one: 'ZZ one', few: 'ZZ few {count}', other: 'ZZ other {count}' } }, select)
    setLocale('zz')
    expect(t('collections.openInvoices', { count: 0 })).toBe('ZZ none')
    expect(t('collections.openInvoices', { count: 1 })).toBe('ZZ one')
    expect(t('collections.openInvoices', { count: 3 })).toBe('ZZ few 3')
    // `many` is not spelled out, so `other` carries it.
    expect(t('collections.openInvoices', { count: 9 })).toBe('ZZ other 9')
  })

  it('resolves to English while English is the only locale shipped', () => {
    // Under node there is no navigator and no document, which is the same
    // answer the browser gives when nothing asked for a locale we have.
    expect(resolveLocale()).toBe(DEFAULT_LOCALE)
  })
})

describe('the catalogue itself is well formed', () => {
  it('has no empty entry, every plural entry has other, and every placeholder is a name', () => {
    for (const key of keys()) {
      const entry = entryOf(DEFAULT_LOCALE, key)
      expect(entry, key).toBeDefined()
      const texts = typeof entry === 'string' ? [entry] : Object.values(entry ?? {})
      expect(texts.length, key).toBeGreaterThan(0)
      if (typeof entry !== 'string') expect(typeof entry?.other, key).toBe('string')
      for (const text of texts) {
        expect(text, key).not.toBe('')
        // Anything brace-wrapped that is not `{name}` would be rendered
        // literally — the silent failure the seam exists to refuse.
        expect(text.replace(/\{[A-Za-z_][A-Za-z0-9_]*\}/g, ''), key).not.toMatch(/\{[^{}]*\}/)
      }
    }
  })

  it('keeps every key of a plural entry counting on the same values', () => {
    for (const key of keys()) {
      const entry = entryOf(DEFAULT_LOCALE, key)
      if (typeof entry === 'string' || !entry) continue
      const names = (text: string) => [...text.matchAll(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g)].map((m) => m[1]).sort().join(',')
      const forms = Object.values(entry).map(names)
      for (const f of forms) expect(f, key).toBe(forms[0])
    }
  })
})

/**
 * The SOURCE SCAN — notify's TestDeclaredFieldsAreUsedOrDeliberatelyNot, one
 * layer up. A key nobody reads is copy the product no longer shows and a
 * translator would be paid to translate for nothing; a key a component reads
 * that the catalogue does not declare would throw in front of an operator.
 *
 * It is deliberately a scan of the SOURCE TEXT rather than anything cleverer:
 * it has one job, which is to hold the catalogue and the components together,
 * and a scan that over-reports fails loudly rather than passing quietly.
 * `?raw` is how the files are read — no filesystem, so it runs wherever the
 * rest of the suite runs.
 */
const SOURCES: Record<string, string> = import.meta.glob('../**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true })

/**
 * Everything OUTSIDE src/i18n — the catalogue may not be its own witness, and
 * neither may this file. A sibling of this one globs as './x'; everything
 * else in src/ globs as '../<dir>/x'.
 */
const CONSUMERS = Object.entries(SOURCES).filter(([path]) => path.startsWith('../'))

describe('the catalogue and the console agree', () => {
  it('reads more than a handful of files, so a silent glob cannot pass this', () => {
    expect(CONSUMERS.length).toBeGreaterThan(50)
    expect(Object.keys(EN).length).toBeGreaterThan(50)
  })

  it('has no key the console never reads', () => {
    const text = CONSUMERS.map(([, src]) => src).join('\n')
    const unused = keys().filter((key) => !text.includes(`'${key}'`) && !text.includes(`"${key}"`))
    expect(unused).toEqual([])
  })

  it('has every key the console reads', () => {
    const declared = new Set<string>(keys())
    const missing = new Set<string>()
    for (const [path, src] of CONSUMERS) {
      for (const m of src.matchAll(/\bt\(\s*'([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9]+)+)'/g)) {
        if (!declared.has(m[1] as string)) missing.add(`${path}: ${m[1]}`)
      }
    }
    expect([...missing]).toEqual([])
  })
})
