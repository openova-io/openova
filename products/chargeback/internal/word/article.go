// Package word holds tiny text helpers for sentences a user reads. It
// imports nothing, so every package that writes a message can use it.
package word

import "strings"

// Article returns "a" or "an" for a word the caller is about to interpolate.
//
// This exists because several messages hardcoded "a %s" over a closed
// vocabulary that contains vowel-initial members — tax kinds (exempt,
// out_of_state) and contract line kinds (allowance) — and printed "a exempt
// rule charges nothing" at an operator.
//
// A first-letter vowel test is wrong in general English ("an hour", "a
// university"): the rule is about SOUND, not spelling. It is right here
// because the vocabularies it serves are closed, enumerated in code, and
// contain no such word. Widen the vocabulary and this must be revisited
// rather than trusted.
func Article(word string) string {
	w := strings.TrimSpace(strings.ToLower(word))
	if w == "" {
		return "a"
	}
	switch w[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "an"
	}
	return "a"
}
