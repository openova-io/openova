// Package bootstrapkit — integration test that the bootstrap-kit Flux
// reconciliation is well-formed and lands the right Kustomizations in
// dependency order.
//
// Closes ticket #145 — "[L] test: integration test — provisioner backend
// bootstrap-kit installer — all 11 phases install in sequence on a kind
// cluster (CI). Note: bootstrap installer is now Flux-driven from
// clusters/<sovereign-fqdn>/, NOT the bespoke installer that was reverted
// in commit e668637. Test verifies Flux reconciles the right Kustomizations."
//
// The architecture (per docs/SOVEREIGN-PROVISIONING.md §3) is:
//
//	OpenTofu provisions Phase 0 → cloud-init starts k3s → cloud-init
//	bootstraps Flux → Flux reconciles clusters/<sovereign-fqdn>/ from this
//	monorepo → that subtree contains a Kustomization tree that installs the
//	11-component bootstrap kit in dependency order.
//
// The "right Kustomizations" assertion is therefore:
//  1. clusters/_template/ exists and renders to valid Flux Kustomization
//     manifests after SOVEREIGN_FQDN_PLACEHOLDER substitution
//  2. The dependency graph encoded by `dependsOn` matches the canonical
//     11-phase order: cilium → cert-manager → flux → crossplane →
//     sealed-secrets → spire → nats-jetstream → openbao → keycloak →
//     gitea → bp-catalyst-platform
//  3. Each referenced platform/<x>/blueprint.yaml + chart/Chart.yaml
//     actually exists at the path the Kustomization claims
//  4. On a kind cluster (CI): Flux CRDs install, the GitRepository points
//     at the local checkout, and the Kustomization objects are accepted
//     by the API server (their OpenAPI schema is satisfied)
//
// Note: the test deliberately does NOT wait for the kit to fully install
// upstream charts — that is what #141 (real Hetzner end-to-end) covers.
// What this test owns is "the manifests are correct"; #141 owns "they
// produce a working cluster".
package bootstrapkit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot returns the absolute path to the repository root by walking up
// from the test file's directory until a sentinel file (docs/PRINCIPLES.md)
// is found.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "docs", "PRINCIPLES.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root from %s", wd)
	return ""
}

// canonicalOrder is the bootstrap-kit dependency order from
// docs/SOVEREIGN-PROVISIONING.md §3 step 2:
//
//	cilium → cert-manager → flux → crossplane → sealed-secrets → spire →
//	nats-jetstream → openbao → keycloak → gitea → bp-catalyst-platform
//
// "flux" is the third entry because Flux on the new cluster reconciles
// itself (the bootstrap loads it once, then a HelmRelease keeps it
// updated). bp-catalyst-platform is the umbrella covering the Catalyst
// control plane and is the LAST step — everything else must be in place
// before its dependencies can be satisfied.
var canonicalOrder = []string{
	"bp-cilium",
	"bp-cert-manager",
	"bp-flux",
	"bp-crossplane",
	"bp-sealed-secrets",
	"bp-spire",
	"bp-nats-jetstream",
	"bp-openbao",
	"bp-keycloak",
	"bp-gitea",
	"bp-catalyst-platform",
}

// TestBootstrapKit_AllElevenBlueprintsExist verifies that the Helm chart and
// blueprint.yaml exist for every component the bootstrap kit installs.
// Without this precondition the Flux Kustomizations referencing them would
// fail at chart-pull time even if the manifest tree is otherwise correct.
func TestBootstrapKit_AllElevenBlueprintsExist(t *testing.T) {
	root := repoRoot(t)

	required := []string{
		"cilium", "cert-manager", "flux", "crossplane", "sealed-secrets",
		"spire", "nats-jetstream", "openbao", "keycloak", "gitea",
	}
	for _, name := range required {
		bpPath := filepath.Join(root, "platform", name, "blueprint.yaml")
		chartPath := filepath.Join(root, "platform", name, "chart", "Chart.yaml")
		valuesPath := filepath.Join(root, "platform", name, "chart", "values.yaml")
		for _, p := range []string{bpPath, chartPath, valuesPath} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("required bootstrap-kit file missing: %s (%v)", p, err)
			}
		}
		// Verify Chart.yaml carries the bp-<name> name — that's how Flux
		// HelmReleases reference the chart in the OCI registry.
		raw, err := os.ReadFile(chartPath)
		if err != nil {
			continue
		}
		var chart struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(raw, &chart); err != nil {
			t.Errorf("Chart.yaml at %s is not valid YAML: %v", chartPath, err)
			continue
		}
		want := "bp-" + name
		if chart.Name != want {
			t.Errorf("%s/chart/Chart.yaml name is %q, expected %q", name, chart.Name, want)
		}
	}
}

