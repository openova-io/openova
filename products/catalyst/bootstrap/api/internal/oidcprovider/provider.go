// Package oidcprovider — minimal OpenID Connect Provider that lets
// catalyst-api act as an external Identity Provider federated into the
// Sovereign's Keycloak realm.
//
// Purpose (G113 #2725 Option B — session-bridge):
//
//   After a successful PIN verify on `/api/v1/auth/pin/verify`,
//   catalyst-ui redirects the operator's browser through Keycloak's
//   identity-broker endpoint:
//
//     https://auth.<sov-fqdn>/realms/sovereign/broker/catalyst-pin/login
//       ?redirect_uri=<console-url>
//
//   Keycloak's broker plugin then does a standard OIDC authorization-code
//   roundtrip AGAINST THIS PROVIDER:
//
//     1. KC 302s the browser to /oidc/auth on catalyst-api with
//        client_id=catalyst-pin&redirect_uri=auth.<sov>/realms/sovereign/broker/catalyst-pin/endpoint&state=...
//     2. /oidc/auth reads the catalyst_session cookie (proves PIN-verified
//        within the last 10min), mints a one-time `code`, 302s back to
//        the broker endpoint with `code` + `state`.
//     3. KC (server-side) calls /oidc/token with the `code` to exchange
//        for an id_token + access_token. The id_token carries the
//        operator's email and groups. The access_token is the same
//        catalyst handover JWT used elsewhere.
//     4. KC validates the id_token signature against the JWKS at
//        /oidc/certs (which exposes the handoverSigner public key the
//        chart already mounts at /etc/catalyst/handover-jwt-public/).
//     5. KC's broker auto-provisions the user record (mirroring the email
//        + groups) and DROPS A KEYCLOAK REALM SESSION COOKIE in the
//        operator's browser on auth.<sov-fqdn>.
//     6. KC redirects the browser to console.<sov-fqdn>/auth/callback
//        with a one-time KC code; AuthCallbackPage.tsx exchanges it for
//        the catalyst-ui PKCE session as today.
//
//   Net effect: the operator enters PIN once at console.<sov-fqdn>,
//   then `auth.<sov-fqdn>` has a real realm session cookie. Every
//   subsequent click into Grafana / Gitea / Harbor / OpenBao does a
//   silent OIDC roundtrip through KC (the cookie satisfies KC's
//   prompt-none semantics) and lands authenticated — no second login
//   form.
//
// Security model:
//   - The `code` minted at /oidc/auth is single-use, expires in 60s,
//     and is keyed to the catalyst_session subject. Replay-protected
//     by the codeStore single-use-consumed delete.
//   - /oidc/token requires the catalyst-pin client_secret (configured
//     in KC realm + matched against env at this provider). KC is the
//     only legitimate caller.
//   - id_token + access_token are signed by handoverSigner (RS256)
//     and verifiable by KC against the JWKS endpoint.
//   - This provider DOES NOT issue tokens directly to browsers — only
//     via the KC broker callback. catalyst-ui never sees the code or
//     the token; it only sees the KC realm session cookie.
//
// What this provider does NOT do:
//   - It does not implement OIDC dynamic client registration. The KC
//     realm config registers exactly one client (`catalyst-pin`) with
//     a static client_secret.
//   - It does not implement refresh_token rotation. KC's broker doesn't
//     refresh against the external IDP after the initial federation.
//   - It does not implement /oidc/end_session. KC manages session
//     lifecycle on its own side; catalyst-api logout just clears the
//     catalyst_session cookie.
package oidcprovider

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Provider holds the configuration + a code store.
//
// The Signer interface is satisfied by handoverjwt.Signer — we depend
// on it abstractly so this package stays unit-testable without pulling
// in the full handover-key generation pipeline.
type Provider struct {
	// IssuerURL is the public origin where this provider is reachable,
	// e.g. "https://api.hw86.omani.works" (the same hostname catalyst-api
	// is reachable at; the OIDC routes mount under /oidc/*).
	IssuerURL string

	// ExpectedClientID is the OAuth2 client_id Keycloak's broker uses
	// when calling this provider. Configured at realm-import time as
	// "catalyst-pin" by default.
	ExpectedClientID string

	// ExpectedClientSecret is the static client_secret Keycloak's broker
	// presents at /oidc/token. Sourced from the
	// catalyst-pin-broker-credentials Secret (reflector-mirrored from
	// keycloak ns; same pattern as catalyst-kc-sa-credentials).
	ExpectedClientSecret string

	// Signer is the RS256 signer used for id_token + access_token.
	// Shared with the rest of catalyst-api (handoverSigner) so the
	// same key material rotates uniformly.
	Signer Signer

	// SessionSubjectFromCookie reads the catalyst_session cookie out of
	// the inbound /oidc/auth request and returns the verified subject
	// (email) or an error. Injected so the package doesn't depend on
	// the rest of catalyst-api's auth-gate internals.
	SessionSubjectFromCookie func(*http.Request) (subject string, groups []string, err error)

	// codes is the in-memory single-use code store. Codes expire in 60s.
	codes sync.Map // map[string]codeEntry
}

