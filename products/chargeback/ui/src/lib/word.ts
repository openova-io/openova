/**
 * "a" or "an" for a word about to be interpolated into a sentence.
 *
 * Several messages hardcoded "a ${kind}" over closed vocabularies that contain
 * vowel-initial members — tax kinds (exempt, out_of_state) and contract line
 * kinds (allowance) — and showed an operator "A exempt rule charges nothing".
 *
 * A first-letter vowel test is wrong in general English ("an hour", "a
 * university"): the rule is about SOUND, not spelling. It is right here because
 * the vocabularies it serves are closed, enumerated in code, and contain no
 * such word. Widen them and this must be revisited rather than trusted.
 */
export function article(word: string): string {
  const w = (word ?? '').trim().toLowerCase()
  if (!w) return 'a'
  return 'aeiou'.includes(w[0]) ? 'an' : 'a'
}

/** The same, capitalised for the start of a sentence. */
export function Article(word: string): string {
  return article(word) === 'an' ? 'An' : 'A'
}
