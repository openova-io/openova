// Per-Org openova-MCP Catalyst-bearer seeding (issue #4276 / #4111 — hop 7).
//
// Why this exists:
//
//	Pillar 4's terminal proof is the agentic mutation: the per-Org
//	bp-agenity dashboard spawns a solo claude-code agent which calls the
//	`openova` MCP's `create_application` tool, and a new Application is
//	created in the Org. The MCP server (products/openova-mcp) is injected
//	into every spawned agent (CHEPHERD_EXTRA_MCP_JSON, agenity
//	statefulset.yaml) and `create_application` is wired to
//	POST /api/v1/org/applications. But the MCP must present a Catalyst
//	SESSION BEARER to catalyst-api, resolved from EITHER a per-call
//	`_auth.token` argument OR the `OPENOVA_MCP_BEARER` env fallback
//	(products/openova-mcp/cmd/openova-mcp/main.go). On a fresh funnel Org
//	NEITHER is supplied:
//	  - `_auth.token` is never injected (chepherd's #4010 seam merges the
//	    MCP server DEFINITION, it does not forward a per-call bearer; no
//	    code anywhere forwards the User's session JWT into the call).
//	  - `OPENOVA_MCP_BEARER` is only projected when the chart value
//	    `openovaMCP.bearerSecret.name` is non-empty, and the org-gitops
//	    emitter `orgTenantBPAgenity` never set it.
//	Result: every `create_application` from a fresh Org's agent hits the
//	MCP with no bearer → -32001 unauthenticated — even after the Anthropic
//	key (#4277/#4303) is seeded (that only lets claude-code talk to
//	ANTHROPIC, not to CATALYST). This file is the missing producer.
//
//	Coupled gap 7b — the MCP also needs the RS256 PUBLIC KEY (PEM) to
//	VERIFY the bearer it is handed (OPENOVA_MCP_RS256_PUBKEY_PEM,
//	verify=rs256). The chart's default rs256PubkeySecret points at a host
//	`catalyst-system` Secret that is absent in the per-Org namespace
//	(optional:true ⇒ unset, #4228). So we seed the verify pubkey-PEM
//	ALONGSIDE the bearer in the SAME openbao path and project it from the
//	SAME ExternalSecret — the bearer and the key that verifies it travel
//	together, sidestepping the JWK-vs-PEM cross-namespace reflection
//	mismatch entirely (the only published mirror,
//	catalyst-handover-jwt-public, holds a JWK; the MCP wants PKIX PEM).
//
// The bearer is an Org-SCOPED session JWT, NOT a sovereign-admin one:
//
//	The MCP runs with OPENOVA_MCP_CONTEXT=organization (chart default).
//	The bearer carries the byte-for-byte shape HandlePinVerify /
//	auth_org_handover mint for an Org-scoped customer session (auth.go
//	#4110): tier=org-admin (auth.OrgScopedTier), role=openova-user,
//	typ=session, org+org_id=<slug>, realm_access.roles=[org-admin],
//	iss=<CATALYST_PIN_ISSUER>, RS256-signed by the Sovereign's handover
//	signer (the SAME key whose pubkey the MCP verifies against). This
//	resolves in the MCP to (context=organization, tier=admin,
//	org_id=<org>) → create_application (MinTier=Admin) is allowed AND
//	routes to the own-org POST /api/v1/org/applications path
//	(OrgScopeGuard-allowlisted, #4116); cross-Org create stays forbidden
//	(resolveCreateTargetOrg). A sovereign-admin bearer would resolve
//	context=sovereign and 403 under the #4110 host-scope guard — the exact
//	wedge values.yaml warns about.
//
// It deliberately carries NO `deployment_id` claim (#5516):
//
//	Neither HandlePinVerify nor auth_org_handover stamps `deployment_id` on
//	an Org-scoped session — only the mothership HANDOVER path (auth_handover.go)
//	does, for a sovereign-admin. Adding one here would BREAK the byte-for-byte
//	parity this mint is contracted to hold, and it would fix nothing: the
//	openova-MCP tools that once demanded the claim used it to address the
//	Sovereign-wide seam `/api/v1/sovereigns/{id}/applications`, which
//	OrgScopeGuard 403s for ANY tier=org-admin session regardless of its claims
//	(the path is not in orgSafePathPrefixes — see
//	TestOrgScopeGuard_OrgScoped_DeniesDeploymentAddressedApplicationRoutes).
//	Stamping the claim would only convert the MCP's "no deployment binding"
//	error into an upstream `org-scoped-forbidden` 403.
//
//	The fix therefore lives in the MCP facade: Org context reads the own-org
//	seam GET /api/v1/org/applications (allowlisted, namespace resolved
//	server-side from X-Tenant-Host), mirroring what create_application already
//	does with POST /api/v1/org/applications. Do NOT re-add a deployment_id
//	claim here to "unblock" an MCP tool — route the tool instead.
//
// Why this path (NOT bare secret/agenity/...):
//
//	Identical reasoning to the Anthropic seed (sovereign_anthropic_seed.go):
//	the catalyst-api Pod holds the write-capable `catalyst-api-write`
//	OpenBao role scoped to `secret/{data,metadata}/catalyst/*`, while the
//	`external-secrets` role vault-region1 authenticates with is read-only.
//	So we write under the `catalyst/` prefix the platform can ALREADY
//	write — zero new OpenBao policy/role. UNLIKE the cluster-shared
//	Anthropic path, the bearer is PER-ORG (it carries the Org slug), so the
//	path is per-Org: `secret/catalyst/agenity/<slug>/mcp-bearer`.
//
// TTL + re-mint policy:
//
//	A session JWT expires (pinSessionTTL = 8h). The agenity StatefulSet is
//	long-lived, so an 8h bearer would go stale within the day. We therefore
//	mint a LONG-LIVED service bearer (mcpBearerTTL = 1 year) for this
//	headless Org-service identity, AND re-seed it idempotently on every Org
//	reconcile (runOrganizationPipeline calls this on every create/reconcile,
//	exactly like seedAnthropicToken). PutKVv2 overwrites, so each reconcile
//	refreshes the token's expiry window — the bearer never ages out while
//	the Org is actively reconciled, and a one-year floor covers quiescent
//	gaps between reconciles. This mirrors the #4303 idempotent-re-seed
//	pattern (the Anthropic OAuth blob is likewise refreshed every reconcile)
//	and the OAuth-expiry pre-flight class #4111 documents.
//
// When this runs:
//
//	runOrganizationPipeline calls seedMCPBearer right after Step 1 (per-Org
//	overlay committed), beside seedAnthropicToken. A nil h.openbao
//	(Catalyst-Zero orchestrator) or nil h.handoverSigner is a non-fatal
//	skip — the agenity HR still installs; only the MCP create_application
//	path stays unauthenticated until the path is seeded.
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): never logs the bearer
// or the PEM — only byte lengths + outcome class.
package handler

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// mcpBearerMountPath — the OpenBao KV-v2 mount the producer writes and the
// agenity ExternalSecret reads. The per-Org secret PATH is built from the
// Org slug (mcpBearerSecretPath); both are fixed-by-contract with the
// agenity chart's mcpBearer.externalSecret.remoteKey and the org-gitops
// emitter override (organization_gitops.go). The `catalyst/` prefix is the
// ONLY sub-tree a Sovereign can write (see the file doc comment).
const mcpBearerMountPath = "secret"

