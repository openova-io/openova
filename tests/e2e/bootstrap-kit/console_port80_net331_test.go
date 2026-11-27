package bootstrapkit

// NET-331 (gate check NET-G17, capability gateway.cilium of the cluster class
// reference-k3s-cilium): GET http://console.<fqdn>/ on port 80 of a gateway node
// answers 200 or a redirect. Measured on the standing two-region Sovereign on
// 27 November 2026: it answered 404 in both regions. Where the Gateways bind host
// ports, node:80 is the port-80 listener of the one Gateway that declares port
// 80, the shared cilium-gateway; the console Gateway's HTTP listener sits on the
// pack's console port (8080) and no route of the shared Gateway held the console
// host.
//
// The test renders what the Sovereign runs: the ConfigMap sovereign-tls-vars
// (platform/sovereign-tls-vars/chart, Helm) with the console ports of every pack
// that binds host ports, then every resource of the Kustomization
// clusters/_template/sovereign-tls substituted as Flux's post-build step does.
// It resolves the answer to GET / for a host on a node port the way Cilium's
// Gateway API attaches routes (operator/pkg/model/ingestion/gateway.go of Cilium
// 1.19.3: sectionName and port of each parentRef) and Gateway API admits them
// (listener hostname, allowedRoutes namespaces), and it holds the answer to
// its value: "301 https://console.<fqdn>/" on port 80, and no redirect to itself
// on port 443.

