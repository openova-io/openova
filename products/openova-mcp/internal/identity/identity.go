// Package identity resolves an MCP caller's identity from their bearer
// token into the SINGLE source-of-truth claim contract
// (core/services/shared/auth.Claims) and derives the (context, tier,
// scope) triple every RBAC decision in the MCP server consults.
//
// This is the "per-relevant-Keycloak identity resolution" of #3988 §4.2,
// reduced to the first-slice essentials:
//
//   - parse the compact JWT into auth.Claims (the SAME type the console
//   - catalyst-api parse — asserted by importing it, never redefining),
//   - choose the realm CONTEXT from the token issuer: an issuer ending in
//     `/realms/sovereign` (or carrying a sovereign-admin role) is the
//     Sovereign context; an issuer ending in `/realms/<org-slug>` (or any
//     token carrying an org_id) is that Organization's context,
//   - derive the TIER from the highest catalyst-<tier> realm role (or the
//     precomputed `tier`/legacy `role` claim),
//   - pin the SCOPE: Org context → the token's OrgID; Sovereign context →
//     the whole Sovereign.
//
// Signature verification (JWKS per realm) is deliberately OUT of this
// first slice — see VerifyMode. The slice's live-acceptance signs the
// test bearer with the Sovereign's own handover RS256 key and the caller
// supplies the matching public key, so the server verifies the signature
// it can verify and resolves identity from the verified claims. The full
// per-realm JWKS cache is a documented follow-up (#3988 §4.2).
package identity

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// Context is the realm context an MCP connection runs in. It selects the
// trusted realm, the pinned scope, and the filtered tool set (#3988 §4.3
// topology table).
type Context string

const (
	// ContextOrganization — the caller authenticated against an
	// Organization's Keycloak realm. Their data scope is pinned to that
	// Org's vCluster; the tool surface is the Org subset.
	ContextOrganization Context = "organization"

	// ContextSovereign — the caller authenticated against the Sovereign
	// realm as a sovereign-admin. Full Sovereign scope; full tool surface.
	ContextSovereign Context = "sovereign"
)

// Tier is the catalog tier the caller holds, lowest→highest. Mirrors the
// 5-tier ladder the console + catalyst-api use
// (viewer < developer < operator < admin < owner) plus the distinguished
// sovereign-admin which sits above owner for cross-Org reach.
type Tier int

const (
	TierNone Tier = iota
	TierViewer
	TierDeveloper
	TierOperator
	TierAdmin
	TierOwner
	TierSovereignAdmin
)

// String renders the canonical tier label.
func (t Tier) String() string {
	switch t {
	case TierViewer:
		return "viewer"
	case TierDeveloper:
		return "developer"
	case TierOperator:
		return "operator"
	case TierAdmin:
		return "admin"
	case TierOwner:
		return "owner"
	case TierSovereignAdmin:
		return "sovereign-admin"
	default:
		return "none"
	}
}

// AtLeast reports whether t is at or above the floor tier.
func (t Tier) AtLeast(floor Tier) bool { return t >= floor }

// Identity is the resolved caller. Every RBAC decision (tools/list filter
// + tools/call re-auth) consults this — derived ONCE per call from the
// validated claims so handlers never re-derive "is this user allowed?".
type Identity struct {
	// Claims is the verified, shared-contract claim set. Carried so
	// handlers + the facade can forward the caller's identity downstream
	// and so whoami can echo exactly what the server saw.
	Claims *sharedauth.Claims

	// Context is the realm context (organization | sovereign).
	Context Context

	// Tier is the highest catalog tier the caller holds.
	Tier Tier

	// OrgID is the pinned Organization scope in Org context (the slug used
	// on the Organization CR + as the vCluster scope). Empty in Sovereign
	// context.
	OrgID string

	// DeploymentID is the Sovereign deployment the caller's session is
	// bound to (handover JWT `deployment_id`). Used to address the
	// catalyst-api `/api/v1/sovereigns/{id}/…` route table.
	DeploymentID string

	// SovereignFQDN is the Sovereign primary FQDN (handover JWT
	// `sovereign_fqdn`). Surfaced via whoami.
	SovereignFQDN string

	// Email is the caller's primary email (echoed by whoami).
	Email string

	// RawBearer is the compact JWT the caller presented, retained so the
	// thin-facade tool handlers can forward the caller's IDENTITY verbatim
	// to the real catalyst-api endpoint (the endpoint's own authz is the
	// final word). Set by Resolve; never logged.
	RawBearer string
}