// mcpBearerSecretPath builds the per-Org OpenBao KV-v2 path for the slug.
// Per-Org (unlike the cluster-shared Anthropic path) because the bearer
// carries the Org slug in its claims.
func mcpBearerSecretPath(slug string) string {
	return "catalyst/agenity/" + slug + "/mcp-bearer"
}

// mcpBearerKVProperties — the KV-v2 field names the agenity ExternalSecret
// reads. `bearer` = the Org-scoped session JWT the MCP forwards to
// catalyst-api; `pubkeyPem` = the RS256 PKIX public key (PEM) the MCP
// verifies that bearer with (gap 7b). Fixed-by-contract with the chart's
// mcpBearer.externalSecret remote-property values.
const (
	mcpBearerKVBearerProperty    = "bearer"
	mcpBearerKVPubkeyPEMProperty = "pubkeyPem"
)

// mcpBearerTTL — the validity window of the minted Org-service bearer. A
// long horizon (1 year) for this headless, idempotently-re-seeded service
// identity: the 8h interactive pinSessionTTL would age out within the day
// on a long-lived StatefulSet. Re-seeded every Org reconcile (PutKVv2
// overwrites), so the live token's expiry is continuously pushed forward;
// the 1y floor covers quiescent gaps between reconciles. See the file doc
// comment §TTL.
const mcpBearerTTL = 365 * 24 * time.Hour

// MCPBearerSeedOutcome — terminal classification of one seed attempt.
type MCPBearerSeedOutcome string

