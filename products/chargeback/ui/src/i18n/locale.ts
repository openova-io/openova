import { EN, type EnglishCatalogue } from './locales/en'

/**
 * The console's LOCALE SEAM (DESIGN.md §21.3, the same idea one layer up).
 *
 * Before this module every string the console showed was inline JSX. There
 * was no list of what the product says, no way to render it in another
 * language, and no way to tell a copy change from a code change. §21 already
 * gave the notification templates a locale key and shipped the English
 * catalogue; this is that shape applied to the screen, so the product has ONE
 * idea of a locale rather than two:
 *
 *	KEY        a stable identifier                    (locales/en.ts)
 *	ENTRY      one string, or one per plural category (locales/en.ts)
 *	CATALOGUE  every entry of one locale              (locales/*.ts)
 *	LOOKUP     key + named values -> text             (t, below)
 *	FALLBACK   a locale without an entry uses English (resolveEntry, below)
 *
 * ENGLISH ONLY is shipped, by direction. No translation is invented here.
 *
 * A SECOND LOCALE IS ONE NEW FILE under locales/ and NOT ONE LINE of code:
 * index.ts discovers every locale module, this module registers it, and every
 * key it does not translate falls back to English. i18n.test.ts proves that
 * with a test-only fixture tag, exactly as §21 does with "zz".
 *
 * WHY A MODULE-LEVEL t AND NOT A REACT CONTEXT: a context would make every
 * component that shows a word depend on a provider, which is a change to
 * every render path and every test that renders one. The active locale is a
 * single value resolved once at start-up (main.tsx), so a function reading it
 * is enough, and a component that renders after a locale change re-reads it.
 */

/** The locale the console renders in when nothing says otherwise. */
export const DEFAULT_LOCALE = 'en'

/**
 * The CLDR plural categories. English uses two of them; a locale that needs
 * more supplies the ones it needs and its own selector, which is the whole
 * reason the category set is the CLDR one rather than "singular/plural".
 */
export type PluralCategory = 'zero' | 'one' | 'two' | 'few' | 'many' | 'other'

/**
 * A count-dependent entry. `other` is REQUIRED: it is what a category the
 * locale did not spell out falls back to, so a plural entry can never render
 * nothing.
 */
export type PluralEntry = { readonly other: string } & { readonly [C in Exclude<PluralCategory, 'other'>]?: string }

/** One catalogue entry: a plain string, or one string per plural category. */
export type Entry = string | PluralEntry

/** One locale's entries. A second locale may translate as few keys as it has. */
export type Catalogue = Readonly<Partial<Record<Key, Entry>>>

/** Which plural form a count takes. English: one for ±1, other for the rest. */
export type PluralSelect = (n: number) => PluralCategory

/**
 * Every key in the catalogue. It is derived from ENGLISH, which is what makes
 * an unknown key a TYPE ERROR at the call site — `npx tsc -b --noEmit` is the
 * first gate a missing key hits, long before a test and far before a reader.
 */
export type Key = keyof EnglishCatalogue & string

/**
 * The named values an entry interpolates, read off the English text itself:
 * `'{count} of {total}'` needs `count` and `total`, and a call site that
 * forgets one does not compile. A plural entry always needs a numeric
 * `count`, which is what selects the category.
 */
type NamesIn<S extends string> = S extends `${string}{${infer P}}${infer Rest}` ? P | NamesIn<Rest> : never
type NamesOf<E> = E extends string ? NamesIn<E> : 'count' | { [C in keyof E]-?: NamesIn<E[C] & string> }[keyof E]

export type Values<K extends Key> = { [N in NamesOf<EnglishCatalogue[K]>]: N extends 'count' ? number : string | number }

/** No values at all when the entry names none; the values object otherwise. */
type Args<K extends Key> = [NamesOf<EnglishCatalogue[K]>] extends [never] ? [] : [values: Values<K>]

interface Registered {
  readonly entries: Catalogue
  readonly select: PluralSelect
}

const registry = new Map<string, Registered>()

/**
 * `{name}` is the WHOLE placeholder syntax. A brace is therefore reserved:
 * registerLocale refuses any other brace-wrapped run rather than letting it
 * render literally, which is how "{ email }" would have reached a reader.
 */
const PLACEHOLDER = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g

function normalise(tag: string): string {
  return tag.trim().toLowerCase().replace(/_/g, '-')
}

/** English: one for a count of ±1, other for everything else. */
export const englishPlural: PluralSelect = (n) => (n === 1 || n === -1 ? 'one' : 'other')

/**
 * Adds one locale's entries. It THROWS on an unknown key, a plural entry with
 * no `other`, an empty entry or a malformed placeholder: every one of them is
 * a programming error, and a catalogue that fails to load is a test that goes
 * red rather than a screen that renders a brace. It is the same bargain
 * notify.RegisterLocale makes by panicking.
 *
 * A new language is exactly one call to this, from one new file under
 * locales/, made for it by index.ts.
 */
