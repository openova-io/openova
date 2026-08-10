// session_hint.go — the readable "a session exists" signal (#5940,
// UAT rows 3 + 91).
//
// ── The problem this exists to solve ─────────────────────────────────
//
// `catalyst_session` is HttpOnly by design and must stay that way. The
// marketplace (`core/marketplace/`) is a STATIC build on a SEPARATE host
// whose auth is deliberately client-side, so its JavaScript cannot read
// that cookie — `document.cookie` is empty even on the console's own
// origin. Measured on hw292 2026-08-10: an owner holding a live console
// session (`GET /api/v1/whoami` -> 200, tier `owner`, seconds later)
// opened their own voucher-redeem URL and was shown the "Sign up to
// redeem" stranger form; `redirectCount` was 0. The marketplace root
// behaved the same way — no returning-user redirect at all.
//
// Neither surface has a bug in its own logic. They have no readable
// signal, so the bounce cannot happen BY CONSTRUCTION.
//
// ── What this adds ───────────────────────────────────────────────────
//
// A second cookie, `catalyst_session_hint`, set alongside every
// `catalyst_session` and cleared alongside it. It is NOT HttpOnly, so a
// static page on a sibling host can read it, and it carries no secret:
//
//	v=1                (a session was minted by this Sovereign)
//	org=<slug>         (optional; the Organization the session is scoped
//	                    to — absent for a Sovereign-admin session)
//
// That is the whole value. No token, no JWT, no email, no jti, no
// expiry-bearing claim. It is a hint, never a credential: nothing on the
// server ever reads it back, so forging it grants a visitor exactly one
// thing — a redirect to a console that will then ask them to sign in.
// Authority stays entirely with the HttpOnly `catalyst_session`.
//
// ── Scope, stated honestly ───────────────────────────────────────────
//
// The hint is scoped to `sessionCookieDomain(r)` — EXACTLY the domain
// the real session cookie already spans. It therefore reaches every host
// the session itself reaches and not one host more:
//
//	console.<sov>            -> Domain=.<sov>        -> marketplace.<sov> READS IT
//	console.<slug>.<sov>     -> Domain=.<sov>        -> marketplace.<sov> READS IT
//	console.<slug>.<pool>    -> Domain=.<slug>.<pool>-> marketplace.<sov> does NOT
//
// The third row is a browser rule (a host cannot set a cookie for an
// unrelated registrable domain), not an omission. That persona signed up
// ON the marketplace origin and therefore holds the marketplace-origin
// `org-token`, which is the path that already works for it. The hint is
// strictly additive: it can only add a redirect where there was none.

package handler

import (
	"net/http"
	"net/url"
	"strings"
)

// SessionHintCookieName is the NON-HttpOnly companion to
// auth.SessionCookieName. Readable by client-side JS on any host inside
// the session cookie's domain, so a static sibling app (the marketplace)
// can tell that a session exists without ever seeing the session itself.
const SessionHintCookieName = "catalyst_session_hint"

// sessionHintVersion is stamped as `v=` so the reader can reject a value
// shape it does not understand instead of guessing at it.
const sessionHintVersion = "1"

// maxSessionHintOrgLen bounds the only variable-length field. A DNS label
// is 63 octets; an Org slug becomes one (`console.<slug>.<zone>`), so
// anything longer could not be a slug this Sovereign issued.
const maxSessionHintOrgLen = 63

// sanitizeHintOrg returns the Org slug if — and only if — it is a
// lowercase DNS label. Anything else yields "" and the hint is emitted
// without an `org` key.
//
// This is a whitelist rather than an escape: the value lands in a cookie
// that client-side JS splices into a hostname, so the set of acceptable
// characters is exactly the set legal in a DNS label. A rejected slug
// degrades to a session-exists-only hint, never to a malformed host.
func sanitizeHintOrg(org string) string {
	s := strings.ToLower(strings.TrimSpace(org))
	if s == "" || len(s) > maxSessionHintOrgLen {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' && i != 0 && i != len(s)-1:
		default:
			return ""
		}
	}
	return s
}

// buildSessionHintValue renders the hint payload.
//
// The output is a sorted, URL-encoded key/value list — `v=1` alone for a
// Sovereign-admin session, `org=<slug>&v=1` for an Org-scoped one. Every
// key is enumerated here; there is no pass-through of caller data, so a
// new claim cannot leak into the hint by accident.
func buildSessionHintValue(org string) string {
	vals := url.Values{}
	vals.Set("v", sessionHintVersion)
	if slug := sanitizeHintOrg(org); slug != "" {
		vals.Set("org", slug)
	}
	return vals.Encode()
}

// setSessionHintCookie emits the hint next to a freshly minted session.
//
// Attributes mirror the session cookie (Path, Domain, Max-Age, Secure,
// SameSite) so the two live and die together in the browser's jar. The
// ONE deliberate difference is HttpOnly:false — that difference is the
// entire point, and it is safe only because the value is not a
// credential.
//
// Call this AFTER the session cookie's own http.SetCookie: HandlePinVerify
// and HandleOrgHandover both call w.Header().Del("Set-Cookie") immediately
// before minting the session (the TBD-F7/#1730 replay defence), which
// would otherwise drop the hint too.
func setSessionHintCookie(w http.ResponseWriter, org, domain string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionHintCookieName,
		Value:    buildSessionHintValue(org),
		Path:     "/",
		Domain:   domain,
		MaxAge:   maxAge,
		HttpOnly: false, // the point of this cookie — see the file header
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// buildClearSessionHintCookie returns the Set-Cookie header value that
// deletes the hint.
//
// Sign-out MUST clear this. A stale hint would bounce a signed-OUT
// visitor to a console that immediately asks them to sign in — an
// infinite door with no way back to the storefront, which is a worse
// customer outcome than the bug being fixed here.
//
// HttpOnly is deliberately absent — a clear-cookie must mirror the
// attributes the cookie was set with, and this one was set readable.
// SameSite stays Lax for the same reason.
//
// `maxAgeToken` is the literal Max-Age the caller's sibling cookies use on
// that response: the DELETE path contracts on `Max-Age=-1`, the SPA POST
// path on `Max-Age=0`. Both are non-positive and therefore identical to a
// browser (RFC 6265bis), so this is a wire-consistency choice — every
// Set-Cookie on one response reads the same way to anyone auditing it.
func buildClearSessionHintCookie(domain string, secure bool, maxAgeToken string) string {
	var b strings.Builder
	b.WriteString(SessionHintCookieName)
	b.WriteString("=; Path=/")
	if domain != "" {
		b.WriteString("; Domain=")
		b.WriteString(domain)
	}
	if secure {
		b.WriteString("; Secure")
	}
	b.WriteString("; SameSite=Lax; ")
	b.WriteString(maxAgeToken)
	return b.String()
}