const (
	MCPBearerSeedOutcomeSeeded       MCPBearerSeedOutcome = "seeded"
	MCPBearerSeedOutcomeSkippedNoBao MCPBearerSeedOutcome = "skipped-no-openbao"
	MCPBearerSeedOutcomeSkippedNoSig MCPBearerSeedOutcome = "skipped-no-signer"
	MCPBearerSeedOutcomeSkippedNoOrg MCPBearerSeedOutcome = "skipped-no-org-slug"
	MCPBearerSeedOutcomeMintFailure  MCPBearerSeedOutcome = "mint-failure"
	MCPBearerSeedOutcomeWriteFailure MCPBearerSeedOutcome = "write-failure"
)

// mintOrgScopedMCPBearer mints the Org-scoped session JWT the openova-MCP
// forwards to catalyst-api. Exported-for-test-via-package: the claim shape
// MUST match what HandlePinVerify / auth_org_handover mint (auth.go #4110)
// so the MCP + OrgScopeGuard resolve it identically.
//
// `slug` is the Org slug (rec.Subdomain); `email` is the Org owner's
// address (rec.AdminEmail), falling back to a synthetic service subject so
// the claim is never empty.
func mintOrgScopedMCPBearer(signer customClaimsSigner, slug, email string) (string, error) {
	subject := strings.TrimSpace(email)
	if subject == "" {
		// Headless Org-service identity — no human owner address on the
		// record. A stable, Org-scoped synthetic subject keeps the claim
		// non-empty and audit-legible without inventing a real mailbox.
		subject = "openova-mcp@" + slug
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":            pinIssuer,
		"sub":            subject,
		"email":          subject,
		"email_verified": true,
		"role":           pinSessionRole, // "openova-user"
		"tier":           orgScopedTier,  // "org-admin" (auth.OrgScopedTier)
		"realm_access": map[string]any{
			"roles": []string{orgScopedRealmRole}, // ["org-admin"]
		},
		"iat":    now.Unix(),
		"exp":    now.Add(mcpBearerTTL).Unix(),
		"jti":    uuid.NewString(),
		"typ":    "session",
		"org":    slug,
		"org_id": slug,
		// NO "deployment_id" — see the file doc comment §"It deliberately
		// carries NO deployment_id claim (#5516)". The key set here is locked to
		// HandlePinVerify's Org-scoped session by
		// Test_mintOrgScopedMCPBearer_ClaimKeySetMatchesPinVerifyOrgSession.
	}
	return signer.SignCustomClaims(claims)
}

// orgScopedSessionClaimKeys is the EXACT claim key set an Org-scoped Catalyst
// session carries, as minted by HandlePinVerify (auth.go, org branch) and
// auth_org_handover (org_handover.go). mintOrgScopedMCPBearer above MUST
// produce this same set — the MCP + OrgScopeGuard resolve the seeded service
// bearer and an interactive Org-console session through identical code, so a
// divergence means the headless path is being authorized on a shape the
// interactive path never produces.
//
// `deployment_id` is absent by design (#5516): an Org-scoped session is not
// deployment-bound. Only the mothership handover mints that claim, for a
// sovereign-admin.
var orgScopedSessionClaimKeys = []string{
	"iss", "sub", "email", "email_verified", "role", "tier",
	"realm_access", "iat", "exp", "jti", "typ", "org", "org_id",
}

// customClaimsSigner is the narrow surface this producer needs from the
// handover signer — kept as an interface so the mint can be unit-tested
// without a real RSA key path. *handoverjwt.Signer satisfies it.
type customClaimsSigner interface {
	SignCustomClaims(claims jwt.Claims) (string, error)
}

