// organization_agenity_overlay_test.go — #4180.
//
// Regression coverage for the FOUR durable bp-agenity overlay-generator
// bugs diagnosed live on the omantel.biz demo Org (2026-06-24, dep
// 4635277cae4ffed9) and previously only hot-fixed in Gitea:
//
//	BUG 1 — missing bp-agenity HelmRepository (sourceRef had no source).
//	BUG 2 — Agenity version "*" resolved to the IMAGE tag (0.9.6), not the
//	        chart; default must be the chart-range "0.5.x".
//	BUG 3 — bp-agenity overlay lacked imagePullSecrets → ImagePullBackOff
//	        on the private ghcr image in the Org ns.
//	BUG 4 — the parent org-tenants/kustomization.yaml could be committed as
//	        a Gitea contents-API JSON envelope, pruning every Org's bp-*
//	        HelmReleases cluster-wide. Guard: never write a '{'-leading
//	        parent index.
package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func renderAgenityOverlay(t *testing.T, versions OrganizationChartVersions) map[string]string {
	t.Helper()
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, versions)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return files
}

// BUG 1 — the shared HelmRepositories file MUST declare bp-agenity, else
// the rendered HelmRelease's sourceRef has no source.
func TestBug1_AgenityHelmRepositoryDeclared(t *testing.T) {
	if !strings.Contains(orgTenantSharedHelmRepositories, "name: bp-agenity") {
		t.Fatalf("orgTenantSharedHelmRepositories missing bp-agenity HelmRepository block")
	}
	// Sanity: every HelmRepository the per-tenant overlay sourceRefs into
	// must be present in the shared file. bp-agenity is the new one.
	for _, want := range []string{
		"name: bp-keycloak", "name: bp-cnpg", "name: bp-newapi",
		"name: bp-wordpress-tenant", "name: bp-openclaw",
		"name: bp-stalwart-tenant", "name: bp-agenity",
	} {
		if !strings.Contains(orgTenantSharedHelmRepositories, want) {
			t.Errorf("shared HelmRepositories missing %q", want)
		}
	}
	// Shape parity with the others: oci type + ghcr url + ghcr-pull secret.
	idx := strings.Index(orgTenantSharedHelmRepositories, "name: bp-agenity")
	block := orgTenantSharedHelmRepositories[idx:]
	for _, want := range []string{"type: oci", "url: oci://ghcr.io/openova-io", "name: ghcr-pull"} {
		if !strings.Contains(block, want) {
			t.Errorf("bp-agenity HelmRepository block missing %q", want)
		}
	}
}

// BUG 2 — an empty CATALYST_ORG_BP_AGENITY_VER must default to the
// chart-version range "0.5.x" (NOT "*", which resolves to the 0.9.x image).
func TestBug2_AgenityVersionDefaultsToChartRange(t *testing.T) {
	got := withVersionDefaults(OrganizationChartVersions{}).Agenity
	if got != "0.5.x" {
		t.Fatalf("empty Agenity version default = %q, want %q", got, "0.5.x")
	}
	if got == "*" {
		t.Fatalf("Agenity default must NOT be '*' — it resolves to the 0.9.x IMAGE tag")
	}
	// An explicit value still wins.
	if v := withVersionDefaults(OrganizationChartVersions{Agenity: "0.5.4"}).Agenity; v != "0.5.4" {
		t.Fatalf("explicit Agenity version = %q, want 0.5.4", v)
	}
	// The OTHER charts still default to "*".
	d := withVersionDefaults(OrganizationChartVersions{})
	for name, v := range map[string]string{
		"Keycloak": d.Keycloak, "CNPG": d.CNPG, "WordPress": d.WordPress,
		"OpenClaw": d.OpenClaw, "Stalwart": d.Stalwart, "NewAPI": d.NewAPI,
	} {
		if v != "*" {
			t.Errorf("%s default = %q, want '*' (only Agenity is special)", name, v)
		}
	}
}

// BUG 2 (rendered) — the emitted bp-agenity HelmRelease carries the
// chart-range version when none is supplied.
func TestBug2_RenderedAgenityVersion(t *testing.T) {
	files := renderAgenityOverlay(t, OrganizationChartVersions{})
	hr := parseAgenityHR(t, files)
	if hr.Spec.Chart.Spec.Version != "0.5.x" {
		t.Fatalf("rendered bp-agenity chart.spec.version = %q, want 0.5.x", hr.Spec.Chart.Spec.Version)
	}
	if hr.Spec.Chart.Spec.Chart != "bp-agenity" {
		t.Fatalf("rendered chart name = %q, want bp-agenity", hr.Spec.Chart.Spec.Chart)
	}
}