import (
	"errors"
	"fmt"
	"io"
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

const n331FQDN = "t99.omani.works"

// n331Pack is a pack whose Gateways bind host ports, with its console ports.
type n331Pack struct{ file, httpPort, httpsPort string }

var n331Key = regexp.MustCompile(`^\s*(CILIUM_GATEWAY_HOSTNETWORK_ENABLED|CONSOLE_GATEWAY_HTTP_PORT|CONSOLE_GATEWAY_HTTPS_PORT)\s*[:=]\s*"([^"]*)"`)

// n331Packs reads the substitutes of every provider pack and keeps those that
// set CILIUM_GATEWAY_HOSTNETWORK_ENABLED "true"; each must set both console
// ports. None found is a failure, not an empty pass.
func n331Packs(t *testing.T, root string) []n331Pack {
	t.Helper()
	var files []string
	for _, g := range []string{"infra/providers/*/provider-substitutes.yaml.tftpl", "infra/providers/*/main.tf"} {
		m, err := filepath.Glob(filepath.Join(root, g))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	sort.Strings(files)
	var packs []n331Pack
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // a file of this repository, found by the glob above
		if err != nil {
			t.Fatal(err)
		}
		kv := map[string]string{}
		for _, line := range strings.Split(string(raw), "\n") {
			if m := n331Key.FindStringSubmatch(line); m != nil {
				kv[m[1]] = m[2]
			}
		}
		if kv["CILIUM_GATEWAY_HOSTNETWORK_ENABLED"] != "true" {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		if kv["CONSOLE_GATEWAY_HTTP_PORT"] == "" || kv["CONSOLE_GATEWAY_HTTPS_PORT"] == "" {
			t.Fatalf("%s binds the Gateways on host ports but does not set both console ports: %v", rel, kv)
		}
		packs = append(packs, n331Pack{rel, kv["CONSOLE_GATEWAY_HTTP_PORT"], kv["CONSOLE_GATEWAY_HTTPS_PORT"]})
	}
	if len(packs) == 0 {
		t.Fatal("no provider pack sets CILIUM_GATEWAY_HOSTNETWORK_ENABLED \"true\": nothing to measure")
	}
	return packs
}

// n331Vars renders the ConfigMap sovereign-tls-vars for the zones and the pack
// and returns the Flux variables the sovereign-tls Kustomization reads.
func n331Vars(t *testing.T, root string, zones []map[string]string, p n331Pack) map[string]string {
	t.Helper()
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Fatalf("helm not on PATH (%v): the listeners cannot be rendered", err)
	}
	src := filepath.Join(root, "platform", "sovereign-tls-vars", "chart")
	dir := t.TempDir()
	read := map[string][]byte{}
	for _, name := range []string{"Chart.yaml", "values.yaml", "templates/configmap.yaml"} {
		b, err := os.ReadFile(filepath.Join(src, name)) //nolint:gosec // a file of the chart, named by the test
		if err != nil {
			t.Fatal(err)
		}
		read[name] = b
	}
	var chart map[string]any
	if err := yaml.Unmarshal(read["Chart.yaml"], &chart); err != nil {
		t.Fatal(err)
	}
	delete(chart, "dependencies") // the library of the test hook; configmap.yaml does not use it
	outChart, _ := yaml.Marshal(chart)
	httpPort, _ := strconv.Atoi(p.httpPort)
	httpsPort, _ := strconv.Atoi(p.httpsPort)
	over, _ := yaml.Marshal(map[string]any{
		"global":         map[string]any{"sovereignFQDN": n331FQDN},
		"parentZones":    zones,
		"consoleGateway": map[string]any{"httpPort": httpPort, "httpsPort": httpsPort},
	})
	for name, b := range map[string][]byte{"Chart.yaml": outChart, "values.yaml": read["values.yaml"], "over.yaml": over,
		"templates/configmap.yaml": read["templates/configmap.yaml"]} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	docs := cbdRender(t, helmBin, dir, "tls-vars", ".", "-f", "over.yaml")
	cm := cbFind(docs, "ConfigMap", "flux-system", "sovereign-tls-vars")
	if cm == nil {
		t.Fatal("the chart rendered no ConfigMap flux-system/sovereign-tls-vars")
	}
	vars := map[string]string{"SOVEREIGN_FQDN": n331FQDN, "SOVEREIGN_FQDN_DASHED": strings.ReplaceAll(n331FQDN, ".", "-")}
	for _, k := range []string{"PARENT_DOMAINS_LISTENERS_YAML", "CONSOLE_LISTENERS_YAML"} {
		v, _ := dig(cm, "data", k).(string)
		if v == "" {
			t.Fatalf("the ConfigMap has no %s", k)
		}
		vars[k] = v
	}
	return vars
}

// n331Tree is every document of the Kustomization sovereign-tls after the
// Flux substitution with vars. The Kustomization must list resources only: a
// patch would change what this reading sees.
func n331Tree(t *testing.T, root string, vars map[string]string) []map[string]any {
	t.Helper()
	dir := filepath.Join(root, "clusters", "_template", "sovereign-tls")
	k := readYAMLDocs(t, filepath.Join(dir, "kustomization.yaml"))
	if len(k) != 1 {
		t.Fatalf("kustomization.yaml holds %d documents, want 1", len(k))
	}
	for key := range k[0] {
		if key != "apiVersion" && key != "kind" && key != "resources" {
			t.Fatalf("kustomization.yaml has %q: this test reads the resources as they are", key)
		}
	}
	var docs []map[string]any
	for _, r := range list(k[0]["resources"]) {
		name, _ := r.(string)
		raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a resource the Kustomization lists
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(strings.NewReader(cbdSubstitute(string(raw), vars)))
		for {
			var d map[string]any
			if err := dec.Decode(&d); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("%s after substitution: %v", name, err)
			}
			if d != nil {
				docs = append(docs, d)
			}
		}
	}
	return docs
}

type n331Listener struct {
	gwNS, gw, name, protocol, hostname string
	port                               int
	allowed                            map[string]any
}

func n331Int(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return -1
}

func n331Str(v any) string { s, _ := v.(string); return s }

// n331HostMatch is Gateway API hostname matching: an exact name, or a wildcard
// *.<suffix> that the host ends in; an empty pattern matches every host.
func n331HostMatch(pattern, host string) bool {
	if pattern == "" || pattern == host {
		return true
	}
	return strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:])
}

