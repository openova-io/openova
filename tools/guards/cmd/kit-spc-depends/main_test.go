package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- the verdict, without tools

const driverID = "flux-system/bp-secrets-store-csi"

func rel(file, name, chart string, deps []string, kinds ...string) release {
	return release{file: file, name: name, namespace: "flux-system", chart: chart, deps: deps, kinds: kinds}
}

func goodKit() []release {
	return []release{
		rel("08-store.yaml", "bp-store", "bp-store", []string{"flux-system/bp-base"}),
		rel("01-base.yaml", "bp-base", "bp-base", nil),
		rel("15b-driver.yaml", "bp-secrets-store-csi", driverChart, []string{"flux-system/bp-store"}),
		rel("25-app.yaml", "bp-app", "bp-app", []string{driverID}, "app/app-sso"),
		rel("30-quiet.yaml", "bp-quiet", "bp-quiet", nil),
	}
}

func judged(t *testing.T, rels []release) ([]string, []string, int) {
	t.Helper()
	return verdict("kit K (pack p, primary)", rels)
}

func TestVerdictHolds(t *testing.T) {
	notes, findings, users := judged(t, goodKit())
	if len(findings) != 0 || users != 1 || len(notes) != 1 {
		t.Fatalf("findings %q, users %d, notes %q; want none, 1, 1", findings, users, notes)
	}
	want := "kit-spc-depends: ok kit K (pack p, primary): flux-system/bp-app (25-app.yaml, chart bp-app) renders SecretProviderClass app/app-sso, and waits for flux-system/bp-secrets-store-csi"
	if notes[0] != want {
		t.Fatalf("note %q, want %q", notes[0], want)
	}
}

func oneFinding(t *testing.T, rels []release, want string) {
	t.Helper()
	_, findings, _ := judged(t, rels)
	if len(findings) != 1 || !strings.Contains(findings[0], want) {
		t.Fatalf("findings %q, want exactly one that holds %q", findings, want)
	}
}

func TestVerdictNoEdge(t *testing.T) {
	k := goodKit()
	k[3].deps = []string{"flux-system/bp-quiet"}
	oneFinding(t, k, "flux-system/bp-app (25-app.yaml, chart bp-app) renders SecretProviderClass app/app-sso, and its spec.dependsOn does not name flux-system/bp-secrets-store-csi (15b-driver.yaml)")
}

func TestVerdictEdgeInAnotherNamespace(t *testing.T) {
	k := goodKit()
	k[3].deps = []string{"other/bp-secrets-store-csi"}
	oneFinding(t, k, "does not name flux-system/bp-secrets-store-csi")
}

func TestVerdictNoDriver(t *testing.T) {
	k := append(goodKit()[:2], goodKit()[3:]...)
	oneFinding(t, k, "renders SecretProviderClass app/app-sso, and the kit holds no release of chart bp-secrets-store-csi that is not suspended")
}

func TestVerdictSuspendedDriver(t *testing.T) {
	k := goodKit()
	k[2].suspended = true
	oneFinding(t, k, "the kit holds no release of chart bp-secrets-store-csi that is not suspended")
}

func TestVerdictDriverDependencyMissing(t *testing.T) {
	k := goodKit()[1:] // no bp-store
	oneFinding(t, k, "the driver's release flux-system/bp-secrets-store-csi (15b-driver.yaml) depends on flux-system/bp-store (through flux-system/bp-secrets-store-csi), which the kit does not hold")
}

func TestVerdictDriverIndirectDependencyMissing(t *testing.T) {
	k := append(goodKit()[:1], goodKit()[2:]...) // no bp-base, which bp-store needs
	oneFinding(t, k, "depends on flux-system/bp-base (through flux-system/bp-store), which the kit does not hold")
}

func TestVerdictDriverDependencySuspended(t *testing.T) {
	k := goodKit()
	k[1].suspended = true
	oneFinding(t, k, "depends on flux-system/bp-base (01-base.yaml, through flux-system/bp-store), which is suspended here")
}

