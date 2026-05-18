// Package auth holds the cross-service Catalyst access-token claim contract.
//
// Single source of truth for what catalyst-api, sandbox-controller, newapi
// (via the per-Sandbox token bridge), and any future consumer find when
// they parse a Catalyst-issued JWT.
//
// Wave 1b (sandbox-wave1b) — Issue: products/sandbox/docs/architecture.md §6.
//
// Background
// ──────────
// Until Wave 1b every catalyst-api consumer carried its OWN Claims shape:
//
//   - core/services/auth/handlers/handlers.go        — {sub, email, role}
//   - products/catalyst/bootstrap/api/internal/auth  — Keycloak ID-token
//     wrapper with .Org, .Groups, .RealmAccess, .Tier, .Scopes
//   - products/catalyst/bootstrap/api/internal/handler/auth_handover.go
//                                                    — RS256 handover JWT
//                                                      with .SovereignFQDN,
//                                                      .DeploymentID
//
// The Sandbox MCP server (Wave 2) needs a single contract it can import
// to authorize tool calls without round-tripping to Keycloak per call. The
// sandbox-controller (Wave 4) needs the same contract to mint per-Sandbox
// PATs that carry an `org_id` claim newapi can use to scope routing.
//
// This file defines that contract. Every consumer that mints or parses a
// Catalyst-issued token SHOULD use this type. Consumers that pre-date
// Wave 1b (the three listed above) keep their existing wrapper structs
// AND embed Claims so a single source of truth exists for the field
// names + JSON tags. Migration to a single struct will happen incrementally.
//
// What is NOT here
// ────────────────
//   - Keycloak ID-token-only fields (preferred_username, email_verified,
//     resource_access, realm_access) — those stay on the catalyst-bootstrap
//     auth.Claims wrapper because newapi + sandbox-controller never see
//     a raw Keycloak ID token; they only see Catalyst-issued JWTs that
//     have already been distilled.
//   - The handover JWT's one-time-replay fields (iss/aud/jti/email_verified)
//     — those are RegisteredClaims + a small handover-specific struct in
//     auth_handover.go.
package auth

import (
	"github.com/golang-jwt/jwt/v5"
)

