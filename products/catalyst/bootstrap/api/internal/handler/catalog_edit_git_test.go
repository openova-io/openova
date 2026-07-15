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
	restore := overrideOrgCatalog(srv.URL)
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

// TestWriteCatalogEditToGit_MirrorsToFluxAggregator — #3668: the load-bearing
// half PR #3702 deferred. On a Sovereign (SOVEREIGN_FQDN set), a console edit
// ALSO writes the merged CR into the Flux-reconciled aggregator tree
// `openova/openova @ catalog-sovereign : clusters/<fqdn>/catalog-
// sovereign/<bp>.yaml`, so the openova-catalog-sovereign Kustomization can
// reconcile it into the LIVE in-cluster Blueprint CR. Per #5113 Facet A the
// Flux write carries the FULL canonical CR INCLUDING the CRD-required
// spec.version — the former #4415 delivery-field strip made the file
// rejected by kustomize-controller's dry-run whenever the named CR did not
// already exist (`spec.version: Required value`), wedging the whole
// catalog-sovereign Kustomization (hw255 walk).
func TestWriteCatalogEditToGit_MirrorsToFluxAggregator(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)
	// A live CR carrying a real version + source — exactly what a seeded
	// Blueprint has on a Sovereign. The first edit seeds the per-Blueprint
	// file from it, so the curation fields AND version/source are present
	// pre-strip, making the #4415 strip assertion meaningful.
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))

	edit := catalogEdit{
		Slug:    "wordpress",
		Name:    "WordPress (Edited)",
		Tagline: "RECONCILE-PROOF",
	}
	if _, err := h.writeCatalogEditToGit(context.Background(), edit); err != nil {
		t.Fatalf("writeCatalogEditToGit: %v", err)
	}

	// The per-Blueprint repo write (UI read source) still lands WITH the full
	// merged CR, including the chart-delivery fields the UI renders.
	perBP := giteaKey(catalogSovereignOrg, "bp-wordpress", catalogEditGitBranch, catalogEditBlueprintPath)
	perBPRaw, ok := fg.files[perBP]
	if !ok {
		t.Fatalf("per-Blueprint write missing at %s; keys=%v", perBP, fileKeys(fg))
	}
	var perBPDoc map[string]interface{}
	if err := yamlv3.Unmarshal(perBPRaw, &perBPDoc); err != nil {
		t.Fatalf("unmarshal per-Blueprint CR: %v", err)
	}
	perBPSpec, _ := perBPDoc["spec"].(map[string]interface{})
	if perBPSpec == nil || asString(perBPSpec["version"]) != "1.2.3" {
		t.Errorf("per-Blueprint UI read source must keep spec.version; got %v", perBPSpec["version"])
	}
	if _, ok := perBPSpec["manifests"].(map[string]interface{}); !ok {
		t.Errorf("per-Blueprint UI read source must keep spec.manifests; spec=%v", perBPSpec)
	}

	// The Flux aggregator write lands on openova/openova @ catalog-sovereign.
	aggPath := "clusters/t99.omani.works/catalog-sovereign/bp-wordpress.yaml"
	aggKey := giteaKey(catalogSovereignAggOrg, catalogSovereignAggRepo, catalogSovereignAggBranch, aggPath)
	aggRaw, ok := fg.files[aggKey]
	if !ok {
		t.Fatalf("Flux aggregator write missing at %s; keys=%v", aggKey, fileKeys(fg))
	}
	var aggDoc map[string]interface{}
	if err := yamlv3.Unmarshal(aggRaw, &aggDoc); err != nil {
		t.Fatalf("unmarshal aggregator CR: %v", err)
	}
	aggSpec, _ := aggDoc["spec"].(map[string]interface{})
	if aggSpec == nil {
		t.Fatalf("aggregator CR has no spec; doc=%v", aggDoc)
	}
	// #5113 Facet A — the Flux file MUST carry the CRD-required spec.version
	// (chart/crds/blueprint.yaml `required: [version, card]`): a version-less
	// file is rejected by the kustomize-controller dry-run whenever the named
	// CR does not already exist, wedging the whole Kustomization.
	if asString(aggSpec["version"]) != "1.2.3" {
		t.Errorf("aggregator (Flux SSA) CR must carry the real spec.version (#5113 Facet A); got %v", aggSpec["version"])
	}
	// #5113 Facet B — the aggregator CR is stamped to the canonical live-CR
	// identity, never a bare name whose inventory swap prunes the live CR.
	aggMeta, _ := aggDoc["metadata"].(map[string]interface{})
	if aggMeta == nil || aggMeta["name"] != "bp-wordpress" {
		t.Errorf("aggregator CR name must be the canonical bp-wordpress (#5113 Facet B); meta=%v", aggMeta)
	}
	// Every other spec field survives too.
	if _, ok := aggSpec["manifests"].(map[string]interface{}); !ok {
		t.Errorf("aggregator CR must keep the full spec (e.g. manifests); spec=%v", aggSpec)
	}
	if !strings.Contains(string(aggRaw), "RECONCILE-PROOF") {
		t.Errorf("aggregator CR must carry the edited summary; got:\n%s", aggRaw)
	}
	// The aggregator path is the one the Flux Kustomization sweeps.
	if got := catalogSovereignAggPath("t99.omani.works", "bp-wordpress"); got != aggPath {
		t.Errorf("catalogSovereignAggPath = %q, want %q", got, aggPath)
	}
}

