package upstream

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

// example is the example document of contract C5: every contract key, no kit key.
func example() map[string]string {
	return map[string]string{
		"SOVEREIGN_MODE":                     "sovereign",
		"SOVEREIGN_CONSUME":                  "origin",
		"HANDOVER_AUTO":                      "true",
		"HANDOVER_HOLD_SECONDS":              "600",
		"REGISTRY_HOST":                      "registry.parent.example.net",
		"REGISTRY_SCHEME":                    "https",
		"LOCAL_REGISTRY_HOST":                "registry.alpha.example.org",
		"ORIGIN_REGISTRY_HOST":               "registry.parent.example.net",
		"ORIGIN_REGISTRY_SCHEME":             "https",
		"ORIGIN_REGISTRY_CA_B64":             "",
		"ORIGIN_PROXY_ENDPOINTS_JSON":        "{}",
		"REGISTRY_PULL_SECRET":               "ghcr-pull",
		"REGISTRY_PATH_CHARTS":               "openova-io",
		"REGISTRY_PATH_RELEASE":              "openova-io/release",
		"REGISTRY_PATH_PROVIDER_PACKS":       "openova-io/provider-packs",
		"REGISTRY_PATH_XPKG":                 "proxy-xpkg",
		"REGISTRY_PATH_TOFU_PROVIDERS":       "tofu-providers",
		"REGISTRY_PATH_NODE_ARTIFACTS":       "node-artifacts",
		"REGISTRY_PROXY_PROJECTS_JSON":       `{"docker.io":"proxy-dockerhub","gcr.io":"proxy-gcr","ghcr.io":"proxy-ghcr","public.ecr.aws":"proxy-ecr","quay.io":"proxy-quay","registry-1.docker.io":"proxy-dockerhub","registry.k8s.io":"proxy-k8s","xpkg.upbound.io":"proxy-xpkg"}`,
		"RELEASE_PUBLIC_KEY_B64":             "LS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS0K",
		"RELEASE_CHANNEL":                    "stable",
		"RELEASE_VERSION":                    "",
		"RELEASE_AUTO":                       "false",
		"RELEASE_SYNC_SCHEDULE":              "0 3 * * *",
		"ORIGIN_PARENT_FQDN":                 "parent.example.net",
		"ORIGIN_PARENT_API_URL":              "https://console.parent.example.net/sovereign",
		"DNS_MODE":                           "delegated",
		"DNS_UPSTREAM_API_URL":               "",
		"MAIL_MODE":                          "local",
		"MAIL_RELAY_HOST":                    "",
		"MAIL_RELAY_PORT":                    "587",
		"MAIL_FROM":                          "",
		"MAIL_RELAY_SECRET":                  "",
		"K3S_VERSION":                        "v1.32.9+k3s1",
		"K3S_UPGRADE_CONCURRENCY":            "1",
		"CLOUD_CREDENTIAL_MODE":              "minted",
		"CLOUD_CREDENTIAL_ROTATION_SCHEDULE": "0 2 1 * *",
		"CLOUD_CREDENTIAL_ROTATION_NONCE":    "",
		"GITOPS_REPO_URL":                    "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack",
	}
}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func mustFromMap(t *testing.T, m map[string]string) Variables {
	t.Helper()
	v, err := FromMap(m)
	if err != nil {
		t.Fatalf("FromMap: %v", err)
	}
	return v
}

// The copies under contract/ are byte for byte the published contract files.
func TestContractCopiesMatch(t *testing.T) {
	for _, name := range []string{"c5-upstream-variables.schema.json", "c5-kit-keys.txt"} {
		published, err := os.ReadFile(filepath.Clean("../../../../docs/contracts/" + name))
		if err != nil {
			t.Fatalf("read the published contract file: %v", err)
		}
		copied, err := os.ReadFile(filepath.Clean("contract/" + name))
		if err != nil {
			t.Fatalf("read the copy: %v", err)
		}
		if !bytes.Equal(published, copied) {
			t.Errorf("contract/%s differs from docs/contracts/%s; copy the published file", name, name)
		}
	}
	if len(KitKeys()) == 0 {
		t.Errorf("no kit keys loaded")
	}
}

