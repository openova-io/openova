package cloudprofile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// providersDir is infra/providers of this repository, or the directory in
// CLOUDPROFILE_PROVIDERS_DIR (scripts/check-provider-contract.sh --self-test
// points it at a copy it has broken on purpose).
func providersDir() string {
	if d := os.Getenv("CLOUDPROFILE_PROVIDERS_DIR"); d != "" {
		return d
	}
	return filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")
}

// installationLiterals are values of one installation that must never be in
// a pack's files (ADR-0016, K11).
var installationLiterals = regexp.MustCompile(`(?i)kom4dc|nationalcloud|212\.72\.|me-east-215`)

// TestPackFiles is the provider contract check of every pack below
// providersDir(): profile.yaml and pack.yaml are valid, their names match the
// directory, and neither holds a value of an installation.
func TestPackFiles(t *testing.T) {
	dir := providersDir()
	profiles, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	packs, err := LoadPacks(dir)
	if err != nil {
		t.Fatalf("LoadPacks: %v", err)
	}
	if len(profiles) == 0 || len(profiles) != len(packs) {
		t.Fatalf("%d profiles and %d packs below %s", len(profiles), len(packs), dir)
	}
	for name := range profiles {
		for _, f := range []string{ProfileFile, PackFile} {
			data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name, f)))
			if err != nil {
				t.Fatal(err)
			}
			if m := installationLiterals.Find(data); m != nil {
				t.Errorf("%s/%s holds the installation value %q", name, f, m)
			}
		}
	}
}

