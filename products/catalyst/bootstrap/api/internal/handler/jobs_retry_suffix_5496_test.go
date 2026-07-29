// Tests for #5496 (Refs #4845, UAT 176) — Re-run returned 422 on a genuinely
// failed row because the resolver matched exact names only.
//
// The Jobs page passes a reconciler-row name (`cutover-harbor-prewarm`) while
// the controller names the real Job with a generated numeric suffix
// (`cutover-harbor-prewarm-1785348872`, observed live on hw291). No exact
// match meant ns == "", wrapped in errNotDirectlyRetryable → graceful 422.
// #4845 turned a raw 502 into that 422; it never made suffixed Jobs resolvable.
//
// The suffix rule is deliberately NARROW, and the bp-velero case below is the
// reason: a bare prefix match is the #5485 defect in a new place, where
// `bp-velero` matched `bp-velero-hcs` and the drill-in served 100% wrong-object
// logs. Anything looser than `<name>-<digits>` reintroduces it.
//
// Vacuity: the suite asserts real positives AND real negatives, so neither a
// match-everything nor a match-nothing implementation can pass.

package handler

import "testing"

func TestHasGeneratedNumericSuffix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cand string
		base string
		want bool
	}{
		// The live hw291 shapes — these fail against exact-match-only code.
		{"cutover job with unix-ish suffix", "cutover-harbor-prewarm-1785348872", "cutover-harbor-prewarm", true},
		{"gitea mirror", "cutover-gitea-mirror-1785356082", "cutover-gitea-mirror", true},
		{"single digit", "some-job-7", "some-job", true},

		// THE #5485 COLLISION — the case that must never match.
		{"bp-velero must not match bp-velero-hcs", "bp-velero-hcs", "bp-velero", false},
		{"bp-cnpg must not match bp-cnpg-pair", "bp-cnpg-pair", "bp-cnpg", false},

		// Other non-digit suffixes.
		{"alphanumeric suffix", "some-job-7a", "some-job", false},
		{"hash-like suffix", "admin-865b6dd6c7", "admin", false},
		{"nested name, digits deeper", "some-job-extra-1", "some-job", false},

		// Boundary shapes.
		{"exact name is not a generated suffix", "some-job", "some-job", false},
		{"trailing dash with no digits", "some-job-", "some-job", false},
		{"no separator", "some-job1", "some-job", false},
		{"shorter than base", "some", "some-job", false},
		{"empty candidate", "", "some-job", false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasGeneratedNumericSuffix(tc.cand, tc.base); got != tc.want {
				t.Errorf("hasGeneratedNumericSuffix(%q, %q) = %v want %v", tc.cand, tc.base, got, tc.want)
			}
		})
	}
}

// A match-everything implementation would pass every positive above; this
// pins that it does not, by requiring the collision cases to be rejected
// while a real generated name is still accepted.
func TestHasGeneratedNumericSuffix_NotDegenerate(t *testing.T) {
	t.Parallel()
	if !hasGeneratedNumericSuffix("cutover-harbor-prewarm-1785348872", "cutover-harbor-prewarm") {
		t.Fatal("a real generated Job name must resolve — a match-nothing resolver leaves UAT 176 unmet")
	}
	if hasGeneratedNumericSuffix("bp-velero-hcs", "bp-velero") {
		t.Fatal("a match-everything resolver reintroduces the #5485 wrong-object collision")
	}
}