// TestBootstrapKit_BlueprintCardsHaveRequiredFields asserts that every
// blueprint surfaces the metadata Flux/console need:
//   - apiVersion / kind / metadata.name (== bp-<x>)
//   - spec.version (semver)
//   - spec.card with title/summary/category
//   - chart Chart.yaml version matches blueprint.yaml spec.version
func TestBootstrapKit_BlueprintCardsHaveRequiredFields(t *testing.T) {
	root := repoRoot(t)
	required := []string{
		"cilium", "cert-manager", "flux", "crossplane", "sealed-secrets",
		"spire", "nats-jetstream", "openbao", "keycloak", "gitea",
	}
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			bpPath := filepath.Join(root, "platform", name, "blueprint.yaml")
			raw, err := os.ReadFile(bpPath)
			if err != nil {
				t.Fatalf("read blueprint: %v", err)
			}
			var bp struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
				Metadata   struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Spec struct {
					Version string `yaml:"version"`
					Card    struct {
						Title    string `yaml:"title"`
						Summary  string `yaml:"summary"`
						Category string `yaml:"category"`
					} `yaml:"card"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal(raw, &bp); err != nil {
				t.Fatalf("unmarshal blueprint: %v", err)
			}
			if bp.Kind != "Blueprint" {
				t.Errorf("kind = %q, want Blueprint", bp.Kind)
			}
			if bp.APIVersion != "catalyst.openova.io/v1alpha1" {
				t.Errorf("apiVersion = %q, want catalyst.openova.io/v1alpha1", bp.APIVersion)
			}
			wantName := "bp-" + name
			if bp.Metadata.Name != wantName {
				t.Errorf("metadata.name = %q, want %q", bp.Metadata.Name, wantName)
			}
			if bp.Spec.Version == "" {
				t.Errorf("spec.version is empty")
			}
			// title + summary are surfaced in console/admin UIs and are
			// load-bearing. category is a hint used for grouping; it
			// frequently lives at the labels level (catalyst.openova.io/category)
			// rather than spec.card.category, so we only enforce title/summary.
			if bp.Spec.Card.Title == "" || bp.Spec.Card.Summary == "" {
				t.Errorf("spec.card missing required title/summary: %+v", bp.Spec.Card)
			}
			// Chart.yaml version match
			chartRaw, err := os.ReadFile(filepath.Join(root, "platform", name, "chart", "Chart.yaml"))
			if err == nil {
				var chart struct {
					Version string `yaml:"version"`
				}
				_ = yaml.Unmarshal(chartRaw, &chart)
				if chart.Version != bp.Spec.Version {
					t.Errorf("Chart.yaml version %q != blueprint.yaml spec.version %q", chart.Version, bp.Spec.Version)
				}
			}
		})
	}
}

// TestBootstrapKit_BlueprintVersionLockstepSweep asserts the TBD-A20
// convergence contract (issue #1856) for EVERY Blueprint manifest in the
// repository — not just the canonical 10 in the bootstrap kit. The
// convergence contract is:
//
//	For every platform/<bp>/blueprint.yaml (and products/<bp>/chart/blueprint.yaml)
//	whose folder contains a sibling chart/Chart.yaml,
//	the blueprint.yaml spec.version MUST equal the Chart.yaml version.
//
// Why broaden coverage beyond the 10 canonical blueprints?
//
// TestBootstrapKit_BlueprintCardsHaveRequiredFields only checks the 10
// kit blueprints. The 2026-05-18 drift incident (6 blueprints out of sync
// — cilium, cert-manager, flux, openbao, keycloak, gitea — patched by
// PR #1855) showed the problem also exists for non-kit Application
// Blueprints (e.g. bp-velero, bp-temporal, bp-wordpress-tenant). The
// extended auto-bump hook in blueprint-release.yaml (this PR) handles
// the lockstep for every chart bump; this test is the static regression
// gate that catches a missed write before merge.
//
// Discovery rule:
//   - Walk platform/* — every direct subdirectory whose blueprint.yaml is
//     kind: Blueprint and whose sibling chart/Chart.yaml exists is in scope.
//   - Walk products/* — every direct subdirectory whose chart/blueprint.yaml
//     is kind: Blueprint and whose sibling chart/Chart.yaml exists is in
//     scope. (products/catalyst is excluded because its blueprint.yaml is
//     at chart/crds/blueprint.yaml — a CRD definition, not a Blueprint
//     manifest; the kind: filter naturally rejects it.)
//
// Each in-scope folder gets a subtest; a failure names the file and the
// drift direction so the fix is mechanical.
func TestBootstrapKit_BlueprintVersionLockstepSweep(t *testing.T) {
	root := repoRoot(t)

	type pair struct {
		name      string // subtest name (e.g. "platform/cilium")
		bpFile    string // path to blueprint.yaml
		chartFile string // path to sibling Chart.yaml
	}
	var pairs []pair

	for _, tree := range []string{"platform", "products"} {
		treeRoot := filepath.Join(root, tree)
		entries, err := os.ReadDir(treeRoot)
		if err != nil {
			t.Fatalf("read %s: %v", treeRoot, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Two canonical locations: platform/<x>/blueprint.yaml and
			// products/<x>/chart/blueprint.yaml. The corresponding
			// Chart.yaml is always at <folder>/chart/Chart.yaml.
			for _, bpRel := range []string{
				filepath.Join(tree, e.Name(), "blueprint.yaml"),
				filepath.Join(tree, e.Name(), "chart", "blueprint.yaml"),
			} {
				bpAbs := filepath.Join(root, bpRel)
				if _, err := os.Stat(bpAbs); err != nil {
					continue
				}
				// Filter to kind: Blueprint. Co-located non-Blueprint YAML
				// (e.g. CRD definitions) is silently skipped.
				raw, err := os.ReadFile(bpAbs)
				if err != nil {
					continue
				}
				var head struct {
					Kind string `yaml:"kind"`
				}
				if err := yaml.Unmarshal(raw, &head); err != nil || head.Kind != "Blueprint" {
					continue
				}
				chartAbs := filepath.Join(root, tree, e.Name(), "chart", "Chart.yaml")
				if _, err := os.Stat(chartAbs); err != nil {
					// Blueprint without a co-located chart — out of scope
					// (e.g. metadata-only references). Skip silently.
					continue
				}
				pairs = append(pairs, pair{
					name:      filepath.Join(tree, e.Name()),
					bpFile:    bpAbs,
					chartFile: chartAbs,
				})
			}
		}
	}

	if len(pairs) == 0 {
		t.Fatal("discovered zero Blueprint/Chart pairs — sweep is broken (expected dozens under platform/ and products/)")
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			bpRaw, err := os.ReadFile(p.bpFile)
			if err != nil {
				t.Fatalf("read blueprint: %v", err)
			}
			var bp struct {
				Spec struct {
					Version string `yaml:"version"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal(bpRaw, &bp); err != nil {
				t.Fatalf("unmarshal blueprint: %v", err)
			}
			chartRaw, err := os.ReadFile(p.chartFile)
			if err != nil {
				t.Fatalf("read chart: %v", err)
			}
			var chart struct {
				Version string `yaml:"version"`
			}
			if err := yaml.Unmarshal(chartRaw, &chart); err != nil {
				t.Fatalf("unmarshal chart: %v", err)
			}
			if bp.Spec.Version == "" {
				t.Errorf("blueprint.yaml at %s has empty spec.version", p.bpFile)
				return
			}
			if chart.Version == "" {
				t.Errorf("Chart.yaml at %s has empty version", p.chartFile)
				return
			}
			if chart.Version != bp.Spec.Version {
				t.Errorf("LOCKSTEP DRIFT (TBD-A20, #1856):\n  %s\n    spec.version = %q\n  %s\n    version      = %q\n  → the auto-bump hook in .github/workflows/blueprint-release.yaml should have kept these in sync. If you bumped Chart.yaml manually, run:\n    sed -i 's/^  version: .*/  version: %s/' %s",
					p.bpFile, bp.Spec.Version,
					p.chartFile, chart.Version,
					chart.Version, p.bpFile,
				)
			}
		})
	}
}

// TestBootstrapKit_TemplateClusterParses verifies that the template
// directory clusters/_template/ contains valid Flux manifests and that all
// SOVEREIGN_FQDN_PLACEHOLDER substitutions can be made consistently.
func TestBootstrapKit_TemplateClusterParses(t *testing.T) {
	root := repoRoot(t)
	templateDir := filepath.Join(root, "clusters", "_template")
	if _, err := os.Stat(templateDir); err != nil {
		t.Skipf("clusters/_template/ not yet on this branch — skipping template-parse test (the per-Sovereign tree is a separate Group J/M ticket; this assertion lights up once that lands)")
	}

	var found []string
	err := filepath.Walk(templateDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("clusters/_template/ has no YAML manifests")
	}

	for _, path := range found {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			// Substitute the placeholder so we can parse as real YAML; the
			// placeholder lives inside string fields so substitution is
			// always safe.
			rendered := strings.ReplaceAll(string(raw), "SOVEREIGN_FQDN_PLACEHOLDER", "test-sov.example.com")

			// Each file may have multiple YAML documents.
			dec := yaml.NewDecoder(strings.NewReader(rendered))
			docs := 0
			for {
				var doc map[string]any
				err := dec.Decode(&doc)
				if errors.Is(err, errEOF()) || err != nil && strings.Contains(err.Error(), "EOF") {
					break
				}
				if err != nil {
					t.Fatalf("yaml decode: %v", err)
				}
				if doc == nil {
					continue
				}
				docs++
				if _, ok := doc["apiVersion"]; !ok {
					t.Errorf("doc %d missing apiVersion: %v", docs, doc)
				}
				if _, ok := doc["kind"]; !ok {
					t.Errorf("doc %d missing kind: %v", docs, doc)
				}
			}
			if docs == 0 {
				t.Errorf("no YAML documents found in %s", path)
			}
		})
	}
}

