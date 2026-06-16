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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	// #3648 — the write path canonicalizes the placement-editor dialect onto
	// the canonical spec.topology.supported vocabulary, so single-region is
	// stored (and read back) as singleton; active-active is already canonical.
	if len(got.SupportedTopologies) != 2 || got.SupportedTopologies[0] != "singleton" || got.SupportedTopologies[1] != "active-active" {
		t.Errorf("topology round-trip: got %v want [singleton active-active]", got.SupportedTopologies)
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
	// #3648 — single-region (placement-editor dialect) canonicalizes to
	// singleton on the canonical spec.topology.supported.
	if len(got.SupportedTopologies) != 1 || got.SupportedTopologies[0] != "singleton" {
		t.Errorf("topologies not committed canonically: got %v want [singleton]", got.SupportedTopologies)
	}
}

// TestCommitCatalogAppEditToGit_CanonicalizesHotStandby (#3648) — the
// founder's failing case: the catalog UI posts the un-hyphenated
// placement-editor dialect `active-hotstandby`, which MUST land in the
// canonical spec.topology.supported as `active-hot-standby` so a later
// instance create resolves against the Blueprint instead of failing
// `topology "active-hotstandby" not in supported [...]`.
func TestCommitCatalogAppEditToGit_CanonicalizesHotStandby(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	body := []byte(`{"slug":"postgres","name":"Postgres","published":true,"supported_topologies":["active-active","active-hotstandby"]}`)
	h.commitCatalogAppEditToGit(context.Background(), body)

	key := giteaKey(catalogSovereignOrg, "bp-postgres", catalogEditGitBranch, catalogEditBlueprintPath)
	raw, ok := fg.files[key]
	if !ok {
		t.Fatalf("expected committed blueprint.yaml at %s; keys=%v", key, fileKeys(fg))
	}
	got, ok := catalogEditFromBlueprintYAML("bp-postgres", raw)
	if !ok {
		t.Fatal("committed CR did not parse")
	}
	if len(got.SupportedTopologies) != 2 || got.SupportedTopologies[1] != "active-hot-standby" {
		t.Errorf("active-hotstandby must canonicalize to active-hot-standby: got %v", got.SupportedTopologies)
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

// TestWriteCatalogEditToGit_FirstEditSeedsFullCRFromLiveSpec — #3668 §5A /
// §9.2-9.3: the FIRST console edit on a blueprint with NO Gitea file must
// SEED the committed file from the live CR's FULL spec (resolved via the
// catalog client), so spec.manifests / spec.version / spec.source survive —
// NOT a lossy `version: 0.0.0` card-only stub. Before #3668 a first edit on
// bp-wordpress committed a stub that dropped every install field.
func TestWriteCatalogEditToGit_FirstEditSeedsFullCRFromLiveSpec(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)
	// The live CR (resolved via the catalog client) carries the full spec —
	// real version + manifests — exactly as the in-cluster Blueprint does.
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))

	// A first edit changing only the card summary (no Gitea file yet).
	edit := catalogEdit{Slug: "wordpress", Tagline: "Edited via console"}
	committed, err := h.writeCatalogEditToGit(context.Background(), edit)
	if err != nil {
		t.Fatalf("writeCatalogEditToGit: %v", err)
	}
	if !committed {
		t.Fatalf("first edit must commit a CR")
	}

	key := giteaKey(catalogSovereignOrg, "bp-wordpress", catalogEditGitBranch, catalogEditBlueprintPath)
	raw, ok := fg.files[key]
	if !ok {
		t.Fatalf("expected committed blueprint.yaml at %s; keys=%v", key, fileKeys(fg))
	}
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal committed CR: %v", err)
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		t.Fatalf("committed CR has no spec; doc=%v", doc)
	}
	// The REAL version is seeded from the live CR — NOT the 0.0.0 stub.
	if asString(spec["version"]) != "1.2.3" {
		t.Errorf("first edit must seed the real spec.version from the live CR, not a 0.0.0 stub; got %v", spec["version"])
	}
	// spec.manifests (an install field the old stub dropped) survives.
	if _, ok := spec["manifests"].(map[string]interface{}); !ok {
		t.Errorf("first edit must seed spec.manifests from the live CR; spec=%v", spec)
	}
	// The card edit landed on top of the seeded full CR.
	card, _ := spec["card"].(map[string]interface{})
	if card == nil || card["summary"] != "Edited via console" {
		t.Errorf("the card edit must overlay the seeded CR; card=%v", card)
	}
	// Server-managed metadata is NOT carried into the committed source.
	meta, _ := doc["metadata"].(map[string]interface{})
	if meta == nil || meta["name"] != "bp-wordpress" {
		t.Errorf("metadata.name must be bp-wordpress; meta=%v", meta)
	}
	if _, leaked := meta["managedFields"]; leaked {
		t.Errorf("managedFields must be stripped from the seeded source")
	}
	if _, leaked := doc["status"]; leaked {
		t.Errorf("status must be stripped from the seeded source")
	}
}

