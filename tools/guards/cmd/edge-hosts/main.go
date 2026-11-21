// Command edge-hosts holds that the secondary region's edge routes every public
// hostname the primary region's edge routes, as the kit renders them.
//
// The reference cloud's gateway and console load balancers admit both
// regions' entry points at the same weight (CLD-G12: the provider cannot send
// a weight of 0), so about half of the requests to any public hostname enter
// through the gateway of the secondary region. Which region serves them is
// decided inside the platform: on a secondary control plane the umbrella (kit
// slot 13) is suspended, and the secondary edge (slot 13e,
// clusters/_template/bootstrap-kit/13e-bp-catalyst-secondary-edge-routes.yaml,
// chart platform/catalyst-edge-routes/chart) routes the hosts the umbrella
// serves to the primary's backends over the cluster mesh. A hostname that the
// primary's edge routes and the secondary's edge does not answers 404 for the
// requests that enter in the secondary region (#6827: 7 of 12 before the fix
// of that host; 6 of 12 for the one such host on a two-region install of 21
// Nov 2026).
//
// Input: two renders of the kit, each written by
//
//	KIT_VALUE_SETS=<set> KIT_RENDER_DIR=<dir> bash scripts/check-freshprov-image-registry.sh --list-images
//
// -primary with the value set of a primary control plane
// (tests/fixtures/variables/today.env), -secondary with the set of the
// secondary control plane of a two-region install
// (today.env+two-region-secondary.env). A render holds one file
// <n>.<chart>.<namespace>.jsonl a release, one JSON object a line.
//
// In each render the guard reads
//   - every HTTPRoute, GRPCRoute and TLSRoute: each name of spec.hostnames on
//     each parent Gateway of spec.parentRefs (kind Gateway when unset; the
//     namespace of the parent defaults to the route's, and the route's to the
//     release's, the last part of the file name);
//   - every Gateway: the hostname and port of each listener;
//   - every key of a ConfigMap whose name ends in _LISTENERS_YAML (kit slot
//     12a renders them; the Gateways of clusters/_template/sovereign-tls take
//     them as their listeners): the hostname and port of each listener, the
//     value read as JSON.
//
// It fails (exit 1), one line a finding, on
//
//	route     a hostname routed on a Gateway in the primary render that no
//	          route of the secondary render routes on the same Gateway (the
//	          same name, or a wildcard *.<suffix> that the name ends in)
//	listener  a listener (hostname and port) of a Gateway or of a listener
//	          value of the primary that the Gateway or the value of the same
//	          name in the secondary render lacks
//
// A finding names the hostname, what renders it on the primary (chart, kind,
// namespace/name) and the files of the secondary edge that lack it. It stops
// (exit 2) when a render cannot be read, when the primary render holds no
// route with a hostname, or when a listener value is not a JSON list.
//
// Not covered, by design: a route without hostnames (it names no public
// hostname and is not compared); the routes that a controller writes at run
// time and no chart of the kit renders: the console of each Organization
// (core/controllers/organization tenant_route.go and the hand-over handler
// org_console_tls.go of catalyst-api) and the applications an Organization
// buys (the `app-<x>-hostroute` documents the provisioning service writes into
// the shared tree vcluster/host-apps of the Organization's repository, which
// only the Organization's home region reconciles:
// core/controllers/organization/internal/gitops/region_local.go).
//
// Usage, from tools/guards:
//
//	go run ./cmd/edge-hosts -primary <dir> -secondary <dir>
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The files of the secondary edge a finding names: what must route the host.
const (
	edgeSlot  = "clusters/_template/bootstrap-kit/13e-bp-catalyst-secondary-edge-routes.yaml"
	edgeChart = "platform/catalyst-edge-routes/chart"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// origin is what renders a hostname: the chart of the release and the object.
type origin struct {
	chart, kind, namespace, name string
}

func (o origin) String() string {
	return fmt.Sprintf("%s %s %s/%s", o.chart, o.kind, o.namespace, o.name)
}

// render is what one render of the kit routes and listens on.
type render struct {
	// routes: gateway "namespace/name" -> hostname -> what renders it.
	routes map[string]map[string][]origin
	// listeners: "Gateway namespace/name" or "ConfigMap namespace/name key" ->
	// "hostname:port" -> what renders it.
	listeners map[string]map[string][]origin
	charts    map[string]bool
}

func newRender() *render {
	return &render{routes: map[string]map[string][]origin{}, listeners: map[string]map[string][]origin{}, charts: map[string]bool{}}
}

func add(m map[string]map[string][]origin, key, item string, o origin) {
	if m[key] == nil {
		m[key] = map[string][]origin{}
	}
	m[key][item] = append(m[key][item], o)
}

type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Hostnames  []string `json:"hostnames"`
		ParentRefs []struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"parentRefs"`
		Listeners []listener `json:"listeners"`
	} `json:"spec"`
	Data map[string]string `json:"data"`
}

type listener struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
}

func (l listener) key() string {
	h := l.Hostname
	if h == "" {
		h = "(any hostname)"
	}
	return fmt.Sprintf("%s:%d", h, l.Port)
}

var routeKinds = map[string]bool{"HTTPRoute": true, "GRPCRoute": true, "TLSRoute": true}

// readRender reads every <n>.<chart>.<namespace>.jsonl of dir.
func readRender(dir string) (*render, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no release render (*.jsonl)", dir)
	}
	sort.Strings(files)
	r := newRender()
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		parts := strings.Split(base, ".")
		if len(parts) < 3 {
			return nil, fmt.Errorf("%s: the name is not <n>.<chart>.<namespace>.jsonl", f)
		}
		chart, releaseNS := strings.Join(parts[1:len(parts)-1], "."), parts[len(parts)-1]
		r.charts[chart] = true
		if err := readFile(f, chart, releaseNS, r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func readFile(f, chart, releaseNS string, r *render) error {
	fh, err := os.Open(f) // #nosec G304 -- the path is the guard's own input
	if err != nil {
		return err
	}
	defer func() { _ = fh.Close() }()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "null" {
			continue
		}
		var o object
		if err := json.Unmarshal([]byte(line), &o); err != nil {
			return fmt.Errorf("%s:%d: not a JSON object: %v", f, n, err)
		}
		ns := o.Metadata.Namespace
		if ns == "" {
			ns = releaseNS
		}
		at := origin{chart: chart, kind: o.Kind, namespace: ns, name: o.Metadata.Name}
		switch {
		case routeKinds[o.Kind]:
			for _, p := range o.Spec.ParentRefs {
				if p.Kind != "" && p.Kind != "Gateway" {
					continue
				}
				pns := p.Namespace
				if pns == "" {
					pns = ns
				}
				for _, h := range o.Spec.Hostnames {
					add(r.routes, pns+"/"+p.Name, strings.ToLower(h), at)
				}
			}
		case o.Kind == "Gateway":
			for _, l := range o.Spec.Listeners {
				add(r.listeners, "Gateway "+ns+"/"+o.Metadata.Name, l.key(), at)
			}
		case o.Kind == "ConfigMap":
			for k, v := range o.Data {
				if !strings.HasSuffix(k, "_LISTENERS_YAML") {
					continue
				}
				var ls []listener
				if err := json.Unmarshal([]byte(v), &ls); err != nil {
					return fmt.Errorf("%s:%d: ConfigMap %s/%s key %s is not a JSON list of listeners: %v", f, n, ns, o.Metadata.Name, k, err)
				}
				for _, l := range ls {
					add(r.listeners, "ConfigMap "+ns+"/"+o.Metadata.Name+" "+k, l.key(), at)
				}
			}
		}
	}
	return sc.Err()
}

// covers reports whether a route hostname of the secondary serves the
// hostname h: the same name, or a wildcard *.<suffix> that h ends in.
func covers(have, h string) bool {
	if have == h {
		return true
	}
	if strings.HasPrefix(have, "*.") {
		return strings.HasSuffix(h, have[1:])
	}
	return false
}

func routed(hosts map[string][]origin, h string) bool {
	for have := range hosts {
		if covers(have, h) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func names(list []origin) string {
	s := make([]string, 0, len(list))
	for _, o := range list {
		s = append(s, o.String())
	}
	sort.Strings(s)
	return strings.Join(s, ", ")
}

// judge returns the findings and the number of routed hostnames it compared.
func judge(primary, secondary *render) (findings []string, compared int) {
	edge := "secondary edge: " + edgeSlot + " and " + edgeChart
	for _, gw := range sortedKeys(primary.routes) {
		for _, h := range sortedKeys(primary.routes[gw]) {
			compared++
			if routed(secondary.routes[gw], h) {
				continue
			}
			findings = append(findings, fmt.Sprintf("route: %s on Gateway %s is routed on the primary by %s and on the secondary by nothing the kit renders: the %s lack it",
				h, gw, names(primary.routes[gw][h]), edge))
		}
	}
	for _, owner := range sortedKeys(primary.listeners) {
		for _, l := range sortedKeys(primary.listeners[owner]) {
			if _, ok := secondary.listeners[owner][l]; ok {
				continue
			}
			findings = append(findings, fmt.Sprintf("listener: %s of %s is rendered on the primary by %s and missing from the secondary render (%s)",
				l, owner, names(primary.listeners[owner][l]), edge))
		}
	}
	return findings, compared
}

var errUsage = errors.New("usage: edge-hosts -primary <dir> -secondary <dir>")

func run(args []string, out, errw io.Writer) int {
	fl := flag.NewFlagSet("edge-hosts", flag.ContinueOnError)
	fl.SetOutput(errw)
	p := fl.String("primary", "", "render of the kit with the value set of a primary control plane")
	s := fl.String("secondary", "", "render of the kit with the value set of a secondary control plane")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *p == "" || *s == "" || fl.NArg() != 0 {
		say(errw, "%v\n", errUsage)
		return 2
	}
	primary, err := readRender(*p)
	if err != nil {
		say(errw, "edge-hosts: cannot read the primary render: %v\n", err)
		return 2
	}
	secondary, err := readRender(*s)
	if err != nil {
		say(errw, "edge-hosts: cannot read the secondary render: %v\n", err)
		return 2
	}
	findings, compared := judge(primary, secondary)
	if compared == 0 {
		say(errw, "edge-hosts: the primary render %s holds no route with a hostname: nothing to judge\n", *p)
		return 2
	}
	for _, f := range findings {
		say(out, "FAIL %s\n", f)
	}
	if len(findings) > 0 {
		say(out, "edge-hosts: %d of the public entries the primary's edge serves are missing on the secondary's edge; a request for one that enters in the secondary region answers 404\n", len(findings))
		return 1
	}
	say(out, "edge-hosts: every one of the %d hostnames the primary's edge routes, and every listener, is routed by the secondary's edge too\n", compared)
	return 0
}

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