func TestRoundTrip(t *testing.T) {
	m := example()
	v := mustFromMap(t, m)
	if got := v.ToMap(); !reflect.DeepEqual(got, m) {
		t.Errorf("ToMap(FromMap(example)) differs from the example:\n got %v\nwant %v", got, m)
	}
	again := mustFromMap(t, v.ToMap())
	if !reflect.DeepEqual(again, v) {
		t.Errorf("FromMap(ToMap(v)) != v:\n got %+v\nwant %+v", again, v)
	}
	// A key that has a default may be left out; ToMap renders it.
	short := example()
	delete(short, "ORIGIN_PROXY_ENDPOINTS_JSON")
	delete(short, "MAIL_RELAY_PORT")
	got := mustFromMap(t, short).ToMap()
	if got["ORIGIN_PROXY_ENDPOINTS_JSON"] != "{}" || got["MAIL_RELAY_PORT"] != "587" {
		t.Errorf("defaults not rendered: %q %q", got["ORIGIN_PROXY_ENDPOINTS_JSON"], got["MAIL_RELAY_PORT"])
	}
	if len(got) != 39 {
		t.Errorf("ToMap renders %d keys, want the 39 contract keys", len(got))
	}
}

func TestFromMapRejectsUnknownKey(t *testing.T) {
	if _, err := FromMap(with(example(), "NOT_A_CONTRACT_KEY", "x")); err == nil || !strings.Contains(err.Error(), "NOT_A_CONTRACT_KEY") {
		t.Errorf("an unknown key must fail and be named, got %v", err)
	}
	// Every kit key is accepted and ignored.
	m := example()
	for _, k := range KitKeys() {
		m[k] = "kit-value"
	}
	if got := mustFromMap(t, m).ToMap(); !reflect.DeepEqual(got, example()) {
		t.Errorf("kit keys changed the contract keys")
	}
	cases := map[string]map[string]string{
		"a required key is missing":   func() map[string]string { m := example(); delete(m, "LOCAL_REGISTRY_HOST"); return m }(),
		"a value outside its enum":    with(example(), "SOVEREIGN_CONSUME", "elsewhere"),
		"a value outside its pattern": with(example(), "K3S_VERSION", "1.32"),
		"a map that is not JSON":      with(example(), "REGISTRY_PROXY_PROJECTS_JSON", "quay.io=proxy-quay"),
	}
	for name, m := range cases {
		if _, err := FromMap(m); err == nil {
			t.Errorf("%s: FromMap accepted it", name)
		}
	}
}

// TestValidateRules has one case per cross-field rule. Rule 5 is a rule on
// key names, so FromMap enforces it.
func TestValidateRules(t *testing.T) {
	const own = "alpha.example.org"
	if errs := mustFromMap(t, example()).Validate(own); len(errs) != 0 {
		t.Fatalf("the example must pass, got %v", errs)
	}
	cases := []struct {
		rule string
		data map[string]string
	}{
		{"rule 1:", with(example(), "SOVEREIGN_CONSUME", "local")},
		{"rule 2:", with(example(), "REGISTRY_HOST", "registry.alpha.example.org")},
		{"rule 3:", with(example(), "MAIL_MODE", "relay")},
		{"rule 4:", with(example(), "DNS_MODE", "upstream-managed")},
		{"rule 5:", with(example(), "DNS_API_TOKEN", "x")},
		{"rule 6:", with(example(), "HANDOVER_HOLD_SECONDS", "3601")},
		{"rule 7:", with(example(), "ORIGIN_PARENT_API_URL", "https://console.alpha.example.org/sovereign")},
	}
	for _, c := range cases {
		v, err := FromMap(c.data)
		var got []string
		if err != nil {
			got = append(got, err.Error())
		} else {
			for _, e := range v.Validate(own) {
				got = append(got, e.Error())
			}
		}
		if len(got) != 1 || !strings.HasPrefix(got[0], c.rule) {
			t.Errorf("%s case: want exactly one error of that rule, got %v", c.rule, got)
		}
	}
	// The passing side of each rule.
	local := mustFromMap(t, with(example(), "SOVEREIGN_CONSUME", "local", "REGISTRY_HOST", "registry.alpha.example.org", "ORIGIN_REGISTRY_HOST", ""))
	relay := mustFromMap(t, with(example(), "MAIL_MODE", "relay", "MAIL_RELAY_HOST", "smtp.example.net", "MAIL_FROM", "noreply@alpha.example.org", "MAIL_RELAY_SECRET", "mail-relay"))
	dns := mustFromMap(t, with(example(), "DNS_MODE", "upstream-managed", "DNS_UPSTREAM_API_URL", "https://dns.parent.example.net"))
	hold := mustFromMap(t, with(example(), "HANDOVER_HOLD_SECONDS", "30"))
	key := mustFromMap(t, with(example(), "SOVEREIGN_REGION_KEY", "fsn1"))
	for i, v := range []Variables{local, relay, dns, hold, key} {
		if errs := v.Validate(own); len(errs) != 0 {
			t.Errorf("passing case %d: got %v", i, errs)
		}
	}
	if errs := mustFromMap(t, with(example(), "HANDOVER_HOLD_SECONDS", "29")).Validate(own); len(errs) != 1 {
		t.Errorf("29 seconds must fail rule 6, got %v", errs)
	}
	proxied := mustFromMap(t, with(example(), "ORIGIN_PROXY_ENDPOINTS_JSON", `{"quay.io":"http://registry.alpha.example.org:5000"}`))
	if errs := proxied.Validate(own); len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "rule 7:") {
		t.Errorf("a proxy endpoint under the own name must fail rule 7, got %v", errs)
	}
	if errs := proxied.Validate(""); len(errs) != 0 {
		t.Errorf("without an own FQDN rule 7 is not judged, got %v", errs)
	}
}