// TestCommitCatalogAppEditToGit_SlowGiteaStillCommits — #3668 §9.6(a):
// a Gitea that sleeps 3s on PutFile no longer aborts the commit. Before
// #3668 the write shared the 1500ms commerce-probe ctx and deadline-d; now
// it runs under the dedicated ~15s catalogEditGitBudget, so a busy-but-
// healthy Gitea commits durably (committed=true).
func TestCommitCatalogAppEditToGit_SlowGiteaStillCommits(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	fg.putFileSleep = 3 * time.Second // far over the dead 1500ms probe budget
	h.SetGiteaClient(fg)

	body := []byte(`{"slug":"alloy","name":"Alloy (IaC write test)","tagline":"telemetry","supported_topologies":["single-region"]}`)
	committed, reason := h.commitCatalogAppEditToGit(context.Background(), body)
	if !committed {
		t.Fatalf("a 3s PutFile must still commit under the dedicated git budget; reason=%q", reason)
	}
	if reason != "" {
		t.Errorf("a durable commit must carry no failure reason; got %q", reason)
	}
	key := giteaKey(catalogSovereignOrg, "bp-alloy", catalogEditGitBranch, catalogEditBlueprintPath)
	if _, ok := fg.files[key]; !ok {
		t.Fatalf("expected a committed blueprint.yaml at %s; keys=%v", key, fileKeys(fg))
	}
}

// TestCommitCatalogAppEditToGit_ErroringGiteaReportsNotCommitted — #3668
// §9.6(b): a Gitea that errors on PutFile makes the edit report
// committed=false + a reason. Before #3668 the error was logged + swallowed
// and the caller relayed a 200 OK; now the verdict is surfaced so the UI can
// show "cache only / IaC commit failed" instead of a false green "Saved".
func TestCommitCatalogAppEditToGit_ErroringGiteaReportsNotCommitted(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	fg.putFileErr = errors.New("gitea: 500 internal error")
	h.SetGiteaClient(fg)

	body := []byte(`{"slug":"alloy","name":"Alloy","tagline":"telemetry"}`)
	committed, reason := h.commitCatalogAppEditToGit(context.Background(), body)
	if committed {
		t.Fatalf("an erroring PutFile must report committed=false")
	}
	if !strings.Contains(reason, "IaC commit failed") {
		t.Errorf("the failure reason must name the IaC commit failure; got %q", reason)
	}
}

// TestCommitCatalogAppEditToGit_UnwiredReportsCacheOnly — #3668 §9.6: with
// no Gitea client (pre-cutover / CI) the commit is reported committed=false
// with a "pre-cutover" reason, NOT swallowed as success — the store cache
// holds the edit but the UI must know it is not yet IaC.
func TestCommitCatalogAppEditToGit_UnwiredReportsCacheOnly(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// no SetGiteaClient → h.giteaClient == nil
	committed, reason := h.commitCatalogAppEditToGit(context.Background(),
		[]byte(`{"slug":"alloy","name":"Alloy"}`))
	if committed {
		t.Fatalf("an unwired client must report committed=false")
	}
	if strings.TrimSpace(reason) == "" {
		t.Errorf("the unwired case must carry a human reason for the UI")
	}
}

// TestCommitCatalogAppEditToGit_ByteIdenticalIsDurable — #3668: a second
// identical edit (PutFile's byte-equal short-circuit returns committed=false,
// err=nil) is a DURABLE state — the source already carries these bytes — so
// it must report committed=true, never a false "cache only" failure.
func TestCommitCatalogAppEditToGit_ByteIdenticalIsDurable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	body := []byte(`{"slug":"alloy","name":"Alloy","tagline":"telemetry"}`)
	if c, _ := h.commitCatalogAppEditToGit(context.Background(), body); !c {
		t.Fatalf("first commit must report committed=true")
	}
	// Second identical edit — PutFile short-circuits (no new bytes written).
	committed, reason := h.commitCatalogAppEditToGit(context.Background(), body)
	if !committed {
		t.Fatalf("a byte-identical re-edit is durable and must report committed=true; reason=%q", reason)
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
