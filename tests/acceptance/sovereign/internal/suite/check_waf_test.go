package suite

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// wafFake is a Sovereign whose region-a holds one WebApplicationFirewall for
// Gateway web/gw, with host a.example in mode block and b.example in mode
// detect, and whose region-b does not serve the kind unless second is set. Its
// RoundTrip is the firewall in front of both hosts, with the faults that the knobs set.
type wafFake struct {
	impl, leak, connects       string // leak: a block-mode request whose query holds it passes; connects: the dial that connects
	ports                      []any  // listener ports of the Gateway
	allBlock, noObject, second bool   // no host override to detect; no object at all; region-b holds the same object, whose requests are not logged
	denyDetect, noLog          bool   // the detect host denies, or logs nothing
	down                       bool   // a.example does not answer
	pastOK, futureNo           bool   // admission accepts the past date, refuses the future one
	benign                     int    // the answer to GET /
	logs                       []string
}

func (f *wafFake) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	switch q := strings.Join(args, " "); {
	case args[1] == wafKind && ((r.ID == "region-b" && !f.second) || f.noObject):
		return nil, errors.New(`error: the server doesn't have a resource type "` + wafKind + `"`)
	case args[1] == wafKind:
		f.noLog = f.noLog || r.ID == "region-b" // the requests of the second object are not logged
		modes := map[bool][]any{false: {obj{"host": "b.example", "mode": "detect"}}}[f.allBlock]
		return []byte(kubeList(obj{"apiVersion": "compose.openova.io/v1alpha1", "kind": "WebApplicationFirewall",
			"metadata": obj{"name": "edge", "namespace": "web"}, "status": obj{"implementation": f.impl},
			"spec": obj{"gatewayRef": obj{"name": "gw"}, "mode": "block", "level": 1, "hostOverrides": modes}})), nil
	case strings.HasPrefix(q, "get gateways.gateway.networking.k8s.io -n web"):
		listeners := []any{}
		for _, p := range f.ports {
			listeners = append(listeners, obj{"port": p})
		}
		return []byte(kubeList(obj{"metadata": obj{"name": "other"}, "spec": obj{"listeners": []any{obj{"port": 9}}}},
			obj{"metadata": obj{"name": "gw"}, "spec": obj{"listeners": listeners}})), nil
	case strings.HasPrefix(q, "get httproutes.gateway.networking.k8s.io -A"):
		return []byte(kubeList(
			obj{"metadata": obj{"namespace": "web"}, "spec": obj{"parentRefs": []any{obj{"name": "gw"}}, "hostnames": []any{"a.example", "*.wild.example"}}},
			obj{"metadata": obj{"namespace": "apps"}, "spec": obj{"parentRefs": []any{obj{"name": "gw", "namespace": "web"}}, "hostnames": []any{"b.example"}}},
			obj{"metadata": obj{"namespace": "apps"}, "spec": obj{"parentRefs": []any{obj{"name": "gw"}}, "hostnames": []any{"c.example"}}})), nil
	case q == "get nodes -o json":
		return []byte(kubeList(obj{"status": obj{"addresses": []any{obj{"type": "InternalIP", "address": "10.0.0.5"},
			obj{"type": "ExternalIP", "address": "192.0.2.10"}}}})), nil
	case strings.HasPrefix(q, "get pods -A -l k8s-app=cilium-envoy"):
		return []byte(map[bool]string{true: kubeList(obj{"metadata": obj{"name": "cilium-envoy-1", "namespace": "kube-system"}}), false: kubeList()}[r.ID == "region-a"]), nil
	case strings.HasPrefix(q, "logs -n kube-system cilium-envoy-1 -c cilium-envoy --since="):
		return []byte(strings.Join(f.logs, "\n")), nil
	case q == "apply --server-side --dry-run=server --force-conflicts --field-manager=net-g15 -f -":
		body, _ := io.ReadAll(stdin)
		if past := strings.Contains(string(body), `"expires":"2026-11-18"`); (past && !f.pastOK) || (!past && f.futureNo) {
			return nil, errors.New(`admission webhook "waf" denied the request: exclusion net-g15-probe expired`)
		}
		return nil, nil
	}
	return nil, errors.New("unexpected kubectl " + strings.Join(args, " "))
}

func (f *wafFake) RoundTrip(r *http.Request) (*http.Response, error) {
	code, host := http.StatusOK, r.URL.Host
	switch {
	case f.down && host == "a.example":
		return nil, errors.New("connection refused")
	case r.URL.Query().Get("net-g15") == "" && f.benign != 0:
		code = f.benign
	case r.URL.Query().Get("net-g15") == "":
	case (host == "a.example" || f.allBlock) && (f.leak == "" || !strings.Contains(r.URL.RawQuery, f.leak)):
		code = http.StatusForbidden
	case host == "b.example" && f.denyDetect:
		code = http.StatusForbidden
	case host == "b.example" && !f.noLog:
		f.logs = append(f.logs, `[wasm] Coraza: Warning. XSS Attack Detected [id "941100"] [uri "`+r.URL.RequestURI()+`"]`)
	}
	w := httptest.NewRecorder()
	w.WriteHeader(code)
	return w.Result(), nil
}