// BUG 3 — the rendered bp-agenity values MUST set imagePullSecrets to
// [{name: ghcr-pull}] so the private-ghcr image pulls in the Org ns.
func TestBug3_AgenityImagePullSecrets(t *testing.T) {
	files := renderAgenityOverlay(t, OrganizationChartVersions{})
	hr := parseAgenityHR(t, files)
	ips, ok := hr.Spec.Values["imagePullSecrets"]
	if !ok {
		t.Fatalf("rendered bp-agenity values missing imagePullSecrets (private ghcr image → ImagePullBackOff)")
	}
	list, ok := ips.([]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("imagePullSecrets is not a non-empty list: %#v", ips)
	}
	first, _ := list[0].(map[string]interface{})
	if first["name"] != "ghcr-pull" {
		t.Fatalf("imagePullSecrets[0].name = %v, want ghcr-pull", first["name"])
	}
}

// BUG 4 — writeParentTenantsIndex must produce a raw-YAML parent index
// (comment banner + apiVersion: kustomize…), never a JSON envelope, and
// must refuse to write a corrupt one.
func TestBug4_ParentIndexIsRawYAML(t *testing.T) {
	dir := t.TempDir()
	// Seed one tenant subdir with its own kustomization.yaml so the index
	// lists it.
	sub := filepath.Join(dir, "t-acme")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeParentTenantsIndex(dir); err != nil {
		t.Fatalf("writeParentTenantsIndex: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// The committed parent index must start with a '#' comment or the
	// apiVersion line — never a '{' (JSON-envelope corruption signature).
	trimmed := strings.TrimLeft(got, " \t\r\n")
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		t.Fatalf("parent index is a JSON envelope, not raw YAML:\n%s", got)
	}
	if !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "apiVersion: kustomize") {
		t.Fatalf("parent index does not begin with '#' or 'apiVersion: kustomize':\n%s", got)
	}
	// It must enumerate the seeded tenant + the shared helmrepositories.yaml.
	if !strings.Contains(got, "- t-acme") {
		t.Errorf("parent index missing tenant subdir t-acme:\n%s", got)
	}
	if !strings.Contains(got, "- helmrepositories.yaml") {
		t.Errorf("parent index missing helmrepositories.yaml:\n%s", got)
	}
	// And it must parse as a valid kustomization (non-empty resources).
	var ks struct {
		APIVersion string   `json:"apiVersion"`
		Kind       string   `json:"kind"`
		Resources  []string `json:"resources"`
	}
	if err := yaml.Unmarshal(raw, &ks); err != nil {
		t.Fatalf("parent index does not parse as YAML: %v\n%s", err, got)
	}
	if ks.Kind != "Kustomization" || len(ks.Resources) == 0 {
		t.Fatalf("parent index parsed to empty/invalid kustomization (this is the prune-everything trap): %+v", ks)
	}
}

// BUG 4 — the validation helper rejects the exact JSON-envelope shape the
// Gitea contents API returns, and accepts genuine raw YAML.
func TestBug4_ValidateRawKustomizationYAML(t *testing.T) {
	bad := []string{
		`{"name":"kustomization.yaml","path":"clusters/x/org-tenants/kustomization.yaml","content":"YXBpVmVyc2lvbg==","encoding":"base64"}`,
		`  {"content":"..."}`,
		`["a","b"]`,
		``,
		"   \n\t",
		"resources:\n  - foo\n", // missing apiVersion/comment prefix
	}
	for _, b := range bad {
		if err := validateRawKustomizationYAML([]byte(b)); err == nil {
			t.Errorf("validateRawKustomizationYAML accepted corrupt input %q", b)
		}
	}
	good := []string{
		"# banner\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n",
		"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n",
		"\uFEFF# BOM-prefixed banner\napiVersion: kustomize.config.k8s.io/v1beta1\n",
	}
	for _, g := range good {
		if err := validateRawKustomizationYAML([]byte(g)); err != nil {
			t.Errorf("validateRawKustomizationYAML rejected valid input %q: %v", g, err)
		}
	}
}

// parseAgenityHR decodes the rendered bp-agenity.yaml into the minimal HR
// struct the assertions need.
func parseAgenityHR(t *testing.T, files map[string]string) helmReleaseYAML {
	t.Helper()
	raw, ok := files["bp-agenity.yaml"]
	if !ok {
		t.Fatalf("overlay missing bp-agenity.yaml; got files: %v", keysOf(files))
	}
	var hr helmReleaseYAML
	if err := yaml.Unmarshal([]byte(raw), &hr); err != nil {
		t.Fatalf("bp-agenity.yaml does not parse as YAML: %v\n%s", err, raw)
	}
	if hr.Kind != "HelmRelease" {
		t.Fatalf("bp-agenity.yaml kind = %q, want HelmRelease", hr.Kind)
	}
	return hr
}
