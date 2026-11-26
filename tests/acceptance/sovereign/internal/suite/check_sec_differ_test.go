package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

const differSalt = "SEC060testSALTsec060testSALTsec060testSALTsec060t"

// differSeed is a client secret as bp-keycloak derives it: the SHA-256 of seed, with |salt appended when salt is set.
func differSeed(seed, salt string) string {
	if salt != "" {
		seed += "|" + salt
	}
	return sha256Hex(seed)
}

// differRealms are the realm imports of one region: the realm sovereign with
// two clients and the pin broker, and the realm of Organization acme with its
// broker. salt "" is a Sovereign without the salt.
func differRealms(salt string) []obj {
	sovereign := map[string]any{"realm": "sovereign",
		"clients": []any{
			map[string]any{"clientId": "grafana", "secret": differSeed("keycloak|keycloak|tier1-sso|grafana", salt)},
			map[string]any{"clientId": "catalyst-api-server", "secret": differSeed("keycloak|keycloak|catalyst-api-server-sa", salt)},
			map[string]any{"clientId": "catalyst-ui"},
		},
		"identityProviders": []any{map[string]any{"alias": "catalyst-pin", "config": map[string]any{"clientId": "catalyst-pin", "clientSecret": differSeed("keycloak|keycloak|catalyst-pin-broker", salt)}}},
	}
	org := map[string]any{"realm": "acme", "identityProviders": []any{map[string]any{"alias": "sovereign", "config": map[string]any{"clientId": "acme-broker", "clientSecret": differSeed("keycloak|keycloak|tenant-broker|acme", salt)}}}}
	text := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return []obj{
		{"metadata": obj{"name": "keycloak-sovereign-realm-config", "namespace": "keycloak"}, "data": obj{"sovereign-realm.json": text(sovereign)}},
		{"metadata": obj{"name": "keycloak-org-realm-acme-config", "namespace": "keycloak", "labels": obj{"catalyst.openova.io/org-slug": "acme"}}, "data": obj{"org-realm.json": text(org)}},
		{"metadata": obj{"name": "kube-root-ca.crt", "namespace": "keycloak"}, "data": obj{"ca.crt": "-----BEGIN CERTIFICATE-----"}},
	}
}

// differFake answers `kubectl get configmaps -n keycloak -o json` per region.
type differFake struct {
	t       *testing.T
	regions map[string][]obj // region ID -> ConfigMaps; a missing region answers an empty list
	fail    map[string]bool  // region ID -> kubectl fails
}

func (f differFake) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	if line := strings.Join(args, " "); line != "get configmaps -n keycloak -o json" {
		f.t.Errorf("unexpected kubectl %s", line)
		return nil, errors.New("unexpected")
	}
	if f.fail[r.ID] {
		return nil, errors.New("the server is unreachable")
	}
	return []byte(kubeList(f.regions[r.ID]...)), nil
}

func runDiffer(t *testing.T, f differFake) *runner {
	t.Helper()
	f.t = t
	return runCheck(t, "sec-differ", secDifferCheck{kubectl: f.kubectl}, &CheckContext{})
}

func TestSecDifferRegistered(t *testing.T) {
	if _, ok := registry[secDifferID]; !ok || secDifferID != "SEC-G06" {
		t.Fatalf("check %s is not registered", secDifferID)
	}
}

func TestSecDifferPassesSaltedSecrets(t *testing.T) {
	r := runDiffer(t, differFake{regions: map[string][]obj{"region-a": differRealms(differSalt), "region-b": differRealms(differSalt)}})
	if f := failedSteps(r); len(f) > 0 {
		t.Fatalf("salted secrets failed: %v", f)
	}
	if n := len(r.run.Steps); n != 2 {
		t.Fatalf("want one step per region, got %d", n)
	}
	ev := r.run.Steps[0].Evidence
	if ev["secrets"] != 4 || ev["release"] != "keycloak" {
		t.Errorf("evidence: %v", ev)
	}
	hashes, _ := ev["secret_sha256_prefix"].(map[string]string)
	if len(hashes) != 4 {
		t.Fatalf("want four hashes, got %v", ev["secret_sha256_prefix"])
	}
	b, _ := json.Marshal(ev)
	for _, cm := range differRealms(differSalt)[:2] {
		for _, v := range cm["data"].(obj) {
			var realm kube.Object
			_ = json.Unmarshal([]byte(v.(string)), &realm)
			for _, idp := range realm.Objects("identityProviders") {
				if s := idp.Str("config", "clientSecret"); strings.Contains(string(b), s) {
					t.Errorf("the evidence holds the secret of %s", idp.Str("alias"))
				}
			}
		}
	}
}