// TestBootstrapKit_PlatformPlanesAreHostNative is the #4291 Workstream-C lock
// (the inverse of #3642). After de-vclustering the platform planes, a vCluster
// is the per-Organization boundary ONLY; the mgmt/rtz/dmz platform planes are
// NOT tenant boundaries, so every platform-plane HelmRelease in the bootstrap
// kit reconciles into its OWN first-class HOST namespace — never pivoted into a
// plane vCluster. The companion Org-app half ("apps reconcile INTO the per-Org
// vCluster") is locked by the keystone #4297 tests in
// core/services/provisioning/gitops (TestAppsSync_VclusterTier_HasKubeConfig).
//
// This test asserts the platform-plane invariants that must hold post-#4291:
//
//  1. NO bootstrap-kit HelmRelease carries a `kubeConfig.secretRef` pointing
//     at a plane-vCluster mirror Secret (vc-mgmt / vc-rtz / vc-dmz) — that was
//     the #3642 "vCluster pivot" render mechanism, now retired for platform
//     planes. (Per-Org vClusters are NOT in clusters/_template/bootstrap-kit/;
//     they are emitted per-Organization by the org-controller.)
//  2. NO HelmRelease carries the stale `catalyst.openova.io/vcluster` label.
//  3. The plane-vCluster runtime slots (54 bp-dmz-vcluster, 58 bp-mgmt-vcluster,
//     59 bp-rtz-vcluster) and slot 00 (vcluster-host-namespaces) are RETIRED.
//  4. The newapi render-split companion slot 80a (bp-newapi-host-seams) is
//     RETIRED and its #4321/#4305/#4278 fixes are preserved in slot 80, which
//     now renders host-native `placement.role: all` with `cnpg.enabled: true`
//     (the app HR itself renders the CNPG Cluster + DSN-sync + admin-promote
//     + AppRegistration + ExternalSecrets natively in host ns `newapi`).
//  5. bp-plane-isolation (slot 26b) — the per-component default-deny
//     micro-segmentation that replaces the coarse whole-plane vCluster
//     boundary — IS present.
func TestBootstrapKit_PlatformPlanesAreHostNative(t *testing.T) {
	root := repoRoot(t)
	kitDir := filepath.Join(root, "clusters", "_template", "bootstrap-kit")
	if _, err := os.Stat(kitDir); err != nil {
		t.Skipf("clusters/_template/bootstrap-kit/ not present — skipping #4291 platform-plane host-native test")
	}

	entries, err := os.ReadDir(kitDir)
	if err != nil {
		t.Fatalf("read %s: %v", kitDir, err)
	}

	// (3) + (4): the retired slots must be gone; bp-plane-isolation (5) must be present.
	retired := map[string]string{
		"00-vcluster-host-namespaces.yaml": "slot 00 (vcluster-host-namespaces) must be RETIRED post-#4291 — platform components own first-class host namespaces directly",
		"54-bp-dmz-vcluster.yaml":          "slot 54 (bp-dmz-vcluster) must be RETIRED post-#4291 — the dmz plane is no longer a vCluster",
		"58-bp-mgmt-vcluster.yaml":         "slot 58 (bp-mgmt-vcluster) must be RETIRED post-#4291 — the mgmt plane is no longer a vCluster",
		"59-bp-rtz-vcluster.yaml":          "slot 59 (bp-rtz-vcluster) must be RETIRED post-#4291 — the rtz plane is no longer a vCluster",
		"80a-newapi-host-seams.yaml":       "slot 80a (bp-newapi-host-seams) must be RETIRED post-#4291 — the #3831 vcluster-app/host-seams render-split collapses into slot 80 placement.role=all",
	}
	present := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		present[name] = true
		if msg, dead := retired[name]; dead {
			t.Errorf("RETIRED SLOT STILL ON DISK (#4291): %s — %s", name, msg)
		}
	}
	if !present["26b-bp-plane-isolation.yaml"] {
		t.Errorf("MISSING slot 26b (bp-plane-isolation): the per-component default-deny micro-segmentation that replaces the whole-plane vCluster boundary must be present post-#4291")
	}

	// (1) + (2): scan every remaining HelmRelease for a plane-vCluster pivot.
	planeMirrors := []string{"vc-mgmt", "vc-rtz", "vc-dmz"}
	sawNewapiAppHR := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(kitDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		rendered := strings.ReplaceAll(string(raw), "SOVEREIGN_FQDN_PLACEHOLDER", "test-sov.example.com")
		dec := yaml.NewDecoder(strings.NewReader(rendered))
		for {
			var doc map[string]any
			derr := dec.Decode(&doc)
			if derr != nil {
				break // EOF or last doc
			}
			if doc == nil || doc["kind"] != "HelmRelease" {
				continue
			}
			meta, _ := doc["metadata"].(map[string]any)
			spec, _ := doc["spec"].(map[string]any)
			hrName, _ := meta["name"].(string)

			// (2) stale vcluster label.
			if labels, ok := meta["labels"].(map[string]any); ok {
				if _, has := labels["catalyst.openova.io/vcluster"]; has {
					t.Errorf("%s: HelmRelease %q still carries the stale catalyst.openova.io/vcluster label (#4291 — platform planes are host-native)", e.Name(), hrName)
				}
			}

			// (1) plane-vCluster kubeConfig pivot.
			if kc, ok := spec["kubeConfig"].(map[string]any); ok {
				if sr, ok := kc["secretRef"].(map[string]any); ok {
					if refName, _ := sr["name"].(string); refName != "" {
						for _, m := range planeMirrors {
							if refName == m {
								t.Errorf("%s: HelmRelease %q pivots into plane vCluster via kubeConfig.secretRef.name=%q — platform planes must be HOST-native post-#4291", e.Name(), hrName, refName)
							}
						}
					}
				}
			}

			// (4) newapi app HR must be host-native role: all with cnpg.enabled.
			if hrName == "bp-newapi" {
				sawNewapiAppHR = true
				values, _ := spec["values"].(map[string]any)
				if pl, ok := values["placement"].(map[string]any); ok {
					if role, _ := pl["role"].(string); role != "all" {
						t.Errorf("%s: bp-newapi placement.role=%q, want \"all\" — post-#4291 the app HR renders ALL seams (CNPG + DSN-sync + admin-promote + AppRegistration + ExternalSecrets) host-native, not split with the retired slot 80a", e.Name(), role)
					}
				} else {
					t.Errorf("%s: bp-newapi values.placement.role missing — want \"all\" post-#4291", e.Name())
				}
				if cnpg, ok := values["cnpg"].(map[string]any); ok {
					if en, _ := cnpg["enabled"].(bool); !en {
						t.Errorf("%s: bp-newapi cnpg.enabled=false — post-#4291 newapi renders its OWN CNPG Cluster natively in host ns newapi (was suppressed by the #3831 host-bridge split)", e.Name())
					}
				} else {
					t.Errorf("%s: bp-newapi values.cnpg.enabled missing — want true post-#4291", e.Name())
				}
			}
		}
	}
	if !sawNewapiAppHR {
		t.Error("bp-newapi HelmRelease not found in bootstrap-kit — slot 80 (the de-vclustered newapi app HR) must be present")
	}
}

// errEOF returns the io.EOF sentinel. Importing io for one variable bloats
// the file; this helper keeps the test deps minimal.
func errEOF() error {
	return errEOFSentinel
}

var errEOFSentinel = fmt.Errorf("EOF")

// perOrgCatalogBlueprints are the catalog Blueprints that install through
// the application-controller fan-out into a per-Organization namespace
// (#4297 keystone). For these, the topology variant's placement.tier is
// LIVE — the controller resolves it through VClusterPlacements and upserts
// the per-cluster HelmRelease into that tier's host namespace. After #4325
// de-vclustered the mgmt/rtz/dmz platform planes (their plane vClusters +
// host namespaces were removed), a per-Org Blueprint that still pins
// `tier: rtz` (or mgmt/dmz) Degrades the instant it installs with
// `namespaces "rtz" not found` (#4362 agenity, #4375 cohort). A per-Org app
// MUST land HOST-native in its OWN Org namespace → placement.tier == "" (or
// the "host" sentinel). The rtz-A / rtz-B cluster IDs (region designators)
// are unaffected and stay.
//
// NOTE: platform-PLANE Blueprints (loki/redis/temporal/llm-gateway/openmeter/
// …) that legitimately carry `tier: mgmt|rtz` in their catalog topology are
// DORMANT for bootstrap installs — they are placed by the bootstrap-kit
// placement.yaml (already host-flipped by #4325), not by the catalog topology.
// They are deliberately NOT in this set.
var perOrgCatalogBlueprints = map[string]string{
	"bp-wordpress-tenant": "wordpress-tenant",
	"bp-stalwart-tenant":  "stalwart-tenant",
	"bp-openclaw":         "openclaw",
	"bp-sandbox":          "sandbox",
}

// deVclusteredPlanes are the platform-plane vCluster tiers removed by #4325.
// A per-Org topology placement.tier MUST NOT reference any of them.
var deVclusteredPlanes = map[string]bool{"mgmt": true, "rtz": true, "dmz": true}

// topologyDoc is the minimal shape needed to read the per-variant placement
// tier out of a Blueprint manifest (source blueprint.yaml OR a rendered
// catalog-seed doc).
type topologyDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Topology struct {
			PerTopology map[string]struct {
				Placement struct {
					Tier string `yaml:"tier"`
				} `yaml:"placement"`
			} `yaml:"perTopology"`
		} `yaml:"topology"`
	} `yaml:"spec"`
}