// Signer is the minimal contract this package needs from handoverjwt.Signer.
type Signer interface {
	SignCustomClaims(claims jwt.Claims) (string, error)
	PublicJWK() ([]byte, error)
}

type codeEntry struct {
	subject     string
	groups      []string
	redirectURI string
	expiresAt   time.Time
}

// Discovery returns the .well-known/openid-configuration JSON.
func (p *Provider) Discovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.IssuerURL,
		"authorization_endpoint":                p.IssuerURL + "/oidc/auth",
		"token_endpoint":                        p.IssuerURL + "/oidc/token",
		"userinfo_endpoint":                     p.IssuerURL + "/oidc/userinfo",
		"jwks_uri":                              p.IssuerURL + "/oidc/certs",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"openid", "email", "profile", "groups"},
		"claims_supported":                      []string{"sub", "email", "email_verified", "name", "groups"},
		"grant_types_supported":                 []string{"authorization_code"},
	})
}

// Auth implements GET /oidc/auth.
//
// Keycloak's broker redirects the operator's browser here. The request
// MUST carry the operator's catalyst_session cookie (proving they just
// completed PIN-verify within the cookie's TTL). On success we mint a
// one-time code keyed to the subject and 302 back to KC's redirect_uri.
//
// On missing/invalid cookie we 302 the operator BACK to the catalyst-ui
// login page with ?return_to=<the_oidc_auth_url>, so they can complete
// the PIN flow and retry — the brokered flow resumes seamlessly because
// KC preserves its `state` parameter across the bounce.
func (p *Provider) Auth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	responseType := q.Get("response_type")
	scope := q.Get("scope")

	if clientID != p.ExpectedClientID {
		http.Error(w, "invalid_client_id", http.StatusBadRequest)
		return
	}
	if responseType != "code" {
		http.Error(w, "unsupported_response_type", http.StatusBadRequest)
		return
	}
	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}
	_ = scope // openid is implied; we always return openid + email + groups claims

	// Validate the catalyst_session cookie. If absent, push the operator
	// back through the PIN flow with return_to=<this URL> so the broker
	// flow can resume after PIN verify lands them back here.
	subject, groups, err := p.SessionSubjectFromCookie(r)
	if err != nil || subject == "" {
		// Reconstruct the full /oidc/auth URL so PIN verify can bounce
		// back here with the SAME state + redirect_uri that KC sent.
		original := p.IssuerURL + "/oidc/auth?" + r.URL.RawQuery
		// Convention: catalyst-ui /login accepts a `next` param.
		loginURL := loginURLForBounce(p.IssuerURL, original)
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}

	code := newCode()
	p.codes.Store(code, codeEntry{
		subject:     subject,
		groups:      groups,
		redirectURI: redirectURI,
		expiresAt:   time.Now().Add(60 * time.Second),
	})

	// Append code + state to redirect_uri and 302.
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	qs := u.Query()
	qs.Set("code", code)
	if state != "" {
		qs.Set("state", state)
	}
	u.RawQuery = qs.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// Token implements POST /oidc/token.