// TestWriteCatalogEditToGit_NoAggregatorOnMothership — #3668: the Catalyst-
// Zero mothership (SOVEREIGN_FQDN empty) has NO local catalog-sovereign Flux
// reconcile (it pushes to github.com), so the aggregator write must NOT fire.
// The per-Blueprint write still lands (it is the read overlay everywhere).
func TestWriteCatalogEditToGit_NoAggregatorOnMothership(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	if _, err := h.writeCatalogEditToGit(context.Background(),
		catalogEdit{Slug: "alloy", Name: "X", Tagline: "y"}); err != nil {
		t.Fatalf("writeCatalogEditToGit: %v", err)
	}
	for k := range fg.files {
		if strings.Contains(k, "/catalog-sovereign/bp-") && strings.HasPrefix(k, catalogSovereignAggOrg+"/"+catalogSovereignAggRepo+"/") {
			t.Errorf("mothership must not write the Flux aggregator tree; found key %q", k)
		}
	}
}

// TestWriteCatalogSovereignAggregator_BestEffortGuards — the aggregator
// mirror no-ops (committed=false, err=nil) when unwired or FQDN-less, and
// never blocks the edit. Erroring Gitea surfaces an err for the caller to
// log+swallow (the edit API still returns the per-Blueprint verdict).
func TestWriteCatalogSovereignAggregator_BestEffortGuards(t *testing.T) {
	// Unwired client → no-op.
	h := NewWithPDM(silentLogger(), &fakePDM{})
	if c, err := h.writeCatalogSovereignAggregator(context.Background(), "bp-alloy", []byte("x")); c || err != nil {
		t.Errorf("unwired: want (false,nil); got (%v,%v)", c, err)
	}

	// Wired but FQDN empty → no-op.
	t.Setenv("SOVEREIGN_FQDN", "")
	fg := newFakeGitea()
	h.SetGiteaClient(fg)
	if c, err := h.writeCatalogSovereignAggregator(context.Background(), "bp-alloy", []byte("x")); c || err != nil {
		t.Errorf("no-FQDN: want (false,nil); got (%v,%v)", c, err)
	}
	if len(fg.files) != 0 {
		t.Errorf("no-FQDN must write nothing; keys=%v", fileKeys(fg))
	}

	// Wired + FQDN + erroring PutFile → err returned (caller logs/surfaces).
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	fgErr := newFakeGitea()
	fgErr.putFileErr = errors.New("gitea: 500")
	h.SetGiteaClient(fgErr)
	validCR := []byte("apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-alloy\nspec:\n  version: \"1.0.0\"\n")
	if _, err := h.writeCatalogSovereignAggregator(context.Background(), "bp-alloy", validCR); err == nil {
		t.Errorf("erroring PutFile must return a non-nil err for the caller to log")
	}

	// Wired + FQDN + UNPARSABLE payload → err surfaced, nothing committed —
	// an unparsable file in the Flux tree is the Kustomization-wide wedge
	// class (#5113), never a best-effort write.
	fgOK := newFakeGitea()
	h.SetGiteaClient(fgOK)
	if _, err := h.writeCatalogSovereignAggregator(context.Background(), "bp-alloy", []byte("\t::not yaml::\t")); err == nil {
		t.Errorf("an unparsable aggregator payload must surface an error, not commit")
	}
	if len(fgOK.files) != 0 {
		t.Errorf("an unparsable aggregator payload must not be committed; keys=%v", fileKeys(fgOK))
	}
}