// TestContractFile checks the form of _contract/contract.json and that each
// fixture sets every required contract variable and nothing else.
func TestContractFile(t *testing.T) {
	dir := filepath.Join(providersDir(), ContractDir)
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "contract.json")))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var c struct {
		Version   string `json:"version"`
		Variables []struct {
			Name      string          `json:"name"`
			Type      string          `json:"type"`
			Required  *bool           `json:"required"`
			Sensitive *bool           `json:"sensitive"`
			Default   json.RawMessage `json:"default"`
		} `json:"variables"`
		Outputs []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"outputs"`
		EngineExtras   []string `json:"engine_extras"`
		TemplateInputs []string `json:"template_inputs"`
	}
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("contract.json: %v", err)
	}
	if c.Version != "1" {
		t.Errorf("version %q, want \"1\"", c.Version)
	}
	ident := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	seen := map[string]string{}
	add := func(kind, name string) {
		if !ident.MatchString(name) {
			t.Errorf("%s %q is not a name", kind, name)
		}
		if prev, ok := seen[kind+":"+name]; ok {
			t.Errorf("%s %q twice (%s)", kind, name, prev)
		}
		seen[kind+":"+name] = kind
	}
	required := map[string]bool{}
	vars := map[string]bool{}
	for _, v := range c.Variables {
		add("variable", v.Name)
		vars[v.Name] = true
		if v.Type == "" || v.Required == nil || v.Sensitive == nil || v.Default == nil {
			t.Errorf("variable %q lacks one of type, required, sensitive, default", v.Name)
			continue
		}
		if *v.Required {
			required[v.Name] = true
			if string(v.Default) != "null" {
				t.Errorf("variable %q is required and has a default", v.Name)
			}
		}
	}
	for _, o := range c.Outputs {
		add("output", o.Name)
		if o.Type == "" {
			t.Errorf("output %q has no type", o.Name)
		}
	}
	for _, e := range c.EngineExtras {
		add("engine extra", e)
		if vars[e] {
			t.Errorf("%q is both a variable and an engine extra", e)
		}
	}
	for _, in := range c.TemplateInputs {
		add("template input", in)
	}
	fixtures, err := filepath.Glob(filepath.Join(dir, "fixtures", "*.tfvars.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures in %s/fixtures (%v)", dir, err)
	}
	for _, f := range fixtures {
		raw, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatal(err)
		}
		var fx map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for k := range fx {
			if !vars[k] {
				t.Errorf("%s sets %q, which is not a contract variable", filepath.Base(f), k)
			}
		}
		for k := range required {
			if _, ok := fx[k]; !ok {
				t.Errorf("%s does not set the required variable %q", filepath.Base(f), k)
			}
		}
	}
}

// TestLoadBothPacks loads the two packs of this repository and checks the
// values the readers of later work packages rely on.
func TestLoadBothPacks(t *testing.T) {
	profiles, err := Load(providersDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var names []string
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "hetzner,huawei" {
		t.Fatalf("packs %v, want [hetzner huawei]", names)
	}
	h := profiles["hetzner"].Spec
	if h.Lifecycle != "unproven" || h.RequiresInstallation || len(h.Regions) == 0 || h.Compute.Image == "" ||
		h.Network.LoadBalancer.Mode != "controller" || h.Bootstrap.UserDataLimitBytes != 32768 ||
		h.Capabilities.MultiRegion || !h.Capabilities.MultiCloudRegion || h.Storage.DefaultClass != "hcloud-volumes" {
		t.Errorf("hetzner profile: %+v", h)
	}
	w := profiles["huawei"].Spec
	if w.Lifecycle != "unproven" || !w.RequiresInstallation || len(w.Regions) != 0 || w.Compute.Image != "" ||
		len(w.Compute.Flavors) != 0 || w.Network.LoadBalancer.Mode != "external-hostport" ||
		w.Bootstrap.UserDataLimitBytes != 0 || !w.Capabilities.MultiRegion || w.Capabilities.MultiCloudRegion ||
		w.Storage.DefaultClass != "evs-ssd" || w.Compute.MinWorkers() != 3 || w.Credentials.Source != "either" {
		t.Errorf("huawei profile: %+v", w)
	}
	packs, err := LoadPacks(providersDir())
	if err != nil {
		t.Fatalf("LoadPacks: %v", err)
	}
	if len(packs["hetzner"].Spec.TofuProviders) != 3 || len(packs["huawei"].Spec.TofuProviders) != 4 {
		t.Errorf("tofuProviders: hetzner %v, huawei %v", packs["hetzner"].Spec.TofuProviders, packs["huawei"].Spec.TofuProviders)
	}
}

// fixtureDir copies the two schemas into a new providers directory and
// writes one pack "demo" with the given profile; pack.yaml is valid.
func fixtureDir(t *testing.T, profile string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ContractDir), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{ProfileSchemaFile, PackSchemaFile} {
		data, err := os.ReadFile(filepath.Clean(filepath.Join(providersDir(), ContractDir, f)))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ContractDir, f), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", ProfileFile), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	pack := `apiVersion: catalyst.openova.io/v1alpha1
kind: ProviderPack
metadata: {name: demo}
spec:
  contractVersion: "1"
  module: infra/providers/demo
  overlay: clusters/_template/providers/demo
  adapter: products/catalyst/bootstrap/api/internal/providers/demo
  tofuProviders: [{source: registry.opentofu.org/hashicorp/random}]
  leakTokens: [demo]
`
	if err := os.WriteFile(filepath.Join(dir, "demo", PackFile), []byte(pack), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const demoProfile = `apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata: {name: demo}
spec:
  displayName: Demo
  lifecycle: unproven
  contractVersion: "1"
  credentials:
    source: user
    fields: [{key: token, label: Token, secret: true, required: true}]
  regions:
    - {code: r1, label: R1, location: Somewhere, zones: [r1a], defaultZone: r1a}
  compute:
    image: base-image
    flavors: [{name: small, vcpu: 2, memoryGiB: 4, roles: [control-plane, worker]}]
  storage: {defaultClass: block, driver: block.csi.example}
  objectStorage: {checksumAlgorithm: ""}
  network:
    loadBalancer: {mode: external-hostport}
  bootstrap: {userDataLimitBytes: 0}
  capabilities: {multiRegion: true, multiCloudRegion: false}
  day2: {contracts: []}
`

// demoPackProfile is a pack that cannot be provisioned on alone.
var demoPackProfile = strings.NewReplacer(
	"  contractVersion: \"1\"\n", "  contractVersion: \"1\"\n  requiresInstallation: true\n",
	"  regions:\n    - {code: r1, label: R1, location: Somewhere, zones: [r1a], defaultZone: r1a}\n", "  regions: []\n",
	"    image: base-image\n    flavors: [{name: small, vcpu: 2, memoryGiB: 4, roles: [control-plane, worker]}]\n", "    image: \"\"\n    flavors: []\n",
).Replace(demoProfile)

func loadDemo(t *testing.T, profile string) *Profile {
	t.Helper()
	profiles, err := Load(fixtureDir(t, profile))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return profiles["demo"]
}

func wantField(t *testing.T, err, kind error, path string) {
	t.Helper()
	var fe *FieldError
	if !errors.Is(err, kind) || !errors.As(err, &fe) || fe.Path != path {
		t.Fatalf("error %v, want %v at %s", err, kind, path)
	}
}

func TestValidateRejectsUnknownField(t *testing.T) {
	p := loadDemo(t, demoProfile)
	if err := Validate(p); err != nil {
		t.Fatalf("valid profile: %v", err)
	}
	p.doc["spec"].(map[string]any)["compute"].(map[string]any)["autoscaling"] = true
	wantField(t, Validate(p), ErrUnknownField, "spec.compute.autoscaling")

	_, err := Load(fixtureDir(t, strings.Replace(demoProfile, "  displayName: Demo\n", "  displayName: Demo\n  cost: {}\n", 1)))
	wantField(t, err, ErrUnknownField, "spec.cost")
}

func TestValidateRejectsMissingFieldAndBadValues(t *testing.T) {
	_, err := Load(fixtureDir(t, strings.Replace(demoProfile, "  storage: {defaultClass: block, driver: block.csi.example}\n", "", 1)))
	wantField(t, err, ErrMissingField, "spec.storage")
	_, err = Load(fixtureDir(t, strings.Replace(demoProfile, "lifecycle: unproven", "lifecycle: beta", 1)))
	wantField(t, err, ErrBadEnum, "spec.lifecycle")
	_, err = Load(fixtureDir(t, strings.Replace(demoProfile, "mode: external-hostport", "mode: nodeport", 1)))
	wantField(t, err, ErrBadEnum, "spec.network.loadBalancer.mode")
	_, err = Load(fixtureDir(t, strings.Replace(demoProfile, "vcpu: 2", "vcpu: two", 1)))
	wantField(t, err, ErrBadValue, "spec.compute.flavors[0].vcpu")
	_, err = Load(fixtureDir(t, strings.Replace(demoProfile, "metadata: {name: demo}", "metadata: {name: other}", 1)))
	wantField(t, err, ErrBadValue, "metadata.name")
	_, err = Load(fixtureDir(t, strings.Replace(demoProfile, "  regions:\n    - {code: r1, label: R1, location: Somewhere, zones: [r1a], defaultZone: r1a}\n", "  regions: []\n", 1)))
	wantField(t, err, ErrMissingField, "spec.regions")
	if _, err := Load(fixtureDir(t, demoPackProfile)); err != nil {
		t.Fatalf("a pack that requires an installation may have no region: %v", err)
	}
}

const installation = `apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata: {name: site-a}
spec:
  extends: demo
  lifecycle: proven
  api: {domain: cloud.example.test, authURL: "", insecureTLS: false}
  network: {apiCIDRs: ["192.0.2.0/24"]}
  regions:
    - {code: region-1, label: Region 1, location: Example City, zones: [region-1a], defaultZone: region-1a}
  compute:
    image: 00000000-0000-0000-0000-000000000000
    flavors:
      - {name: example.xlarge, vcpu: 4, memoryGiB: 32, roles: [control-plane, worker]}
    defaults: {controlPlaneFlavor: example.xlarge, workerFlavor: example.xlarge}
  registry: {mirrorEndpoint: "https://mirror.example.test"}
`

func parse(t *testing.T, doc string) *Profile {
	t.Helper()
	p, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestMergeRejectsNonInstallationField(t *testing.T) {
	pack := loadDemo(t, demoPackProfile)
	_, err := Merge(pack, parse(t, installation+"  storage: {defaultClass: other, driver: other}\n"))
	wantField(t, err, ErrNotInstallationField, "spec.storage")
	_, err = Merge(pack, parse(t, strings.Replace(installation, "  compute:\n", "  compute:\n    minWorkerCount: 9\n", 1)))
	wantField(t, err, ErrNotInstallationField, "spec.compute.minWorkerCount")
	_, err = Merge(pack, parse(t, strings.Replace(installation, "  network: {apiCIDRs: [\"192.0.2.0/24\"]}\n", "  network: {loadBalancer: {mode: controller}}\n", 1)))
	wantField(t, err, ErrNotInstallationField, "spec.network.loadBalancer")
	_, err = Merge(pack, parse(t, strings.Replace(installation, "extends: demo", "extends: nothere", 1)))
	wantField(t, err, ErrUnknownPack, "spec.extends")
	_, err = Merge(pack, parse(t, strings.Replace(installation, "  extends: demo\n", "", 1)))
	wantField(t, err, ErrMissingField, "spec.extends")
}

func TestMergeInstallation(t *testing.T) {
	pack := loadDemo(t, demoPackProfile)
	eff, err := Merge(pack, parse(t, installation))
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	s := eff.Spec
	if eff.Metadata.Name != "site-a" || s.Extends != "demo" || s.Lifecycle != "proven" || len(s.Regions) != 1 ||
		s.Compute.Image == "" || len(s.Compute.Flavors) != 1 || s.Storage.DefaultClass != "block" ||
		s.Network.LoadBalancer.Mode != "external-hostport" || len(s.Network.APICIDRs) != 1 ||
		s.Registry == nil || s.API == nil || s.API.Domain != "cloud.example.test" || !s.RequiresInstallation {
		t.Errorf("effective profile: %+v", eff)
	}
	if pack.Spec.Lifecycle != "unproven" || len(pack.Spec.Regions) != 0 {
		t.Errorf("Merge changed the pack profile: %+v", pack.Spec)
	}
	if err := Validate(eff); err != nil {
		t.Errorf("Validate(effective): %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(installation, "memoryGiB: 32", "memoryGiB: lots", 1))); !errors.Is(err, ErrBadValue) {
		t.Errorf("Parse of a flavor whose memory is not a number: %v, want ErrBadValue", err)
	}
}

func TestIncompleteWhenInstallationRequired(t *testing.T) {
	pack := loadDemo(t, demoPackProfile)
	noRegions := strings.Replace(installation, "  regions:\n    - {code: region-1, label: Region 1, location: Example City, zones: [region-1a], defaultZone: region-1a}\n", "", 1)
	_, err := Merge(pack, parse(t, noRegions))
	wantField(t, err, ErrIncomplete, "spec.regions")
	noImage := strings.Replace(installation, "    image: 00000000-0000-0000-0000-000000000000\n", "", 1)
	_, err = Merge(pack, parse(t, noImage))
	wantField(t, err, ErrIncomplete, "spec.compute.image")
	noFlavors := strings.Replace(installation, "    flavors:\n      - {name: example.xlarge, vcpu: 4, memoryGiB: 32, roles: [control-plane, worker]}\n", "", 1)
	_, err = Merge(pack, parse(t, noFlavors))
	wantField(t, err, ErrIncomplete, "spec.compute.flavors")
}
