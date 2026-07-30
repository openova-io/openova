package handler

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// newTestSigner builds a real handover signer over a fresh RSA keypair so
// the mint + PEM emission exercise the production path (no mock signer).
func newTestSigner(t *testing.T) *handoverjwt.Signer {
	t.Helper()
	priv, _, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	s, err := handoverjwt.New(priv, "https://console.t99.omani.works", 8*time.Hour)
	if err != nil {
		t.Fatalf("handoverjwt.New: %v", err)
	}
	return s
}

// Test_seedMCPBearer_SeedsExpectedPathAndFields verifies the producer writes
// the per-Org secret/catalyst/agenity/<slug>/mcp-bearer path with BOTH the
// bearer and pubkeyPem properties the agenity ExternalSecret consumes
// (#4276). The path + field names are the contract with the chart's
// externalsecret-mcp-bearer.yaml + the org-gitops emitter — a drift here
// re-breaks hop 7.
func Test_seedMCPBearer_SeedsExpectedPathAndFields(t *testing.T) {
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}
	h.handoverSigner = newTestSigner(t)

	outcome := h.seedMCPBearer(context.Background(), "acme", "owner@acme.omani.homes")
	if outcome != MCPBearerSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q", outcome, MCPBearerSeedOutcomeSeeded)
	}

	// KV-v2 PUT path is /v1/<mount>/data/<secretPath>.
	wantPath := "/v1/secret/data/catalyst/agenity/acme/mcp-bearer"
	srv.mu.Lock()
	gotPath := srv.lastURL
	srv.mu.Unlock()
	if gotPath != wantPath {
		t.Errorf("PUT path = %q, want %q", gotPath, wantPath)
	}

	bearer, ok := srv.dataField(mcpBearerKVBearerProperty)
	if !ok || bearer == "" {
		t.Fatalf("bearer field missing/empty (present=%v)", ok)
	}
	pubkeyPEM, ok := srv.dataField(mcpBearerKVPubkeyPEMProperty)
	if !ok || pubkeyPEM == "" {
		t.Fatalf("pubkeyPem field missing/empty (present=%v)", ok)
	}

	// The seeded pubkey MUST parse exactly the way the openova-MCP parses
	// OPENOVA_MCP_RS256_PUBKEY_PEM (pem.Decode → x509.ParsePKIXPublicKey →
	// *rsa.PublicKey), else the MCP cannot verify the bearer (gap 7b).
	block, _ := pem.Decode([]byte(pubkeyPEM))
	if block == nil {
		t.Fatalf("seeded pubkeyPem is not a PEM block")
	}
	parsed, perr := x509.ParsePKIXPublicKey(block.Bytes)
	if perr != nil {
		t.Fatalf("seeded pubkeyPem failed PKIX parse (MCP would reject): %v", perr)
	}
	rsaPub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("seeded pubkeyPem is not an RSA key")
	}

	// The seeded pubkey MUST verify the seeded bearer — they are the public
	// half + signature of the SAME signer (the property the whole hop relies
	// on). Parse the bearer with the seeded key.
	tok, verr := jwt.Parse(bearer, func(tk *jwt.Token) (any, error) {
		if _, methodOK := tk.Method.(*jwt.SigningMethodRSA); !methodOK {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return rsaPub, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if verr != nil || tok == nil || !tok.Valid {
		t.Fatalf("seeded bearer did NOT verify against seeded pubkey: %v", verr)
	}
}

// Test_mintOrgScopedMCPBearer_ClaimShape verifies the bearer carries the
// EXACT Org-scoped session shape the MCP + OrgScopeGuard resolve to
// (context=organization, tier=admin, org_id=<slug>) — tier=org-admin,
// role=openova-user, typ=session, org+org_id=<slug>, realm_access roles
// [org-admin], iss=pinIssuer, a long (service) TTL. A sovereign-admin shape
// here would 403 under the #4110 host-scope guard.
func Test_mintOrgScopedMCPBearer_ClaimShape(t *testing.T) {
	signer := newTestSigner(t)

	raw, err := mintOrgScopedMCPBearer(signer, "acme", "owner@acme.omani.homes")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	pub, _ := signer.PublicRSAKey()
	claims := jwt.MapClaims{}
	if _, perr := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return pub, nil },
		jwt.WithValidMethods([]string{"RS256"})); perr != nil {
		t.Fatalf("parse minted bearer: %v", perr)
	}

	wantStr := map[string]string{
		"tier":   orgScopedTier, // "org-admin"
		"role":   pinSessionRole, // "openova-user"
		"typ":    "session",
		"org":    "acme",
		"org_id": "acme",
		"iss":    pinIssuer,
		"sub":    "owner@acme.omani.homes",
		"email":  "owner@acme.omani.homes",
	}
	for k, want := range wantStr {
		got, _ := claims[k].(string)
		if got != want {
			t.Errorf("claim %q = %q, want %q", k, got, want)
		}
	}

	// realm_access.roles must be [org-admin] so the OrgScopeGuard resolves
	// the org-scoped surface.
	ra, ok := claims["realm_access"].(map[string]any)
	if !ok {
		t.Fatalf("realm_access missing or wrong type: %T", claims["realm_access"])
	}
	roles, ok := ra["roles"].([]any)
	if !ok || len(roles) != 1 || roles[0] != orgScopedRealmRole {
		t.Errorf("realm_access.roles = %v, want [%q]", ra["roles"], orgScopedRealmRole)
	}

	// email_verified must be true.
	if v, _ := claims["email_verified"].(bool); !v {
		t.Errorf("email_verified = false, want true")
	}

	// The bearer must be long-lived (service identity), not the 8h
	// interactive session — assert exp is well beyond 8h from now.
	expF, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp missing/wrong type")
	}
	if remaining := time.Until(time.Unix(int64(expF), 0)); remaining < 30*24*time.Hour {
		t.Errorf("bearer TTL too short for a long-lived service identity: %s remaining", remaining)
	}
}