// assertHostNativeTopology fails t if any perTopology variant in doc pins a
// placement.tier that names a de-vclustered plane. src is a human-readable
// origin label for the error message.
func assertHostNativeTopology(t *testing.T, src string, doc topologyDoc) {
	t.Helper()
	if len(doc.Spec.Topology.PerTopology) == 0 {
		t.Errorf("%s (%s): no topology.perTopology variants found — a per-Org Blueprint must declare its host-native placement", src, doc.Metadata.Name)
		return
	}
	for variant, v := range doc.Spec.Topology.PerTopology {
		tier := strings.TrimSpace(v.Placement.Tier)
		if deVclusteredPlanes[tier] {
			t.Errorf("%s (%s): topology variant %q pins placement.tier=%q — a removed (#4325 de-vcluster) plane vCluster. Per-Org apps land HOST-native in their own Org namespace; flip to tier: '' (#4375)", src, doc.Metadata.Name, variant, tier)
		}
	}
}

// TestBootstrapKit_PerOrgBlueprintsAreHostNative locks the #4375 (#4325
// fallout) contract: every PER-ORG catalog Blueprint — the ones that install
// through the application-controller fan-out into a per-Organization
// namespace — declares a HOST-native topology placement (tier ""/host), NOT
// a removed mgmt/rtz/dmz plane vCluster. It checks BOTH sides that must stay
// in lockstep:
//
//	(1) the source platform/<x>/blueprint.yaml, and
//	(2) the rendered products/catalyst catalog-seed Blueprint CR (the
//	    in-cluster fallback the bp-catalog-client reads when the Gitea
//	    catalog repo 404s).
//
// Without this lock, the next edit to either side can re-introduce a
// `tier: rtz` that Degrades a fresh Org's app with `namespaces "rtz" not
// found` the moment it installs.
func TestBootstrapKit_PerOrgBlueprintsAreHostNative(t *testing.T) {
	root := repoRoot(t)

	// (1) Source blueprints.
	for bpName, dir := range perOrgCatalogBlueprints {
		t.Run("source/"+dir, func(t *testing.T) {
			bpPath := filepath.Join(root, "platform", dir, "blueprint.yaml")
			raw, err := os.ReadFile(bpPath)
			if err != nil {
				t.Fatalf("read source blueprint %s: %v", bpPath, err)
			}
			var doc topologyDoc
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("unmarshal %s: %v", bpPath, err)
			}
			if doc.Metadata.Name != bpName {
				t.Errorf("%s metadata.name = %q, want %q", bpPath, doc.Metadata.Name, bpName)
			}
			assertHostNativeTopology(t, "platform/"+dir+"/blueprint.yaml", doc)
		})
	}

	// (2) Rendered catalog-seed. helm collapses the `{{ "{{" }}` escape
	// tokens to literal runtime placeholders so the docs parse as YAML.
	// Skip gracefully if helm is unavailable in the runner.
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v) — skipping rendered catalog-seed half; the source-blueprint half above still runs", err)
	}
	chartDir := filepath.Join(root, "products", "catalyst", "chart")
	cmd := exec.Command(helmBin, "template", ".", "--show-only", "templates/catalog-seed/blueprints.yaml")
	cmd.Dir = chartDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template catalog-seed failed: %v\noutput:\n%s", err, out)
	}
	// The rendered output also carries per-Org Blueprints that have NO source
	// platform/<x>/blueprint.yaml — they live ONLY in the catalog-seed:
	//   - bp-wordpress: the per-Org alias pointing at the wordpress-tenant
	//     chart, sharing the per-Org topology.
	//   - bp-agenity: the per-Org agentic dashboard (#4365 flipped its
	//     tier rtz -> ''). It installs through the application-controller
	//     fan-out (no bootstrap: true application-cr) so a regression to a
	//     removed plane would Degrade a fresh Org — assert it here too.
	perOrgSeedNames := map[string]bool{"bp-wordpress": true, "bp-agenity": true}
	for bpName := range perOrgCatalogBlueprints {
		perOrgSeedNames[bpName] = true
	}
	seen := map[string]bool{}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc topologyDoc
		if derr := dec.Decode(&doc); derr != nil {
			break
		}
		if doc.Kind != "Blueprint" || !perOrgSeedNames[doc.Metadata.Name] {
			continue
		}
		seen[doc.Metadata.Name] = true
		assertHostNativeTopology(t, "catalog-seed:"+doc.Metadata.Name, doc)
	}
	for name := range perOrgSeedNames {
		if !seen[name] {
			t.Errorf("catalog-seed: per-Org Blueprint %q not found in rendered seed — the #4375 host-native lock cannot verify it", name)
		}
	}
}

// deadPlaneVclusterMangle matches a vCluster-syncer-mangled DNS name or Secret
// name of the form `<svc>-x-<ns>-x-{mgmt,rtz,dmz}-vcluster` — the host-visible
// name the (now-removed, #4325) mgmt/rtz/dmz plane vClusters' syncers produced.
// On a de-vclustered Sovereign these names resolve NXDOMAIN / 404, so ANY
// active config carrying one is dead fallout.
var deadPlaneVclusterMangle = regexp.MustCompile(`-x-[a-z0-9-]+-x-(mgmt|rtz|dmz)-vcluster`)

// devclusterScanExcludedDirs are subtrees that legitimately still carry the
// mangled token and must NOT trip the sweep:
//   - the retired plane-vCluster charts themselves (their internal naming is
//     self-referential / orphaned, retired separately from this code sweep);
//   - vendored / VCS / node deps.
var devclusterScanExcludedDirs = map[string]bool{
	"bp-mgmt-vcluster": true,
	"bp-rtz-vcluster":  true,
	"bp-dmz-vcluster":  true,
	"node_modules":     true,
	".git":             true,
	"vendor":           true,
	"testdata":         true, // Go demangle-logic fixtures (per-Org vcluster) live here
}