// TestStampBlueprintCRForFluxAggregator_RoundTripsLiveCRFaithfully — #5113
// Facets A+B+C+E unit on the canonical commit serializer, fed the exact
// hw255 failure shape: a live CR dump with a BARE metadata.name, the full
// server-side metadata (uid / resourceVersion / generation /
// creationTimestamp / managedFields / status / last-applied), AND the Helm
// ownership + catalog-seed labels/annotations. The committed YAML must:
//   - stamp the canonical bp-<slug> name (Facet B — bare `name: wordpress`
//     made Flux prune the live bp-wordpress CR);
//   - keep spec.version + every spec field (Facet A — a version-less file is
//     CRD-rejected on create, wedging the Kustomization);
//   - drop ONLY the server-side fields (Facet C — a pinned uid produced the
//     stale-uid Conflict wedge);
//   - PRESERVE the Helm ownership labels/annotations (Facet E — without them
//     a prune-recreated CR breaks the next `helm upgrade` with no console
//     repair path).
func TestStampBlueprintCRForFluxAggregator_RoundTripsLiveCRFaithfully(t *testing.T) {
	in := []byte(`apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: wordpress
  uid: 6d1f7f2e-dead-beef-a5b0-3c1de1a2b3c4
  resourceVersion: "123456"
  generation: 7
  creationTimestamp: "2026-07-10T08:00:00Z"
  managedFields:
    - manager: helm
      operation: Update
  labels:
    app.kubernetes.io/managed-by: Helm
    catalyst.openova.io/catalog-seed: "true"
  annotations:
    meta.helm.sh/release-name: catalyst-platform
    meta.helm.sh/release-namespace: catalyst
    catalyst.openova.io/alias-of: wordpress
    kubectl.kubernetes.io/last-applied-configuration: '{"apiVersion":"catalyst.openova.io/v1"}'
spec:
  version: "0.4.12"
  visibility: listed
  card:
    title: WordPress Turnkey
    summary: curated summary
  source:
    kind: HelmRepository
    type: oci
    url: oci://ghcr.io/openova-io
    chart: bp-wordpress
    version: 0.4.12
  manifests:
    chart: bp-wordpress
status:
  observedGeneration: 7
`)
	out := stampBlueprintCRForFluxAggregator(in, "bp-wordpress")
	if len(out) == 0 {
		t.Fatal("canonical serializer returned empty output for a valid CR")
	}
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal canonical CR: %v", err)
	}

	// Facet B — canonical name.
	meta, _ := doc["metadata"].(map[string]interface{})
	if meta == nil || meta["name"] != "bp-wordpress" {
		t.Errorf("metadata.name must be stamped to bp-wordpress; meta=%v", meta)
	}

	// Facet C — server-side fields gone.
	for _, k := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields"} {
		if _, leaked := meta[k]; leaked {
			t.Errorf("metadata.%s must be stripped from the committed source; meta=%v", k, meta)
		}
	}
	if _, leaked := doc["status"]; leaked {
		t.Errorf("status must be stripped from the committed source")
	}

	// Facet E — Helm ownership + catalog labels/annotations preserved.
	lbl, _ := meta["labels"].(map[string]interface{})
	if lbl == nil || lbl["app.kubernetes.io/managed-by"] != "Helm" || lbl["catalyst.openova.io/catalog-seed"] != "true" {
		t.Errorf("Helm/catalog-seed labels must be preserved; labels=%v", lbl)
	}
	ann, _ := meta["annotations"].(map[string]interface{})
	if ann == nil || ann["meta.helm.sh/release-name"] != "catalyst-platform" ||
		ann["meta.helm.sh/release-namespace"] != "catalyst" ||
		ann["catalyst.openova.io/alias-of"] != "wordpress" {
		t.Errorf("Helm ownership + alias-of annotations must be preserved; annotations=%v", ann)
	}
	if _, leaked := ann["kubectl.kubernetes.io/last-applied-configuration"]; leaked {
		t.Errorf("last-applied-configuration is apply bookkeeping and must be stripped; annotations=%v", ann)
	}

	// Facet A — spec round-trips in full, version included.
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		t.Fatalf("canonical CR lost its spec; doc=%v", doc)
	}
	if asString(spec["version"]) != "0.4.12" {
		t.Errorf("spec.version (CRD-required) must survive; got %v", spec["version"])
	}
	if _, ok := spec["source"].(map[string]interface{}); !ok {
		t.Errorf("spec.source must survive; spec=%v", spec)
	}
	if _, ok := spec["manifests"].(map[string]interface{}); !ok {
		t.Errorf("spec.manifests must survive; spec=%v", spec)
	}
	card, _ := spec["card"].(map[string]interface{})
	if card == nil || card["summary"] != "curated summary" {
		t.Errorf("spec.card must survive; card=%v", card)
	}

	// Un-parseable input renders nil — the callers surface that instead of
	// committing a wedge-class file.
	if got := stampBlueprintCRForFluxAggregator([]byte("\t::not yaml::\t"), "bp-x"); got != nil {
		t.Errorf("un-parseable input must render nil; got %q", got)
	}
}