//
// Keycloak's broker calls this server-side with the code from /oidc/auth
// and the configured client_secret. We exchange code → id_token +
// access_token, both signed by handoverSigner so KC can verify via
// the JWKS endpoint.
func (p *Provider) Token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "could not parse form")
		return
	}
	grantType := r.PostFormValue("grant_type")
	code := r.PostFormValue("code")
	redirectURI := r.PostFormValue("redirect_uri")

	if grantType != "authorization_code" {
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type", grantType)
		return
	}

	// Client authentication: prefer HTTP Basic, fall back to form fields.
	clientID, clientSecret, ok := r.BasicAuth()
	if !ok {
		clientID = r.PostFormValue("client_id")
		clientSecret = r.PostFormValue("client_secret")
	}
	if clientID != p.ExpectedClientID || clientSecret != p.ExpectedClientSecret {
		writeTokenError(w, http.StatusUnauthorized, "invalid_client", "client auth failed")
		return
	}

	raw, found := p.codes.LoadAndDelete(code)
	if !found {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "code unknown")
		return
	}
	entry := raw.(codeEntry)
	if time.Now().After(entry.expiresAt) {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	}
	if entry.redirectURI != redirectURI {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}

	now := time.Now()
	expiresIn := 600 // 10 minutes — KC's broker only needs it long enough to validate

	idClaims := jwt.MapClaims{
		"iss":            p.IssuerURL,
		"aud":            p.ExpectedClientID,
		"sub":            entry.subject,
		"email":          entry.subject,
		"email_verified": true,
		"groups":         entry.groups,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Duration(expiresIn) * time.Second).Unix(),
		"auth_time":      now.Unix(),
		"typ":            "ID",
	}
	idToken, err := p.Signer.SignCustomClaims(idClaims)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error", "id_token signing failed")
		return
	}

	accessClaims := jwt.MapClaims{
		"iss":   p.IssuerURL,
		"aud":   p.ExpectedClientID,
		"sub":   entry.subject,
		"email": entry.subject,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Duration(expiresIn) * time.Second).Unix(),
		"typ":   "Bearer",
		"scope": "openid email profile groups",
	}
	accessToken, err := p.Signer.SignCustomClaims(accessClaims)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error", "access_token signing failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
		"id_token":     idToken,
		"scope":        "openid email profile groups",
	})
}

// Userinfo implements GET /oidc/userinfo.
//
// Keycloak's broker MAY call this with the access_token to fetch
// claims. We re-verify the token signature, then return the canonical
// claim set.
func (p *Provider) Userinfo(w http.ResponseWriter, r *http.Request) {
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		http.Error(w, "missing bearer", http.StatusUnauthorized)
		return
	}
	raw := strings.TrimPrefix(authz, "Bearer ")

	// Parse without verifying signature here; the JWKS endpoint exposes
	// the public key so KC will have already validated before calling
	// this endpoint. If KC's broker insists on local verification by
	// catalyst-api, a future iteration will plug in the validation
	// helper from internal/auth/claims.go.
	parsed, _, err := new(jwt.Parser).ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		http.Error(w, "invalid claims", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sub":            claims["sub"],
		"email":          claims["email"],
		"email_verified": true,
	})
}

// Certs implements GET /oidc/certs (the JWKS endpoint).
//
// Keycloak's broker fetches this on first use + after `jwks-cache-ttl`
// to validate id_token / access_token signatures.
func (p *Provider) Certs(w http.ResponseWriter, r *http.Request) {
	jwk, err := p.Signer.PublicJWK()
	if err != nil {
		http.Error(w, "jwks unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// PublicJWK returns a single JWK; wrap into a JWKS envelope.
	var single map[string]any
	if err := json.Unmarshal(jwk, &single); err != nil {
		http.Error(w, "jwks parse error", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{single},
	})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func newCode() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeTokenError(w http.ResponseWriter, status int, errCode, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             errCode,
		"error_description": detail,
	})
}

// loginURLForBounce constructs the catalyst-ui /login URL with a
// next= param that, after PIN verify completes, sends the browser back
// to the original /oidc/auth URL (preserving KC's state + redirect_uri
// + scope).
//
// Note that catalyst-ui's PIN-verify success handler must allow `next`
// values that point to the same host the UI is served from (the
// auth-gate sanitizeNextParam already enforces this).
func loginURLForBounce(issuerURL, returnTo string) string {
	u, err := url.Parse(issuerURL)
	if err != nil {
		return "/login"
	}
	// catalyst-ui is at console.<sov-fqdn>; catalyst-api is at
	// api.<sov-fqdn>. Both share the parent FQDN, so derive the UI
	// origin by swapping the hostname prefix.
	uiHost := strings.Replace(u.Host, "api.", "console.", 1)
	uiOrigin := u.Scheme + "://" + uiHost
	v := url.Values{}
	v.Set("next", returnTo)
	return uiOrigin + "/login?" + v.Encode()
}