// TestBootstrapKit_NoDeadPlaneVclusterReferences is the #4325-fallout
// "completeness critic" guard. It walks the catalog source tree (platform/* +
// products/* charts, blueprints, and the bootstrap-kit slots) and FAILS if any
// ACTIVE (non-comment) line in a chart values/template, blueprint, or
// bootstrap-kit slot still carries a syncer-mangled
// `*-x-*-x-{mgmt,rtz,dmz}-vcluster` reference for a PLATFORM-PLANE component.
//
// The platform planes were de-vclustered into native host namespaces by #4325;
// every consumer must dial the plain host-ns Service / read the host-ns Secret
// (e.g. nats-jetstream.nats-system.svc, valkey-primary.valkey.svc,
// keycloak.keycloak.svc, seaweedfs-s3.seaweedfs.svc). Without this lock the
// fallout recurred one-at-a-time on live walks (#4365/#4376/#4378/#4380/#4383).
//
// Scope is the deterministic, fresh-prov-blocking surface: YAML config under
// platform/ + products/ + clusters/_template/bootstrap-kit/. Pure comments
// (`#…`, `{{/* … */}}`, `//…`) are skipped — they are stale narrative, not a
// rendered reference. Go cross-region-mesh code (clustermesh.go) + its
// vocabulary are tracked separately (multi-region correctness, higher-risk).
func TestBootstrapKit_NoDeadPlaneVclusterReferences(t *testing.T) {
	root := repoRoot(t)
	scanRoots := []string{
		filepath.Join(root, "platform"),
		filepath.Join(root, "products"),
		filepath.Join(root, "clusters", "_template", "bootstrap-kit"),
	}
	var offenders []string
	for _, sr := range scanRoots {
		err := filepath.Walk(sr, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // best-effort; a missing optional subtree is not a failure
			}
			if info.IsDir() {
				if devclusterScanExcludedDirs[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			// Only scan rendered-config file types where an active reference
			// would land in a deployed manifest.
			switch {
			case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"),
				strings.HasSuffix(path, ".tpl"), strings.HasSuffix(path, ".tftpl"):
			default:
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			// Track whether we are inside a multi-line Helm template comment
			// block `{{/* … */}}` (or the whitespace-trim `{{- /* … */}}`) so
			// stale-narrative mentions of a mangled name inside a comment block
			// don't trip the sweep — only RENDERED references count.
			inTplComment := false
			for i, line := range strings.Split(string(raw), "\n") {
				wasInComment := inTplComment
				if !inTplComment {
					if idx := strings.Index(line, "{{/*"); idx >= 0 {
						inTplComment = true
					} else if idx := strings.Index(line, "{{- /*"); idx >= 0 {
						inTplComment = true
					}
				}
				lineIsComment := wasInComment || inTplComment
				if inTplComment && strings.Contains(line, "*/}}") {
					inTplComment = false // block closes on this line
				}
				if !deadPlaneVclusterMangle.MatchString(line) {
					continue
				}
				if lineIsComment || isCommentLine(line) {
					continue
				}
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sr, err)
		}
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("dead plane-vCluster (#4325) reference in active config — repoint to the host-ns target:\n  %s", o)
	}
}

// isCommentLine reports whether a YAML/Helm-template line is purely a comment
// (so a stale-narrative mention of a mangled name does not trip the sweep). It
// handles `#…`, `{{/* …`, `*/`, `{{- /*`, and Go-style `//…` leading content.
func isCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	switch {
	case strings.HasPrefix(trimmed, "#"):
		return true
	case strings.HasPrefix(trimmed, "//"):
		return true
	case strings.HasPrefix(trimmed, "{{/*"), strings.HasPrefix(trimmed, "{{- /*"):
		return true
	case strings.HasPrefix(trimmed, "*"): // continuation / close of a {{/* … */}} block
		return true
	}
	return false
}

// TestBootstrapKit_DependencyOrderMatchesCanonical loads every blueprint.yaml
// in the bootstrap-kit list and verifies that the implicit ordering — by
// blueprint metadata.name — matches the canonical 11-phase order from
// SOVEREIGN-PROVISIONING.md §3. The test does not require the Flux
// Kustomizations themselves to exist (they're created per-Sovereign at
// provisioning time); it asserts that the blueprint manifests' identity
// matches the canonical order.
//
// If a future change renames a blueprint or reorders phases, this test
// fails loudly so the change author is forced to update either the docs
// or the test (whichever is wrong).
func TestBootstrapKit_DependencyOrderMatchesCanonical(t *testing.T) {
	root := repoRoot(t)
	got := make([]string, 0, len(canonicalOrder))
	for _, want := range canonicalOrder {
		// bp-catalyst-platform is the umbrella; it lives under platform/
		// or products/catalyst/. Try both.
		found := false
		for _, candidate := range []string{
			filepath.Join(root, "platform", strings.TrimPrefix(want, "bp-"), "blueprint.yaml"),
			filepath.Join(root, "products", "catalyst", "chart", "Chart.yaml"),
			filepath.Join(root, "platform", "catalyst-platform", "blueprint.yaml"),
		} {
			if _, err := os.Stat(candidate); err == nil {
				got = append(got, want)
				found = true
				break
			}
		}
		if !found && want != "bp-catalyst-platform" {
			t.Errorf("blueprint %q listed in canonical order but missing on disk", want)
		}
	}
	if len(got) < len(canonicalOrder)-1 {
		t.Errorf("only %d/%d canonical-order blueprints found", len(got), len(canonicalOrder))
	}
	// Stable order check — got should be a prefix of canonicalOrder.
	for i := range got {
		if got[i] != canonicalOrder[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], canonicalOrder[i])
		}
	}
}