// Each kind of unsalted secret fails the step and is named.
func TestSecDifferFailsEachUnsaltedForm(t *testing.T) {
	for _, c := range []struct{ what, where string }{
		{"tier1-sso|grafana", "client:grafana"},
		{"catalyst-api-server-sa", "client:catalyst-api-server"},
		{"catalyst-pin-broker", "idp:catalyst-pin"},
		{"tenant-broker|acme", "idp:sovereign"},
	} {
		t.Run(c.what, func(t *testing.T) {
			realms := differRealms(differSalt)
			text := realms[0]["data"].(obj)["sovereign-realm.json"].(string)
			key, idx := "sovereign-realm.json", 0
			if strings.HasPrefix(c.what, "tenant-broker") {
				key, idx = "org-realm.json", 1
				text = realms[1]["data"].(obj)[key].(string)
			}
			text = strings.ReplaceAll(text, differSeed("keycloak|keycloak|"+c.what, differSalt), differSeed("keycloak|keycloak|"+c.what, ""))
			realms[idx]["data"] = obj{key: text}
			r := runDiffer(t, differFake{regions: map[string][]obj{"region-a": realms}})
			f := failedSteps(r)
			if len(f) != 1 || !strings.Contains(f[0], c.where+" is the SHA-256 of keycloak|keycloak|"+c.what) {
				t.Fatalf("want one failure naming %s, got %v", c.where, f)
			}
		})
	}
}

func TestSecDifferFailsWithoutMeasurement(t *testing.T) {
	cases := map[string]struct {
		f    differFake
		want string
	}{
		"no region holds a realm import": {differFake{}, "nothing was measured"},
		"the imports hold no secret": {differFake{regions: map[string][]obj{"region-a": {
			{"metadata": obj{"name": "keycloak-sovereign-realm-config"}, "data": obj{"sovereign-realm.json": `{"realm":"sovereign","clients":[{"clientId":"catalyst-ui"}]}`}}}}}, "hold no client secret"},
		"every secret is a placeholder": {differFake{regions: map[string][]obj{"region-a": {
			{"metadata": obj{"name": "keycloak-sovereign-realm-config"}, "data": obj{"sovereign-realm.json": `{"realm":"sovereign","clients":[{"clientId":"grafana","secret":"$(env:GRAFANA_SECRET)"}]}`}}}}}, "placeholder"},
		"no import of the realm sovereign": {differFake{regions: map[string][]obj{"region-a": differRealms(differSalt)[1:]}}, "release name is unknown"},
		"kubectl fails":                    {differFake{regions: map[string][]obj{"region-a": differRealms(differSalt)}, fail: map[string]bool{"region-b": true}}, "the server is unreachable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := runDiffer(t, c.f)
			f := failedSteps(r)
			if len(f) != 1 || !strings.Contains(f[0], c.want) {
				t.Fatalf("want one failure with %q, got %v", c.want, f)
			}
		})
	}
}