// handoverSignerPubkeyPEM returns the signer's RS256 public key as a PKIX
// PEM block — the exact format the openova-MCP's
// OPENOVA_MCP_RS256_PUBKEY_PEM expects (x509.ParsePKIXPublicKey, see
// products/openova-mcp/cmd/openova-mcp/main.go). The MCP verifies caller
// bearers against THIS key; it must be the public half of the SAME signer
// that minted the bearer above, which is guaranteed because both come from
// h.handoverSigner.
func handoverSignerPubkeyPEM(h *Handler) (string, error) {
	if h.handoverSigner == nil {
		return "", fmt.Errorf("mcp bearer seed: handover signer unavailable")
	}
	pub, err := h.handoverSigner.PublicRSAKey()
	if err != nil {
		return "", fmt.Errorf("mcp bearer seed: read signer pubkey: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("mcp bearer seed: marshal PKIX pubkey: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return string(pemBytes), nil
}

// seedMCPBearer mints an Org-scoped Catalyst session bearer + the RS256
// verify pubkey (PEM) and writes them to the per-Org OpenBao path so the
// Org's agenity ExternalSecret materialises OPENOVA_MCP_BEARER (and the
// pubkey the MCP verifies it with) — closing #4276 hop 7/7b. Idempotent:
// PutKVv2 overwrites, so every Org reconcile refreshes the bearer's expiry
// window.
//
// Returns the terminal outcome so the caller can emit an SSE / log line.
// NEVER returns an error that fails the Org pipeline — a signer/OpenBao gap
// must not block the rest of the Org (the agenity HR still installs; only
// its MCP create_application path stays unauthenticated until seeded). The
// outcome is surfaced loudly instead.
func (h *Handler) seedMCPBearer(ctx context.Context, slug, ownerEmail string) MCPBearerSeedOutcome {
	slug = strings.TrimSpace(slug)

	// Catalyst-Zero (the orchestrator) has no in-cluster OpenBao client and
	// is never itself an agenity host — skip without noise. Production
	// Sovereign catalyst-api always has h.openbao wired.
	if h.openbao == nil {
		if h.log != nil {
			h.log.Debug("mcp bearer seed: skipped — no OpenBao client (orchestrator / Catalyst-Zero)")
		}
		return MCPBearerSeedOutcomeSkippedNoBao
	}
	if h.handoverSigner == nil {
		if h.log != nil {
			h.log.Warn("mcp bearer seed: SKIPPED — handover signer unavailable; agenity MCP create_application will stay unauthenticated until a bearer is seeded",
				"openbaoPath", mcpBearerMountPath+"/"+mcpBearerSecretPath(slug),
			)
		}
		return MCPBearerSeedOutcomeSkippedNoSig
	}
	if slug == "" {
		// No Org slug ⇒ can't scope the bearer; refuse to seed a path with
		// an empty/cross-Org claim (the resolveCreateTargetOrg guard the
		// MCP relies on would otherwise be defeated).
		if h.log != nil {
			h.log.Warn("mcp bearer seed: SKIPPED — empty Org slug; cannot scope an Org bearer")
		}
		return MCPBearerSeedOutcomeSkippedNoOrg
	}

	// The org owner email comes from the record at the call site; the mint
	// falls back to a synthetic Org-scoped subject when empty.
	bearer, err := mintOrgScopedMCPBearer(h.handoverSigner, slug, ownerEmail)
	if err != nil {
		if h.log != nil {
			h.log.Error("mcp bearer seed: mint failed", "slug", slug, "err", err)
		}
		return MCPBearerSeedOutcomeMintFailure
	}

	pubkeyPEM, err := handoverSignerPubkeyPEM(h)
	if err != nil {
		if h.log != nil {
			h.log.Error("mcp bearer seed: pubkey PEM failed", "slug", slug, "err", err)
		}
		return MCPBearerSeedOutcomeMintFailure
	}

	// KV-v2 payload. Field names MUST match the agenity ExternalSecret's
	// remoteRef.property values (bearer + pubkeyPem).
	data := map[string]any{
		mcpBearerKVBearerProperty:    bearer,
		mcpBearerKVPubkeyPEMProperty: pubkeyPEM,
	}

	path := mcpBearerSecretPath(slug)
	if err := h.openbao.PutKVv2(ctx, mcpBearerMountPath, path, data); err != nil {
		if h.log != nil {
			h.log.Error("mcp bearer seed: OpenBao write failed",
				"openbaoPath", mcpBearerMountPath+"/"+path,
				"err", err,
			)
		}
		return MCPBearerSeedOutcomeWriteFailure
	}

	if h.log != nil {
		// Per docs/PRINCIPLES.md #10: byte lengths + claim scope only,
		// never the token/PEM plaintext.
		h.log.Info("mcp bearer seed: wrote Org-scoped Catalyst bearer + verify pubkey so the agenity MCP create_application authenticates",
			"openbaoPath", mcpBearerMountPath+"/"+path,
			"slug", slug,
			"tier", orgScopedTier,
			"bearerBytes", len(bearer),
			"pubkeyPemBytes", len(pubkeyPEM),
			"ttl", mcpBearerTTL.String(),
		)
	}
	return MCPBearerSeedOutcomeSeeded
}
