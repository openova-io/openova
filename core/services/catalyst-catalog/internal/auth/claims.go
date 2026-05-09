// Package auth extracts the caller's Keycloak claims from incoming
// HTTP requests so per-Org access filtering in the source resolver can
// honor the session's Group memberships.
//
// CONTRACT: catalyst-catalog is deployed behind catalyst-api (Cilium
// Gateway HTTPRoute proxy) per the EPIC-2 Slice L brief — catalyst-api
// validates the JWT signature against Keycloak JWKS and forwards the
// validated request. Catalog-svc therefore extracts claims from the
// already-validated session token without re-running JWKS validation
// on the hot path.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 we still validate the JWT
// shape (3 base64url segments), parse claims, check `exp`, and reject
// anonymous reads unless `CATALOG_ANONYMOUS_READS=true` is set. JWKS
// signature verification is not done in-process — it is the proxy's
// responsibility. A future hardening slice can add an optional
// `CATALOG_JWKS_URL` env-var to verify in-process when not deployed
// behind catalyst-api.
//
// SEAM: this file mirrors the `Claims` struct from
// products/catalyst/bootstrap/api/internal/auth/session.go (slice D2)
// — Groups, Org, Tier, RealmAccess. Catalog-svc cannot import that
// package directly (different Go module + internal/ rule), so the
// shape is reproduced here. If catalyst-api's Claims shape grows new
// fields the catalog will need, add them here in the same migration.
package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Claims is the subset of Keycloak claims the catalog uses. Mirrors
// the canonical `auth.Claims` shape in catalyst-api.
type Claims struct {
	Sub           string   `json:"sub"`
	Email         string   `json:"email"`
	PreferredName string   `json:"preferred_username"`
	Groups        []string `json:"groups,omitempty"`
	Org           string   `json:"org,omitempty"`
	Tier          string   `json:"tier,omitempty"`
	RealmAccess   struct {
		Roles []string `json:"roles,omitempty"`
	} `json:"realm_access,omitempty"`
	Exp int64 `json:"exp,omitempty"`
}

// ErrNoSession is returned when no session cookie / bearer header is
// present.
var ErrNoSession = errors.New("auth: no session token")

// ErrInvalidSession wraps malformed-JWT / expired-token failures.
var ErrInvalidSession = errors.New("auth: invalid session token")

// ExtractFromRequest reads the session token from (1) Authorization
// Bearer header, (2) ?access_token=… query param, or (3) the named
// session cookie — first match wins. Returns ErrNoSession when none
// is present.
func ExtractFromRequest(r *http.Request, cookieName string) (string, error) {
	if hdr := r.Header.Get("Authorization"); hdr != "" {
		const prefix = "Bearer "
		if len(hdr) > len(prefix) && strings.EqualFold(hdr[:len(prefix)], prefix) {
			tok := strings.TrimSpace(hdr[len(prefix):])
			if isJWTShape(tok) {
				return tok, nil
			}
		}
	}
	if qtok := strings.TrimSpace(r.URL.Query().Get("access_token")); qtok != "" {
		if isJWTShape(qtok) {
			return qtok, nil
		}
	}
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		// catalyst-api wraps the cookie value as `<jwt>.<hmac>` when
		// CookieSecret is set. We accept either: 3 segments = raw JWT,
		// 4 segments = HMAC-wrapped (we strip the trailing HMAC).
		v := c.Value
		parts := strings.Split(v, ".")
		switch len(parts) {
		case 3:
			return v, nil
		case 4:
			// Drop the trailing HMAC suffix.
			return strings.Join(parts[:3], "."), nil
		}
	}
	return "", ErrNoSession
}

// ParseClaims decodes the JWT payload (no signature verification — see
// package doc) and validates `exp`.
func ParseClaims(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: malformed JWT (got %d parts)", ErrInvalidSession, len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: decode payload: %v", ErrInvalidSession, err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("%w: parse payload: %v", ErrInvalidSession, err)
	}
	if c.Exp > 0 && time.Unix(c.Exp, 0).Before(time.Now()) {
		return nil, fmt.Errorf("%w: token expired at %s", ErrInvalidSession, time.Unix(c.Exp, 0))
	}
	return &c, nil
}

// VisibleOrgs returns the set of Org slugs whose private blueprints
// the caller may see. The slugs come from:
//
//   - Claims.Org (single-Org sessions per docs/EPICS-1-6-unified-design.md §6.2)
//   - Claims.Groups[] paths of the form "/<org>/..." or "<org>/..." —
//     the leading segment is the Org slug. Both Keycloak v22 and v24
//     conventions are accepted.
//
// Result is deterministic (sorted, de-duplicated).
func VisibleOrgs(c *Claims) []string {
	if c == nil {
		return nil
	}
	seen := make(map[string]struct{})
	if c.Org != "" {
		seen[c.Org] = struct{}{}
	}
	for _, g := range c.Groups {
		g = strings.TrimPrefix(g, "/")
		if g == "" {
			continue
		}
		head, _, _ := strings.Cut(g, "/")
		if head != "" {
			seen[head] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// HasOrg reports whether the caller may see private blueprints for
// the named Org slug.
func HasOrg(c *Claims, org string) bool {
	if c == nil || org == "" {
		return false
	}
	if c.Org == org {
		return true
	}
	want := strings.TrimPrefix(org, "/")
	for _, g := range c.Groups {
		g = strings.TrimPrefix(g, "/")
		head, _, _ := strings.Cut(g, "/")
		if head == want {
			return true
		}
	}
	return false
}

// isJWTShape returns true when s looks like a 3-segment compact JWT.
// Used as a cheap first-pass guard before any base64 decode.
func isJWTShape(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// sortStrings is an inlined sort.Strings that avoids pulling sort/strings
// dependency drift through the test surface.
func sortStrings(s []string) {
	// insertion sort — bounded n (<= number of Groups, typically <10).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
