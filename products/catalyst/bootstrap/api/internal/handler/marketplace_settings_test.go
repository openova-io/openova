package handler

import (
	"strings"
	"testing"
)

// TestPatchMarketplaceYAML_EnableSetsValues confirms the YAML patcher
// flips ingress.marketplace.enabled to true and writes the brand fields
// into the existing overlay structure used by every Sovereign.
func TestPatchMarketplaceYAML_EnableSetsValues(t *testing.T) {
	overlay := `---
apiVersion: v1
kind: Namespace
metadata:
  name: catalyst-system
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-catalyst-platform
spec:
  values:
    global:
      sovereignFQDN: omantel.omani.works
    ingress:
      hosts:
        console:
          host: console.omantel.omani.works
      marketplace:
        enabled: false
`
	patched, err := patchMarketplaceYAML([]byte(overlay), SetMarketplaceRequest{
		Enabled: true,
		Brand: MarketplaceBrand{
			Name:         "Otech Cloud",
			Tagline:      "Cloud + SaaS for Oman",
			PrimaryColor: "#3B82F6",
		},
	})
	if err != nil {
		t.Fatalf("patchMarketplaceYAML returned error: %v", err)
	}
	out := string(patched)
	if !strings.Contains(out, "enabled: true") {
		t.Fatalf("expected enabled: true; got:\n%s", out)
	}
	if !strings.Contains(out, "Otech Cloud") {
		t.Fatalf("expected brand name Otech Cloud; got:\n%s", out)
	}
	if !strings.Contains(out, "Cloud + SaaS for Oman") {
		t.Fatalf("expected tagline; got:\n%s", out)
	}
	if !strings.Contains(out, "#3B82F6") {
		t.Fatalf("expected primary colour; got:\n%s", out)
	}
	// The Namespace doc must survive the round-trip — encoding must
	// preserve both documents and their order.
	if !strings.Contains(out, "kind: Namespace") {
		t.Fatalf("expected Namespace doc preserved; got:\n%s", out)
	}
}

// TestPatchMarketplaceYAML_DisableUnsetsEnabled flips a previously-true
// overlay back to false and confirms the brand fields are NOT cleared
// (re-enable must reuse the prior values without re-typing).
func TestPatchMarketplaceYAML_DisableUnsetsEnabled(t *testing.T) {
	overlay := `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-catalyst-platform
spec:
  values:
    ingress:
      marketplace:
        enabled: true
    marketplace:
      brand:
        name: Otech Cloud
        tagline: Existing tagline
        primaryColor: "#FF00FF"
`
	patched, err := patchMarketplaceYAML([]byte(overlay), SetMarketplaceRequest{
		Enabled: false,
		Brand:   MarketplaceBrand{}, // empty — operator clicked Disable without touching brand
	})
	if err != nil {
		t.Fatalf("patchMarketplaceYAML returned error: %v", err)
	}
	out := string(patched)
	if !strings.Contains(out, "enabled: false") {
		t.Fatalf("expected enabled: false; got:\n%s", out)
	}
	// Brand fields must survive — empty inputs are no-op writes.
	if !strings.Contains(out, "Otech Cloud") {
		t.Fatalf("expected brand name preserved on disable; got:\n%s", out)
	}
	if !strings.Contains(out, "Existing tagline") {
		t.Fatalf("expected tagline preserved on disable; got:\n%s", out)
	}
}

// TestIsValidHexColor matches the 7-char #RRGGBB contract used both
// server-side (handler validation) and client-side (UI input validation)
// so the two never drift.
func TestIsValidHexColor(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"#3B82F6", true},
		{"#000000", true},
		{"#abcdef", true},
		{"#ABCDEF", true},
		{"#3b82f6", true},
		{"3B82F6", false},
		{"#3B82F", false},
		{"#3B82F66", false},
		{"#GGGGGG", false},
		{"", false},
		{"red", false},
	}
	for _, c := range cases {
		if got := isValidHexColor(c.in); got != c.want {
			t.Errorf("isValidHexColor(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// TestInjectTokenIntoURL covers the auth-injection helper. The token
// MUST end up inside the userinfo segment, never as a separate argv.
func TestInjectTokenIntoURL(t *testing.T) {
	cases := []struct {
		in    string
		token string
		want  string
		err   bool
	}{
		{"https://github.com/openova-io/openova", "abc123", "https://x-access-token:abc123@github.com/openova-io/openova", false},
		{"https://example.com/path", "tok", "https://x-access-token:tok@example.com/path", false},
		// Stripping pre-existing userinfo (defensive — should never happen
		// in practice but the helper handles it for resilience).
		{"https://old:old@github.com/foo", "new", "https://x-access-token:new@github.com/foo", false},
		// Empty token returns input unchanged.
		{"https://github.com/foo", "", "https://github.com/foo", false},
		// SSH URLs are rejected.
		{"git@github.com:openova-io/openova.git", "tok", "", true},
	}
	for _, c := range cases {
		got, err := injectTokenIntoURL(c.in, c.token)
		if c.err {
			if err == nil {
				t.Errorf("injectTokenIntoURL(%q,…) expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("injectTokenIntoURL(%q,…) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("injectTokenIntoURL(%q,…)=%q, want %q", c.in, got, c.want)
		}
	}
}

// TestRedactString ensures the token-bearing URL never leaks into log
// output. Every error wrap path runs through redactString; a regression
// here would expose the GitOps PAT in the operator-visible 500 body.
func TestRedactString(t *testing.T) {
	in := "fatal: could not authenticate to https://x-access-token:supersecret@github.com/openova-io/openova"
	out := redactString(in)
	if strings.Contains(out, "supersecret") {
		t.Fatalf("redactString leaked token: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("redactString did not insert REDACTED marker: %q", out)
	}
}
