package providersapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

// secretValue sits beside the installation document in its ConfigMap; no answer may hold it.
const secretValue = "s3cr3t-value-never-served"

// profile is a pack profile; requires true makes it need an installation.
func profile(name, lifecycle, source string, requires bool) string {
	regions := "\n    - {code: r1, label: R1, location: Somewhere, zones: [r1a], defaultZone: r1a}"
	image, flavors := "base-image", "[{name: small, vcpu: 2, memoryGiB: 4, roles: [control-plane, worker]}]"
	if requires {
		regions, image, flavors = " []\n  requiresInstallation: true", `""`, "[]"
	}
	return `apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata: {name: ` + name + `}
spec:
  displayName: Pack ` + name + `
  lifecycle: ` + lifecycle + `
  contractVersion: "1"
  credentials:
    source: ` + source + `
    fields: [{key: token, label: Token, secret: true, required: true}]
  regions:` + regions + `
  compute: {image: ` + image + `, flavors: ` + flavors + `}
  storage: {defaultClass: block, driver: block.csi.example}
  objectStorage: {checksumAlgorithm: ""}
  network: {loadBalancer: {mode: external-hostport}}
  seal: {kind: static-node-file}
  bootstrap: {userDataLimitBytes: 0}
  capabilities: {multiRegion: true, multiCloudRegion: false}
  day2: {contracts: []}
`
}

const installation = `apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata: {name: site-1}
spec:
  extends: beta
  regions: [{code: s1, label: S1, location: Site, zones: [s1a], defaultZone: s1a}]
  compute: {image: site-image, flavors: [{name: big, vcpu: 8, memoryGiB: 32, roles: [control-plane, worker]}]}
`

// packsDir: the repository's profile schema and packs alpha (proven), beta (needs an installation,
// operator credentials) and gamma (unproven); bad gives gamma a field the schema refuses.
func packsDir(t *testing.T, bad bool) string {
	dir := t.TempDir()
	schema, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers", cloudprofile.ContractDir, cloudprofile.ProfileSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	gamma := profile("gamma", "unproven", "user", false)
	if bad {
		gamma = strings.Replace(gamma, "  lifecycle:", "  colour: red\n  lifecycle:", 1)
	}
	for name, data := range map[string]string{filepath.Join(cloudprofile.ContractDir, cloudprofile.ProfileSchemaFile): string(schema),
		"alpha/profile.yaml": profile("alpha", "proven", "user", false), "beta/profile.yaml": profile("beta", "unproven", "operator", true), "gamma/profile.yaml": gamma} {
		if p := filepath.Join(dir, name); os.MkdirAll(filepath.Dir(p), 0o750) != nil || os.WriteFile(p, []byte(data), 0o600) != nil {
			t.Fatalf("write %s", p)
		}
	}
	return dir
}

// cluster: a labelled ConfigMap per document, a credential beside it, and an unlabelled one.
func cluster(docs ...string) Installations {
	c := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unlabelled", Namespace: cloudprofile.InstallationNamespace},
		Data: map[string]string{cloudprofile.InstallationKey: strings.Replace(installation, "site-1", "site-2", 1)}})
	for _, d := range docs {
		_ = c.Tracker().Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cloud-installation-site-1", Namespace: cloudprofile.InstallationNamespace,
			Labels: map[string]string{cloudprofile.InstallationLabel: "true"}}, Data: map[string]string{cloudprofile.InstallationKey: d, "secret_key": secretValue}})
	}
	return FromCluster(func() (kubernetes.Interface, error) { return c, nil })
}

func serve(t *testing.T, rt *Routes, path string) (int, string) {
	r := chi.NewRouter()
	r.Get("/api/v1/providers", rt.List)
	r.Get("/api/v1/providers/{provider}", rt.Profile)
	r.Get("/api/v1/sovereign/cloud-profile", rt.Sovereign)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Errorf("GET %s answers a credential value: %s", path, rec.Body)
	}
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func TestList(t *testing.T) {
	proven := strings.Replace(installation, "  extends: beta\n", "  extends: beta\n  lifecycle: proven\n", 1)
	for _, c := range []struct {
		path string
		docs []string
		want []string
	}{
		{"/api/v1/providers", nil, []string{"alpha proven user"}},
		{"/api/v1/providers?include=all", nil, []string{"alpha proven user", "gamma unproven user"}},
		{"/api/v1/providers", []string{installation}, []string{"alpha proven user"}},
		{"/api/v1/providers?include=all", []string{installation}, []string{"alpha proven user", "beta/site-1 unproven operator", "gamma unproven user"}},
		{"/api/v1/providers", []string{proven}, []string{"alpha proven user", "beta/site-1 proven operator"}},
	} {
		code, body := serve(t, New(packsDir(t, false), cluster(c.docs...)), c.path)
		var doc struct{ Providers []Entry }
		got := []string{}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		for _, e := range doc.Providers {
			got = append(got, e.ID+" "+e.Lifecycle+" "+e.CredentialSource)
		}
		if code != http.StatusOK || !reflect.DeepEqual(got, c.want) {
			t.Errorf("GET %s with %d documents: %d %v, want 200 %v", c.path, len(c.docs), code, got, c.want)
		}
	}
}

