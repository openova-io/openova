// Tests for #5485 — the reconciler drill-in rendered ANOTHER object's logs.
//
// Live on hw291: GET .../reconcilers/bp-velero/logs returned total=1 with
// payload name "bp-velero-hcs" — 100% wrong object, with nothing in the UI
// indicating the substitution. GET .../bp-cnpg/logs mixed bp-cnpg with
// bp-cnpg-pair.
//
// Cause: the matchers guarded only the LEADING boundary. `name=bp-velero`
// and `/bp-velero` both match inside `name=bp-velero-hcs`, and the primary
// check was a bare strings.Contains on `<ns>/<name>`, so
// `flux-system/bp-velero` matched `flux-system/bp-velero-hcs`. The old
// function's own doc comment claimed it prevented exactly this.
//
// Anti-theater: every "must NOT match" case below passes trivially against
// the pre-fix code only if you delete the assertion — with the old bare
// Contains they all FAIL. The "must still match" cases are the control:
// without them a matcher that returns false for everything would pass.

package handler

import "testing"

func TestLogLineMentionsName_DoesNotMatchLongerSiblingName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		query string
		line  string
		want  bool
	}{
		// The exact hw291 failures.
		{"velero vs velero-hcs, name= form", "bp-velero",
			`ts=2026-07-29T16:00:00Z level=info name=bp-velero-hcs namespace=flux-system msg="reconciliation finished"`, false},
		{"velero vs velero-hcs, path form", "bp-velero",
			`ts=2026-07-29T16:00:00Z level=info msg="applied" path=/flux-system/bp-velero-hcs`, false},
		{"cnpg vs cnpg-pair", "bp-cnpg",
			`level=info name=bp-cnpg-pair namespace=flux-system msg="drift detected"`, false},

		// The object itself must still match — otherwise the fix is a
		// matcher that never matches, and the drill-in renders nothing.
		{"exact name= match", "bp-velero",
			`level=info name=bp-velero namespace=flux-system msg="reconciliation finished"`, true},
		{"exact path match", "bp-velero",
			`level=info msg="applied" path=/flux-system/bp-velero`, true},
		{"quoted name form", "bp-velero",
			`level=info name="bp-velero" namespace="flux-system"`, true},
		{"json name form", "bp-velero",
			`{"level":"info","name":"bp-velero","namespace":"flux-system"}`, true},
		{"name= followed by a space", "bp-cnpg",
			`level=info name=bp-cnpg msg="ok"`, true},

		// A quoted marker is delimited on both sides, so a longer sibling
		// cannot produce a false positive through it.
		{"quoted form is not fooled by a sibling", "bp-velero",
			`level=info name="bp-velero-hcs" namespace="flux-system"`, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := logLineMentionsName(tc.line, tc.query); got != tc.want {
				t.Errorf("logLineMentionsName(%q) = %v want %v\n  line: %s",
					tc.query, got, tc.want, tc.line)
			}
		})
	}
}

// The namespaced primary check has the same boundary requirement.
func TestLogLineMentionsToken_NamespacedFormRespectsBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		token string
		line  string
		want  bool
	}{
		{"sibling must not match", "flux-system/bp-velero",
			`level=info msg="reconciled" object=flux-system/bp-velero-hcs`, false},
		{"exact must match", "flux-system/bp-velero",
			`level=info msg="reconciled" object=flux-system/bp-velero`, true},
		{"exact at end of line", "flux-system/bp-cnpg",
			`level=info object=flux-system/bp-cnpg`, true},
		{"a later exact occurrence still matches", "flux-system/bp-cnpg",
			`msg="saw flux-system/bp-cnpg-pair then flux-system/bp-cnpg"`, true},
		{"empty token never matches", "",
			`level=info name=anything`, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := logLineMentionsToken(tc.line, tc.token); got != tc.want {
				t.Errorf("logLineMentionsToken(%q) = %v want %v\n  line: %s",
					tc.token, got, tc.want, tc.line)
			}
		})
	}
}
