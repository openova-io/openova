import { registerLocale, type Catalogue, type PluralSelect } from './locale'

/**
 * The i18n entry point. Import `t` from here, never from ./locale directly:
 * this is the module that loads the locale catalogues, and a component that
 * bypassed it would render whichever locales happened to be loaded already.
 *
 * A SECOND LOCALE IS A NEW FILE AND NO CODE CHANGE. Drop
 * `src/i18n/locales/ar.ts` next to `en.ts`, exporting `locale` and `entries`
 * (and its own `select` if its plural rules are not English's), and the glob
 * below finds it. Nothing here, in ./locale, in a component or in a test has
 * to learn about it.
 *
 * ENGLISH is not discovered this way: ./locale imports it directly, because
 * English is the reference catalogue every key is typed from and the fallback
 * every other locale leans on. It cannot be optional, so it is not optional.
 */

/** What a locale module under locales/ exports, English excepted. */
interface LocaleModule {
  locale?: unknown
  entries?: unknown
  select?: unknown
}

for (const mod of Object.values(import.meta.glob<LocaleModule>('./locales/*.ts', { eager: true }))) {
  if (typeof mod.locale !== 'string' || typeof mod.entries !== 'object' || mod.entries === null) continue
  registerLocale(mod.locale, mod.entries as Catalogue, typeof mod.select === 'function' ? (mod.select as PluralSelect) : undefined)
}

export { DEFAULT_LOCALE, activeLocale, englishPlural, entryOf, initLocale, keys, locales, registerLocale, resolveLocale, setLocale, t } from './locale'
export type { Catalogue, Entry, Key, PluralCategory, PluralEntry, PluralSelect, Values } from './locale'