// VerifyMode selects how the bearer signature is verified for this slice.
type VerifyMode int

const (
	// VerifyRS256PublicKey verifies the bearer against a single RS256
	// public key (the Sovereign's handover-jwt public key). This is the
	// mode the first-slice live-acceptance uses.
	VerifyRS256PublicKey VerifyMode = iota

	// VerifyHS256Secret verifies the bearer against a single HS256 shared
	// secret (the catalyst-api service-token signing key). Mirrors the
	// sandbox MCP's call-gate so an in-cluster service token is accepted.
	VerifyHS256Secret

	// VerifyInsecureNoSignature SKIPS signature verification and resolves
	// identity from the unverified claims. ONLY for unit tests + a
	// trusted in-cluster transport that already terminated auth upstream.
	// Production binaries MUST NOT select this.
	VerifyInsecureNoSignature
)

// Resolver turns a raw bearer into a resolved Identity. Constructed once
// at server start with the trusted key/secret + verify mode.
type Resolver struct {
	mode      VerifyMode
	rsaPub    *rsa.PublicKey
	hmacKey   []byte
	pinnedCtx Context // when non-empty, forces the connection context (per-Org-MCP vs Sovereign-MCP topology).

	// expectedIssuer — when non-empty, the bearer's `iss` claim MUST equal
	// it exactly or Resolve rejects. This is the instance-level realm pin
	// of #3988 §4.3 ("mode selects the TRUSTED realm"): a per-Org instance
	// pins its own realm issuer URL; the Sovereign instance pins the
	// Sovereign issuer. The full per-realm JWKS cache remains the §4.2
	// follow-up — this pin is the slice-level issuer trust boundary.
	expectedIssuer string

	// orgPin — when non-empty, the resolved OrgID MUST equal it or Resolve
	// rejects. A per-Org MCP instance sets this so a token minted for a
	// DIFFERENT Organization (same signer, same realm shape) cannot reach
	// this instance's surface at all (#3988 §4.3 "pinned scope").
	orgPin string
}

// WithExpectedIssuer pins the exact `iss` claim value this resolver trusts.
// Empty = no issuer pin (any issuer the signature verifies for).
func (r *Resolver) WithExpectedIssuer(iss string) *Resolver {
	r.expectedIssuer = strings.TrimSpace(iss)
	return r
}

// WithOrgPin pins the exact Organization scope this resolver serves.
// Empty = no org pin (the token's own org_id is the scope).
func (r *Resolver) WithOrgPin(org string) *Resolver {
	r.orgPin = strings.TrimSpace(org)
	return r
}

// NewRS256Resolver builds a resolver that verifies bearers against the
// supplied RS256 public key. pinnedCtx (optional) forces the connection
// context per the #3988 §4.3 topology table — a per-Org MCP instance pins
// ContextOrganization so even a Sovereign token cannot widen the surface.
func NewRS256Resolver(pub *rsa.PublicKey, pinnedCtx Context) *Resolver {
	return &Resolver{mode: VerifyRS256PublicKey, rsaPub: pub, pinnedCtx: pinnedCtx}
}

// NewHS256Resolver builds a resolver that verifies bearers against the
// supplied HS256 secret.
func NewHS256Resolver(secret []byte, pinnedCtx Context) *Resolver {
	return &Resolver{mode: VerifyHS256Secret, hmacKey: secret, pinnedCtx: pinnedCtx}
}

