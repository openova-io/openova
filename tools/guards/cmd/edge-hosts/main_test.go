package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// route renders one HTTPRoute line of a render file.
func route(ns, name, gateway string, hosts ...string) string {
	q := make([]string, 0, len(hosts))
	for _, h := range hosts {
		q = append(q, `"`+h+`"`)
	}
	meta := `"name":"` + name + `"`
	if ns != "" {
		meta += `,"namespace":"` + ns + `"`
	}
	return `{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{` + meta + `},"spec":{"parentRefs":[{"name":"` +
		gateway + `","namespace":"kube-system"}],"hostnames":[` + strings.Join(q, ",") + `]}}`
}

const listeners = `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"sovereign-tls-vars","namespace":"flux-system"},"data":{` +
	`"CONSOLE_LISTENERS_YAML":"[{\"hostname\":\"console.sov.example.test\",\"name\":\"console-https\",\"port\":8443,\"protocol\":\"HTTPS\"}]",` +
	`"PARENT_DOMAINS_LISTENERS_YAML":"[{\"hostname\":\"*.example.test\",\"name\":\"https\",\"port\":443,\"protocol\":\"HTTPS\"}]",` +
	`"OTHER":"not a listener value"}}`

// The primary's edge as the kit renders it: the umbrella's console hosts on
// the console Gateway, a component's host on the shared Gateway, the listeners.
func primaryRender() map[string]string {
	return map[string]string{
		"1.bp-keycloak.keycloak.jsonl": route("keycloak", "keycloak", "cilium-gateway", "auth.sov.example.test") + "\nnull\n",
		"2.bp-catalyst-platform.catalyst-system.jsonl": route("catalyst-system", "catalyst-ui", "cilium-gateway-console", "console.sov.example.test") + "\n" +
			route("catalyst-system", "catalyst-api", "cilium-gateway-console", "api.sov.example.test") + "\n" +
			`{"apiVersion":"v1","kind":"Service","metadata":{"name":"catalyst-api"}}` + "\n",
		"3.bp-sovereign-tls-vars.flux-system.jsonl": listeners + "\n",
	}
}

// The secondary's edge: the component runs there too, the umbrella is
// suspended and the secondary edge routes its hosts to the primary.
func secondaryRender() map[string]string {
	return map[string]string{
		"1.bp-keycloak.keycloak.jsonl": route("keycloak", "keycloak", "cilium-gateway", "auth.sov.example.test") + "\n",
		"2.bp-catalyst-edge-routes.catalyst-system.jsonl": route("catalyst-system", "catalyst-ui", "cilium-gateway-console", "console.sov.example.test") + "\n" +
			route("catalyst-system", "catalyst-api", "cilium-gateway-console", "api.sov.example.test") + "\n",
		"3.bp-sovereign-tls-vars.flux-system.jsonl": listeners + "\n",
	}
}

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runOn(t *testing.T, primary, secondary map[string]string) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run([]string{"-primary", write(t, primary), "-secondary", write(t, secondary)}, &out, &errw)
	return code, out.String() + errw.String()
}

func TestTheSecondaryEdgeServingEveryHostPasses(t *testing.T) {
	code, out := runOn(t, primaryRender(), secondaryRender())
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "every one of the 3 hostnames") {
		t.Fatalf("the pass does not say how many hostnames it compared:\n%s", out)
	}
}

// The measured defect: an application host the primary routes and the
// secondary does not (6 of 12 requests answered 404 on 21 Nov 2026).
func TestAHostRoutedOnlyOnThePrimaryFails(t *testing.T) {
	p := primaryRender()
	p["4.bp-bookstack.acme.jsonl"] = route("", "app-bookstack-hostroute", "cilium-gateway-console", "bookstack.acme.example.test") + "\n"
	code, out := runOn(t, p, secondaryRender())
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"FAIL route: bookstack.acme.example.test on Gateway kube-system/cilium-gateway-console",
		"bp-bookstack HTTPRoute acme/app-bookstack-hostroute", // the namespace of the release, from the file name
		edgeSlot, edgeChart,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the finding lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "FAIL ") != 1 {
		t.Errorf("want exactly one finding:\n%s", out)
	}
}

// The umbrella's host when the secondary edge stops rendering it (slot 13e
// suspended, or a host added to the umbrella and not to the edge chart).
func TestAnUmbrellaHostTheEdgeChartLacksFails(t *testing.T) {
	s := secondaryRender()
	s["2.bp-catalyst-edge-routes.catalyst-system.jsonl"] = route("catalyst-system", "catalyst-ui", "cilium-gateway-console", "console.sov.example.test") + "\n"
	code, out := runOn(t, primaryRender(), s)
	if code != 1 || !strings.Contains(out, "FAIL route: api.sov.example.test on Gateway kube-system/cilium-gateway-console is routed on the primary by bp-catalyst-platform HTTPRoute catalyst-system/catalyst-api") {
		t.Fatalf("exit %d, want 1 naming api.sov.example.test:\n%s", code, out)
	}
}

// A host the secondary routes on another Gateway does not serve the requests
// that enter through the console balancer.
func TestTheSameHostOnAnotherGatewayFails(t *testing.T) {
	s := secondaryRender()
	s["2.bp-catalyst-edge-routes.catalyst-system.jsonl"] = route("catalyst-system", "catalyst-ui", "cilium-gateway", "console.sov.example.test") + "\n" +
		route("catalyst-system", "catalyst-api", "cilium-gateway-console", "api.sov.example.test") + "\n"
	code, out := runOn(t, primaryRender(), s)
	if code != 1 || !strings.Contains(out, "FAIL route: console.sov.example.test on Gateway kube-system/cilium-gateway-console") {
		t.Fatalf("exit %d, want 1 naming console.sov.example.test:\n%s", code, out)
	}
}