// n331Admits holds allowedRoutes.namespaces of a listener for a route in ns,
// which carries the label kubernetes.io/metadata.name=<ns> only.
func n331Admits(t *testing.T, l n331Listener, ns string) bool {
	t.Helper()
	switch from := n331Str(dig(l.allowed, "namespaces", "from")); from {
	case "", "Same":
		return ns == l.gwNS
	case "All":
		return true
	case "Selector":
		labels := map[string]string{"kubernetes.io/metadata.name": ns}
		sel, _ := dig(l.allowed, "namespaces", "selector").(map[string]any)
		ml, _ := sel["matchLabels"].(map[string]any)
		for k, v := range ml {
			if labels[k] != n331Str(v) {
				return false
			}
		}
		for _, e := range list(sel["matchExpressions"]) {
			key, op := n331Str(dig(e, "key")), n331Str(dig(e, "operator"))
			val, has := labels[key]
			in := false
			for _, v := range list(dig(e, "values")) {
				in = in || (has && n331Str(v) == val)
			}
			switch op {
			case "In":
				if !in {
					return false
				}
			case "NotIn":
				if in {
					return false
				}
			case "Exists":
				if !has {
					return false
				}
			case "DoesNotExist":
				if has {
					return false
				}
			default:
				t.Fatalf("listener %s: selector operator %q", l.name, op)
			}
		}
		return true
	default:
		t.Fatalf("listener %s: allowedRoutes from %q", l.name, from)
	}
	return false
}

// n331Answer resolves GET / for host on a node port: the most specific
// listener of that port and protocol that holds the host, then the rules of the
// routes attached to it. Every Gateway listener on the port must belong to one
// Gateway: a host port is bound once.
func n331Answer(t *testing.T, docs []map[string]any, host string, port int, protocol string) string {
	t.Helper()
	var on []n331Listener
	gws := map[string]bool{}
	for _, d := range docs {
		if d["kind"] != "Gateway" {
			continue
		}
		for _, l := range list(dig(d, "spec", "listeners")) {
			if n331Int(dig(l, "port")) != port {
				continue
			}
			al, _ := dig(l, "allowedRoutes").(map[string]any)
			li := n331Listener{n331Str(dig(d, "metadata", "namespace")), n331Str(dig(d, "metadata", "name")), n331Str(dig(l, "name")),
				n331Str(dig(l, "protocol")), n331Str(dig(l, "hostname")), port, al}
			gws[li.gwNS+"/"+li.gw] = true
			if li.protocol == protocol && n331HostMatch(li.hostname, host) {
				on = append(on, li)
			}
		}
	}
	if len(gws) > 1 {
		t.Fatalf("Gateways %v all declare port %d: one host port is bound by one Gateway", gws, port)
	}
	if len(on) == 0 {
		return fmt.Sprintf("404 (no %s listener on port %d holds %s)", protocol, port, host)
	}
	// Envoy picks the most specific virtual host: an exact name, else the longest wildcard.
	sort.SliceStable(on, func(i, j int) bool {
		ei, ej := !strings.HasPrefix(on[i].hostname, "*"), !strings.HasPrefix(on[j].hostname, "*")
		if ei != ej {
			return ei
		}
		return len(on[i].hostname) > len(on[j].hostname)
	})
	l := on[0]
	answers := map[string]bool{}
	for _, r := range docs {
		if r["kind"] != "HTTPRoute" {
			continue
		}
		ns := n331Str(dig(r, "metadata", "namespace"))
		if !n331Attached(r, ns, l) || !n331Admits(t, l, ns) {
			continue
		}
		hosts := list(dig(r, "spec", "hostnames"))
		held := len(hosts) == 0
		for _, h := range hosts {
			held = held || n331HostMatch(n331Str(h), host)
		}
		if !held {
			continue
		}
		if a := n331Rule(r, l, host); a != "" {
			answers[a] = true
		}
	}
	if len(answers) == 0 {
		return fmt.Sprintf("404 (no route of Gateway %s/%s, listener %s, port %d, holds %s)", l.gwNS, l.gw, l.name, port, host)
	}
	if len(answers) > 1 {
		t.Fatalf("routes on listener %s give %s different answers %v: Gateway API orders them by creation time, which no render has", l.name, host, answers)
	}
	for a := range answers {
		return a
	}
	return ""
}

// n331Attached is the parentRef rule of Cilium's ingestion: neither sectionName
// nor port attaches to every listener; a sectionName must name the listener and
// a port, when set, must be its port.
func n331Attached(r map[string]any, ns string, l n331Listener) bool {
	for _, pr := range list(dig(r, "spec", "parentRefs")) {
		group, kind, pns := n331Str(dig(pr, "group")), n331Str(dig(pr, "kind")), n331Str(dig(pr, "namespace"))
		if group == "" {
			group = "gateway.networking.k8s.io"
		}
		if kind == "" {
			kind = "Gateway"
		}
		if pns == "" {
			pns = ns
		}
		if group != "gateway.networking.k8s.io" || kind != "Gateway" || pns != l.gwNS || n331Str(dig(pr, "name")) != l.gw {
			continue
		}
		section, port := n331Str(dig(pr, "sectionName")), dig(pr, "port")
		if section != "" && section != l.name {
			continue
		}
		if port != nil && n331Int(port) != l.port {
			continue
		}
		return true
	}
	return false
}