// NewInsecureResolver builds a resolver that does NOT verify signatures.
// Tests + trusted-transport only.
func NewInsecureResolver(pinnedCtx Context) *Resolver {
	return &Resolver{mode: VerifyInsecureNoSignature, pinnedCtx: pinnedCtx}
}

// Resolve parses + verifies the bearer and derives the Identity. Returns
// an error (→ MCP 401) when the bearer is missing, malformed, or its
// signature/expiry fails.
func (r *Resolver) Resolve(rawBearer string) (*Identity, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(rawBearer, "Bearer "))
	if raw == "" {
		return nil, errors.New("identity: empty bearer")
	}

	claims := &sharedauth.Claims{}
	parser := jwt.NewParser()

	var err error
	switch r.mode {
	case VerifyRS256PublicKey:
		_, err = parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("identity: unexpected signing method %v (want RS256)", t.Header["alg"])
			}
			return r.rsaPub, nil
		})
	case VerifyHS256Secret:
		_, err = parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("identity: unexpected signing method %v (want HS256)", t.Header["alg"])
			}
			return r.hmacKey, nil
		})
	case VerifyInsecureNoSignature:
		_, _, err = parser.ParseUnverified(raw, claims)
	default:
		return nil, errors.New("identity: unknown verify mode")
	}
	if err != nil {
		return nil, fmt.Errorf("identity: bearer rejected: %w", err)
	}

	if r.expectedIssuer != "" {
		iss, _ := claims.GetIssuer()
		if strings.TrimSpace(iss) != r.expectedIssuer {
			return nil, fmt.Errorf("identity: issuer %q not trusted by this instance (want %q)", iss, r.expectedIssuer)
		}
	}

	id, err := r.fromClaims(claims)
	if err != nil {
		return nil, err
	}
	id.RawBearer = raw
	return id, nil
}

// fromClaims derives the (context, tier, scope) triple from already-parsed
// claims. Exported-via-Resolve only; split out so unit tests can exercise
// the derivation without minting a signed token.
func (r *Resolver) fromClaims(claims *sharedauth.Claims) (*Identity, error) {
	id := &Identity{
		Claims:        claims,
		OrgID:         claims.OrgID,
		DeploymentID:  claims.DeploymentID,
		SovereignFQDN: claims.SovereignFQDN,
		Email:         claims.Email,
		Tier:          deriveTier(claims),
	}

	// Realm context: an explicit sovereign-admin role / tier wins; else a
	// token carrying an org_id is an Org context; else (issuer ends
	// /realms/sovereign) Sovereign.
	id.Context = deriveContext(claims, id.Tier)

	// A per-instance pin (per-Org MCP vs Sovereign MCP) is the final word
	// on context so a Sovereign token presented to a per-Org instance
	// cannot widen the surface beyond that Org. When the pin forces Org
	// context the token MUST carry the matching org_id.
	if r.pinnedCtx != "" {
		if r.pinnedCtx == ContextOrganization && id.OrgID == "" {
			return nil, errors.New("identity: per-Org MCP requires an org-scoped token (no org_id claim)")
		}
		id.Context = r.pinnedCtx
	}

	if id.Context == ContextOrganization && id.OrgID == "" {
		return nil, errors.New("identity: organization context requires an org_id claim")
	}

	// A per-Org instance's org pin is the final scope word: a token minted
	// for a DIFFERENT Org (even by the same trusted signer) is rejected
	// outright — it never sees this instance's tool surface (#3988 §4.3).
	if r.orgPin != "" && id.OrgID != r.orgPin {
		return nil, fmt.Errorf("identity: token org scope %q does not match this instance's pinned org %q", id.OrgID, r.orgPin)
	}
	return id, nil
}

