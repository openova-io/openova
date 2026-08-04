package gitops

// Golden tests for the cutover-aware bp-* catalog OCI base (#5527) — BOTH
// directions, occurrence-counted against the block count so a silently
// no-op'd swap cannot pass (vacuity discipline per
// reference_render_guard_needs_a_vacuity_check_or_it_passes_on_nothing).
//
// Domains follow the test canon (docs/DOD.md §Domains-canon): omani.works /
// omantel.biz — never openova.io.

import (
	"strings"
	"testing"
)

// clearCutoverFactEnv guarantees a pre-cutover process env regardless of what
// the host running the tests exports (t.Setenv registers cleanup + blocks
// t.Parallel, which is exactly the isolation these need).
func clearCutoverFactEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CATALYST_LOCAL_REGISTRY_URL",
		"CATALYST_PIN_ISSUER",
		"CATALYST_HANDOVER_JWT_ISSUER",
		"SOVEREIGN_FQDN",
		"CATALYST_OTECH_FQDN",
	} {
		t.Setenv(k, "")
	}
}

// ── resolver matrix (pure function, no env) ────────────────────────────────

func TestResolveCatalogOCIBase_Matrix(t *testing.T) {
	cases := []struct {
		name                                                    string
		override, pinIssuer, handoverIssuer, fqdn, otech, want string
	}{
		{"no fact -> public catalog", "", "", "", "", "", orgBPCatalogGHCRBase},
		// Sovereign identity alone must NOT flip: SOVEREIGN_FQDN is stamped
		// from birth on every Sovereign (provisioning.yaml), pre-cutover its
		// Harbor holds no charts.
		{"fqdn alone stays public", "", "", "", "t90.omani.works", "", orgBPCatalogGHCRBase},
		{"pin issuer + fqdn -> local", "", "https://console.t90.omani.works", "", "t90.omani.works", "", "oci://registry.t90.omani.works/openova-io"},
		{"handover issuer alone suffices", "", "", "https://console.t91.omantel.biz", "t91.omantel.biz", "", "oci://registry.t91.omantel.biz/openova-io"},
		{"otech fqdn synonym resolves", "", "https://console.t90.omani.works", "", "", "t90.omani.works", "oci://registry.t90.omani.works/openova-io"},
		// Fact without identity is inconsistent -> fail safe to public,
		// never mint a host that resolves nowhere.
		{"fact without fqdn fails safe", "", "https://console.t90.omani.works", "", "", "", orgBPCatalogGHCRBase},
		// The explicit override (step-07 Phase-3d stamp) wins outright.
		{"override full base wins", "oci://registry.t90.omani.works/openova-io", "", "", "", "", "oci://registry.t90.omani.works/openova-io"},
		{"override beats derivation", "oci://registry.t90.omani.works/openova-io", "x", "x", "t91.omantel.biz", "", "oci://registry.t90.omani.works/openova-io"},
		{"override missing scheme prefixed", "registry.t90.omani.works/openova-io", "", "", "", "", "oci://registry.t90.omani.works/openova-io"},
		{"override https scheme swapped", "https://registry.t90.omani.works/openova-io", "", "", "", "", "oci://registry.t90.omani.works/openova-io"},
		{"override trailing slash trimmed", "oci://registry.t90.omani.works/openova-io/", "", "", "", "", "oci://registry.t90.omani.works/openova-io"},
		{"whitespace-only override is no fact", "   ", "", "", "", "", orgBPCatalogGHCRBase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCatalogOCIBase(tc.override, tc.pinIssuer, tc.handoverIssuer, tc.fqdn, tc.otech)
			if got != tc.want {
				t.Fatalf("resolveCatalogOCIBase(%q,%q,%q,%q,%q) = %q, want %q",
					tc.override, tc.pinIssuer, tc.handoverIssuer, tc.fqdn, tc.otech, got, tc.want)
			}
		})
	}
}

// ── golden render: fact=false emits upstream ───────────────────────────────

