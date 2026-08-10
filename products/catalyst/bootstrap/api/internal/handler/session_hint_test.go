// session_hint_test.go — #5940 (UAT rows 3 + 91).
//
// WHAT THESE ASSERT, and why each one can fail.
//
// The fix's whole value is that a static sibling app can READ a signal the
// HttpOnly session cookie cannot give it. Two properties therefore carry the
// entire contract, and both are asserted here against the real handlers:
//
//	1. The hint is emitted, alongside the session, with HttpOnly=false and the
//	   SAME Domain/Path/Max-Age/Secure as the session. Wrong Domain and the
//	   browser drops it; HttpOnly=true and the marketplace still cannot read it
//	   — either way the fix is inert while every other test stays green.
//	2. The hint is NOT a credential. The session JWT must not appear in it,
//	   and the value must stay inside the enumerated `v` / `org` shape.
//
// Every assertion below has a CONTROL that fails if the check is vacuous —
// e.g. the "no token in the hint" test also asserts the token IS in the
// session cookie, so a harness that minted no token at all cannot pass it.

package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ─── the value shape ──────────────────────────────────────────────────────

func TestBuildSessionHintValue_ShapeAndSanitisation(t *testing.T) {
	cases := []struct {
		name string
		org  string
		want string
	}{
		{name: "sovereign-admin session carries no org", org: "", want: "v=1"},
		{name: "org-scoped session carries the slug", org: "acme", want: "org=acme&v=1"},
		{name: "slug is lowercased", org: "ACME", want: "org=acme&v=1"},
		{name: "surrounding whitespace trimmed", org: "  acme  ", want: "org=acme&v=1"},
		{name: "internal hyphen kept (legal DNS label)", org: "acme-corp", want: "org=acme-corp&v=1"},
		// Rejections. Each of these would otherwise be spliced into a hostname
		// by the marketplace, so the whitelist is the security boundary.
		{name: "dot rejected — would widen the host", org: "acme.evil", want: "v=1"},
		{name: "slash rejected", org: "acme/evil", want: "v=1"},
		{name: "leading hyphen rejected (illegal DNS label)", org: "-acme", want: "v=1"},
		{name: "trailing hyphen rejected", org: "acme-", want: "v=1"},
		{name: "over-long slug rejected", org: strings.Repeat("a", 64), want: "v=1"},
		{name: "ampersand rejected — would forge a second key", org: "a&v=9", want: "v=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildSessionHintValue(tc.org); got != tc.want {
				t.Errorf("buildSessionHintValue(%q) = %q, want %q", tc.org, got, tc.want)
			}
		})
	}
}

// TestBuildSessionHintValue_OnlyEnumeratedKeys is the leak guard: the hint
// must never grow a key that was not deliberately added. Parsing the output
// and comparing the key set (rather than the whole string) means a future
// `vals.Set("email", ...)` fails here even if the rest of the shape holds.
func TestBuildSessionHintValue_OnlyEnumeratedKeys(t *testing.T) {
	for _, org := range []string{"", "acme"} {
		parsed, err := url.ParseQuery(buildSessionHintValue(org))
		if err != nil {
			t.Fatalf("hint value is not parseable: %v", err)
		}
		for k := range parsed {
			if k != "v" && k != "org" {
				t.Errorf("hint carries unexpected key %q — the hint must never carry anything but a version and a slug", k)
			}
		}
		if parsed.Get("v") != "1" {
			t.Errorf("hint version: got %q want %q", parsed.Get("v"), "1")
		}
	}
}

// ─── PIN verify: the hint rides with the session ──────────────────────────