// TestSecDifferRealChart renders the realm imports of bp-keycloak with helm,
// as slot 09 installs it, and gives them to the check: without the salt the
// check names the derived secrets, with a salt it passes. It proves that the
// seed forms of the check are those of the chart.
func TestSecDifferRealChart(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not on PATH")
	}
	// A copy of the chart, so that fetching its dependency writes nothing into the checkout.
	chart := filepath.Join(t.TempDir(), "chart")
	if err := os.CopyFS(chart, os.DirFS("../../../../../platform/keycloak/chart")); err != nil {
		t.Fatal(err)
	}
	if vendored, _ := filepath.Glob(filepath.Join(chart, "charts", "*.tgz")); len(vendored) == 0 {
		if out, err := exec.Command(helm, "dependency", "build", chart).CombinedOutput(); err != nil { //nolint:gosec // G204: the helm of PATH, with arguments of the test
			t.Skipf("the dependency of bp-keycloak cannot be fetched: %v\n%s", err, out)
		}
	}
	render := func(salt string) []kube.Object {
		args := []string{"template", "keycloak", chart, "--namespace", "keycloak",
			"--set", "sovereignFQDN=hw00.omani.works", "--set", "tenantRealms[0].slug=acme",
			"--set", "keycloak.postgresql.enabled=false", "--set", "keycloak.externalDatabase.host=pg.test",
			"--set", "keycloak.externalDatabase.existingSecret=keycloak-database-secret",
			"--set", "sso.tier1ClientSecretSalt=" + salt,
			"--show-only", "templates/configmap-sovereign-realm.yaml", "--show-only", "templates/configmap-per-org-realms.yaml"}
		out, err := exec.Command(helm, args...).CombinedOutput() //nolint:gosec // G204: the helm of PATH, with arguments of the test
		if err != nil {
			t.Fatalf("helm template: %v\n%s", err, out)
		}
		return differConfigMaps(t, string(out))
	}
	unsalted := collectRealmImports(render(""))
	ev := Evidence{}
	err = judgeRealmSecrets(unsalted, ev)
	if err == nil {
		t.Fatal("the check passes the realm imports of a render without the salt")
	}
	for _, want := range []string{"client:grafana is the SHA-256 of keycloak|keycloak|tier1-sso|grafana", "client:catalyst-api-server is the SHA-256 of keycloak|keycloak|catalyst-api-server-sa",
		"idp:catalyst-pin is the SHA-256 of keycloak|keycloak|catalyst-pin-broker", "is the SHA-256 of keycloak|keycloak|tenant-broker|acme", "is the SHA-256 of keycloak|keycloak|tier2-sso|guacamole"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("without the salt the check does not say %q: %v", want, err)
		}
	}
	if n := strings.Count(err.Error(), " is the SHA-256 of "); n != len(unsalted.secrets) {
		t.Errorf("without the salt %d of %d secrets are named derived", n, len(unsalted.secrets))
	}
	salted := collectRealmImports(render(differSalt))
	if err := judgeRealmSecrets(salted, Evidence{}); err != nil {
		t.Fatalf("the check fails the realm imports of a render with a salt: %v", err)
	}
	if len(salted.secrets) < 10 {
		t.Errorf("the render holds %d client secrets, want at least the eight clients, the pin broker and one Organization broker", len(salted.secrets))
	}
}

// differConfigMaps reads the ConfigMaps of a helm render: for each document,
// its name, its labels and each data value written as a literal block.
func differConfigMaps(t *testing.T, render string) []kube.Object {
	t.Helper()
	out := []kube.Object{}
	for _, doc := range strings.Split(render, "\n---\n") {
		lines := strings.Split(doc, "\n")
		meta, labels, data := obj{}, obj{}, obj{}
		section, key, block := "", "", []string{}
		flush := func() {
			if key != "" {
				data[key] = strings.Join(block, "\n")
			}
			key, block = "", nil
		}
		for _, l := range lines {
			switch {
			case key != "" && (strings.HasPrefix(l, "    ") || l == ""):
				block = append(block, strings.TrimPrefix(l, "    "))
				continue
			case key != "":
				flush()
			}
			switch {
			case l == "metadata:" || l == "data:":
				section = strings.TrimSuffix(l, ":")
			case section == "metadata" && strings.HasPrefix(l, "  name: "):
				meta["name"] = strings.TrimPrefix(l, "  name: ")
			case section == "metadata" && strings.HasPrefix(l, "    catalyst.openova.io/org-slug: "):
				labels["catalyst.openova.io/org-slug"] = strings.Trim(strings.TrimPrefix(l, "    catalyst.openova.io/org-slug: "), `"`)
			case section == "data" && strings.HasPrefix(l, "  ") && strings.HasSuffix(l, ": |"):
				key = strings.TrimSuffix(strings.TrimPrefix(l, "  "), ": |")
			case !strings.HasPrefix(l, " "):
				section = ""
			}
		}
		flush()
		if strings.Contains(doc, "\nkind: ConfigMap\n") && len(data) > 0 {
			meta["labels"] = labels
			out = append(out, kube.Object{"metadata": meta, "data": data})
		}
	}
	if len(out) == 0 {
		t.Fatalf("the render holds no ConfigMap:\n%s", render)
	}
	return out
}
