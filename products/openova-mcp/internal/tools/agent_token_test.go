// agent_token_test.go — what an agent token (typ agent) sees at the tool
// server (AIP-150): the six read tools and never create_application, because
// its tier is capped at developer and create_application needs admin.
package tools

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// resolveAgent signs an agent token the way catalyst-api does (RS256, kid =
// the signer's key identifier) with the given tier claim and resolves it
// through an RS256 resolver pinned to pin.
func resolveAgent(t *testing.T, tier string, pin identity.Context) (*identity.Identity, error) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://console.hw307.omani.works", "sub": "agent:acme/fixer",
		"aud": []string{"model-gateway", "platform-tools", "catalyst-api"},
		"typ": "agent", "org": "acme", "org_id": "acme", "agent": "fixer", "tier": tier,
		"role": "openova-user", "email": "fixer@acme.agents.invalid",
		"iat": now.Unix(), "exp": now.Add(30 * time.Minute).Unix(),
	})
	tok.Header["kid"] = identity.KeyID(&k.PublicKey)
	signed, err := tok.SignedString(k)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return identity.NewRS256Resolver(&k.PublicKey, pin).Resolve(signed)
}

// An agent token sees exactly the six read tools, whatever tier it claims.
func TestAgentTokenSeesTheReadToolsOnly(t *testing.T) {
	want := []string{"get_application", "list_applications", "list_blueprints", "list_environments", "list_organizations", "whoami"}
	reg := NewRegistry(nil)
	for _, tier := range []string{"viewer", "developer", "admin", "owner"} {
		id, err := resolveAgent(t, tier, "")
		if err != nil {
			t.Fatalf("tier %q: refused: %v", tier, err)
		}
		var got []string
		for _, tl := range reg.List(id) {
			got = append(got, tl.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tier %q: tools/list = %v, want %v", tier, got, want)
		}
	}
}

// A hand-made call of create_application with an agent token is forbidden.
func TestAgentTokenCannotCallCreateApplication(t *testing.T) {
	id, err := resolveAgent(t, "admin", "")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	_, err = NewRegistry(nil).Call(context.Background(), id, "create_application", []byte(`{"blueprint":"bp-wordpress","name":"blog"}`))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("create_application with an agent token: got %v, want ErrForbidden", err)
	}
}

// whoami echoes the agent name and the capped tier.
func TestAgentTokenWhoami(t *testing.T) {
	id, err := resolveAgent(t, "admin", identity.ContextOrganization)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	out, err := NewRegistry(nil).Call(context.Background(), id, "whoami", nil)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	m := out.(map[string]any)
	if m["agent"] != "fixer" || m["tier"] != "developer" || m["org_id"] != "acme" || m["context"] != "organization" {
		t.Fatalf("whoami = %+v", m)
	}
}

// A Sovereign-mode instance serves sovereign-admins only, so it refuses an
// agent token: agents use an instance in organization mode.
func TestAgentTokenRefusedBySovereignModeInstance(t *testing.T) {
	if _, err := resolveAgent(t, "developer", identity.ContextSovereign); err == nil {
		t.Fatal("a Sovereign-mode instance accepted an agent token")
	}
}