// wafRun runs check NET-G15 against f and returns the names of its failed steps and their errors.
func wafRun(t *testing.T, f *wafFake, root string) (failed []string, reason string) {
	c := wafCheck{kubectl: f.kubectl, scheme: "http", transport: f,
		dial: func(_ context.Context, addr string) error {
			return map[bool]error{true: nil, false: errors.New("connection refused")}[addr == f.connects]
		},
		lookup: func(context.Context, string) ([]string, error) { return []string{"198.51.100.7"}, nil },
		now:    func() time.Time { return time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC) }}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(wafID, StateExpectedFail)}}, []Check{c},
		&CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}, RepoRoot: root}, true)
	for _, s := range r.run.Steps {
		if s.Status == Fail {
			failed, reason = append(failed, strings.SplitN(s.Name, ":", 2)[0]), reason+s.Error+"\n"
		}
	}
	if (r.run.Checks[0].Status == Pass) != (len(failed) == 0) {
		t.Errorf("check status %s with failed steps %v", r.run.Checks[0].Status, failed)
	}
	return failed, reason
}

// TestWAFCheck: the check passes on a firewall that meets WAF-1 to WAF-5 with
// the corpus of the repository, and each fault fails exactly its step.
func TestWAFCheck(t *testing.T) {
	repo, bad := filepath.Join("..", "..", "..", "..", ".."), t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, wafCorpus), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, wafCorpus, "x.txt"), []byte("# a comment\n{\"rule\":1,\"method\":\"GET\",\"target\":\"get\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		f          *wafFake
		root, want string // want: "failed step names|text of their errors"; "" passes
	}{
		"firewall in the path":        {&wafFake{impl: "coraza"}, repo, ""},
		"native, ports 80 and 443":    {&wafFake{impl: "native", ports: []any{443}, connects: "192.0.2.10:80"}, repo, "WAF-3,WAF-4|192.0.2.10:80 connected"},
		"no object":                   {&wafFake{noObject: true}, repo, wafNoObject + "|" + wafNoObject + ": no WebApplicationFirewall object"},
		"benign request denied":       {&wafFake{impl: "coraza", benign: 403}, repo, "WAF-1|a.example: HTTP 403"},
		"a host does not answer":      {&wafFake{impl: "coraza", down: true}, repo, "WAF-1,WAF-2|-1-1-\": connection refused\n"},
		"a corpus request passes":     {&wafFake{impl: "coraza", leak: "pg_catalog"}, repo, "WAF-2|a.example rule 942140 test 1: HTTP 200, not denied"},
		"no host in mode detect":      {&wafFake{impl: "coraza", allBlock: true}, repo, "WAF-3|no public host is in mode detect; nothing was measured"},
		"denied in mode detect":       {&wafFake{impl: "coraza", denyDetect: true}, repo, "WAF-3|HTTP 403, denied in mode detect"},
		"passed but not logged":       {&wafFake{impl: "coraza", second: true}, repo, "WAF-3|passed but not logged"},
		"no log reader":               {&wafFake{impl: "native", ports: []any{443}}, repo, `WAF-3|implementation "native": the check reads the log of implementation coraza only`},
		"internal port reachable":     {&wafFake{impl: "coraza", connects: "198.51.100.7:18443"}, repo, "WAF-4|198.51.100.7:18443 connected"},
		"listener on a public port":   {&wafFake{impl: "coraza", ports: []any{18443, 443}}, repo, "WAF-4|a listener of the Gateway is on port 80 or 443"},
		"past expiry accepted":        {&wafFake{impl: "coraza", pastOK: true}, repo, "WAF-5|expired yesterday was accepted"},
		"every expiry refused":        {&wafFake{impl: "coraza", futureNo: true}, repo, "WAF-5|not shown to be about the date"},
		"empty corpus":                {&wafFake{impl: "coraza"}, t.TempDir(), "the check could run|the corpus tests/waf/corpus holds no request"},
		"corpus line without a slash": {&wafFake{impl: "coraza"}, bad, "the check could run|x.txt line 2 is not a corpus entry"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.f.ports == nil {
				c.f.ports = []any{18443}
			}
			failed, reason := wafRun(t, c.f, c.root)
			if want := strings.SplitN(c.want+"|", "|", 2); strings.Join(failed, ",") != want[0] || !strings.Contains(reason, strings.TrimSuffix(want[1], "|")) {
				t.Errorf("failed steps %v, errors %q; want steps %q and errors that hold %q", failed, reason, want[0], want[1])
			}
		})
	}
}