func TestServedNeverCopiesOrigin(t *testing.T) {
	parent := mustFromMap(t, with(example(),
		"SOVEREIGN_MODE", "mothership",
		"LOCAL_REGISTRY_HOST", "registry.parent.example.net",
		"REGISTRY_HOST", "registry.grand.example.com",
		"ORIGIN_REGISTRY_HOST", "registry.grand.example.com",
		"ORIGIN_REGISTRY_SCHEME", "http",
		"REGISTRY_SCHEME", "http",
		"ORIGIN_REGISTRY_CA_B64", "Q0EtT0YtVEhFLUdSQU5E",
		"ORIGIN_PROXY_ENDPOINTS_JSON", `{"quay.io":"http://registry.grand.example.com:5002"}`,
		"ORIGIN_PARENT_FQDN", "grand.example.com",
		"ORIGIN_PARENT_API_URL", "https://console.grand.example.com/sovereign",
		"RELEASE_CHANNEL", "candidate",
		"REGISTRY_PATH_CHARTS", "charts-path",
	))
	child := Served("parent.example.net", parent)
	m := child.ToMap()
	for k, v := range m {
		for _, own := range []string{"grand.example.com", "Q0EtT0YtVEhFLUdSQU5E", "5002"} {
			if strings.Contains(v, own) {
				t.Errorf("%s = %q copies the parent's own origin value %q", k, v, own)
			}
		}
	}
	want := map[string]string{
		"SOVEREIGN_MODE":               "sovereign",
		"SOVEREIGN_CONSUME":            "origin",
		"REGISTRY_HOST":                "registry.parent.example.net",
		"REGISTRY_SCHEME":              "https",
		"ORIGIN_REGISTRY_HOST":         "registry.parent.example.net",
		"ORIGIN_REGISTRY_SCHEME":       "https",
		"ORIGIN_REGISTRY_CA_B64":       "",
		"ORIGIN_PROXY_ENDPOINTS_JSON":  "{}",
		"ORIGIN_PARENT_FQDN":           "parent.example.net",
		"ORIGIN_PARENT_API_URL":        "https://console.parent.example.net/sovereign",
		"RELEASE_CHANNEL":              "candidate",
		"REGISTRY_PATH_CHARTS":         "charts-path",
		"REGISTRY_PROXY_PROJECTS_JSON": example()["REGISTRY_PROXY_PROJECTS_JSON"],
		"RELEASE_PUBLIC_KEY_B64":       "LS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS0K",
		"LOCAL_REGISTRY_HOST":          "",
		"K3S_VERSION":                  "",
		"MAIL_MODE":                    "",
	}
	for k, w := range want {
		if m[k] != w {
			t.Errorf("Served: %s = %q, want %q", k, m[k], w)
		}
	}
	// The caller fills the per-child keys; the result is then a valid document for the child.
	filled := with(m, "LOCAL_REGISTRY_HOST", "registry.alpha.example.org", "K3S_VERSION", "v1.32.9+k3s1", "MAIL_MODE", "local", "CLOUD_CREDENTIAL_MODE", "minted")
	v := mustFromMap(t, filled)
	if errs := v.Validate("alpha.example.org"); len(errs) != 0 {
		t.Errorf("the served document, filled, must pass: %v", errs)
	}
	// Served does not share the parent's map.
	child.ProxyProjects["quay.io"] = "changed"
	if parent.ProxyProjects["quay.io"] != "proxy-quay" {
		t.Errorf("Served shares the parent's proxy project map")
	}
}

