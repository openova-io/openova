package main

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

// ── the hw293 shape, reproduced ──────────────────────────────────────────
//
// UAT rows 212/213 recorded, verbatim off the wire against the Sovereign-mode
// instance at https://mcp.<sovereign-fqdn>/mcp with a real Org-scoped session:
//
//	-32001 unauthenticated — identity: bearer rejected:
//	token signature is invalid: crypto/rsa: verification error   (3/3)
//
// and the row's two controls proved that message FALSE on its face: the
// byte-identical bearer was accepted by catalyst-api's own /whoami (200,
// orgScoped:true), and a Sovereign session from the SAME catalyst-api was
// accepted by the SAME MCP endpoint. A token the issuer verifies does not have
// an invalid signature.
//
// The message is a MASK. resolveBearer runs the #5175 whoami fallback after a
// local verify miss; catalyst-api answers "verified, organization-scoped"; the
// resolver's #5206 instance pin then refuses the caller — correctly, by design,
// a Sovereign-mode instance is the wrong door for an Org-scoped session — and
// resolveBearer discards THAT verdict and returns the stale signature error
// that preceded it. The server reports a signature failure while holding
// positive evidence the signature is fine.
//
// The cost is not cosmetic: row 212's own "SURFACE TO LOOK AT" names
// NewRS256Resolver / identity.go:213, i.e. the key-pinning subsystem, because
// the key-pinning subsystem is what the error accused. The real verdict names
// the door and the remedy ("use that Organization's own MCP instance").

// mintRS256 signs claims with key and returns the compact JWT.
func mintRS256(t *testing.T, key *rsa.PrivateKey, claims *sharedauth.Claims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return k
}

// whoamiStub serves GET /api/v1/whoami with the supplied status + body, and
// 404s everything else, so a test that accidentally exercises another endpoint
// fails loudly instead of silently passing.
func whoamiStub(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/whoami" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

const (
	orgWhoamiVerdict = `{"email":"u@omani.homes","verified":true,"mode":"organization","tier":"org-admin","org":"hw293walkone","orgScoped":true,"deploymentId":"dep1"}`
	sovWhoamiVerdict = `{"email":"a@omani.works","verified":true,"mode":"sovereign","tier":"owner","deploymentId":"dep1"}`
)

// SUBJECT (#5843 / UAT rows 212-213). A Sovereign-mode instance, an Org-scoped
// bearer it cannot verify locally, and a catalyst-api that VERIFIES that bearer
// as organization-scoped. The refusal is correct; the REASON must be the
// instance-pin verdict, never the signature error the server already has
// evidence against.
func TestResolveBearer_SovereignInstance_OrgSession_SurfacesTheWrongDoorVerdict(t *testing.T) {
	pinned, other := genKey(t), genKey(t)
	bearer := mintRS256(t, other, &sharedauth.Claims{OrgID: "hw293walkone", Tier: "org-admin"})

	ts := whoamiStub(http.StatusOK, orgWhoamiVerdict)
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewRS256Resolver(&pinned.PublicKey, identity.ContextSovereign),
	}

	id, err := srv.resolveBearer(bearer)
	if err == nil {
		t.Fatalf("a Sovereign-mode instance ACCEPTED an Org-scoped session (surface widened): %+v", id)
	}
	got := err.Error()

	if strings.Contains(got, "crypto/rsa") || strings.Contains(got, "signature is invalid") {
		t.Fatalf("the signature error MASKED the authoritative verdict — catalyst-api verified this bearer, so the signature claim is false.\n  got: %s", got)
	}
	if !strings.Contains(got, "hw293walkone") {
		t.Fatalf("the verdict does not name the caller's Organization, so a reader cannot tell which door to use.\n  got: %s", got)
	}
	if !strings.Contains(got, "own MCP instance") {
		t.Fatalf("the verdict does not name the remedy (the Organization's own MCP instance).\n  got: %s", got)
	}
}