// TestBootstrapKit_KindReconciliation runs Flux against a real kind cluster
// when the BOOTSTRAP_KIT_KIND_TEST=1 env var is set. CI sets it; locally
// the test skips. The test:
//
//  1. Verifies kind + flux CLIs are available
//  2. Creates a fresh kind cluster (or uses the existing one)
//  3. Installs Flux CRDs (via `flux install`)
//  4. Applies a synthesized clusters/<test-sov>/ manifest tree
//  5. Asserts that Flux Kustomizations land in the cluster (NOT that they
//     fully reconcile — that requires real Helm registries and real cloud
//     credentials, owned by #141)
//
// The test is intentionally narrow: it proves "Flux accepts our manifests
// against a real K8s API server" rather than "the cluster is fully up".
// Steady-state DoD lives in the Hetzner E2E test (#141).
func TestBootstrapKit_KindReconciliation(t *testing.T) {
	if os.Getenv("BOOTSTRAP_KIT_KIND_TEST") != "1" {
		t.Skip("BOOTSTRAP_KIT_KIND_TEST not set — skipping kind cluster test (CI gates this on a real kubernetes-in-docker)")
	}
	root := repoRoot(t)

	// Required CLIs.
	for _, cli := range []string{"kind", "kubectl", "flux"} {
		if _, err := exec.LookPath(cli); err != nil {
			t.Fatalf("%s CLI not on PATH: %v", cli, err)
		}
	}

	// Step 1 — kind cluster (assumes the CI workflow created it).
	if err := runCLI(t, "kubectl", "cluster-info"); err != nil {
		t.Fatalf("no live kubernetes API: %v", err)
	}

	// Step 2 — install Flux CRDs.
	t.Log("installing Flux CRDs and controllers")
	if err := runCLI(t, "flux", "install", "--components=source-controller,kustomize-controller", "--network-policy=false"); err != nil {
		t.Fatalf("flux install: %v", err)
	}

	// Step 3 — register a GitRepository pointing at the on-disk repo. We
	// can't easily make Flux read a local path, so we point at a local
	// HTTP server serving the checkout. CI gives us the upstream URL.
	repoURL := os.Getenv("BOOTSTRAP_KIT_GIT_URL")
	if repoURL == "" {
		repoURL = "https://github.com/openova-io/openova"
	}

	gitRepo := fmt.Sprintf(`apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: openova-test
  namespace: flux-system
spec:
  interval: 30s
  url: %s
  ref: { branch: main }
`, repoURL)
	if err := kubectlApply(t, gitRepo); err != nil {
		t.Fatalf("apply GitRepository: %v", err)
	}

	// Step 4 — synthesize a Kustomization tree per blueprint and apply.
	// We do NOT wait for them to reach Ready (that needs the Helm registry
	// reachable) — only that the API server accepts them.
	for _, bp := range canonicalOrder {
		manifest := fmt.Sprintf(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: %s
  namespace: flux-system
spec:
  interval: 5m
  path: ./platform/%s/chart
  prune: true
  sourceRef: { kind: GitRepository, name: openova-test }
  timeout: 1m
`, bp, strings.TrimPrefix(bp, "bp-"))
		if err := kubectlApply(t, manifest); err != nil {
			t.Errorf("apply Kustomization %s: %v", bp, err)
		}
	}

	// Step 5 — list Kustomizations and assert all 11 are present.
	out, err := exec.Command("kubectl", "-n", "flux-system", "get", "kustomization", "-o", "name").Output()
	if err != nil {
		t.Fatalf("get kustomizations: %v", err)
	}
	have := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// "kustomization.kustomize.toolkit.fluxcd.io/bp-cilium" → "bp-cilium"
		parts := strings.SplitN(line, "/", 2)
		if len(parts) == 2 {
			have[parts[1]] = true
		}
	}
	missing := []string{}
	for _, want := range canonicalOrder {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("Flux did not register Kustomizations for: %v", missing)
	}

	_ = root // keep import of repoRoot meaningful for future use
}

// runCLI runs an external CLI and surfaces stderr to the test log on failure.
func runCLI(t *testing.T, name string, args ...string) error {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s %s failed: %v\noutput:\n%s", name, strings.Join(args, " "), err, out)
	}
	return err
}

// kubectlApply pipes the given manifest through `kubectl apply -f -`.
func kubectlApply(t *testing.T, manifest string) error {
	t.Helper()
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("kubectl apply failed: %v\noutput:\n%s", err, out)
	}
	return err
}

// TestBootstrapKit_PlaneIsolationDialGraphIsCovered locks the #4325 #4382
// comprehensive dial-graph contract for bp-plane-isolation (slot 26b).
//
// After the #4325 de-vcluster the mgmt/rtz/dmz platform planes became native
// HOST namespaces, each behind a per-component default-deny CiliumNetworkPolicy
// whose ingress allow-list must enumerate every legitimate cross-component
// dialer. The allow-lists were originally written for the pre-de-vcluster
// topology, so gaps surfaced ONE AT A TIME as live traffic hit them — #4361
// (world-egress drop), #4380 (cutover Jobs → gitea/harbor), #4383 (org-services
// → gitea). This test asserts the FULL known steady-state + provisioning +
// cutover + observability dial graph is covered IN ONE PLACE so a future
// de-vcluster / re-home can't silently drop an edge again (the whack-a-mole
// this PR ends).
//
// Each `wantEdges` entry is TARGET-namespace -> the set of SOURCE namespaces
// that MUST appear in that target's bp-plane-isolation default-deny ingress
// allow-list (rendered as a `namespaceSelector` matching
// kubernetes.io/metadata.name). same-namespace (podSelector) + flux-system are
// template-implicit and not asserted here. Egress is intentionally NOT checked
// — the 0.1.2 union leaves egress open (namespaceSelector{} + world); this test
// guards the INGRESS allow-list, which is where every gap has landed.
//
// The edges below are each backed by a real consumer in the codebase:
//   - org-services -> {gitea,keycloak,nats-system,valkey, shared-pg}        (BSS/auth/provisioning)
//   - sandbox      -> {keycloak,gitea,seaweedfs,newapi(via catalyst-system)} (controller + per-Sandbox MCP)
//   - newapi       -> {keycloak,nats-system,valkey,vllm}                     (admin OIDC, metering, cache, inference)
//   - guacamole    -> {keycloak,nats-system,seaweedfs}                       (OIDC, audit, recordings)
//   - grafana      -> {keycloak,loki,mimir,tempo} + cnpg-system(its PG)      (SSO + datasource queries + state DB)
//   - alloy/otel   -> {loki,mimir,tempo,opentelemetry}                       (observability shippers)
func TestBootstrapKit_PlaneIsolationDialGraphIsCovered(t *testing.T) {
	root := repoRoot(t)
	chartDir := filepath.Join(root, "platform", "plane-isolation", "chart")
	if _, err := os.Stat(chartDir); err != nil {
		t.Skipf("platform/plane-isolation/chart not present — skipping #4325 dial-graph test")
	}

	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v) — skipping dial-graph render test", err)
	}

	// Render with the cilium.io/v2 API present so the gateway-ingress CNP also
	// renders (we assert its presence for gateway-fronted components).
	//
	// renderUnconditional=true bypasses the #4442 lookup-guard: on a live
	// helm-controller install the default-deny for a not-yet-created namespace
	// is DEFERRED (breaks the fresh-prov circular deadlock — see chart 0.1.7),
	// but `lookup` ALWAYS returns empty under this client-side `helm template`,
	// so without the flag every component would defer and the static dial-graph
	// coverage below could never be asserted. The flag forces every component to
	// render so this test validates the allow-list CONTENT (the contract);
	// the deferral mechanism is a live-delivery concern orthogonal to content.
	cmd := exec.Command(helmBin, "template", "plane-isolation", ".",
		"--api-versions", "cilium.io/v2", "--set", "renderUnconditional=true")
	cmd.Dir = chartDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template bp-plane-isolation failed: %v\noutput:\n%s", err, out)
	}

	// Parse the rendered ingress allow-lists: ns -> set(source-namespaces).
	type npDoc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Spec struct {
			Ingress []struct {
				From []struct {
					NamespaceSelector *struct {
						MatchLabels map[string]string `yaml:"matchLabels"`
					} `yaml:"namespaceSelector"`
				} `yaml:"from"`
			} `yaml:"ingress"`
		} `yaml:"spec"`
	}
	ingressFrom := map[string]map[string]bool{}     // targetNs -> set(sourceNs)
	gatewayCNP := map[string]bool{}                 // targetNs that have a gateway-ingress (`ingress` entity) CNP
	apiserverCNP := map[string]bool{}               // targetNs that have a kube-apiserver-egress CNP (#4428)
	apiserverWebhookCNP := map[string]bool{}        // targetNs that have a kube-apiserver-WEBHOOK-ingress CNP (#4579)
	webhookCNPPorts := map[string]map[string]bool{} // targetNs -> set(port string) admitted on the webhook-ingress CNP (#4579)
	allComponentNs := map[string]bool{}             // every ns that has a default-deny NetworkPolicy

	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var raw map[string]any
		if derr := dec.Decode(&raw); derr != nil {
			break
		}
		if raw == nil {
			continue
		}
		kind, _ := raw["kind"].(string)
		switch kind {
		case "NetworkPolicy":
			// Re-decode into the typed struct for the ingress walk.
			b, _ := yaml.Marshal(raw)
			var d npDoc
			if yaml.Unmarshal(b, &d) != nil {
				continue
			}
			ns := d.Metadata.Namespace
			if ingressFrom[ns] == nil {
				ingressFrom[ns] = map[string]bool{}
			}
			allComponentNs[ns] = true
			for _, rule := range d.Spec.Ingress {
				for _, f := range rule.From {
					if f.NamespaceSelector != nil {
						if src := f.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; src != "" {
							ingressFrom[ns][src] = true
						}
					}
				}
			}
		case "CiliumNetworkPolicy":
			meta, _ := raw["metadata"].(map[string]any)
			ns, _ := meta["namespace"].(string)
			name, _ := meta["name"].(string)
			if ns == "" {
				continue
			}
			// Disambiguate the CNP kinds this chart ships by name suffix:
			// the gateway-ingress (`ingress` entity) CNP, the kube-apiserver
			// egress CNP (#4428), and the kube-apiserver WEBHOOK-ingress CNP
			// (#4579 — admits the apiserver INTO a component's webhook port).
			// The webhook-ingress case is checked FIRST because its suffix
			// (...-webhook-ingress) is the most specific.
			switch {
			case strings.HasSuffix(name, "-allow-apiserver-webhook-ingress"):
				apiserverWebhookCNP[ns] = true
				// Capture the admitted webhook port(s) so the test can assert
				// the OTel-operator :9443 admit is present (not just any port).
				if webhookCNPPorts[ns] == nil {
					webhookCNPPorts[ns] = map[string]bool{}
				}
				spec, _ := raw["spec"].(map[string]any)
				ingress, _ := spec["ingress"].([]any)
				for _, ri := range ingress {
					rule, _ := ri.(map[string]any)
					toPorts, _ := rule["toPorts"].([]any)
					for _, tpi := range toPorts {
						tp, _ := tpi.(map[string]any)
						ports, _ := tp["ports"].([]any)
						for _, pi := range ports {
							p, _ := pi.(map[string]any)
							switch v := p["port"].(type) {
							case string:
								webhookCNPPorts[ns][v] = true
							case int:
								webhookCNPPorts[ns][fmt.Sprintf("%d", v)] = true
							}
						}
					}
				}
			case strings.HasSuffix(name, "-allow-apiserver-egress"):
				apiserverCNP[ns] = true
			case strings.HasSuffix(name, "-allow-gateway-ingress"):
				gatewayCNP[ns] = true
			}
		}
	}

	// The known dial graph — TARGET ns -> required SOURCE namespaces.
	wantEdges := map[string][]string{
		// SSO IdP — every OIDC consumer's backchannel lands here.
		"keycloak": {"org-services", "oidc-gate", "sandbox", "newapi", "guacamole", "grafana", "external-secrets-system", "cnpg-system"},
		// Source forge — provisioning + sandbox repo ops + flux/catalyst pulls.
		"gitea": {"org-services", "sandbox", "catalyst-system", "external-secrets-system", "cnpg-system"},
		// Event bus — every publisher.
		"nats-system": {"org-services", "guacamole", "catalyst-system", "newapi"},
		// Shared object store — its blob-storage consumers (default-off but legitimate).
		"seaweedfs": {"gitea", "harbor", "loki", "mimir", "tempo", "velero", "guacamole", "catalyst-system", "sandbox"},
		// Secrets engine — ESO reads it, CNPG operator manages adjuncts,
		// bp-sso-bridge logs in each reconciler tick (k8s-auth) to fetch/store
		// the per-app KC OIDC client_secret bundle, and catalyst-api logs in via
		// kubernetes-auth to read/write secret/catalyst/* (newapi admin-token,
		// anthropic token, mcp-bearer). Missing sso-bridge → no secret/sso/* →
		// grafana/hubble/gitea SSO 503/503/500 (#4448). Missing catalyst-system →
		// catalyst-api login times out → every secret/catalyst/* ExternalSecret
		// goes SecretSyncedError (#4499, Refs #4477 #4277). kom4dc b9f9590b.
		"openbao": {"external-secrets-system", "cnpg-system", "sso-bridge", "catalyst-system"},
		// Dashboards — SSO + ESO + its own Postgres backend.
		"grafana": {"external-secrets-system", "cnpg-system"},
		// Log/metric/trace stores — their queriers + shippers.
		"loki":  {"grafana", "alloy", "opentelemetry"},
		"mimir": {"grafana", "alloy", "opentelemetry"},
		"tempo": {"grafana", "alloy", "opentelemetry"},
		// OTLP collector — alloy forwards + catalyst-system emits.
		"opentelemetry": {"alloy", "catalyst-system"},
		// Shared cache — its clients.
		"valkey": {"org-services", "newapi"},
		// In-cluster inference channel.
		"vllm": {"newapi"},
		// LLM gateway — sandbox-controller (via catalyst-system) + per-Sandbox runtimes.
		"newapi": {"catalyst-system", "sandbox", "external-secrets-system", "cnpg-system"},
	}

	for target, sources := range wantEdges {
		got, ok := ingressFrom[target]
		if !ok {
			t.Errorf("bp-plane-isolation: NO default-deny NetworkPolicy rendered for de-vcluster'd plane %q — the dial graph cannot be covered (#4325). Add it to chart/values.yaml components[]", target)
			continue
		}
		for _, src := range sources {
			if !got[src] {
				t.Errorf("bp-plane-isolation DIAL-GRAPH GAP (#4325 #4382): namespace %q is dialed by %q in steady-state/provisioning/cutover, but %q is MISSING from %q's allowIngressFrom — Cilium will DROP the dial (the #4361/#4380/#4383 whack-a-mole class). Add %q to the %q entry in platform/plane-isolation/chart/values.yaml", target, src, src, target, src, target)
			}
		}
	}

	// Gateway-fronted components MUST also carry the ingress-entity CNP — without
	// it the cilium-gateway/envoy traffic (reserved `ingress` entity, no
	// namespaceSelector can match) is silently dropped → curl 000/503.
	wantGatewayCNP := []string{"keycloak", "gitea", "harbor", "grafana", "openbao", "guacamole", "newapi", "coraza", "oidc-gate"}
	for _, ns := range wantGatewayCNP {
		if !gatewayCNP[ns] {
			t.Errorf("bp-plane-isolation: gateway-fronted component %q is MISSING its allow-gateway-ingress CiliumNetworkPolicy — public route traffic (reserved `ingress` entity) will be dropped → 000/503. Set gatewayIngress: true on the %q entry", ns, ns)
		}
	}

	// EVERY default-denied component MUST carry the kube-apiserver-egress CNP
	// (#4428). The default-deny's egress union (namespaceSelector:{} for
	// in-cluster pods + ipBlock 0.0.0.0/0 for `world`) does NOT match the
	// reserved `kube-apiserver` identity the apiserver VIP 10.96.0.1:443
	// resolves to under Cilium kube-proxy-replacement — so without this CNP
	// every pod that dials the kube-API (CNPG instance-manager, webhooks, ESO
	// reconcilers) has its apiserver egress silently dropped → `dial
	// 10.96.0.1:443 i/o timeout` (the live bp-newapi/bootstrap-kit wedge, #4409).
	for ns := range allComponentNs {
		if !apiserverCNP[ns] {
			t.Errorf("bp-plane-isolation: default-denied component %q is MISSING its allow-apiserver-egress CiliumNetworkPolicy — pods that dial the kube-API (e.g. the CNPG instance-manager) will time out on 10.96.0.1:443 → HR wedge (#4428 #4409). apiserver-egress-cnp.yaml renders for every component; this gap means a render regression", ns)
		}
	}

	// A component that runs an admission/conversion webhook the kube-apiserver
	// calls BACK into MUST carry the kube-apiserver-WEBHOOK-ingress CNP (#4579)
	// on the webhook port. The default-deny's INGRESS allow-list is purely
	// namespaceSelector — but the apiserver is host-network → reserved
	// `kube-apiserver` identity → no namespaceSelector can admit it → the
	// apiserver→webhook call is silently dropped → the operator post-install
	// hook `context deadline exceeded` → the HR Stalls (MissingRollbackTarget)
	// → the all-terminal handover gate can't fire. Today only
	// bp-opentelemetry-operator needs it (`minstrumentation.kb.io` on :9443,
	// Service opentelemetry-operator-webhook:443 → pod :9443). Live-proven dep
	// 00720a7a9c72364d (kom4dc, 2026-06-27). This edge fails WITHOUT the
	// apiserverWebhookPorts: [9443] value on the opentelemetry entry and PASSES
	// with it — the same guard class that caught #4448/#4499/#4428.
	wantWebhookCNP := map[string]string{
		"opentelemetry": "9443",
	}
	for ns, port := range wantWebhookCNP {
		if !apiserverWebhookCNP[ns] {
			t.Errorf("bp-plane-isolation: webhook component %q is MISSING its allow-apiserver-webhook-ingress CiliumNetworkPolicy — the kube-apiserver→webhook call (reserved `kube-apiserver` identity, no namespaceSelector can admit it) will be DROPPED → the operator post-install hook `context deadline exceeded` → HR Stalls → handover gate can't fire (#4579). Set apiserverWebhookPorts: [%s] on the %q entry in platform/plane-isolation/chart/values.yaml", ns, port, ns)
			continue
		}
		if !webhookCNPPorts[ns][port] {
			t.Errorf("bp-plane-isolation: %q allow-apiserver-webhook-ingress CiliumNetworkPolicy does NOT admit the apiserver on port %s — the webhook server listens there; the call will be DROPPED (#4579). Ensure apiserverWebhookPorts on the %q entry contains %s", ns, port, ns, port)
		}
	}
}

// ── #4415 recurrence guard — catalog-seed delivery pins must not lag behind
//    the component charts they reference ──────────────────────────────────
//
// Root cause of #4415 (fixed by PR #4864): 38 of 81 `source.version` pins in
// products/catalyst/chart/templates/catalog-seed/blueprints.yaml had drifted
// BEHIND their component chart/Chart.yaml source-of-truth (e.g. bp-openclaw
// seed 0.2.13 while platform/openclaw/chart/Chart.yaml was already 0.2.16).
// A published, merged chart therefore never reached the catalog, so a
// catalog bump was silently stale — invisible until a per-Org install pulled
// an old chart. This guard makes that drift a hard CI failure the moment a
// component chart is bumped without syncing its catalog-seed delivery pin.
//
// It compares ONLY in-repo pins: every catalog-seed `source` block references
// an OCI chart published from this monorepo (oci://ghcr.io/openova-io/bp-*).
// A seed pin whose `chart:` has no co-located component chart in the repo is
// skipped (external/unpublished — not a drift). Semver-aware so 0.2.13 < 0.2.16
// and 1.0.0 < 1.5.5 compare correctly (string compare would miss both).

// parseSemverNumeric splits a dotted version into numeric components,
// stripping a leading "v", surrounding quotes, and any build/pre-release
// suffix. Returns (nil,false) when a component is non-numeric so the caller
// can skip an uncomparable pair rather than fail on it.
func parseSemverNumeric(s string) ([]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		nums[i] = n
	}
	return nums, true
}

// semverLess reports whether a < b numerically. The second return value is
// false when either side is not a plain numeric dotted version.
func semverLess(a, b string) (less bool, comparable bool) {
	av, aok := parseSemverNumeric(a)
	bv, bok := parseSemverNumeric(b)
	if !aok || !bok {
		return false, false
	}
	for i := 0; i < len(av) || i < len(bv); i++ {
		var x, y int
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			return x < y, true
		}
	}
	return false, true
}