// Test_mintOrgScopedMCPBearer_ClaimKeySetMatchesPinVerifyOrgSession locks the
// seeded service bearer's claim key set to orgScopedSessionClaimKeys — the set
// HandlePinVerify (auth.go, Org branch) and auth_org_handover mint for an
// interactive Org-console session.
//
// The load-bearing assertion is the EXACT-SET match, in both directions: a
// missing key fails, and so does an EXTRA one. That second direction is the
// #5516 guard. `deployment_id` is absent from an Org session on every mint
// path, and the deployment-addressed seam it would address is 403'd for
// tier=org-admin regardless (see
// TestOrgScopeGuard_OrgScoped_DeniesDeploymentAddressedApplicationRoutes), so
// re-adding it here would break parity while fixing nothing. If an MCP tool
// needs Org data, route it to the own-org seam instead of widening this token.
func Test_mintOrgScopedMCPBearer_ClaimKeySetMatchesPinVerifyOrgSession(t *testing.T) {
	signer := newTestSigner(t)

	raw, err := mintOrgScopedMCPBearer(signer, "acme", "owner@acme.omani.homes")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	pub, _ := signer.PublicRSAKey()
	claims := jwt.MapClaims{}
	if _, perr := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return pub, nil },
		jwt.WithValidMethods([]string{"RS256"})); perr != nil {
		t.Fatalf("parse minted bearer: %v", perr)
	}

	// Vacuity guard: without a decoded, populated claim map every set
	// comparison below would pass on nothing. Assert the map is non-empty AND
	// carries a known-present control claim before comparing sets.
	if len(claims) == 0 {
		t.Fatal("minted bearer decoded to an EMPTY claim map — the set assertions would pass vacuously")
	}
	if got, _ := claims["org_id"].(string); got != "acme" {
		t.Fatalf("control claim org_id = %q, want acme (the parse produced an unusable map)", got)
	}

	want := map[string]bool{}
	for _, k := range orgScopedSessionClaimKeys {
		want[k] = true
	}

	// Direction 1 — every contracted claim is present.
	for k := range want {
		if _, ok := claims[k]; !ok {
			t.Errorf("minted bearer is MISSING contracted claim %q", k)
		}
	}
	// Direction 2 — no claim beyond the contract. This is what fails if
	// deployment_id (or any other claim an Org console session never carries)
	// is stamped onto the service bearer.
	for k := range claims {
		if !want[k] {
			t.Errorf("minted bearer carries claim %q, which an Org-scoped session (HandlePinVerify / auth_org_handover) never mints — parity broken", k)
		}
	}

	// Spelled out for the reader: the claim the openova-MCP's requireDeployment
	// used to demand is deliberately absent, and the MCP no longer asks for it
	// in Org context.
	if _, present := claims["deployment_id"]; present {
		t.Error("deployment_id must NOT be stamped on an Org-scoped bearer (#5516) — the deployment-addressed seam is 403'd for tier=org-admin anyway; route the MCP tool to /api/v1/org/applications instead")
	}
}