// TestWriteCatalogEditToGit_CanonicalizesLegacyServerMetadata — #5113 Facets
// B+C on the card-save leg end-to-end: when the EXISTING per-Blueprint Gitea
// file carries the legacy hw255 shape (bare name + uid + resourceVersion +
// Helm annotations, committed by an older build), the next card edit must
// commit BOTH files (per-Blueprint + Flux aggregator) with the canonical
// bp- name, the server junk scrubbed, spec.version intact, and the Helm
// ownership annotations preserved.
func TestWriteCatalogEditToGit_CanonicalizesLegacyServerMetadata(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// Seed the legacy per-Blueprint file exactly as an older build left it.
	legacy := []byte(`apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: wordpress
  uid: 6d1f7f2e-dead-beef-a5b0-3c1de1a2b3c4
  resourceVersion: "123456"
  creationTimestamp: "2026-07-10T08:00:00Z"
  annotations:
    meta.helm.sh/release-name: catalyst-platform
spec:
  version: "0.4.12"
  visibility: listed
  card:
    title: WordPress Turnkey
    summary: curated summary
`)
	perBPKey := giteaKey(catalogSovereignOrg, "bp-wordpress", catalogEditGitBranch, catalogEditBlueprintPath)
	fg.files[perBPKey] = legacy
	fg.repos[catalogSovereignOrg+"/bp-wordpress"] = true
	fg.orgs[catalogSovereignOrg] = true

	edit := catalogEdit{Slug: "wordpress", IconLight: "new-icon.svg"}
	if _, err := h.writeCatalogEditToGit(context.Background(), edit); err != nil {
		t.Fatalf("writeCatalogEditToGit: %v", err)
	}

	aggKey := giteaKey(catalogSovereignAggOrg, catalogSovereignAggRepo, catalogSovereignAggBranch,
		catalogSovereignAggPath("t99.omani.works", "bp-wordpress"))
	for _, tc := range []struct{ leg, key string }{
		{"per-Blueprint", perBPKey},
		{"Flux aggregator", aggKey},
	} {
		raw, ok := fg.files[tc.key]
		if !ok {
			t.Fatalf("%s write missing at %s; keys=%v", tc.leg, tc.key, fileKeys(fg))
		}
		var doc map[string]interface{}
		if err := yamlv3.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: unmarshal committed CR: %v", tc.leg, err)
		}
		meta, _ := doc["metadata"].(map[string]interface{})
		if meta == nil || meta["name"] != "bp-wordpress" {
			t.Errorf("%s: name must be canonical bp-wordpress (Facet B); meta=%v", tc.leg, meta)
		}
		for _, k := range []string{"uid", "resourceVersion", "creationTimestamp"} {
			if _, leaked := meta[k]; leaked {
				t.Errorf("%s: metadata.%s must be scrubbed (Facet C); meta=%v", tc.leg, k, meta)
			}
		}
		ann, _ := meta["annotations"].(map[string]interface{})
		if ann == nil || ann["meta.helm.sh/release-name"] != "catalyst-platform" {
			t.Errorf("%s: Helm ownership annotation must be preserved (Facet E); annotations=%v", tc.leg, ann)
		}
		spec, _ := doc["spec"].(map[string]interface{})
		if spec == nil || asString(spec["version"]) != "0.4.12" {
			t.Errorf("%s: spec.version must survive (Facet A); spec=%v", tc.leg, spec)
		}
		card, _ := spec["card"].(map[string]interface{})
		if card == nil || card["iconLight"] != "new-icon.svg" {
			t.Errorf("%s: the card edit must land; card=%v", tc.leg, card)
		}
		if card["summary"] != "curated summary" {
			t.Errorf("%s: the untouched curated summary must survive; card=%v", tc.leg, card)
		}
	}
}

// TestCommitCatalogAppEditToGit_AggregatorFailureSurfaced — #5113 Facet D on
// the card-save leg: when the per-Blueprint source write lands but the Flux
// aggregator leg (the write that actually reaches the live CR) fails, the
// verdict must be committed=false with a reason — never the silent
// store/IaC split-brain `committed:true` the hw255 walk caught. Mirrors the
// #5018 honesty fix already shipped on the full-CR /iac leg.
func TestCommitCatalogAppEditToGit_AggregatorFailureSurfaced(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	// Fail ONLY the aggregator write (openova/openova @ catalog-sovereign);
	// the per-Blueprint source write succeeds.
	fg.putFileErrFor = func(org, repo, branch, _ string) error {
		if org == catalogSovereignAggOrg && repo == catalogSovereignAggRepo && branch == catalogSovereignAggBranch {
			return errors.New("gitea: 500 on aggregator")
		}
		return nil
	}
	h.SetGiteaClient(fg)

	body := []byte(`{"slug":"wordpress","name":"WordPress","tagline":"edited"}`)
	committed, reason := h.commitCatalogAppEditToGit(context.Background(), body)
	if committed {
		t.Fatalf("a failed Flux reconcile leg must report committed=false (the edit never reaches the live CR)")
	}
	if !strings.Contains(reason, "Flux reconcile leg failed") {
		t.Errorf("the reason must name the failed reconcile leg; got %q", reason)
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