func TestVerdictCycle(t *testing.T) {
	k := goodKit()
	k[0].kinds = []string{"store/store-spc"}
	oneFinding(t, k, "flux-system/bp-store (08-store.yaml, chart bp-store) renders SecretProviderClass store/store-spc, and the driver's release flux-system/bp-secrets-store-csi depends on it: an edge to the driver would be a cycle")
}

func TestVerdictTwoDrivers(t *testing.T) {
	k := append(goodKit(), rel("15c-driver.yaml", "bp-other-driver", driverChart, nil))
	oneFinding(t, k, "2 releases of chart bp-secrets-store-csi")
}

func TestVerdictSuspendedUserNotJudged(t *testing.T) {
	k := goodKit()
	k[3].deps, k[3].suspended = nil, true
	if _, findings, users := judged(t, k); len(findings) != 0 || users != 0 {
		t.Fatalf("findings %q, users %d; a suspended release installs nothing", findings, users)
	}
}

// ---------------------------------------------------------------- the packs and the units, without tools

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, text := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPackSubstitutes(t *testing.T) {
	root := write(t, map[string]string{
		"p/provider-substitutes.yaml.tftpl": "# A: \"no\"\nA_ONE: \"1\"\nA_TWO: \"${region}\"\nA_THREE: \"x%{ if y }z%{ endif }\"\n  A_FOUR: \"indented is not an entry\"\n",
		"p/main.tf": `locals {
  B_OUTSIDE = "not in the block"
  pack_substitutes = { for r in var.regions : r.code => {
    B_ONE   = "1"
    B_TWO   = "${local.ip[r.code]}/32"
    B_THREE = tostring(var.port)
    B_FOUR  = var.storage_class
    A_ONE   = "2"
  } }
  B_AFTER = "after the block"
  pack_substitutes =
  {
    B_NEXT_LINE = "3"
  }
}
`,
	})
	got, err := packSubstitutes(filepath.Join(root, "p"))
	if err != nil {
		t.Fatal(err)
	}
	want := "A_ONE=1|B_ONE=1|A_ONE=2|B_NEXT_LINE=3"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, "|"), want)
	}
	none, err := packSubstitutes(filepath.Join(root, "absent"))
	if err != nil || len(none) != 0 {
		t.Fatalf("a pack without either file: %q %v, want none and no error", none, err)
	}
}

func TestPlan(t *testing.T) {
	files := map[string]string{
		baseVars:      "K=1\n",
		secondaryVars: "K=2\n",
		sharedKit:     "resources: []\n",
		"clusters/_template/providers/beta/kit/kustomization.yaml":  "resources: []\n",
		"clusters/_template/providers/gamma/kit/kustomization.yaml": "resources: []\n",
		"infra/providers/alpha/pack.yaml":                           "{}\n",
		"infra/providers/alpha/provider-substitutes.yaml.tftpl":     "K: \"3\"\n",
		"infra/providers/beta/pack.yaml":                            "{}\n",
		"infra/providers/_shared/pack.yaml":                         "{}\n",
	}
	root := write(t, files)
	work := t.TempDir()
	units, err := plan(root, work)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range units {
		var vs []string
		for _, v := range u.vars {
			vs = append(vs, filepath.Base(v))
		}
		got = append(got, u.String()+" "+strings.Join(vs, "+"))
	}
	want := []string{
		"clusters/_template/bootstrap-kit/kustomization.yaml (pack alpha, primary) today.env+pack-alpha.env",
		"clusters/_template/bootstrap-kit/kustomization.yaml (pack alpha, secondary) today.env+two-region-secondary.env+pack-alpha.env",
		"clusters/_template/providers/beta/kit/kustomization.yaml (pack beta, primary) today.env",
		"clusters/_template/providers/beta/kit/kustomization.yaml (pack beta, secondary) today.env+two-region-secondary.env",
		"clusters/_template/providers/gamma/kit/kustomization.yaml (no pack, the base values, primary) today.env",
		"clusters/_template/providers/gamma/kit/kustomization.yaml (no pack, the base values, secondary) today.env+two-region-secondary.env",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("units:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	b, err := os.ReadFile(filepath.Join(work, "pack-alpha.env")) // #nosec G304 -- a file the test wrote
	if err != nil || string(b) != "K=3\n" {
		t.Fatalf("pack-alpha.env: %q %v, want K=3", b, err)
	}
	delete(files, secondaryVars)
	if _, err := plan(write(t, files), work); err == nil || !strings.Contains(err.Error(), secondaryVars) {
		t.Fatalf("a missing value set: %v, want an error that names it", err)
	}
}

// ---------------------------------------------------------------- the render and the run, with tools

// The cases that render need helm, yq, bash and Go on PATH. Without them they
// skip, unless KIT_SPC_DEPENDS_TOOLS=required (the tree-guards step sets it):
// then a missing tool fails them.
func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"helm", "yq", "bash", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("KIT_SPC_DEPENDS_TOOLS") == "required" {
				t.Fatalf("%s is not on PATH, and KIT_SPC_DEPENDS_TOOLS=required", tool)
			}
			t.Skipf("%s is not on PATH", tool)
		}
	}
}