// n331Rule is the answer of the first rule of r that GET / matches: a redirect
// as "<code> <Location>" (Gateway API: code 302 when unset; the port of the
// scheme when a scheme is set, else the listener's), or "200 (backend ...)".
func n331Rule(r map[string]any, l n331Listener, host string) string {
	for _, rule := range list(dig(r, "spec", "rules")) {
		matches := list(dig(rule, "matches"))
		hit := len(matches) == 0
		for _, m := range matches {
			path, method := dig(m, "path"), n331Str(dig(m, "method"))
			pt, pv := n331Str(dig(path, "type")), n331Str(dig(path, "value"))
			hit = hit || ((path == nil || ((pt == "" || pt == "PathPrefix") && (pv == "" || pv == "/"))) &&
				dig(m, "headers") == nil && dig(m, "queryParams") == nil && (method == "" || method == "GET"))
		}
		if !hit {
			continue
		}
		for _, f := range list(dig(rule, "filters")) {
			if n331Str(dig(f, "type")) != "RequestRedirect" {
				continue
			}
			rr := dig(f, "requestRedirect")
			code := 302
			if c := dig(rr, "statusCode"); c != nil {
				code = n331Int(c)
			}
			scheme, to := n331Str(dig(rr, "scheme")), n331Str(dig(rr, "hostname"))
			port := l.port
			if p := dig(rr, "port"); p != nil {
				port = n331Int(p)
			} else if scheme == "https" {
				port = 443
			} else if scheme == "http" {
				port = 80
			}
			if scheme == "" {
				scheme = map[string]string{"HTTPS": "https"}[l.protocol]
				if scheme == "" {
					scheme = "http"
				}
			}
			if to == "" {
				to = host
			}
			if (scheme == "https" && port != 443) || (scheme == "http" && port != 80) {
				to += ":" + strconv.Itoa(port)
			}
			path := "/"
			if v := n331Str(dig(rr, "path", "replaceFullPath")) + n331Str(dig(rr, "path", "replacePrefixMatch")); v != "" {
				path = v
			}
			return fmt.Sprintf("%d %s://%s%s", code, scheme, to, path)
		}
		if refs := list(dig(rule, "backendRefs")); len(refs) > 0 {
			return fmt.Sprintf("200 (backend %v)", dig(refs[0], "name"))
		}
		return "500 (a rule with neither a redirect nor a backend)"
	}
	return ""
}

func TestSovereignTLS_ConsoleHostAnswersOnNodePort80(t *testing.T) {
	root := repoRoot(t)
	host := "console." + n331FQDN
	want := "301 https://" + host + "/"
	layouts := []struct {
		name  string
		zones []map[string]string
	}{
		{"one zone, the Sovereign's own (listener http)", []map[string]string{{"name": n331FQDN, "role": "primary"}}},
		{"the Sovereign's zone and an Organization pool (listener http-<fqdn-dashed>)",
			[]map[string]string{{"name": n331FQDN, "role": "primary"}, {"name": "omani.homes", "role": "org-pool"}}},
		{"a Sovereign under a parent zone (listeners http and http-<fqdn-dashed>)", []map[string]string{{"name": "omani.works", "role": "primary"}}},
	}
	for _, p := range n331Packs(t, root) {
		for _, lay := range layouts {
			t.Run(p.file+": "+lay.name, func(t *testing.T) {
				docs := n331Tree(t, root, n331Vars(t, root, lay.zones, p))
				if got := n331Answer(t, docs, host, 80, "HTTP"); got != want {
					t.Errorf("GET http://%s/ on node port 80 answers %s, want %s", host, got, want)
				}
				// node:443 must not redirect the console host to itself: that is a loop.
				if got := n331Answer(t, docs, host, 443, "HTTPS"); strings.HasSuffix(got, " https://"+host+"/") {
					t.Errorf("GET https://%s/ on node port 443 answers %s: a redirect to itself", host, got)
				}
			})
		}
	}
}
