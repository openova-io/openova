// gitea_mirror_per_region_test.go — slice G3-flux contract guards for
// the self-sovereign-cutover step-01 gitea-mirror Job script.
//
// Slice G3-flux moves Sovereigns from a single shared
// `clusters/_template/bootstrap-kit` git path that every region's k3s
// reconciles against → per-region subtrees under
// `clusters/<sovereign_fqdn>/<region_key>/bootstrap-kit`. The cutover
// step-01 gitea-mirror Job (platform/self-sovereign-cutover/chart/
// templates/01-gitea-mirror-job.yaml) is the engine that materialises
// those subtrees: after `git push --mirror`-ing the upstream openova-io
// repo into the local Sovereign Gitea, the script walks the CSV-encoded
// region key list (sovereign.regionsCSV → SOVEREIGN_REGION_KEYS_CSV env
// var) and copies the _template subtree into each region's directory.
//
// These tests pin the shape of that script. A regression that drops
// the SOVEREIGN_REGION_KEYS_CSV env var, the per-region loop, or the
// final commit + push lands as a test failure in CI rather than as a
// silent single-region cutover on a real Sovereign.
package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readGiteaMirrorTemplate loads the raw chart template file. We read
// the raw template (not a helm-rendered output) because the Sprig
// expressions inside `{{ ... }}` are not the part this test cares
// about — we care about the script body, which is pure shell + envsubst
// markers and renders byte-identical regardless of values.
func readGiteaMirrorTemplate(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// products/catalyst/bootstrap/api/internal/provisioner → repo root
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	p := filepath.Join(repoRoot, "platform", "self-sovereign-cutover", "chart", "templates", "01-gitea-mirror-job.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(raw)
}

// TestGiteaMirror_HasPerRegionEnvVars proves the Job ConfigMap declares
// SOVEREIGN_FQDN + SOVEREIGN_REGION_KEYS_CSV env vars sourced from the
// chart's sovereign values block. Without these the per-region loop has
// no input — gitea-mirror would silently skip subtree materialisation
// and leave every cluster's Flux Kustomization targetting a path that
// step-05 will later patch into nonexistence.
func TestGiteaMirror_HasPerRegionEnvVars(t *testing.T) {
	tpl := readGiteaMirrorTemplate(t)

	for _, want := range []string{
		"- name: SOVEREIGN_FQDN",
		`value: {{ .Values.sovereign.fqdn | quote }}`,
		"- name: SOVEREIGN_REGION_KEYS_CSV",
		`value: {{ .Values.sovereign.regionsCSV | quote }}`,
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("gitea-mirror Job must declare %q for slice G3-flux per-region subtree materialisation", want)
		}
	}
}

// TestGiteaMirror_HasPerRegionLoop proves the script body iterates the
// CSV-encoded region key list and copies the _template bootstrap-kit
// subtree into each region's directory. The exact shape is locked
// (IFS=, parse, cp -R from `clusters/_template/bootstrap-kit` into
// `clusters/${SOVEREIGN_FQDN}/<region_key>/bootstrap-kit`) so a
// refactor that breaks the regex-style guarantees here gets flagged.
func TestGiteaMirror_HasPerRegionLoop(t *testing.T) {
	tpl := readGiteaMirrorTemplate(t)

	for _, want := range []string{
		// The "skip when CSV empty" guard — keeps the upgrade path
		// idempotent for legacy single-region Sovereigns.
		`if [ -z "${SOVEREIGN_REGION_KEYS_CSV}" ] || [ -z "${SOVEREIGN_FQDN}" ]; then`,
		// The CSV parser — IFS=, is the BusyBox-safe split.
		"IFS=,",
		"set -- ${SOVEREIGN_REGION_KEYS_CSV}",
		// The source-of-truth template directory.
		`template_dir="clusters/_template/bootstrap-kit"`,
		// The per-region destination root.
		`sovereign_dir="clusters/${SOVEREIGN_FQDN}"`,
		// The copy step.
		`cp -R "${template_dir}" "${region_dst}"`,
		// The commit + push gate (idempotent on empty diff).
		"git diff --cached --quiet",
		`git push "${local_url}" "${UPSTREAM_BRANCH}"`,
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("gitea-mirror per-region materialisation regressed — missing %q", want)
		}
	}
}

// TestGiteaMirror_WritesSovereignIndexKustomization proves the script
// emits a top-level kustomization.yaml index at the Sovereign root
// listing every region's bootstrap-kit subtree. The index isn't what
// each cluster's Flux watches at spec.path (that pivots to the per-
// region subtree post-cutover), but it provides a single-pane manifest
// of "what regions does this Sovereign have?" for ops + future audits.
func TestGiteaMirror_WritesSovereignIndexKustomization(t *testing.T) {
	tpl := readGiteaMirrorTemplate(t)

	for _, want := range []string{
		`echo "apiVersion: kustomize.config.k8s.io/v1beta1"`,
		`echo "kind: Kustomization"`,
		`echo "resources:"`,
		`echo "  - ${region_key}/bootstrap-kit"`,
		`} > "${sovereign_dir}/kustomization.yaml"`,
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("gitea-mirror Sovereign-root kustomization index regressed — missing %q", want)
		}
	}
}

// TestGiteaMirror_ChartValuesCarryRegionsCSV proves the chart's
// values.yaml exposes the per-region CSV key. Without this the Helm
// template wouldn't resolve {{ .Values.sovereign.regionsCSV }} and
// the Job rendering would fail.
func TestGiteaMirror_ChartValuesCarryRegionsCSV(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	p := filepath.Join(repoRoot, "platform", "self-sovereign-cutover", "chart", "values.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	body := string(raw)

	for _, want := range []string{
		"regionsCSV:",
		"regionKey:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cutover chart values.yaml must expose %q for slice G3-flux", want)
		}
	}
}

// TestFluxGitRepositoryPatch_PivotsKustomizationPath proves step-05
// (flux-gitrepository-patch) reads the per-cluster region key and
// patches the bootstrap-kit Kustomization spec.path to the per-region
// subtree post-cutover. Without this pivot, even though gitea-mirror
// materialised the per-region subtrees in step-01, each cluster's
// Flux would keep reconciling the shared `_template` path.
func TestFluxGitRepositoryPatch_PivotsKustomizationPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	p := filepath.Join(repoRoot, "platform", "self-sovereign-cutover", "chart", "templates", "05-flux-gitrepository-patch-job.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	body := string(raw)

	for _, want := range []string{
		"- name: SOVEREIGN_REGION_KEY",
		`value: {{ .Values.sovereign.regionKey | default "" | quote }}`,
		`new_path="./clusters/${SOVEREIGN_FQDN}/${SOVEREIGN_REGION_KEY}/bootstrap-kit"`,
		`kubectl patch kustomization "bootstrap-kit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("step-05 flux-gitrepository-patch must pivot Kustomization spec.path to per-region subtree (slice G3-flux) — missing %q", want)
		}
	}
}