export function registerLocale(locale: string, entries: Catalogue, select: PluralSelect = englishPlural): void {
  const tag = normalise(locale)
  if (!tag) throw new Error('i18n: registerLocale with an empty locale')
  for (const key of Object.keys(entries).sort()) {
    if (!(key in EN)) throw new Error(`i18n: locale "${tag}" declares "${key}", which the English catalogue does not have`)
    const entry = entries[key as Key] as Entry
    if (typeof entry === 'string') {
      if (entry === '') throw new Error(`i18n: locale "${tag}", key "${key}": empty entry`)
    } else if (typeof entry !== 'object' || entry === null) {
      throw new Error(`i18n: locale "${tag}", key "${key}": entry is neither a string nor a plural entry`)
    } else if (typeof entry.other !== 'string' || entry.other === '') {
      throw new Error(`i18n: locale "${tag}", key "${key}": a plural entry needs a non-empty "other"`)
    }
    // A placeholder that is not `{name}` would be rendered literally, which
    // is the silent failure this whole module exists to refuse.
    for (const text of typeof entry === 'string' ? [entry] : Object.values(entry)) {
      if (typeof text !== 'string') throw new Error(`i18n: locale "${tag}", key "${key}": a plural category is not a string`)
      const stray = text.replace(PLACEHOLDER, '').match(/\{[^{}]*\}/)
      if (stray) throw new Error(`i18n: locale "${tag}", key "${key}": malformed placeholder ${stray[0]}`)
    }
  }
  registry.set(tag, { entries, select })
}

registerLocale(DEFAULT_LOCALE, EN, englishPlural)

/** Every registered locale, sorted. */
export function locales(): string[] {
  return [...registry.keys()].sort()
}

let active = DEFAULT_LOCALE

/** The locale the console is rendering in. */
export function activeLocale(): string {
  return active
}

/** The registered locale a tag asks for: exact, then its primary subtag. */
function match(tag: string): string | null {
  const want = normalise(tag)
  if (!want) return null
  if (registry.has(want)) return want
  const primary = want.split('-')[0] as string
  return registry.has(primary) ? primary : null
}

/**
 * Sets the active locale, and tells the document which language it is in so a
 * screen reader and the browser agree with the screen. An unregistered tag
 * falls back to English rather than leaving the console half-translated.
 * Returns the locale that is now active.
 */
export function setLocale(tag: string): string {
  active = match(tag) ?? DEFAULT_LOCALE
  if (typeof document !== 'undefined' && document.documentElement) document.documentElement.lang = active
  return active
}

/**
 * THE ONE PLACE the active locale comes from: what the reader's browser asks
 * for, else what the document declares, else English. Only a REGISTERED
 * locale can win, so shipping English alone means English is what renders —
 * a reader with an Arabic browser sees the English console until an Arabic
 * catalogue exists, rather than a page of missing keys.
 */
export function resolveLocale(): string {
  const asked: string[] = []
  if (typeof navigator !== 'undefined') {
    for (const tag of navigator.languages ?? []) asked.push(tag)
    if (navigator.language) asked.push(navigator.language)
  }
  if (typeof document !== 'undefined' && document.documentElement?.lang) asked.push(document.documentElement.lang)
  for (const tag of asked) {
    const found = match(tag)
    if (found) return found
  }
  return DEFAULT_LOCALE
}

/** Resolves the active locale once, at start-up. Called from main.tsx. */
export function initLocale(): string {
  return setLocale(resolveLocale())
}

/**
 * The entry for a key, and the plural selector to read it with. A locale that
 * has not translated a key falls back to English — never to the raw key and
 * never to nothing — and the selector used belongs to the locale that
 * supplied the text, which is the only selector that can be right for it.
 */
function resolveEntry(key: string): { entry: Entry; select: PluralSelect } {
  const here = registry.get(active)
  const mine = here?.entries[key as Key]
  if (mine !== undefined) return { entry: mine, select: here?.select ?? englishPlural }
  const english = registry.get(DEFAULT_LOCALE)
  const fallback = english?.entries[key as Key]
  if (fallback !== undefined) return { entry: fallback, select: english?.select ?? englishPlural }
  throw new Error(`i18n: unknown key "${key}"`)
}

function interpolate(text: string, key: string, values: Record<string, unknown> | undefined): string {
  return text.replace(PLACEHOLDER, (_, name: string) => {
    const v = values?.[name]
    if (v === undefined || v === null) throw new Error(`i18n: key "${key}" needs a value for {${name}}`)
    return String(v)
  })
}

/**
 * The lookup. `t('nav.customers')` for a plain string; `t('table.rows', {
 * count, n })` for one that interpolates or counts.
 *
 * It THROWS on a key no locale has and on a missing value, because both are
 * defects the type system already refuses at the call site — reaching them at
 * runtime means something bypassed it, and a blank where a sentence belongs
 * is the outcome worth preventing.
 */
export function t<K extends Key>(key: K, ...args: Args<K>): string {
  return lookup(key, args[0] as Record<string, unknown> | undefined)
}

function lookup(key: string, values: Record<string, unknown> | undefined): string {
  const { entry, select } = resolveEntry(key)
  if (typeof entry === 'string') return interpolate(entry, key, values)
  const count = values?.count
  if (typeof count !== 'number' || !Number.isFinite(count)) throw new Error(`i18n: key "${key}" counts, and needs a numeric count`)
  const text = entry[select(count)] ?? entry.other
  return interpolate(text, key, values)
}

/** Every key the shipped catalogue declares, sorted — what a test iterates. */
export function keys(): Key[] {
  return (Object.keys(EN) as Key[]).sort()
}

/** One entry of one locale, for a test that reads a catalogue directly. */
export function entryOf(locale: string, key: string): Entry | undefined {
  return registry.get(normalise(locale))?.entries[key as Key]
}