// deriveTier picks the highest catalog tier the claims express. Order of
// precedence: an explicit sovereign-admin signal (role/realm-role) →
// TierSovereignAdmin; else the highest catalyst-<tier> realm role; else
// the precomputed `tier`/legacy `role` string.
func deriveTier(claims *sharedauth.Claims) Tier {
	if isSovereignAdmin(claims) {
		return TierSovereignAdmin
	}
	best := TierNone
	for _, role := range claims.Groups { // groups can also carry tier hints; harmless if absent
		if t := tierFromLabel(role); t > best {
			best = t
		}
	}
	// The shared Claims type does not carry realm_access directly; the
	// Keycloak realm roles are projected into Capabilities + the `role`
	// claim by the mint path. We accept both the catalyst-<tier> role
	// strings (when present in Capabilities) and the flattened `role`.
	for _, cap := range claims.Capabilities {
		if t := tierFromLabel(cap); t > best {
			best = t
		}
	}
	if t := tierFromLabel(claims.Role); t > best {
		best = t
	}
	// The precomputed `tier` claim is the authoritative tier signal on a
	// catalyst-api-minted session (HandlePinVerify stamps it). It carries
	// the standard ladder (viewer…owner) AND the distinguished Org-scoped
	// marker `org-admin` — the tier a customer session minted on an Org
	// console holds. tierFromLabel maps `org-admin` → TierAdmin so an
	// Org-scoped customer is the admin of THEIR OWN Org (satisfying the
	// create_application MinTier=Admin gate) while carrying NO Sovereign
	// signal — deriveContext keeps them in ContextOrganization, so the
	// #4110 own-org confinement holds end-to-end.
	if t := tierFromLabel(claims.Tier); t > best {
		best = t
	}
	return best
}

// tierFromLabel maps a single role/label string to a Tier. Accepts both
// the bare tier name (`owner`) and the catalyst-prefixed realm role
// (`catalyst-owner`).
func tierFromLabel(s string) Tier {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "catalyst-")
	switch s {
	case "owner":
		return TierOwner
	case "org-admin":
		// The Org-scoped customer-session marker (auth.OrgScopedTier). An
		// Org-scoped session is the admin of its OWN Organization — the top
		// of that Org's authority (it can install/manage Applications in its
		// own Org) while holding ZERO Sovereign authority. Map it to
		// TierAdmin so it satisfies the Org-own write gates (create_application
		// MinTier=Admin); it never widens past the Org because deriveContext
		// keeps an org_id-bearing token in ContextOrganization.
		return TierAdmin
	case "admin":
		return TierAdmin
	case "operator":
		return TierOperator
	case "developer":
		return TierDeveloper
	case "viewer":
		return TierViewer
	default:
		return TierNone
	}
}

// isSovereignAdmin reports whether the claims express the distinguished
// sovereign-admin authority (the handover JWT `role: sovereign-admin`, the
// `sovereign-admins` realm role/group, or the legacy `superadmin`).
func isSovereignAdmin(claims *sharedauth.Claims) bool {
	role := strings.ToLower(strings.TrimSpace(claims.Role))
	if role == "sovereign-admin" || role == "superadmin" {
		return true
	}
	for _, g := range claims.Groups {
		gg := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(g), "/"))
		if gg == "sovereign-admins" || gg == "sovereign-admin" {
			return true
		}
	}
	for _, c := range claims.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), "sovereign-admins") {
			return true
		}
	}
	return false
}

// deriveContext picks the realm context. A sovereign-admin tier always
// resolves Sovereign; a token carrying an org_id resolves that Org;
// otherwise an issuer ending in /realms/sovereign resolves Sovereign.
func deriveContext(claims *sharedauth.Claims, tier Tier) Context {
	if tier == TierSovereignAdmin {
		return ContextSovereign
	}
	if claims.OrgID != "" {
		return ContextOrganization
	}
	iss, _ := claims.GetIssuer()
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(iss)), "/realms/sovereign") {
		return ContextSovereign
	}
	// Default: organization context (a non-admin token without an org_id
	// is under-scoped — fromClaims rejects it with a clear error).
	return ContextOrganization
}
