package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// complianceAPI answers the compliance routes of the console API from its fields.
type complianceAPI struct {
	sync.Mutex
	num, den   int
	violations []map[string]string
	policies   []map[string]string
	frames     chan string // each text goes to the open stream as the data of one frame
	waiting    bool        // the seed is applied and its frame is not sent yet
	early      int         // requests that arrived while waiting
}

func (a *complianceAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if route == "stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": connected\n\nevent: snapshot\ndata: {\"type\":\"snapshot\"}\n\n")
		for {
			w.(http.Flusher).Flush()
			select {
			case text := <-a.frames:
				_, _ = fmt.Fprintf(w, "event: score\ndata: %s\n\n", text)
			case <-r.Context().Done():
				return
			}
		}
	}
	a.Lock()
	defer a.Unlock()
	if a.waiting {
		a.early++
	}
	body := map[string]any{"items": a.policies}
	switch route {
	case "scorecard":
		body = map[string]any{"sovereign": map[string]int{"numerator": a.num, "denominator": a.den}}
	case "violations": // one item per page: the check must read every page
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		body = map[string]any{"total": len(a.violations),
			"items": a.violations[min(offset, len(a.violations)):min(offset+1, len(a.violations))]}
	}
	_ = json.NewEncoder(w).Encode(body)
}

// complianceRun runs one check against the fake API the way the runner does.
// It fails the test unless the check passed (want is empty) or failed with a
// reason that holds want.
func complianceRun(t *testing.T, a *complianceAPI, c Check, want string) {
	t.Helper()
	srv := httptest.NewServer(a)
	defer srv.Close()
	cc := &CheckContext{API: api.New(srv.URL, "session"), Deploy: &DeploymentInfo{ID: "dep"}, Regions: []kube.Region{{ID: "region-a"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}, []Check{c}, cc, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Fatalf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
}

func TestCompliancePush(t *testing.T) {
	const image = "registry.example/proxy-ghcr/kyverno/kyverno:v1.18.0"
	for fault, want := range map[string]string{
		"":          "", // the seed is counted, pushed and dropped
		"empty":     "scorecard empty",
		"refused":   "admission webhook denied the request",
		"silent":    "no frame named the seed",
		"uncounted": "numerator 90 and denominator 100 (before the seed 90 and 100)",
		"passes":    "numerator 95 and denominator 105 (before the seed 90 and 100)",
		"unlisted":  "0 violations of the seed",
		"kept":      "the violations still list the seed 2 times",
	} {
		t.Run("fault="+fault, func(t *testing.T) {
			a := &complianceAPI{num: 90, den: 100, frames: make(chan string, 8)}
			if fault == "empty" {
				a.den = 0
			}
			calls := []string{}
			kubectl := func(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
				a.Lock()
				defer a.Unlock()
				calls = append(calls, strings.Join(args, " "))
				switch {
				case args[2] == "get":
					return []byte(image + "\n"), nil
				case args[0] == "delete":
					if fault != "kept" {
						a.violations = a.violations[:min(1, len(a.violations))]
					}
					a.frames <- `{"type":"score","score":{"scope":"sovereign"}}`
				case args[2] == "apply":
					ns, key := args[1], "deployment/"+args[1]+"/cmp-bad"
					var seed kube.Object
					b, _ := io.ReadAll(stdin)
					_ = json.Unmarshal(b, &seed)
					containers := seed.Objects("spec", "template", "spec", "containers")
					if !strings.HasPrefix(ns, "cmp-gate-") || seed.Namespace() != ns || seed.Get("spec", "replicas") != 0.0 ||
						len(containers) != 1 || containers[0].Str("image") != image {
						t.Errorf("the seed is not a Deployment of its namespace cmp-gate-... with zero replicas and one container of the image %s: %s", image, b)
					}
					if fault == "refused" {
						return nil, errors.New("admission webhook denied the request")
					}
					a.waiting = true
					time.AfterFunc(50*time.Millisecond, func() { // the API takes the seed in: its score, its violations, then its frame
						a.Lock()
						defer a.Unlock()
						a.waiting = false
						a.num, a.den = 93, 105
						a.violations = []map[string]string{{"resource": "deployment/other/cmp-bad", "namespace": "other", "policy": "p"},
							{"resource": key, "namespace": ns, "policy": "topology-spread"}, {"resource": key, "namespace": ns, "policy": "multi-replica-drainability"}}
						switch fault {
						case "silent":
							return
						case "uncounted":
							a.num, a.den = 90, 100
						case "passes":
							a.num = 95
						case "unlisted":
							a.violations = a.violations[:1]
						}
						a.frames <- `{"type":"score","score":{"scope":"resource","resource":"` + key + `"}}`
					})
				}
				return nil, nil
			}
			complianceRun(t, a, compliancePushCheck{kubectl: kubectl, wait: time.Second}, want)
			wantCalls := []string{}
			if fault != "empty" && len(calls) > 1 { // the seed is applied once, in one namespace, and that namespace is always deleted
				ns := strings.TrimPrefix(calls[1], "create namespace ")
				wantCalls = []string{"-n kyverno get deploy -o jsonpath={.items[0].spec.template.spec.containers[0].image}",
					"create namespace " + ns, "-n " + ns + " apply -f -", "delete namespace " + ns + " --wait=false --ignore-not-found"}
			}
			if !reflect.DeepEqual(calls, wantCalls) || a.early != 0 {
				t.Errorf("kubectl calls %q, want %q; %d requests while waiting for a frame, want 0", calls, wantCalls, a.early)
			}
		})
	}
}

func TestComplianceMode(t *testing.T) {
	const legacy = `{"items": [
	  {"metadata": {"name": "audits"}, "spec": {"validationFailureAction": "Audit", "rules": [{"name": "r", "validate": {}}]}},
	  {"metadata": {"name": "enforces"}, "spec": {"validationFailureAction": "Enforce"}},
	  {"metadata": {"name": "rule-enforces"}, "spec": {"validationFailureAction": "Audit", "rules": [{"name": "r", "validate": {"failureAction": "Enforce"}}]}},
	  {"metadata": {"name": "lower-case"}, "spec": {"validationFailureAction": "enforce"}},
	  {"metadata": {"name": "mutates"}, "spec": {"rules": [{"name": "r", "mutate": {}}]}}]}`
	const validating = `{"items": [
	  {"metadata": {"name": "denies"}, "spec": {"validationActions": ["Audit", "Deny"]}},
	  {"metadata": {"name": "warns"}, "spec": {"validationActions": ["Warn"]}}]}`
	absent, refused := errors.New(`error: the server doesn't have a resource type "x"`), errors.New("connection refused")
	shown := func(pairs ...string) []map[string]string { // name, mode, name, mode, ...: the list of the API
		out := []map[string]string{{"name": "judged", "mode": "permissive", "source": "evaluator"}}
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, map[string]string{"name": pairs[i], "mode": pairs[i+1], "source": "kyverno"})
		}
		return out
	}
	old := []string{"audits", "permissive", "enforces", "enforcing", "rule-enforces", "enforce", "lower-case", "enforcing", "mutates", "audit"}
	all := append(append([]string{}, old...), "denies", "enforcing", "warns", "permissive")
	for name, tc := range map[string]struct {
		policies            []map[string]string
		legacyErr, validErr error
		want                string
	}{
		"every mode is the engine's":               {policies: shown(all...)},
		"validating policies are not served":       {policies: shown(old...), validErr: absent},
		"the API shows audit, the engine enforces": {policies: shown("enforces", "permissive"), want: `enforces: the API shows "permissive" and the engine object carries enforce`},
		"the API shows enforce, the engine audits": {policies: shown("warns", "enforcing"), want: `warns: the API shows "enforcing" and the engine object carries audit`},
		"a mode word that is neither":              {policies: shown("audits", "relaxed"), want: `audits: the API shows "relaxed"`},
		"a policy without an engine object":        {policies: shown("ghost", "permissive"), want: "ghost: the engine has no policy object of that name"},
		"the API lists no policy":                  {want: "nothing was measured: the API lists no policy"},
		"only a policy the API judges itself":      {policies: shown(), want: "nothing was measured: no listed policy has an engine object"},
		"no kind is served":                        {policies: shown(old...), legacyErr: absent, validErr: absent, want: "the cluster serves neither"},
		"the region cannot be read":                {policies: shown(old...), legacyErr: refused, want: "connection refused"},
	} {
		t.Run(name, func(t *testing.T) {
			kubectl := func(_ kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
				if args[1] == legacyPolicies {
					return []byte(legacy), tc.legacyErr
				}
				return []byte(validating), tc.validErr
			}
			complianceRun(t, &complianceAPI{policies: tc.policies}, complianceModeCheck{kubectl: kubectl}, tc.want)
		})
	}
}

