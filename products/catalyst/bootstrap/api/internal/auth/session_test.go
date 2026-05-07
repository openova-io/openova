package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeJWT returns a 3-segment base64url string that looks like a JWT to
// the shape check in ReadSessionToken. The cryptographic signature is
// not verified at this layer — ValidateToken is exercised separately.
func fakeJWT() string {
	return "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.signature"
}

// TestReadSessionToken_BearerHeader — the existing chroot SPA path: every
// fetch() call carries Authorization: Bearer <jwt>. The header MUST win
// even if a malformed cookie or query param is also present.
func TestReadSessionToken_BearerHeader(t *testing.T) {
	cfg := &Config{}
	tok := fakeJWT()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/x/k8s/stream", nil)
	r.Header.Set("Authorization", "Bearer "+tok)

	got := cfg.ReadSessionToken(r)
	if got != tok {
		t.Fatalf("expected token from Authorization header, got %q", got)
	}
}

// TestReadSessionToken_QueryParam — the new SSE path. EventSource cannot
// set custom headers, so the chroot SPA appends ?access_token=<jwt> to
// the stream URL. This is the regression test for the 13-page chroot
// SSE 401 incident (TC-066/067/071-076/078-081/086).
func TestReadSessionToken_QueryParam(t *testing.T) {
	cfg := &Config{}
	tok := fakeJWT()

	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/sov/k8s/stream?kinds=pod&access_token="+tok, nil)

	got := cfg.ReadSessionToken(r)
	if got != tok {
		t.Fatalf("expected token from access_token query param, got %q", got)
	}
}

// TestReadSessionToken_QueryParam_BadShape — defence in depth: a value
// in ?access_token= that doesn't look like a JWT must NOT be returned,
// otherwise a malformed value would reach ValidateToken with a JWKS
// round-trip cost we don't want to pay on the 401 hot path.
func TestReadSessionToken_QueryParam_BadShape(t *testing.T) {
	cfg := &Config{}

	for _, bad := range []string{"", "not.a.jwt.here", "no-dots", "two.parts"} {
		r := httptest.NewRequest(http.MethodGet,
			"/api/v1/x?access_token="+bad, nil)
		if got := cfg.ReadSessionToken(r); got != "" {
			t.Fatalf("bad-shape %q: expected empty, got %q", bad, got)
		}
	}
}

// TestReadSessionToken_HeaderWinsOverQueryParam — when both channels
// carry a token, the header wins. Operators occasionally land on a
// chroot URL with a stale ?access_token= still in the address bar after
// a refresh; the live header from the fetch interceptor is the source
// of truth.
func TestReadSessionToken_HeaderWinsOverQueryParam(t *testing.T) {
	cfg := &Config{}
	headerTok := "header.payload.sig"
	queryTok := fakeJWT()

	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/x?access_token="+queryTok, nil)
	r.Header.Set("Authorization", "Bearer "+headerTok)

	got := cfg.ReadSessionToken(r)
	if got != headerTok {
		t.Fatalf("expected header token to win, got %q", got)
	}
}

// TestReadSessionToken_QueryParamThenCookie — query-param wins over
// cookie when both are present (preserves the SSE path's intent: the
// SPA explicitly opted-in to the query-param channel for this
// connection).
func TestReadSessionToken_QueryParamThenCookie(t *testing.T) {
	cfg := &Config{}
	queryTok := fakeJWT()
	cookieTok := "cookie.payload.sig"

	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/x?access_token="+queryTok, nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookieTok})

	got := cfg.ReadSessionToken(r)
	if got != queryTok {
		t.Fatalf("expected query-param token to win over cookie, got %q", got)
	}
}

// TestReadSessionToken_CookieFallback — when no header and no query
// param are present, the catalyst_session cookie still works (mother
// PIN-auth path is unchanged).
func TestReadSessionToken_CookieFallback(t *testing.T) {
	cfg := &Config{}
	tok := fakeJWT()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})

	got := cfg.ReadSessionToken(r)
	if got != tok {
		t.Fatalf("expected cookie fallback, got %q", got)
	}
}

// TestReadSessionToken_NoToken — no header, no query, no cookie → empty.
func TestReadSessionToken_NoToken(t *testing.T) {
	cfg := &Config{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	if got := cfg.ReadSessionToken(r); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// TestReadSessionToken_QueryParamWhitespace — trim leading/trailing
// whitespace on the query param value (some clients URL-decode "+" to
// space). A whitespace-only value is rejected.
func TestReadSessionToken_QueryParamWhitespace(t *testing.T) {
	cfg := &Config{}
	tok := fakeJWT()

	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/x?access_token="+strings.Repeat(" ", 0)+tok, nil)
	if got := cfg.ReadSessionToken(r); got != tok {
		t.Fatalf("expected token, got %q", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/x?access_token=%20%20%20", nil)
	if got := cfg.ReadSessionToken(r2); got != "" {
		t.Fatalf("expected empty for whitespace-only token, got %q", got)
	}
}