func TestAWildcardOfTheSecondaryCoversTheHost(t *testing.T) {
	s := secondaryRender()
	s["2.bp-catalyst-edge-routes.catalyst-system.jsonl"] = route("catalyst-system", "edge", "cilium-gateway-console", "*.sov.example.test") + "\n"
	if code, out := runOn(t, primaryRender(), s); code != 0 {
		t.Fatalf("exit %d, want 0 (*.sov.example.test covers console. and api.):\n%s", code, out)
	}
	s["2.bp-catalyst-edge-routes.catalyst-system.jsonl"] = route("catalyst-system", "edge", "cilium-gateway-console", "*.other.example.test") + "\n"
	if code, out := runOn(t, primaryRender(), s); code != 1 {
		t.Fatalf("exit %d, want 1 (*.other.example.test covers neither):\n%s", code, out)
	}
}

func TestAListenerMissingOnTheSecondaryFails(t *testing.T) {
	s := secondaryRender()
	s["3.bp-sovereign-tls-vars.flux-system.jsonl"] = strings.Replace(listeners, `console.sov.example.test\",\"name\":\"console-https\",\"port\":8443`, `console.sov.example.test\",\"name\":\"console-https\",\"port\":9443`, 1) + "\n"
	if s["3.bp-sovereign-tls-vars.flux-system.jsonl"] == listeners+"\n" {
		t.Fatal("the mutant changed nothing")
	}
	code, out := runOn(t, primaryRender(), s)
	if code != 1 || !strings.Contains(out, "FAIL listener: console.sov.example.test:8443 of ConfigMap flux-system/sovereign-tls-vars CONSOLE_LISTENERS_YAML") {
		t.Fatalf("exit %d, want 1 naming the listener:\n%s", code, out)
	}
	p := primaryRender()
	p["5.bp-gateway-api.kube-system.jsonl"] = `{"kind":"Gateway","metadata":{"name":"extra"},"spec":{"listeners":[{"name":"https","hostname":"x.sov.example.test","port":443}]}}` + "\n"
	code, out = runOn(t, p, secondaryRender())
	if code != 1 || !strings.Contains(out, "FAIL listener: x.sov.example.test:443 of Gateway kube-system/extra") {
		t.Fatalf("exit %d, want 1 naming the Gateway listener:\n%s", code, out)
	}
}

// A parent that is not a Gateway (a Service of the mesh) and a route without
// hostnames name no public hostname.
func TestOnlyGatewayParentsWithHostnamesCount(t *testing.T) {
	p := primaryRender()
	p["6.bp-x.x.jsonl"] = `{"kind":"HTTPRoute","metadata":{"name":"mesh"},"spec":{"parentRefs":[{"kind":"Service","name":"svc"}],"hostnames":["mesh.sov.example.test"]}}` + "\n" +
		`{"kind":"HTTPRoute","metadata":{"name":"bare"},"spec":{"parentRefs":[{"name":"cilium-gateway","namespace":"kube-system"}]}}` + "\n"
	if code, out := runOn(t, p, secondaryRender()); code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

func TestNothingToJudgeIsNotAPass(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"-primary", t.TempDir(), "-secondary", write(t, secondaryRender())}, &out, &errw); code != 2 {
		t.Errorf("an empty primary render: exit %d, want 2", code)
	}
	if code := run([]string{"-primary", write(t, primaryRender())}, &out, &errw); code != 2 {
		t.Errorf("no secondary: exit %d, want 2", code)
	}
	noRoute := map[string]string{"1.bp-x.x.jsonl": `{"kind":"ConfigMap","metadata":{"name":"c"},"data":{}}` + "\n"}
	if code, msg := runOn(t, noRoute, secondaryRender()); code != 2 || !strings.Contains(msg, "holds no route with a hostname") {
		t.Errorf("a primary render without a route: exit %d, want 2:\n%s", code, msg)
	}
	bad := primaryRender()
	bad["1.bp-keycloak.keycloak.jsonl"] = "{not json\n"
	if code, msg := runOn(t, bad, secondaryRender()); code != 2 || !strings.Contains(msg, "not a JSON object") {
		t.Errorf("a line that is not JSON: exit %d, want 2:\n%s", code, msg)
	}
	badListeners := secondaryRender()
	badListeners["3.bp-sovereign-tls-vars.flux-system.jsonl"] = `{"kind":"ConfigMap","metadata":{"name":"v","namespace":"flux-system"},"data":{"CONSOLE_LISTENERS_YAML":"- name: x"}}` + "\n"
	if code, msg := runOn(t, primaryRender(), badListeners); code != 2 || !strings.Contains(msg, "not a JSON list of listeners") {
		t.Errorf("a listener value that is not JSON: exit %d, want 2:\n%s", code, msg)
	}
	badName := map[string]string{"routes.jsonl": route("a", "b", "g", "h.example.test") + "\n"}
	if code, msg := runOn(t, badName, secondaryRender()); code != 2 || !strings.Contains(msg, "<n>.<chart>.<namespace>.jsonl") {
		t.Errorf("a file not named by the render: exit %d, want 2:\n%s", code, msg)
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		have, host string
		want       bool
	}{
		{"a.example.test", "a.example.test", true},
		{"*.example.test", "a.example.test", true},
		{"*.example.test", "a.b.example.test", true},
		{"*.example.test", "example.test", false},
		{"*.example.test", "aexample.test", false},
		{"a.example.test", "b.example.test", false},
		{"a.example.test", "*.example.test", false},
	} {
		if got := covers(c.have, c.host); got != c.want {
			t.Errorf("covers(%q, %q) = %v, want %v", c.have, c.host, got, c.want)
		}
	}
}
