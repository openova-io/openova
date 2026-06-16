// catalog_edit_git_test.go — #3648 (train/hw150) coverage for the
// catalog-edit-to-git IaC write path.
//
// The DoD: a catalog edit is genuinely IaC — it produces a git commit to
// the LOCAL catalog repo (the catalog-sovereign Gitea Org), and the
// committed values are read back through the catalog read overlay (so an
// out-of-band git edit round-trips to the UI). These tests pin:
//
//   - the pure Blueprint-CR YAML merge/parse (no I/O);
//   - writeCatalogEditToGit → a real PutFile into catalog-sovereign;
//   - fetchCatalogEditsFromGit reads the committed CR back;
//   - fetchCatalogEditsMerged makes the git source WIN over the store;
//   - commitCatalogAppEditToGit (the proxyCommerce hook) decodes the
//     commerce App body + commits it.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	yamlv3 "gopkg.in/yaml.v3"
)

// TestMergeCatalogEditIntoBlueprintYAML_FreshCR — with no existing CR the
// merge synthesises a minimal-but-valid Blueprint carrying the edited
// card + supported topologies, and the result parses back to the same
// edit.
func TestMergeCatalogEditIntoBlueprintYAML_FreshCR(t *testing.T) {
	edit := catalogEdit{
		Slug:                "grafana",
		Name:                "Grafana (Edited)",
		Tagline:             "Admin-curated dashboards",
		IconLight:           "https://cdn/light.svg",
		IconDark:            "https://cdn/dark.svg",
		SupportedTopologies: []string{"single-region", "active-active"},
	}
	out, err := mergeCatalogEditIntoBlueprintYAML(nil, "bp-grafana", edit)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Canonical envelope present.
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if doc["kind"] != "Blueprint" {
		t.Errorf("kind: got %v want Blueprint", doc["kind"])
	}
	meta, _ := doc["metadata"].(map[string]interface{})
	if meta == nil || meta["name"] != "bp-grafana" {
		t.Errorf("metadata.name: got %v want bp-grafana", meta["name"])
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil || strings.TrimSpace(asString(spec["version"])) == "" {
		t.Errorf("spec.version must be stamped on a fresh CR; got %v", spec["version"])
	}

	// Round-trips back to the edit.
	got, ok := catalogEditFromBlueprintYAML("bp-grafana", out)
	if !ok {
		t.Fatal("catalogEditFromBlueprintYAML returned !ok")
	}
	if got.Name != "Grafana (Edited)" || got.Tagline != "Admin-curated dashboards" {
		t.Errorf("card round-trip: name=%q tagline=%q", got.Name, got.Tagline)
	}
	if got.IconLight != "https://cdn/light.svg" || got.IconDark != "https://cdn/dark.svg" {
		t.Errorf("icon round-trip: light=%q dark=%q", got.IconLight, got.IconDark)
	}
	if len(got.SupportedTopologies) != 2 || got.SupportedTopologies[0] != "single-region" {
		t.Errorf("topology round-trip: %v", got.SupportedTopologies)
	}
}

// TestMergeCatalogEditIntoBlueprintYAML_PreservesExistingSpecFields — a
// read-modify-write over a curated CR must NOT drop spec.source /
// spec.version / spec.manifests; it only overwrites the edited card keys.
func TestMergeCatalogEditIntoBlueprintYAML_PreservesExistingSpecFields(t *testing.T) {
	existing := []byte(`apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-grafana
  labels:
    catalyst.openova.io/managed-by: catalog-seed
spec:
  version: "9.5.1"
  visibility: listed
  card:
    title: "Grafana"
    summary: "Seed summary"
    icon: grafana.svg
  source:
    kind: HelmRepository
    type: oci
    url: oci://ghcr.io/openova-io
    chart: bp-grafana
    version: 9.5.1
  manifests:
    chart: bp-grafana
`)
	edit := catalogEdit{
		Slug:                "grafana",
		Name:                "Observability",
		SupportedTopologies: []string{"active-active"},
	}
	out, err := mergeCatalogEditIntoBlueprintYAML(existing, "bp-grafana", edit)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := doc["spec"].(map[string]interface{})

	// Edited fields applied.
	card := spec["card"].(map[string]interface{})
	if card["title"] != "Observability" {
		t.Errorf("edited title: got %v", card["title"])
	}
	// Untouched card field survives.
	if card["summary"] != "Seed summary" {
		t.Errorf("untouched summary must survive: got %v", card["summary"])
	}
	if card["icon"] != "grafana.svg" {
		t.Errorf("untouched icon must survive: got %v", card["icon"])
	}
	// Real version preserved (NOT clobbered to the 0.0.0 placeholder).
	if asString(spec["version"]) != "9.5.1" {
		t.Errorf("existing spec.version must survive: got %v", spec["version"])
	}
	// spec.source + spec.manifests survive the round-trip.
	if _, ok := spec["source"].(map[string]interface{}); !ok {
		t.Errorf("spec.source must survive the merge; spec=%v", spec)
	}
	if _, ok := spec["manifests"].(map[string]interface{}); !ok {
		t.Errorf("spec.manifests must survive the merge; spec=%v", spec)
	}
	// supported topologies set under spec.topology.
	topo, ok := spec["topology"].(map[string]interface{})
	if !ok {
		t.Fatalf("spec.topology must be present; spec=%v", spec)
	}
	if got := asStringSlice(topo["supported"]); len(got) != 1 || got[0] != "active-active" {
		t.Errorf("spec.topology.supported: got %v", topo["supported"])
	}
}

// TestWriteCatalogEditToGit_CommitsAndReadsBack is the core DoD: an edit
// produces a git commit into catalog-sovereign, and the committed CR is
// read back through fetchCatalogEditsFromGit — proving git is the source.
func TestWriteCatalogEditToGit_CommitsAndReadsBack(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	edit := catalogEdit{
		Slug:                "cilium",
		Name:                "Cilium (Edited)",
		Tagline:             "eBPF networking",
		IconLight:           "https://cdn/cilium-light.svg",
		IconDark:            "https://cdn/cilium-dark.svg",
		SupportedTopologies: []string{"single-region", "active-active"},
	}
	committed, err := h.writeCatalogEditToGit(context.Background(), edit)
	if err != nil {
		t.Fatalf("writeCatalogEditToGit: %v", err)
	}
	if !committed {
		t.Fatalf("expected a git commit, got committed=false")
	}

	// The Blueprint CR landed at catalog-sovereign/bp-cilium/blueprint.yaml.
	key := giteaKey(catalogSovereignOrg, "bp-cilium", catalogEditGitBranch, catalogEditBlueprintPath)
	raw, ok := fg.files[key]
	if !ok {
		t.Fatalf("expected a committed blueprint.yaml at %s; have keys %v", key, fileKeys(fg))
	}
	if !strings.Contains(string(raw), "Cilium (Edited)") {
		t.Errorf("committed CR missing edited title; got:\n%s", raw)
	}

	// Read it back through the git overlay source.
	edits := h.fetchCatalogEditsFromGit(context.Background())
	got, ok := edits["cilium"]
	if !ok {
		t.Fatalf("fetchCatalogEditsFromGit must surface the committed edit; got %v", editKeys(edits))
	}
	if got.Name != "Cilium (Edited)" || got.IconDark != "https://cdn/cilium-dark.svg" {
		t.Errorf("read-back edit wrong: %+v", got)
	}
	if len(got.SupportedTopologies) != 2 {
		t.Errorf("read-back topologies: %v", got.SupportedTopologies)
	}
}

// TestWriteCatalogEditToGit_UnwiredIsBestEffortNoop — with no Gitea client
// (chroot pre-cutover / CI) the write is a silent no-op, never an error,
// so the catalog edit API never fails for lack of a local catalog git.
func TestWriteCatalogEditToGit_UnwiredIsBestEffortNoop(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// no SetGiteaClient → h.giteaClient == nil
	committed, err := h.writeCatalogEditToGit(context.Background(), catalogEdit{
		Slug: "grafana", Name: "X",
	})
	if err != nil {
		t.Fatalf("unwired write must not error: %v", err)
	}
	if committed {
		t.Fatalf("unwired write must not report a commit")
	}
}

// TestFetchCatalogEditsMerged_GitSourceWinsOverStore — the read overlay
// must treat the git source as authoritative: when both the commerce
// store AND the catalog git carry an edit for the same entry, the git
// value wins (IaC is the single source of truth). The store-only entry
// still shows (git has no opinion → cache fills it).
func TestFetchCatalogEditsMerged_GitSourceWinsOverStore(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// Seed the git source: grafana carries a git-committed edit.
	if _, err := h.writeCatalogEditToGit(context.Background(), catalogEdit{
		Slug:    "grafana",
		Name:    "Grafana (from git)",
		Tagline: "git is the source",
	}); err != nil {
		t.Fatalf("seed git: %v", err)
	}

	// Stand up the commerce store with a DIFFERENT grafana name + a
	// store-only entry (keycloak) that git has no CR for.
	storeBody := `[
		{"slug":"grafana","name":"Grafana (from store)","tagline":"stale cache"},
		{"slug":"keycloak","name":"Keycloak (store only)"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(storeBody))
	}))
	defer srv.Close()
	restore := overrideSMECatalog(srv.URL)
	defer restore()

	merged := h.fetchCatalogEditsMerged(context.Background())

	g, ok := merged["grafana"]
	if !ok {
		t.Fatalf("grafana must be present; got %v", editKeys(merged))
	}
	if g.Name != "Grafana (from git)" {
		t.Errorf("git source must win over store: got %q want %q", g.Name, "Grafana (from git)")
	}
	// Store-only entry still surfaces (git silent → cache fills it).
	k, ok := merged["keycloak"]
	if !ok || k.Name != "Keycloak (store only)" {
		t.Errorf("store-only entry must survive the merge: got %+v ok=%v", k, ok)
	}
}

// TestCommitCatalogAppEditToGit_DecodesCommerceAppBody — the proxyCommerce
// hook decodes the EXACT commerce App JSON the UI's saveCatalogEdit PUTs
// (icon_light / icon_dark / supported_topologies tags) and commits it.
func TestCommitCatalogAppEditToGit_DecodesCommerceAppBody(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// Body shape == commerce.api.ts CommerceApp $set (a full merged row).
	body := []byte(`{"_id":"abc","slug":"redis","name":"Redis (Edited)","tagline":"In-memory KV","published":true,"icon_light":"l.svg","icon_dark":"d.svg","supported_topologies":["single-region"]}`)
	h.commitCatalogAppEditToGit(context.Background(), body)

	key := giteaKey(catalogSovereignOrg, "bp-redis", catalogEditGitBranch, catalogEditBlueprintPath)
	raw, ok := fg.files[key]
	if !ok {
		t.Fatalf("expected committed blueprint.yaml at %s; keys=%v", key, fileKeys(fg))
	}
	got, ok := catalogEditFromBlueprintYAML("bp-redis", raw)
	if !ok {
		t.Fatal("committed CR did not parse")
	}
	if got.Name != "Redis (Edited)" || got.Tagline != "In-memory KV" {
		t.Errorf("decoded commerce body wrong: %+v", got)
	}
	if got.IconLight != "l.svg" || got.IconDark != "d.svg" {
		t.Errorf("theme icons not committed: %+v", got)
	}
	if len(got.SupportedTopologies) != 1 || got.SupportedTopologies[0] != "single-region" {
		t.Errorf("topologies not committed: %v", got.SupportedTopologies)
	}
}

// TestCommitCatalogAppEditToGit_EmptyOverlayIsNoop — a commerce row with
// no card-overlay content (the common ~100 pre-seeded commerce rows) must
// NOT create a spurious Blueprint CR.
func TestCommitCatalogAppEditToGit_EmptyOverlayIsNoop(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	h.commitCatalogAppEditToGit(context.Background(),
		[]byte(`{"slug":"redis","published":true,"category":"data"}`))

	if len(fg.files) != 0 {
		t.Errorf("an empty-overlay row must not commit a CR; got files %v", fileKeys(fg))
	}
}

// ── tiny test helpers ────────────────────────────────────────────────

func fileKeys(f *fakeGiteaClient) []string {
	out := make([]string, 0, len(f.files))
	for k := range f.files {
		out = append(out, k)
	}
	return out
}