func TestFlippedChangesThreeKeys(t *testing.T) {
	// The origin of this document is served over http, so all three keys differ after the flip.
	v := mustFromMap(t, with(example(), "REGISTRY_SCHEME", "http", "ORIGIN_REGISTRY_SCHEME", "http"))
	if errs := v.Validate("alpha.example.org"); len(errs) != 0 {
		t.Fatalf("the document must pass before the flip: %v", errs)
	}
	before, after := v.ToMap(), v.Flipped().ToMap()
	var changed []string
	for k := range before {
		if before[k] != after[k] {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	if want := []string{"REGISTRY_HOST", "REGISTRY_SCHEME", "SOVEREIGN_CONSUME"}; !reflect.DeepEqual(changed, want) {
		t.Errorf("Flipped changed %v, want exactly %v", changed, want)
	}
	if after["SOVEREIGN_CONSUME"] != "local" || after["REGISTRY_HOST"] != "registry.alpha.example.org" || after["REGISTRY_SCHEME"] != "https" {
		t.Errorf("Flipped values: %q %q %q", after["SOVEREIGN_CONSUME"], after["REGISTRY_HOST"], after["REGISTRY_SCHEME"])
	}
	if errs := v.Flipped().Validate("alpha.example.org"); len(errs) != 0 {
		t.Errorf("the flipped example must pass: %v", errs)
	}
	if v.ToMap()["SOVEREIGN_CONSUME"] != "origin" {
		t.Errorf("Flipped changed its receiver")
	}
}

func TestRegistriesYAMLTwoEndpointsThenOne(t *testing.T) {
	type mirror struct {
		Endpoint []string          `json:"endpoint"`
		Rewrite  map[string]string `json:"rewrite"`
	}
	parse := func(b []byte) map[string]mirror {
		t.Helper()
		var f struct {
			Mirrors map[string]mirror `json:"mirrors"`
		}
		if err := yaml.UnmarshalStrict(b, &f); err != nil {
			t.Fatalf("not a registries file: %v\n%s", err, b)
		}
		return f.Mirrors
	}
	v := mustFromMap(t, with(example(), "ORIGIN_PROXY_ENDPOINTS_JSON", `{"docker.io":"http://registry.parent.example.net:5002"}`))
	hosted := []byte("ghcr.io:\n  \"^openova-io/openova/(.*)\": \"openova-io/openova/$1\"\n")
	b, err := v.RegistriesYAML("", hosted)
	if err != nil {
		t.Fatal(err)
	}
	before := parse(b)
	local, origin := "https://registry.alpha.example.org", "https://registry.parent.example.net"
	if got := before["quay.io"]; !reflect.DeepEqual(got.Endpoint, []string{local, origin}) || got.Rewrite["(.*)"] != "proxy-quay/$1" {
		t.Errorf("quay.io before the flip: %+v", got)
	}
	if got := before["docker.io"].Endpoint; !reflect.DeepEqual(got, []string{local, "http://registry.parent.example.net:5002"}) {
		t.Errorf("docker.io must use its proxy endpoint: %v", got)
	}
	if got := before["ghcr.io"].Rewrite; !reflect.DeepEqual(got, map[string]string{"^openova-io/openova/(.*)": "openova-io/openova/$1"}) {
		t.Errorf("ghcr.io must take the hosted rewrite: %v", got)
	}
	if got := before["registry.parent.example.net"].Endpoint; !reflect.DeepEqual(got, []string{local, origin}) {
		t.Errorf("origin host entry: %v", got)
	}
	if got := before["registry.alpha.example.org"].Endpoint; !reflect.DeepEqual(got, []string{local}) {
		t.Errorf("local host entry: %v", got)
	}
	if got := before["*"]; !reflect.DeepEqual(got.Endpoint, []string{local}) || got.Rewrite != nil {
		t.Errorf("wildcard entry: %+v", got)
	}
	if len(before) != 8+3 {
		t.Errorf("before the flip: %d entries, want 11", len(before))
	}

	b, err = v.Flipped().RegistriesYAML("", hosted)
	if err != nil {
		t.Fatal(err)
	}
	after := parse(b)
	for n, m := range after {
		if !reflect.DeepEqual(m.Endpoint, []string{local}) {
			t.Errorf("after the flip %s has %v, want only the local registry", n, m.Endpoint)
		}
	}
	if _, ok := after["registry.parent.example.net"]; ok || len(after) != 8+2 {
		t.Errorf("after the flip the origin host entry must be gone; %d entries", len(after))
	}

	b, err = v.RegistriesYAML("http://127.0.0.1:65001", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(b)["quay.io"].Endpoint; !reflect.DeepEqual(got, []string{"http://127.0.0.1:65001", local, origin}) {
		t.Errorf("with the node-local proxy: %v", got)
	}
	if _, err := v.RegistriesYAML("", []byte("not-a-proxied-name.example:\n  a: b\n")); err == nil {
		t.Errorf("a hosted rewrite for an unknown name must fail")
	}
}

func TestLoad(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: Name}, Data: example()}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
	v, err := Load(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if v.LocalRegistryHost != "registry.alpha.example.org" {
		t.Errorf("Load: %+v", v)
	}
	empty := fake.NewClientBuilder().WithScheme(scheme).Build()
	if _, err := Load(context.Background(), empty); err == nil {
		t.Errorf("Load without the ConfigMap must fail")
	}
}