// Test_mintOrgScopedMCPBearer_SyntheticSubjectWhenNoEmail verifies the
// headless-Org fallback: no owner email ⇒ a stable Org-scoped synthetic
// subject (never an empty claim).
func Test_mintOrgScopedMCPBearer_SyntheticSubjectWhenNoEmail(t *testing.T) {
	signer := newTestSigner(t)
	raw, err := mintOrgScopedMCPBearer(signer, "acme", "")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	pub, _ := signer.PublicRSAKey()
	claims := jwt.MapClaims{}
	if _, perr := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return pub, nil },
		jwt.WithValidMethods([]string{"RS256"})); perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if sub, _ := claims["sub"].(string); sub != "openova-mcp@acme" {
		t.Errorf("synthetic sub = %q, want openova-mcp@acme", sub)
	}
	// Still Org-scoped.
	if org, _ := claims["org_id"].(string); org != "acme" {
		t.Errorf("org_id = %q, want acme", org)
	}
}

// Test_seedMCPBearer_SkipNoOpenBao verifies the Catalyst-Zero orchestrator
// path: nil OpenBao client ⇒ non-fatal skip, no panic.
func Test_seedMCPBearer_SkipNoOpenBao(t *testing.T) {
	h := &Handler{log: silentLogger()}
	h.handoverSigner = newTestSigner(t)
	// h.openbao deliberately nil.
	if outcome := h.seedMCPBearer(context.Background(), "acme", "x@y.z"); outcome != MCPBearerSeedOutcomeSkippedNoBao {
		t.Fatalf("outcome = %q, want %q", outcome, MCPBearerSeedOutcomeSkippedNoBao)
	}
}

// Test_seedMCPBearer_SkipNoSigner verifies the missing-signer guard: a nil
// handover signer ⇒ loud skip, NO OpenBao write (we never seed a path
// without a real bearer).
func Test_seedMCPBearer_SkipNoSigner(t *testing.T) {
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}
	// h.handoverSigner deliberately nil.
	if outcome := h.seedMCPBearer(context.Background(), "acme", "x@y.z"); outcome != MCPBearerSeedOutcomeSkippedNoSig {
		t.Fatalf("outcome = %q, want %q", outcome, MCPBearerSeedOutcomeSkippedNoSig)
	}
	srv.mu.Lock()
	wrote := srv.lastURL != ""
	srv.mu.Unlock()
	if wrote {
		t.Errorf("expected NO OpenBao write when signer unavailable, but a write hit %q", srv.lastURL)
	}
}

// Test_seedMCPBearer_SkipNoOrgSlug verifies the empty-slug guard: an empty
// Org slug ⇒ skip, NO write (a bearer with no org_id would defeat the
// cross-Org create guard the MCP relies on).
func Test_seedMCPBearer_SkipNoOrgSlug(t *testing.T) {
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}
	h.handoverSigner = newTestSigner(t)
	if outcome := h.seedMCPBearer(context.Background(), "   ", "x@y.z"); outcome != MCPBearerSeedOutcomeSkippedNoOrg {
		t.Fatalf("outcome = %q, want %q", outcome, MCPBearerSeedOutcomeSkippedNoOrg)
	}
	srv.mu.Lock()
	wrote := srv.lastURL != ""
	srv.mu.Unlock()
	if wrote {
		t.Errorf("expected NO OpenBao write for empty slug, but a write hit %q", srv.lastURL)
	}
}