func chart(name, templates string) map[string]string {
	dir := "platform/" + strings.TrimPrefix(name, "bp-") + "/chart/"
	return map[string]string{
		dir + "Chart.yaml":          "apiVersion: v2\nname: " + name + "\nversion: 0.1.0\n",
		dir + "values.yaml":         "sso:\n  enabled: false\n",
		dir + "templates/main.yaml": templates,
	}
}

const spc = `apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: {{ .Release.Name }}-sso
spec:
  provider: openbao
`

func hr(slot, name, chartName, extra string) string {
	return `# slot ` + slot + `
apiVersion: v1
kind: Namespace
metadata:
  name: not-a-release
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ` + name + `
  namespace: flux-system
spec:
  releaseName: ` + strings.TrimPrefix(name, "bp-") + `
  targetNamespace: ` + strings.TrimPrefix(name, "bp-") + `
  chart:
    spec:
      chart: ` + chartName + `
      sourceRef: {kind: HelmRepository, name: bp, namespace: flux-system}
` + extra
}

const toDriver = "  dependsOn:\n    - name: bp-secrets-store-csi\n      namespace: flux-system\n"

// fixture is a tree that holds: the shared kit, applied by pack alpha, whose
// literal substitute turns on the SecretProviderClass of bp-app (the base
// values leave it off), and the own kit of pack beta. bp-console renders the
// kind only when the cluster serves it and is suspended in the secondary role;
// bp-store renders it only as a test hook, which its release does not enable.
func fixture() map[string]string {
	t := map[string]string{
		baseVars:                          "APP_SSO=false\nSECONDARY_HR_SUSPEND=false\n",
		secondaryVars:                     "SECONDARY_HR_SUSPEND=true\n",
		"infra/providers/alpha/pack.yaml": "{}\n",
		"infra/providers/alpha/provider-substitutes.yaml.tftpl":    "APP_SSO: \"true\"\nOTHER: \"${computed}\"\n",
		"infra/providers/beta/pack.yaml":                           "{}\n",
		sharedKit:                                                  "resources:\n  - 08-store.yaml\n  - 15b-driver.yaml\n  - 13-console.yaml\n  - 25-app.yaml\n",
		"clusters/_template/bootstrap-kit/08-store.yaml":           hr("08", "bp-store", "bp-store", ""),
		"clusters/_template/bootstrap-kit/15b-driver.yaml":         hr("15b", "bp-secrets-store-csi", "bp-secrets-store-csi", "  dependsOn:\n    - name: bp-store\n"),
		"clusters/_template/bootstrap-kit/13-console.yaml":         hr("13", "bp-console", "bp-console", "  suspend: ${SECONDARY_HR_SUSPEND:=false}\n"+toDriver),
		"clusters/_template/bootstrap-kit/25-app.yaml":             hr("25", "bp-app", "bp-app", toDriver+"  values:\n    sso:\n      enabled: ${APP_SSO:=false}\n"),
		"clusters/_template/providers/beta/kit/kustomization.yaml": "resources:\n  - ../../../bootstrap-kit/08-store.yaml\n  - ../../../bootstrap-kit/15b-driver.yaml\n  - ../../../bootstrap-kit/13-console.yaml\n  - local-path.yaml\n",
		"clusters/_template/providers/beta/kit/local-path.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: local-path
  namespace: flux-system
spec:
  chart:
    spec:
      chart: ./deploy/chart/local-path-provisioner
      sourceRef: {kind: GitRepository, name: local-path-provisioner}
`,
	}
	for k, v := range chart("bp-store", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: store\n---\n"+strings.Replace(spc, "metadata:\n", "metadata:\n  annotations:\n    helm.sh/hook: test\n", 1)) {
		t[k] = v
	}
	for k, v := range chart("bp-secrets-store-csi", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: driver\n") {
		t[k] = v
	}
	for k, v := range chart("bp-console", "{{- if .Capabilities.APIVersions.Has \"secrets-store.csi.x-k8s.io/v1\" }}\n"+spc+"{{- end }}\n") {
		t[k] = v
	}
	for k, v := range chart("bp-app", "{{- if .Values.sso.enabled }}\n"+spc+"{{- end }}\n") {
		t[k] = v
	}
	return t
}

func runOn(t *testing.T, files map[string]string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run([]string{"-root", write(t, files)}, &out, &errb)
	return code, out.String() + errb.String()
}

func expect(t *testing.T, files map[string]string, code int, lines ...string) {
	t.Helper()
	got, text := runOn(t, files)
	if got != code {
		t.Fatalf("exit %d, want %d:\n%s", got, code, text)
	}
	for _, l := range lines {
		if !strings.Contains(text, l) {
			t.Fatalf("the output has no line holding %q:\n%s", l, text)
		}
	}
}

const (
	shared = "clusters/_template/bootstrap-kit/kustomization.yaml"
	beta   = "clusters/_template/providers/beta/kit/kustomization.yaml"
)

func TestRunHolds(t *testing.T) {
	needTools(t)
	// shared kit, primary: bp-console and bp-app; secondary: bp-app (bp-console
	// is suspended); beta kit, primary: bp-console; secondary: none. bp-store's
	// object is a test hook its release does not enable.
	expect(t, fixture(), 0,
		"kit-spc-depends: 4 unit(s) (kit, pack, role); 4 release(s) render SecretProviderClass across them; 0 finding(s).",
		"kit-spc-depends: ok "+shared+" (pack alpha, primary): flux-system/bp-app (clusters/_template/bootstrap-kit/25-app.yaml, chart bp-app) renders SecretProviderClass app/app-sso, and waits for flux-system/bp-secrets-store-csi",
		"kit-spc-depends: ok "+shared+" (pack alpha, secondary): flux-system/bp-app",
		"kit-spc-depends: ok "+beta+" (pack beta, primary): flux-system/bp-console (clusters/_template/bootstrap-kit/13-console.yaml, chart bp-console) renders SecretProviderClass console/console-sso, and waits for flux-system/bp-secrets-store-csi")
}

func TestRunEdgeMissing(t *testing.T) {
	needTools(t)
	f := fixture()
	f["clusters/_template/bootstrap-kit/25-app.yaml"] = hr("25", "bp-app", "bp-app", "  values:\n    sso:\n      enabled: ${APP_SSO:=false}\n")
	expect(t, f, 1,
		"kit-spc-depends: FINDING "+shared+" (pack alpha, primary): flux-system/bp-app (clusters/_template/bootstrap-kit/25-app.yaml, chart bp-app) renders SecretProviderClass app/app-sso, and its spec.dependsOn does not name flux-system/bp-secrets-store-csi",
		"kit-spc-depends: FINDING "+shared+" (pack alpha, secondary): flux-system/bp-app",
		"2 finding(s).")
}

func TestRunPackKitWithoutTheDriver(t *testing.T) {
	needTools(t)
	f := fixture()
	f[beta] = "resources:\n  - ../../../bootstrap-kit/13-console.yaml\n  - local-path.yaml\n"
	expect(t, f, 1,
		"kit-spc-depends: FINDING "+beta+" (pack beta, primary): flux-system/bp-console (clusters/_template/bootstrap-kit/13-console.yaml, chart bp-console) renders SecretProviderClass console/console-sso, and the kit holds no release of chart bp-secrets-store-csi that is not suspended: the kind never arrives",
		"1 finding(s).")
}

func TestRunPackKitWithoutWhatTheDriverNeeds(t *testing.T) {
	needTools(t)
	f := fixture()
	f[beta] = "resources:\n  - ../../../bootstrap-kit/15b-driver.yaml\n  - ../../../bootstrap-kit/13-console.yaml\n  - local-path.yaml\n"
	expect(t, f, 1,
		"kit-spc-depends: FINDING "+beta+" (pack beta, primary): the driver's release flux-system/bp-secrets-store-csi (clusters/_template/bootstrap-kit/15b-driver.yaml) depends on flux-system/bp-store (through flux-system/bp-secrets-store-csi), which the kit does not hold",
		"kit-spc-depends: FINDING "+beta+" (pack beta, secondary): the driver's release")
}

func TestRunTheBaseValuesAlone(t *testing.T) {
	needTools(t)
	// Without the pack's literal substitute bp-app renders no SecretProviderClass,
	// so the missing edge of TestRunEdgeMissing is not a finding: the guard reads
	// the pack's values, not only the base set.
	f := fixture()
	f["infra/providers/alpha/provider-substitutes.yaml.tftpl"] = "APP_SSO: \"${computed}\"\n"
	f["clusters/_template/bootstrap-kit/25-app.yaml"] = hr("25", "bp-app", "bp-app", "  values:\n    sso:\n      enabled: ${APP_SSO:=false}\n")
	expect(t, f, 0, "2 release(s) render SecretProviderClass across them; 0 finding(s).")
}

func TestRunNothingRendersTheKind(t *testing.T) {
	needTools(t)
	f := fixture()
	f["infra/providers/alpha/provider-substitutes.yaml.tftpl"] = ""
	for k, v := range chart("bp-console", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: console\n") {
		f[k] = v
	}
	expect(t, f, 2, "no release of any kit renders a SecretProviderClass")
}

func TestRunChartOutsideTheTree(t *testing.T) {
	needTools(t)
	f := fixture()
	f["clusters/_template/providers/beta/kit/local-path.yaml"] = strings.Replace(f["clusters/_template/providers/beta/kit/local-path.yaml"], "name: local-path\n", "name: other-path\n", 1)
	expect(t, f, 2, `the chart "./deploy/chart/local-path-provisioner" of flux-system/other-path is not a chart of this tree`)
}

func TestRunStaleOutsideEntry(t *testing.T) {
	needTools(t)
	f := fixture()
	f[beta] = "resources:\n  - ../../../bootstrap-kit/08-store.yaml\n  - ../../../bootstrap-kit/15b-driver.yaml\n  - ../../../bootstrap-kit/13-console.yaml\n"
	expect(t, f, 1, "kit-spc-depends: STALE outsideTree names flux-system/local-path, which no kit holds")
}

func TestRunKitEntryNotAFile(t *testing.T) {
	needTools(t)
	f := fixture()
	f[beta] = "resources:\n  - ../../../bootstrap-kit\n"
	expect(t, f, 2, "the entry ../../../bootstrap-kit is not a file")
}
