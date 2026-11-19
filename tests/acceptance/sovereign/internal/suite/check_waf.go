package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G15, Firewall proof: journey WAF of ADR-0038 point WF7 for each
// WebApplicationFirewall object of each region, on the public hosts of the
// HTTPRoutes of its Gateway, each in the mode of its hostOverrides entry or
// spec.mode. WAF-1 a GET / answers 200 or a redirect; WAF-2 each corpus
// request (tests/waf/corpus/) to a block host answers 403; WAF-3 to a detect
// host it passes and a cilium-envoy log line holds "Coraza" and its marker
// net-g15=<run>-<host>-<entry>-, <host> numbered over all objects of the run,
// so no two requests share one; WAF-4 no dial past the firewall connects;
// WAF-5 an exclusion that expired yesterday is refused at admission and one
// that expires tomorrow is not. Nothing measured fails. The rules in full are
// in tests/acceptance/sovereign/README.md.
const (
	wafID       = "NET-G15"
	wafKind     = "webapplicationfirewalls.compose.openova.io"
	wafCorpus   = "tests/waf/corpus"
	wafNoObject = "no web application firewall in the path"
)

// wafEntry is one request of the corpus.
type wafEntry struct {
	Rule    int               `json:"rule"`
	Test    int               `json:"test"`
	Method  string            `json:"method"`
	Target  string            `json:"target"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type wafCheck struct {
	kubectl   kubectlFunc
	transport http.RoundTripper // nil: http.DefaultTransport
	scheme    string            // "": https
	dial      func(ctx context.Context, addr string) error
	lookup    func(ctx context.Context, host string) ([]string, error)
	now       func() time.Time
}

func init() {
	RegisterCheck(wafCheck{kubectl: kube.Region.Run, now: time.Now, lookup: net.DefaultResolver.LookupHost, dial: func(ctx context.Context, addr string) error {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}})
}

func (wafCheck) ID() string { return wafID }

func (c wafCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	corpus, err := loadWAFCorpus(cc.RepoRoot)
	if err != nil {
		return err
	}
	run, n, objects, numbered, seen := strings.ToLower(NewRunID(c.now())), 0, 0, 0, Evidence{}
	for _, region := range cc.Regions {
		list, err := c.list(ctx, region, wafKind, "-A")
		switch {
		case err != nil && strings.Contains(err.Error(), notServed):
			seen[region.ID] = "kind not served"
		case err != nil:
			n++
			checkStep(rec, wafID, n, "the WebApplicationFirewall objects of region "+region.ID+" are read", func(Evidence) error { return err })
		default:
			seen[region.ID] = len(list)
			for _, o := range list {
				objects++
				n, numbered = c.object(ctx, cc, rec, region, o, corpus, run, numbered, n)
			}
		}
	}
	if objects == 0 {
		checkStep(rec, wafID, n+1, wafNoObject, func(ev Evidence) error {
			ev["objects_by_region"] = seen
			return errors.New(wafNoObject + ": no WebApplicationFirewall object, so no firewall was measured")
		})
	}
	return nil
}

// object runs WAF-1 to WAF-5 for one object, whose hosts the markers number from numbered+1, and returns the last step and host numbers.
func (c wafCheck) object(ctx context.Context, cc *CheckContext, rec StepRecorder, region kube.Region, o kube.Object, corpus []wafEntry, run string, numbered, n int) (int, int) {
	ns, gw, impl := o.Namespace(), o.Str("spec", "gatewayRef", "name"), o.Str("status", "implementation")
	of := " (WebApplicationFirewall " + ns + "/" + o.Name() + ", region " + region.ID + ")"
	var hosts []string
	modes, ports := map[string]string{}, []int{}
	n++
	if !checkStep(rec, wafID, n, "the Gateway "+ns+"/"+gw+" and its public hosts are read"+of, func(ev Evidence) error {
		ev["implementation"], ev["mode"], ev["level"], ev["rule_set"] = impl, o.Str("spec", "mode"), o.Get("spec", "level"), o.Str("spec", "ruleSet", "version")
		gws, err := c.list(ctx, region, "gateways.gateway.networking.k8s.io", "-n", ns)
		if err != nil {
			return err
		}
		for _, g := range gws {
			for _, l := range g.Objects("spec", "listeners") {
				if g.Name() == gw {
					ports = append(ports, l.Int("port"))
				}
			}
		}
		routes, err := c.list(ctx, region, "httproutes.gateway.networking.k8s.io", "-A")
		if err != nil {
			return err
		}
		for _, r := range routes {
			for _, p := range r.Objects("spec", "parentRefs") {
				names, _ := r.Get("spec", "hostnames").([]any)
				for _, h := range names {
					if s, _ := h.(string); p.Str("name") == gw && nonEmpty(p.Str("namespace"), r.Namespace()) == ns && s != "" && !strings.Contains(s, "*") && modes[s] == "" {
						hosts, modes[s] = append(hosts, s), o.Str("spec", "mode")
					}
				}
			}
		}
		for _, h := range o.Objects("spec", "hostOverrides") {
			if modes[h.Str("host")] != "" && h.Str("mode") != "" {
				modes[h.Str("host")] = h.Str("mode")
			}
		}
		sort.Strings(hosts)
		ev["listener_ports"], ev["hosts"] = ports, modes
		if len(ports) == 0 || len(hosts) == 0 {
			return fmt.Errorf("the Gateway %s/%s has %d listeners and %d public hosts; nothing to measure", ns, gw, len(ports), len(hosts))
		}
		return nil
	}) {
		return n, numbered
	}
	client := &http.Client{Transport: c.transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	n++
	checkStep(rec, wafID, n, "WAF-1: a benign GET / of every public host answers 200 or a redirect"+of, func(ev Evidence) error {
		var bad []string
		for _, h := range hosts {
			if code, err := c.ask(ctx, client, h, wafEntry{Method: http.MethodGet, Target: "/"}, ""); err != nil || !slices.Contains(openCodes, code) {
				bad = append(bad, fmt.Sprintf("%s: HTTP %d %v", h, code, err))
			}
		}
		ev["hosts"], ev["failed"] = len(hosts), bad
		return joinFailures(bad)
	})
	for _, mode := range []string{"block", "detect"} {
		n++
		start := c.now()
		checkStep(rec, wafID, n, map[string]string{"block": "WAF-2: every corpus request to a host in mode block answers 403",
			"detect": "WAF-3: every corpus request to a host in mode detect passes and is logged"}[mode]+of, func(ev Evidence) error {
			var bad []string
			markers := map[string]string{}
		nextHost:
			for i, h := range hosts {
				for j, e := range corpus {
					if modes[h] != mode {
						break
					}
					marker, what := fmt.Sprintf("net-g15=%s-%d-%d-", run, numbered+i+1, j+1), fmt.Sprintf("%s rule %d test %d", h, e.Rule, e.Test)
					switch code, err := c.ask(ctx, client, h, e, marker); {
					case err != nil: // the host does not answer: its other requests are not sent
						bad = append(bad, what+": "+err.Error())
						continue nextHost
					case mode == "block" && code != http.StatusForbidden:
						bad = append(bad, fmt.Sprintf("%s: HTTP %d, not denied", what, code))
					case mode == "detect" && code == http.StatusForbidden:
						bad = append(bad, what+": HTTP 403, denied in mode detect")
					default:
						markers[marker] = what
					}
				}
			}
			ev["requests"] = len(markers) + len(bad)
			if len(markers)+len(bad) == 0 {
				return fmt.Errorf("no public host is in mode %s; nothing was measured", mode)
			}
			if mode == "detect" && len(markers) > 0 {
				missing, err := c.unlogged(ctx, cc.Regions, impl, markers, c.now().Sub(start))
				if err != nil {
					return err
				}
				bad = append(bad, missing...)
			}
			ev["failed"] = bad
			return joinFailures(bad)
		})
	}
	n++
	checkStep(rec, wafID, n, "WAF-4: no dial to a node or entry address reaches a listener of the Gateway past the firewall"+of, func(ev Evidence) error {
		nodes, err := c.list(ctx, region, "nodes")
		if err != nil {
			return err
		}
		var targets, bad []string
		add := func(addr string, ports ...int) {
			for _, p := range ports {
				targets = append(targets, net.JoinHostPort(addr, strconv.Itoa(p)))
			}
		}
		internal, nodePorts := slices.DeleteFunc(slices.Clone(ports), func(p int) bool { return p == 80 || p == 443 }), ports
		if impl == "native" {
			nodePorts = append(slices.Clone(ports), 80, 443)
		} else if impl == "coraza" && len(internal) < len(ports) {
			bad = append(bad, "a listener of the Gateway is on port 80 or 443, in front of the firewall")
		}
		for _, node := range nodes {
			for _, a := range node.Objects("status", "addresses") {
				if a.Str("type") == "ExternalIP" {
					add(a.Str("address"), nodePorts...)
				}
			}
		}
		for _, h := range hosts {
			addrs, err := c.lookup(ctx, h)
			if err != nil {
				return err
			}
			for _, a := range addrs {
				add(a, internal...)
			}
		}
		slices.Sort(targets)
		targets = slices.Compact(targets)
		for _, t := range targets {
			if c.dial(ctx, t) == nil {
				bad = append(bad, t+" connected")
			}
		}
		if len(targets) == 0 {
			bad = append(bad, "no address to dial; nothing was measured")
		}
		ev["dials"], ev["failed"] = targets, bad
		return joinFailures(bad)
	})
	n++
	checkStep(rec, wafID, n, "WAF-5: an exclusion whose expires is in the past is refused at admission"+of, func(ev Evidence) error {
		past := c.dryRun(ctx, region, o, hosts[0], c.now().UTC().Add(-24*time.Hour).Format(time.DateOnly))
		future := c.dryRun(ctx, region, o, hosts[0], c.now().UTC().Add(24*time.Hour).Format(time.DateOnly))
		ev["past"], ev["future"] = fmt.Sprint(past), fmt.Sprint(future)
		switch {
		case past == nil:
			return errors.New("the update with an exclusion that expired yesterday was accepted")
		case future != nil:
			return fmt.Errorf("the same update with an exclusion that expires tomorrow was refused too, so the refusal is not shown to be about the date: %v", future)
		}
		return nil
	})
	return n, numbered + len(hosts)
}

// ask sends one request to host and returns the HTTP code. A marker is added
// to the query of the target.
func (c wafCheck) ask(ctx context.Context, client *http.Client, host string, e wafEntry, marker string) (int, error) {
	target := e.Target
	if marker != "" {
		target += map[bool]string{true: "&", false: "?"}[strings.Contains(target, "?")] + marker
	}
	req, err := http.NewRequestWithContext(ctx, e.Method, nonEmpty(c.scheme, "https")+"://"+host+target, strings.NewReader(e.Body))
	if err != nil {
		return 0, err
	}
	for k, v := range e.Headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	return res.StatusCode, nil
}

// unlogged returns the requests of markers that no line of a cilium-envoy
// Pod's log names, read back over the time since the requests started.
func (c wafCheck) unlogged(ctx context.Context, regions []kube.Region, impl string, markers map[string]string, since time.Duration) ([]string, error) {
	if impl != "coraza" {
		return nil, fmt.Errorf("implementation %q: the check reads the log of implementation coraza only", impl)
	}
	var lines []string
	for _, region := range regions {
		pods, err := c.list(ctx, region, "pods", "-A", "-l", "k8s-app=cilium-envoy")
		if err != nil {
			return nil, err
		}
		for _, p := range pods {
			out, err := c.kubectl(region, ctx, nil, "logs", "-n", p.Namespace(), p.Name(), "-c", "cilium-envoy", "--since="+strconv.Itoa(int(since.Seconds())+120)+"s")
			if err != nil {
				return nil, err
			}
			lines = append(lines, strings.Split(string(out), "\n")...)
		}
	}
	var missing []string
	for marker, what := range markers {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "Coraza") && strings.Contains(l, marker) }) {
			missing = append(missing, what+": passed but not logged")
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// dryRun sends the object back with one more exclusion that expires on day,
// as a server-side dry-run update, and returns the refusal or nil.
func (c wafCheck) dryRun(ctx context.Context, region kube.Region, o kube.Object, host, day string) error {
	s, _ := o.Get("spec").(map[string]any)
	spec := map[string]any{}
	maps.Copy(spec, s)
	ex, _ := spec["exclusions"].([]any)
	spec["exclusions"] = append(slices.Clone(ex), map[string]any{"id": "net-g15-probe", "ruleIds": []int{941100}, "host": host,
		"pathPrefix": "/net-g15-probe", "reason": "admission probe of gate check NET-G15, never stored", "approvedBy": "NET-G15", "expires": day})
	body, err := json.Marshal(map[string]any{"apiVersion": o.Str("apiVersion"), "kind": o.Str("kind"),
		"metadata": map[string]any{"name": o.Name(), "namespace": o.Namespace()}, "spec": spec})
	if err != nil {
		return err
	}
	_, err = c.kubectl(region, ctx, strings.NewReader(string(body)), "apply", "--server-side", "--dry-run=server", "--force-conflicts", "--field-manager=net-g15", "-f", "-")
	return err
}

func (c wafCheck) list(ctx context.Context, region kube.Region, resource string, scope ...string) ([]kube.Object, error) {
	out, err := c.kubectl(region, ctx, nil, append(append([]string{"get", resource}, scope...), "-o", "json")...)
	if err != nil {
		return nil, err
	}
	var l kube.List
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("[%s] the list of %s is not JSON: %v", region.ID, resource, err)
	}
	return l.Items, nil
}

// loadWAFCorpus reads every file *.txt of the corpus folder under root.
func loadWAFCorpus(root string) ([]wafEntry, error) {
	files, _ := filepath.Glob(filepath.Join(root, wafCorpus, "*.txt"))
	var corpus []wafEntry
	for _, name := range files {
		text, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(text), "\n") {
			var e wafEntry
			if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
				continue
			} else if err := json.Unmarshal([]byte(t), &e); err != nil || e.Rule <= 0 || e.Method == "" || !strings.HasPrefix(e.Target, "/") {
				return nil, fmt.Errorf("%s line %d is not a corpus entry with rule, method and target: %v", name, i+1, err)
			}
			corpus = append(corpus, e)
		}
	}
	if len(corpus) == 0 {
		return nil, fmt.Errorf("the corpus %s holds no request", wafCorpus)
	}
	return corpus, nil
}

// joinFailures is nil for no failure, else one error that names the first ten.
func joinFailures(bad []string) error {
	if len(bad) > 10 {
		bad = append(bad[:10:10], fmt.Sprintf("and %d more", len(bad)-10))
	}
	if len(bad) == 0 {
		return nil
	}
	return errors.New(strings.Join(bad, "; "))
}