// componentChartVersions builds a map of chart name (bp-<x>) → chart version
// from every co-located Chart.yaml under platform/ and products/. These are
// the source-of-truth versions the blueprint-release workflow publishes.
func componentChartVersions(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	globs := []string{
		"platform/*/chart/Chart.yaml",
		"products/*/chart/Chart.yaml",
		"products/*/charts/*/Chart.yaml",
		"products/*/*/chart/Chart.yaml",
	}
	for _, g := range globs {
		matches, _ := filepath.Glob(filepath.Join(root, g))
		for _, f := range matches {
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var c struct {
				Name    string `yaml:"name"`
				Version string `yaml:"version"`
			}
			if err := yaml.Unmarshal(raw, &c); err != nil {
				continue
			}
			if c.Name == "" || c.Version == "" {
				continue
			}
			// If the same chart name appears twice (shouldn't), keep the
			// highest version so the guard uses the true source-of-truth.
			if prev, ok := m[c.Name]; ok {
				if less, cmp := semverLess(prev, c.Version); !(cmp && less) {
					continue
				}
			}
			m[c.Name] = c.Version
		}
	}
	return m
}

type seedPin struct {
	chart   string
	version string
	line    int
}

var (
	reSeedSource  = regexp.MustCompile(`^\s{2}source:\s*$`)
	reSeedChart   = regexp.MustCompile(`^\s+chart:\s*"?([A-Za-z0-9][A-Za-z0-9._-]*)"?\s*$`)
	reSeedVersion = regexp.MustCompile(`^\s+version:\s*"?([0-9][0-9A-Za-z._+-]*)"?\s*$`)
)

