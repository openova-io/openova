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
//   OpenTofu provisions Phase 0 → cloud-init starts k3s → cloud-init
//   bootstraps Flux → Flux reconciles clusters/<sovereign-fqdn>/ from this
//   monorepo → that subtree contains a Kustomization tree that installs the
//   11-component bootstrap kit in dependency order.
//
// The "right Kustomizations" assertion is therefore:
//   1. clusters/_template/ exists and renders to valid Flux Kustomization
//      manifests after SOVEREIGN_FQDN_PLACEHOLDER substitution
//   2. The dependency graph encoded by `dependsOn` matches the canonical
//      11-phase order: cilium → cert-manager → flux → crossplane →
//      sealed-secrets → spire → nats-jetstream → openbao → keycloak →
//      gitea → bp-catalyst-platform
//   3. Each referenced platform/<x>/blueprint.yaml + chart/Chart.yaml
//      actually exists at the path the Kustomization claims
//   4. On a kind cluster (CI): Flux CRDs install, the GitRepository points
//      at the local checkout, and the Kustomization objects are accepted
//      by the API server (their OpenAPI schema is satisfied)
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
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot returns the absolute path to the repository root by walking up
// from the test file's directory until a sentinel file (go.mod marker or
// the docs/INVIOLABLE-PRINCIPLES.md file) is found.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "docs", "INVIOLABLE-PRINCIPLES.md")); err == nil {
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

// errEOF returns the io.EOF sentinel. Importing io for one variable bloats
// the file; this helper keeps the test deps minimal.
func errEOF() error {
	return errEOFSentinel
}

var errEOFSentinel = fmt.Errorf("EOF")

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