// TestPinVerify_EmitsReadableSessionHint is the headline guard for #5940.
// Without the fix there is no catalyst_session_hint on the response at all,
// which is precisely why the marketplace had no readable signal.
func TestPinVerify_EmitsReadableSessionHint(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")

	h := testPinSetup(t)
	h.pinStore.put("ops@omantel.biz", "123456", "req-1")

	req := httptest.NewRequest(http.MethodPost,
		"http://console.omantel.biz/api/v1/auth/pin/verify",
		strings.NewReader(`{"email":"ops@omantel.biz","pin":"123456","requestId":"req-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "console.omantel.biz")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}

	session := findCookie(w.Result().Cookies(), "catalyst_session")
	if session == nil {
		t.Fatal("control failed: catalyst_session was not set, so this test proves nothing about the hint")
	}
	hint := findCookie(w.Result().Cookies(), SessionHintCookieName)
	if hint == nil {
		t.Fatalf("%s not set — the marketplace has no readable signal that a session exists, which IS the #5940 defect (UAT rows 3 + 91)", SessionHintCookieName)
	}

	// (1) readable by client-side JS. This single attribute is the fix.
	if hint.HttpOnly {
		t.Errorf("%s must NOT be HttpOnly — a static sibling app cannot read an HttpOnly cookie, so the redirect stays impossible", SessionHintCookieName)
	}
	// CONTROL for (1): the real session must stay HttpOnly. A fix that made
	// the session itself readable would satisfy the line above and would be a
	// straight security regression.
	if !session.HttpOnly {
		t.Error("catalyst_session must remain HttpOnly — the hint exists so the session never has to be readable")
	}

	// (2) the hint must reach the same hosts as the session, no more, no less.
	if hint.Domain != session.Domain {
		t.Errorf("hint Domain %q != session Domain %q — a mismatched Domain either drops the cookie or widens it beyond the session", hint.Domain, session.Domain)
	}
	if hint.Domain != "omantel.biz" {
		t.Errorf("hint Domain: got %q want %q (marketplace.omantel.biz must be able to read it)", hint.Domain, "omantel.biz")
	}
	if hint.Path != session.Path {
		t.Errorf("hint Path %q != session Path %q", hint.Path, session.Path)
	}
	if hint.MaxAge != session.MaxAge {
		t.Errorf("hint MaxAge %d != session MaxAge %d — a hint that outlives the session bounces a signed-out visitor forever", hint.MaxAge, session.MaxAge)
	}
	if hint.Secure != session.Secure {
		t.Errorf("hint Secure %v != session Secure %v", hint.Secure, session.Secure)
	}

	// (3) the hint is not a credential.
	if hint.Value != "v=1" {
		t.Errorf("hint value: got %q want %q (Sovereign-admin session — no Org slug)", hint.Value, "v=1")
	}
	assertHintCarriesNoToken(t, hint.Value, session.Value)
}

// TestPinVerify_HintCarriesOrgSlugForOrgScopedSession covers the customer
// persona of UAT row 91: the marketplace must send the visitor to THEIR OWN
// Org console, which it can only do if it knows the slug.
func TestPinVerify_HintCarriesOrgSlugForOrgScopedSession(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")

	h := testPinSetup(t)
	// An Org console host registered as tenant_kind=org is what makes
	// HandlePinVerify mint an Org-SCOPED session (resolveOrgScope), which is
	// the only session shape that has a slug to hand the marketplace.
	h.tenantRegistry = orgRegistry(t, "console.acme.omantel.biz", "acme")
	h.pinStore.put("owner@acme.test", "123456", "req-1")

	req := httptest.NewRequest(http.MethodPost,
		"http://console.acme.omantel.biz/api/v1/auth/pin/verify",
		strings.NewReader(`{"email":"owner@acme.test","pin":"123456","requestId":"req-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "console.acme.omantel.biz")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	hint := findCookie(w.Result().Cookies(), SessionHintCookieName)
	if hint == nil {
		t.Fatalf("%s not set", SessionHintCookieName)
	}
	parsed, err := url.ParseQuery(hint.Value)
	if err != nil {
		t.Fatalf("hint value %q is not parseable: %v", hint.Value, err)
	}
	if got := parsed.Get("org"); got != "acme" {
		t.Errorf("hint org: got %q want %q — without the slug the marketplace can only reach the Sovereign console, not the customer's own Org console (UAT row 91)", got, "acme")
	}
}

// ─── logout: the hint dies with the session ───────────────────────────────

// TestHandleAuthLogout_ClearsSessionHint. A hint that survives sign-out is
// worse than the bug being fixed: the storefront would bounce a signed-OUT
// visitor into a console that immediately demands a sign-in.
func TestHandleAuthLogout_ClearsSessionHint(t *testing.T) {
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "console.example.test")

	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandleAuthLogout(w, req)

	line := findSetCookieLine(w.Result().Header.Values("Set-Cookie"), SessionHintCookieName+"=;")
	if line == "" {
		t.Fatalf("DELETE /auth/session does not clear %s — cookies=%v", SessionHintCookieName, w.Result().Header.Values("Set-Cookie"))
	}
	for _, want := range []string{"Max-Age=-1", "Path=/", "Domain=console.example.test", "Secure", "SameSite=Lax"} {
		if !strings.Contains(line, want) {
			t.Errorf("hint clear-cookie missing %q in %q", want, line)
		}
	}
	// The clear MUST mirror the set: a HttpOnly clear does not delete a
	// non-HttpOnly cookie's twin in every browser, and it would also be an
	// attribute the set never carried.
	if strings.Contains(line, "HttpOnly") {
		t.Errorf("hint clear-cookie must not be HttpOnly (the cookie was set without it) — got %q", line)
	}
}

// TestHandleAuthSessionLogout_ClearsSessionHint covers the SPA POST path.
// The DELETE path passing is not evidence for this one — they are two
// separate builders with two separate attribute sets.
func TestHandleAuthSessionLogout_ClearsSessionHint(t *testing.T) {
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "console.example.test")

	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandleAuthSessionLogout(w, req)

	line := findSetCookieLine(w.Result().Header.Values("Set-Cookie"), SessionHintCookieName+"=;")
	if line == "" {
		t.Fatalf("POST /auth/session does not clear %s — cookies=%v", SessionHintCookieName, w.Result().Header.Values("Set-Cookie"))
	}
	if strings.Contains(line, "HttpOnly") {
		t.Errorf("hint clear-cookie must not be HttpOnly — got %q", line)
	}
	if !strings.Contains(line, "Domain=console.example.test") {
		t.Errorf("hint clear-cookie missing Domain — got %q", line)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────

// assertHintCarriesNoToken proves the hint is not a credential in transit.
//
// The CONTROL is the second half: the session JWT's own segments must be
// found in the SESSION cookie. Without that, a harness that minted an empty
// token would make the "not in the hint" assertion trivially true.
func assertHintCarriesNoToken(t *testing.T, hintValue, sessionValue string) {
	t.Helper()
	segments := strings.Split(sessionValue, ".")
	if len(segments) != 3 {
		t.Fatalf("control failed: session cookie %q is not a 3-segment JWT, so 'the token is absent from the hint' proves nothing", sessionValue)
	}
	for i, seg := range segments {
		if seg == "" {
			t.Fatalf("control failed: session JWT segment %d is empty", i)
		}
		if strings.Contains(hintValue, seg) {
			t.Errorf("hint value %q contains JWT segment %d — the hint must carry NO token material", hintValue, i)
		}
	}
	// A hint that merely looks like a JWT would also be wrong: the reader
	// treats it as an opaque key/value list, so a dotted 3-segment value is a
	// sign something credential-shaped leaked in.
	if strings.Count(hintValue, ".") >= 2 {
		t.Errorf("hint value %q is JWT-shaped — the hint is a key/value list, never a token", hintValue)
	}
}

func findSetCookieLine(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