// Claims is the single source-of-truth shape for a Catalyst-issued access
// token. Every cross-service consumer (sandbox-controller, newapi bridge,
// MCP server) imports this type so a new claim added here propagates
// without per-service code edits.
//
// Wire field names match what existing catalyst-api JWT mint paths
// already emit (issueTokens in core/services/auth/handlers/handlers.go;
// auth_handover.go session-JWT path). Adding a field here is non-breaking
// as long as the new field is `omitempty` — old tokens parse fine.
//
// JWT signing method: HS256 for service-issued tokens (auth-service shared
// secret) and RS256 for handover JWTs (cloud-init-distributed RSA keypair).
// The two use the same field names, just different signers.
type Claims struct {
	// RegisteredClaims gives us iss/sub/aud/exp/iat/nbf/jti for free.
	// `sub` carries the user-id, `jti` is the per-token unique id used
	// for revocation (POST /auth/pat/{jti}/revoke — Wave 4).
	jwt.RegisteredClaims

	// Email — operator's primary email. Mirrors the Keycloak `email`
	// claim. Always populated on Catalyst-issued tokens.
	Email string `json:"email,omitempty"`

	// EmailVerified — true when Keycloak (or our magic-link verifier)
	// confirmed the address. Handover JWT path requires true.
	EmailVerified bool `json:"email_verified,omitempty"`

	// Role — legacy single-string role (`member`, `superadmin`,
	// `sovereign-admin`). Kept for backwards compatibility with the
	// pre-Wave-1b /auth/me handler. New consumers should prefer
	// .Capabilities and the .RealmRoles list under Catalyst Console
	// instead.
	Role string `json:"role,omitempty"`

	// OrgID — Organization slug (`acme`, `bankdhofar`, etc.). Wave 1b
	// gap: today this field is empty on every catalyst-api-issued JWT
	// because the Keycloak tenant-realm has no `org` mapper. The
	// configmap-tenant-realm.yaml + configmap-sovereign-realm.yaml
	// patches in this PR add the mapper; once a Sovereign rolls the
	// chart bump, fresh tokens carry the claim.
	//
	// MUST be the slug used everywhere in the Catalyst CRDs
	// (`metadata.name` on the Organization CR), NOT the human-readable
	// display name. The MCP server's `org_id` scoping check is an exact
	// string compare; case + whitespace must round-trip.
	OrgID string `json:"org_id,omitempty"`

	// Groups — Keycloak group paths the bearer holds (`/acme/admins`,
	// `/openova-internal/sre`). Catalyst-bootstrap's auth.Claims has
	// the same field; this is the cross-service mirror.
	Groups []string `json:"groups,omitempty"`

	// Capabilities — flat list of Sandbox + Catalyst capability strings
	// the bearer holds. Each entry is a single permission scope
	// (`sandbox:db:provision`, `sandbox:deploy:production`,
	// `marketplace:domain:byod`). The MCP server (`openova-sandbox-mcp`)
	// gates every tool call with an exact-string `slices.Contains` —
	// no role-graph traversal at call time, matching
	// products/sandbox/docs/architecture.md §3.
	//
	// Computed by core/services/auth at token-mint time from the
	// bearer's tier + per-Org grants. Cached in the token so each tool
	// call doesn't re-query Keycloak.
	Capabilities []string `json:"capabilities,omitempty"`

	// SovereignFQDN — populated on Sovereign-side sessions (set by
	// /auth/handover when the operator arrived from the wizard).
	// Empty on Catalyst-Zero (mothership) sessions.
	SovereignFQDN string `json:"sovereign_fqdn,omitempty"`

	// DeploymentID — populated alongside SovereignFQDN. The chroot
	// Sovereign Console reads this via /api/v1/sovereign/self.
	DeploymentID string `json:"deployment_id,omitempty"`

	// Typ — token-type discriminator. Today's emitters set this to:
	//
	//   "session"  — short-lived (15m) interactive session token
	//   "handover" — one-time RS256 handover JWT
	//   "pat"      — long-lived Personal Access Token issued by
	//                POST /auth/pat (Wave 1b — see core/services/auth)
	//
	// Consumers that mint sandbox-scoped tokens (sandbox-controller in
	// Wave 4) MUST set Typ="pat" so the audit trail can distinguish a
	// human-initiated session from an automated Sandbox-pod token.
	Typ string `json:"typ,omitempty"`
}

// HasCapability reports whether the bearer holds the named capability.
// Used by the MCP server and every Sandbox-aware HTTP handler.
//
// Returns false on a nil receiver so unauthenticated request paths can
// `if claims.HasCapability("...")` without a nil check.
func (c *Claims) HasCapability(want string) bool {
	if c == nil {
		return false
	}
	for _, got := range c.Capabilities {
		if got == want {
			return true
		}
	}
	return false
}

// HasGroup returns true when the bearer holds the named Keycloak group
// path. Leading "/" is optional on `want` so both `/acme/admins` and
// `acme/admins` match, absorbing the Keycloak v22→v24 path-format
// difference.
func (c *Claims) HasGroup(want string) bool {
	if c == nil {
		return false
	}
	trim := want
	if len(trim) > 0 && trim[0] == '/' {
		trim = trim[1:]
	}
	for _, g := range c.Groups {
		got := g
		if len(got) > 0 && got[0] == '/' {
			got = got[1:]
		}
		if got == trim {
			return true
		}
	}
	return false
}

// ContextKey is the unexported context-key type used by every package
// that installs Claims on a request context. Re-exported as a typed
// constant so middleware in different services binary-collide-safely
// on the same context key.
type ContextKey struct{}

// Key is the singleton context key. Use `ctx.Value(auth.Key)` to
// retrieve Claims after middleware has installed it.
//
// Conventional usage:
//
//	claims, _ := ctx.Value(auth.Key).(*Claims)
//	if !claims.HasCapability("sandbox:db:provision") { ... }
var Key = ContextKey{}