// SUBJECT, second instance of the same class: a per-Org instance pinned to one
// Organization, handed a session catalyst-api verifies for a DIFFERENT one. The
// org-pin refusal is the authoritative verdict and must survive the same way.
func TestResolveBearer_PerOrgInstance_ForeignOrgSession_SurfacesTheOrgPinVerdict(t *testing.T) {
	pinned, other := genKey(t), genKey(t)
	bearer := mintRS256(t, other, &sharedauth.Claims{OrgID: "hw293walkone", Tier: "org-admin"})

	ts := whoamiStub(http.StatusOK, orgWhoamiVerdict)
	defer ts.Close()

	srv := &server{
		reg: tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewRS256Resolver(&pinned.PublicKey, identity.ContextOrganization).
			WithOrgPin("hw293walktwo"),
	}

	id, err := srv.resolveBearer(bearer)
	if err == nil {
		t.Fatalf("a per-Org instance ACCEPTED a foreign Org's session (surface widened): %+v", id)
	}
	got := err.Error()
	if strings.Contains(got, "crypto/rsa") || strings.Contains(got, "signature is invalid") {
		t.Fatalf("the signature error MASKED the org-pin verdict.\n  got: %s", got)
	}
	if !strings.Contains(got, "hw293walktwo") {
		t.Fatalf("the verdict does not name this instance's pinned Organization.\n  got: %s", got)
	}
}

// CONTROL — shares the suspect property (a bearer the local resolver CANNOT
// verify, taking the identical whoami-fallback path) and must stay green both
// before and after: when catalyst-api REFUSES the bearer, there is no
// better-informed verdict, so the local signature error is the honest answer
// and must be preserved verbatim.
func TestResolveBearer_ControlCatalystApiRejects_KeepsTheSignatureError(t *testing.T) {
	pinned, other := genKey(t), genKey(t)
	bearer := mintRS256(t, other, &sharedauth.Claims{OrgID: "hw293walkone", Tier: "org-admin"})

	ts := whoamiStub(http.StatusUnauthorized, `{"error":"unauthorized"}`)
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewRS256Resolver(&pinned.PublicKey, identity.ContextSovereign),
	}

	id, err := srv.resolveBearer(bearer)
	if err == nil {
		t.Fatalf("accepted a bearer catalyst-api rejected (surface widened): %+v", id)
	}
	if !strings.Contains(err.Error(), "crypto/rsa: verification error") {
		t.Fatalf("lost the local-verify error on a token catalyst-api also refuses.\n  got: %s", err.Error())
	}
}

// CONTROL — the #5175 happy path is untouched: same unverifiable-by-local-key
// bearer, Sovereign whoami verdict, Sovereign-pinned instance → ACCEPTED.
func TestResolveBearer_ControlSovereignSession_StillAccepted(t *testing.T) {
	pinned, other := genKey(t), genKey(t)
	bearer := mintRS256(t, other, &sharedauth.Claims{Tier: "owner"})

	ts := whoamiStub(http.StatusOK, sovWhoamiVerdict)
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewRS256Resolver(&pinned.PublicKey, identity.ContextSovereign),
	}

	id, err := srv.resolveBearer(bearer)
	if err != nil {
		t.Fatalf("the #5175 sovereign fallback regressed: %v", err)
	}
	if id.Context != identity.ContextSovereign || id.Tier != identity.TierOwner {
		t.Fatalf("resolved ctx=%s tier=%v", id.Context, id.Tier)
	}
}

// CONTROL — a bearer the local resolver verifies fine still resolves locally
// and never consults catalyst-api. The stub 404s any other path, and /whoami
// here would refuse, so a regression that always delegates fails this test.
func TestResolveBearer_ControlLocallyVerifiedBearer_NeverDelegates(t *testing.T) {
	pinned := genKey(t)
	bearer := mintRS256(t, pinned, &sharedauth.Claims{Role: "sovereign-admin", Tier: "owner"})

	ts := whoamiStub(http.StatusUnauthorized, `{"error":"unauthorized"}`)
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewRS256Resolver(&pinned.PublicKey, identity.ContextSovereign),
	}

	id, err := srv.resolveBearer(bearer)
	if err != nil {
		t.Fatalf("a locally-verifiable sovereign-admin bearer was refused: %v", err)
	}
	if id.Context != identity.ContextSovereign || id.Tier != identity.TierSovereignAdmin {
		t.Fatalf("resolved ctx=%s tier=%v", id.Context, id.Tier)
	}
}