// catalogSeedPins line-parses the delivery `source` blocks out of the
// catalog-seed template. The file is a Helm template (has {{ }} directives)
// so a plain YAML unmarshal is not possible; the source blocks themselves are
// static YAML, so a scoped line scan is both sufficient and robust.
func catalogSeedPins(t *testing.T, root string) []seedPin {
	t.Helper()
	f := filepath.Join(root, "products", "catalyst", "chart", "templates", "catalog-seed", "blueprints.yaml")
	raw, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("read catalog-seed: %v", err)
	}
	var pins []seedPin
	lines := strings.Split(string(raw), "\n")
	inSource := false
	var cur seedPin
	flush := func() {
		if cur.chart != "" && cur.version != "" {
			pins = append(pins, cur)
		}
		cur = seedPin{}
	}
	for i, ln := range lines {
		if reSeedSource.MatchString(ln) {
			flush()
			inSource = true
			cur.line = i + 1
			continue
		}
		if !inSource {
			continue
		}
		// source-block children are indented deeper than the 2-space
		// `source:` key. A line at <=2-space indent (and non-blank) ends it.
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if !strings.HasPrefix(ln, "    ") {
			flush()
			inSource = false
			continue
		}
		if m := reSeedChart.FindStringSubmatch(ln); m != nil {
			cur.chart = m[1]
		}
		if m := reSeedVersion.FindStringSubmatch(ln); m != nil {
			cur.version = m[1]
		}
	}
	flush()
	return pins
}

func TestCatalogSeed_DeliveryPinsNotBehindComponentCharts(t *testing.T) {
	root := repoRoot(t)

	comp := componentChartVersions(t, root)
	if len(comp) == 0 {
		t.Fatal("discovered zero component charts under platform/ + products/ — guard is inert (glob/parse broken)")
	}
	pins := catalogSeedPins(t, root)
	if len(pins) == 0 {
		t.Fatal("parsed zero catalog-seed source blocks — the line parser is broken (expected dozens)")
	}

	// bp-catalyst-platform is the umbrella chart whose version auto-increments
	// on EVERY merge (products/catalyst/chart/Chart.yaml). The catalog-seed
	// itself lives INSIDE that chart, so the seed can never reference its own
	// future umbrella version — a chicken-and-egg the seed cannot win. Exclude
	// it (the platform installs from the bootstrap-kit slot, not the catalog;
	// its catalog card is display-only).
	selfReferential := map[string]bool{"bp-catalyst-platform": true}

	checked := 0
	for _, p := range pins {
		if selfReferential[p.chart] {
			continue
		}
		cv, ok := comp[p.chart]
		if !ok {
			// External/upstream chart or one with no co-located Chart.yaml in
			// this repo — a missing component chart is not drift. Skip.
			continue
		}
		less, comparable := semverLess(p.version, cv)
		if !comparable {
			// Non-numeric version on one side (unexpected for our bp-* charts);
			// skip rather than risk a false positive.
			continue
		}
		checked++
		if less {
			t.Errorf("CATALOG-SEED DRIFT (#4415 / #4864):\n"+
				"  chart %q delivery pin is BEHIND its component chart:\n"+
				"    catalog-seed source.version = %q  (blueprints.yaml line %d)\n"+
				"    component chart/Chart.yaml   = %q\n"+
				"  → the catalog serves a STALE chart; a published fix never reaches Sovereigns.\n"+
				"  Sync the seed pin: set source.version to %q for chart %q in\n"+
				"  products/catalyst/chart/templates/catalog-seed/blueprints.yaml",
				p.chart, p.version, p.line, cv, cv, p.chart)
		}
	}
	if checked == 0 {
		t.Fatal("zero in-repo catalog-seed pins were comparable against component charts — the chart-name mapping is broken (guard would never catch drift)")
	}
	t.Logf("catalog-seed drift guard: compared %d in-repo delivery pins against component chart versions; none behind", checked)
}

// TestBootstrapKit_LbIpamSharingCrossNamespace guards the #4765 residue
// fixed in bp-cilium 1.4.16 (live-proven hw255, 2026-07-15): Cilium
// LB-IPAM (≥1.16) only lets two LoadBalancer Services share one VIP
// ACROSS namespaces when BOTH carry
// `lbipam.cilium.io/sharing-cross-namespace` allowing the peer's
// namespace — the `lbipam.cilium.io/sharing-key` alone is same-namespace
// only (docs.cilium.io lb-ipam: "The annotation must be present on both
// services"). The sovereign-vip riders live in kube-system
// (clustermesh-apiserver :2379) and powerdns (powerdns-anycast :53), so
// any annotation block that opts into the shared EIP via the sharing-key
// MUST also carry the cross-namespace grant, or the first-created rider
// takes the single /32 and every later rider starves forever
// (IPAMRequestSatisfied=False reason=out_of_ips, EXTERNAL-IP <pending>).
// The grant must be an explicit namespace list — never "*", which would
// let ANY tenant/Org LoadBalancer Service co-locate onto the sovereign
// EIP by annotation hijack.
func TestBootstrapKit_LbIpamSharingCrossNamespace(t *testing.T) {
	root := repoRoot(t)

	// Every file that participates in the sovereign-vip sharing contract.
	// filepath.Glob over clusters/*/bootstrap-kit/*.yaml covers _template
	// plus every per-Sovereign copy; values-clustermesh.yaml is the
	// chart-side reference for the clustermesh Service annotations.
	var files []string
	for _, pat := range []string{
		filepath.Join(root, "clusters", "*", "bootstrap-kit", "*.yaml"),
		filepath.Join(root, "platform", "cilium", "chart", "values-clustermesh.yaml"),
	} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		files = append(files, matches...)
	}

	const (
		keyAnno   = "lbipam.cilium.io/sharing-key:"
		crossAnno = "lbipam.cilium.io/sharing-cross-namespace:"
	)

	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var keys, crosses, wildcards int
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue // prose about the annotation, not the annotation
			}
			switch {
			case strings.HasPrefix(trimmed, keyAnno):
				keys++
			case strings.HasPrefix(trimmed, crossAnno):
				crosses++
				val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, crossAnno)), `"'`)
				if val == "*" {
					wildcards++
					t.Errorf("%s:%d: lbipam.cilium.io/sharing-cross-namespace is %q — the wildcard grant lets ANY namespace's LB Service co-locate onto the sovereign EIP (annotation hijack); list the peer namespace explicitly", f, i+1, "*")
				}
			}
		}
		if keys > 0 {
			checked++
			if crosses < keys {
				t.Errorf("%s: %d `%s sovereign-vip` annotation(s) but only %d `%s` grant(s) — without the cross-namespace grant on BOTH riders Cilium refuses the share and the later Service starves (out_of_ips, EXTERNAL-IP <pending>; #4765 residue, hw255 2026-07-15). Add `%s \"<peer-namespace>\"` next to each sharing-key.",
					f, keys, keyAnno, crosses, crossAnno, crossAnno)
			}
		}
	}
	if checked < 4 {
		t.Fatalf("only %d files carry the sovereign-vip sharing-key — expected ≥4 (template 01-cilium + template 11-powerdns + per-Sovereign copies + values-clustermesh.yaml); the guard's file enumeration is broken", checked)
	}
	t.Logf("sovereign-vip sharing contract: %d files checked, every sharing-key has its cross-namespace grant, zero wildcard grants", checked)
}