func TestGenerateTree_PreCutover_EmitsPublicCatalog(t *testing.T) {
	clearCutoverFactEnv(t)
	g := NewManifestGenerator("clusters/t90.omani.works/org-tenants")
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw", "stalwart-mail", "umami"},
		"pw", map[string]map[string]any{"postgres": {
			"active_hot_standby": true,
			"primary_region":     "hz-fsn-rtz-prod",
			"replica_region":     "hz-hel-rtz-prod",
		}})

	ghcrBlocks, localLeak := 0, 0
	helmRepoFiles := 0
	for name, content := range out {
		ghcrBlocks += strings.Count(content, "url: "+orgBPCatalogGHCRBase)
		localLeak += strings.Count(content, "registry.t90.omani.works")
		if strings.Contains(content, "kind: HelmRepository") {
			helmRepoFiles++
		}
		_ = name
	}
	// openclaw + stalwart HR files each carry one shared bp-* block; the
	// active-hot-standby path adds bp-cnpg-pair primary + replica. All of
	// them must declare the public catalog pre-cutover — and NOTHING may
	// leak a local registry host.
	if helmRepoFiles < 4 {
		t.Fatalf("expected >=4 files carrying HelmRepository blocks (openclaw, stalwart, cnpg-pair primary+replica), got %d", helmRepoFiles)
	}
	if ghcrBlocks < 4 {
		t.Fatalf("pre-cutover: expected >=4 public-catalog url lines, got %d — the ghcr default regressed", ghcrBlocks)
	}
	if localLeak != 0 {
		t.Fatalf("pre-cutover: %d local-registry references leaked without any cutover fact", localLeak)
	}
}

// ── golden render: fact=true emits local ───────────────────────────────────

func TestGenerateTree_PostCutover_EmitsLocalRegistry(t *testing.T) {
	clearCutoverFactEnv(t)
	// The step-07 Phase-3d stamp: the exact value shape step-06 patches the
	// live objects to (hw292: oci://registry.hw292.omani.works/openova-io).
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "oci://registry.t90.omani.works/openova-io")

	g := NewManifestGenerator("clusters/t90.omani.works/org-tenants")
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw", "stalwart-mail", "umami"},
		"pw", map[string]map[string]any{"postgres": {
			"active_hot_standby": true,
			"primary_region":     "hz-fsn-rtz-prod",
			"replica_region":     "hz-hel-rtz-prod",
		}})

	localBlocks, ghcrResidue, secretRefs := 0, 0, 0
	for _, content := range out {
		localBlocks += strings.Count(content, "url: oci://registry.t90.omani.works/openova-io")
		ghcrResidue += strings.Count(content, "ghcr.io/openova-io")
		secretRefs += strings.Count(content, "name: ghcr-pull")
	}
	if localBlocks < 4 {
		t.Fatalf("post-cutover: expected >=4 local url lines (openclaw, stalwart, cnpg-pair primary+replica), got %d — the swap no-op'd", localBlocks)
	}
	// The whole point of #5527: ZERO upstream URL residue post-cutover —
	// one leaked line re-tethers the Sovereign on the next Flux reconcile.
	if ghcrResidue != 0 {
		t.Fatalf("post-cutover: %d ghcr.io/openova-io references remain — the Sovereign re-tethers (Pillar 5)", ghcrResidue)
	}
	// secretRef keeps its NAME in both phases (the cutover rewrites the
	// Secret's contents, not its name — step-06 parity, per #5529).
	if secretRefs < 4 {
		t.Fatalf("post-cutover: expected ghcr-pull secretRef retained on every HelmRepository block, got %d", secretRefs)
	}
}

// The derived path (issuer fact, no explicit override) must produce the same
// canonical registry.<fqdn> host end-to-end through a real render.
func TestGenerateTree_PostCutover_DerivedFromIssuerFact(t *testing.T) {
	clearCutoverFactEnv(t)
	t.Setenv("CATALYST_PIN_ISSUER", "https://console.t91.omantel.biz")
	t.Setenv("SOVEREIGN_FQDN", "t91.omantel.biz")

	g := NewManifestGenerator("clusters/t91.omantel.biz/org-tenants")
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw"}, "pw", nil)

	local, ghcr := 0, 0
	for _, content := range out {
		local += strings.Count(content, "url: oci://registry.t91.omantel.biz/openova-io")
		ghcr += strings.Count(content, "ghcr.io/openova-io")
	}
	if local < 1 {
		t.Fatalf("issuer-fact derivation: expected >=1 local url line, got %d", local)
	}
	if ghcr != 0 {
		t.Fatalf("issuer-fact derivation: %d ghcr references remain", ghcr)
	}
}

// Pre-cutover output must be BYTE-IDENTICAL to the historical template so a
// regeneration on the mothership / a pre-cutover Sovereign produces zero
// Flux drift — assert the exact historical block shape survives.
func TestHelmRepoBlock_PreCutover_ByteIdentical(t *testing.T) {
	clearCutoverFactEnv(t)
	got := helmRepoBlock("bp-openclaw")
	want := `apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-openclaw
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull`
	if got != want {
		t.Fatalf("pre-cutover helmRepoBlock drifted from the historical template:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