func TestComplianceFrames(t *testing.T) {
	const body = ": connected\n\nevent: snapshot\ndata: {\"a\":1}\n\n: ping 7\n\nevent: score\r\ndata: one\r\ndata:two\r\n\r\nevent: bare\n\ndata: cut off"
	got := []string{}
	if err := readFrames(strings.NewReader(body), func(data string) { got = append(got, data) }); err != nil {
		t.Fatal(err)
	}
	if want := []string{`{"a":1}`, "one\ntwo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frames %q, want %q: a comment, an event without data and a frame that is cut off are no frames", got, want)
	}
	n, err := onFrames(make(chan string), 20*time.Millisecond, false, func(string) (bool, string, error) { return true, "", nil })
	if n != 0 || err == nil || !strings.Contains(err.Error(), "no frame arrived") {
		t.Fatalf("a wait without a frame gave %d frames and the error %v; want 0 and \"no frame arrived\"", n, err)
	}
}

func TestComplianceRegistered(t *testing.T) {
	push, isPush := registry[compliancePushID].(compliancePushCheck)
	mode, isMode := registry[complianceModeID].(complianceModeCheck)
	if !isPush || !isMode || push.kubectl == nil || mode.kubectl == nil || push.wait != 120*time.Second {
		t.Fatalf("the registry must hold %s and %s, each with kube.Region.Run, and a wait of 120 seconds for %s",
			compliancePushID, complianceModeID, compliancePushID)
	}
}