func TestErrors(t *testing.T) {
	good, bad := packsDir(t, false), packsDir(t, true)
	installs := func(old, new string) []string { return []string{strings.Replace(installation, old, new, 1)} }
	for _, c := range []struct {
		dir, path, env string
		docs           []string
		status         int
		want           string
	}{
		{bad, "/api/v1/providers", "", nil, 500, `"error":"profile-invalid","provider":"gamma"`},
		{bad, "/api/v1/providers/alpha", "", nil, 500, `"error":"profile-invalid","provider":"gamma"`},
		{good, "/api/v1/providers?include=all", "", installs("  regions:", "  storage: {defaultClass: other, driver: other}\n  regions:"), 500, `"error":"profile-invalid","provider":"beta"`},
		{good, "/api/v1/providers", "", installs("extends: beta", "extends: delta"), 500, `"error":"profile-invalid","provider":"delta"`},
		{good, "/api/v1/providers", "", nil, 500, `"error":"installations-unreadable"`},
		{good, "/api/v1/providers/delta", "", nil, 404, `{"error":"unknown-provider"}`},
		{good, "/api/v1/providers/alpha?installation=site-1", "", []string{installation}, 404, `{"error":"unknown-installation"}`},
		{good, "/api/v1/providers/beta?installation=site-2", "", []string{installation}, 404, `{"error":"unknown-installation"}`},
		{good, "/api/v1/sovereign/cloud-profile", "", nil, 503, `{"error":"cloud-profile-missing"}`},
		{good, "/api/v1/sovereign/cloud-profile", "delta", nil, 503, `{"error":"cloud-profile-missing"}`},
	} {
		t.Setenv(cloudprofile.ProviderEnv, c.env)
		list := cluster(c.docs...)
		if strings.Contains(c.want, "unreadable") {
			list = FromCluster(func() (kubernetes.Interface, error) { return nil, errors.New("not in a cluster") })
		}
		code, body := serve(t, New(c.dir, list), c.path)
		if code != c.status || !strings.Contains(body, c.want) || (code == 500 && !strings.Contains(body, `"detail":"`)) {
			t.Errorf("GET %s: %d %s, want %d with %s", c.path, code, body, c.status, c.want)
		}
	}
}

// TestProfiles: the effective profile is the merged spec, and every credential
// field carries its description and no value.
func TestProfiles(t *testing.T) {
	t.Setenv(cloudprofile.ProviderEnv, "beta")
	t.Setenv(cloudprofile.InstallationEnv, "site-1")
	rt := New(packsDir(t, false), cluster(installation))
	for path, want := range map[string]string{
		"/api/v1/providers/alpha":                    "Pack alpha  base-image [{r1}]",
		"/api/v1/providers/beta?installation=site-1": "Pack beta beta site-image [{s1}]",
		"/api/v1/sovereign/cloud-profile":            "Pack beta beta site-image [{s1}]",
		"/api/v1/providers/beta":                     "Pack beta   []",
	} {
		code, body := serve(t, rt, path)
		var spec struct {
			DisplayName, Extends string
			Compute              struct{ Image string }
			Regions              []struct{ Code string }
			Credentials          struct{ Fields []map[string]any }
		}
		if err := json.Unmarshal([]byte(body), &spec); err != nil {
			t.Fatalf("GET %s: %v: %s", path, err, body)
		}
		got := fmt.Sprintf("%s %s %s %v", spec.DisplayName, spec.Extends, spec.Compute.Image, spec.Regions)
		field := map[string]any{"key": "token", "label": "Token", "secret": true, "required": true}
		if code != http.StatusOK || got != want || len(spec.Credentials.Fields) != 1 || !reflect.DeepEqual(spec.Credentials.Fields[0], field) {
			t.Errorf("GET %s: %d %q fields %v, want 200 %q fields [%v]", path, code, got, spec.Credentials.Fields, want, field)
		}
	}
}
