package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// aipG06Fake answers kubectl for check AIP-G06: the reads of the Deployment,
// the Secrets and the Service of the namespace axon, by region. Every other
// call (the dial Pod) goes to the fake of check_policy_dials_test.go, which runs
// the real dial script with a fake nc. The port-forward is an httptest server
// that answers GET /stats with code and stats.
type aipG06Fake struct {
	dial       *dialFake
	deployment map[string]string // region -> the JSON of deployment axon/axon; absent: the read fails
	secrets    string            // the JSON list of the Secrets of axon in the primary region
	services   map[string]bool   // region -> the Service axon/axon exists
	code       int
	stats      string
	forwardErr error
	forwarded  []string // <region>/<namespace>/<service>:<port> of every forward
	stopped    int
}

func (f *aipG06Fake) kubectl(r kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	switch strings.Join(args, " ") {
	case "-n axon get deployment axon -o json":
		if d, ok := f.deployment[r.ID]; ok {
			return []byte(d), nil
		}
		return nil, fmt.Errorf("[%s] kubectl get deployment axon: NotFound", r.ID)
	case "-n axon get secret -o json":
		return []byte(f.secrets), nil
	case "-n axon get service axon -o json":
		if f.services[r.ID] {
			return []byte(`{"kind": "Service", "metadata": {"name": "axon", "namespace": "axon"}, "spec": {"type": "ClusterIP"}}`), nil
		}
		return nil, fmt.Errorf("[%s] kubectl get service axon: NotFound", r.ID)
	}
	return f.dial.kubectl(r, ctx, stdin, args...)
}

func (f *aipG06Fake) forward(_ context.Context, r kube.Region, namespace, service string, port int) (string, func(), error) {
	f.forwarded = append(f.forwarded, fmt.Sprintf("%s/%s/%s:%d", r.ID, namespace, service, port))
	if f.forwardErr != nil {
		return "", nil, f.forwardErr
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/stats" {
			http.NotFound(w, req)
			return
		}
		w.WriteHeader(f.code)
		_, _ = io.WriteString(w, f.stats)
	}))
	return strings.TrimPrefix(srv.URL, "http://"), func() { f.stopped++; srv.Close() }, nil
}

const (
	aipG06ReadyOne = `{"metadata": {"name": "axon", "generation": 3}, "spec": {"replicas": 1}, "status": {"observedGeneration": 3, "replicas": 1, "readyReplicas": 1}}`
	// The API leaves status.replicas and status.readyReplicas out when they are 0.
	aipG06Zero  = `{"metadata": {"name": "axon", "generation": 2}, "spec": {"replicas": 0}, "status": {"observedGeneration": 2}}`
	aipG06Stats = `{"credential": {"state": "absent"}, "turns": {"active": 0, "parked": 0, "queued": 0, "maxActive": 4, "maxParked": 8}, "counters": {}}`
)

// aipG06Good is a Sovereign of two regions where every clause holds.
func aipG06Good(t *testing.T) *aipG06Fake {
	return &aipG06Fake{
		dial: &dialFake{t: t, answers: map[string]string{
			dialControlHost + ":53":                    DialConnected,
			"kubernetes.default.svc.cluster.local:443": DialConnected,
			"axon.axon.svc.cluster.local:3000":         DialTimedOut,
		}},
		deployment: map[string]string{"region-a": aipG06ReadyOne, "region-b": aipG06Zero},
		secrets:    `{"items": [{"metadata": {"name": "secrets-birth-ca"}, "data": {"ca.crt": "eA=="}}]}`,
		services:   map[string]bool{"region-a": true, "region-b": true},
		code:       http.StatusOK,
		stats:      aipG06Stats,
	}
}

// aipG06Run runs check AIP-G06 the way the runner does. It fails the test
// unless the check passed (want is empty) or failed with a reason that holds
// want, and unless every dial Pod it created was deleted and every forward
// stopped.
func aipG06Run(t *testing.T, f *aipG06Fake, regions []kube.Region, want string) *runner {
	t.Helper()
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	c := aipG06Check{kubectl: f.kubectl, forward: f.forward, get: aipG06Get}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}, []Check{c}, &CheckContext{Regions: regions}, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Errorf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
	if strings.Join(f.dial.created, " ") != strings.Join(f.dial.deleted, " ") {
		t.Errorf("dial Pods created %v, deleted %v", f.dial.created, f.dial.deleted)
	}
	if f.forwardErr == nil && len(f.forwarded) != f.stopped {
		t.Errorf("forwards started %v, stopped %d", f.forwarded, f.stopped)
	}
	return r
}

// TestAipG06Check: with every clause true the check passes, on one region and
// on two; it forwards to port 3001 of axon/axon of the primary region only, and
// its one dial Pod runs in the namespace default of the primary region.
func TestAipG06Check(t *testing.T) {
	f := aipG06Good(t)
	r := aipG06Run(t, f, twoRegions(), "")
	if got := strings.Join(f.forwarded, " "); got != "region-a/axon/axon:3001" {
		t.Errorf("forwards %q, want region-a/axon/axon:3001", got)
	}
	if len(f.dial.created) != 1 || !strings.HasPrefix(f.dial.created[0], "default/") {
		t.Errorf("dial Pods %v, want one in the namespace default", f.dial.created)
	}
	if n := len(r.run.Steps); n != 9 {
		t.Errorf("%d steps, want 9 (3 reads and 4 steps of the fence in region-a, 2 reads in region-b)", n)
	}
	one := aipG06Good(t)
	aipG06Run(t, one, []kube.Region{{ID: "region-a"}}, "")
	for _, s := range []string{"expired", "valid", "rejected", "conflict"} {
		g := aipG06Good(t)
		g.stats = `{"credential": {"state": "` + s + `"}}`
		aipG06Run(t, g, twoRegions(), "")
	}
}

// TestAipG06Check_Faults: each clause can fail, and the reason names it.
func TestAipG06Check_Faults(t *testing.T) {
	for name, c := range map[string]struct {
		change func(f *aipG06Fake)
		want   string
	}{
		"no ready replica": {func(f *aipG06Fake) {
			f.deployment["region-a"] = `{"metadata": {"generation": 3}, "spec": {"replicas": 1}, "status": {"observedGeneration": 3, "replicas": 1}}`
		}, "has 1 replicas, 0 ready, want 1"},
		"status not current": {func(f *aipG06Fake) {
			f.deployment["region-a"] = `{"metadata": {"generation": 4}, "spec": {"replicas": 1}, "status": {"observedGeneration": 3, "replicas": 1, "readyReplicas": 1}}`
		}, "is not current"},
		"no status": {func(f *aipG06Fake) {
			f.deployment["region-a"] = `{"metadata": {"generation": 1}, "spec": {"replicas": 1}}`
		}, "is not current"},
		"no spec.replicas": {func(f *aipG06Fake) {
			f.deployment["region-a"] = `{"metadata": {"generation": 1}, "status": {"observedGeneration": 1}}`
		}, "states no spec.replicas"},
		"two replicas": {func(f *aipG06Fake) {
			f.deployment["region-a"] = `{"metadata": {"generation": 1}, "spec": {"replicas": 2}, "status": {"observedGeneration": 1, "replicas": 2, "readyReplicas": 2}}`
		}, "asks for 2 replicas, want 1"},
		"no Deployment":  {func(f *aipG06Fake) { delete(f.deployment, "region-a") }, "get deployment axon: NotFound"},
		"stats 503":      {func(f *aipG06Fake) { f.code = http.StatusServiceUnavailable }, "answered 503, want 200"},
		"unknown state":  {func(f *aipG06Fake) { f.stats = `{"credential": {"state": "stored"}}` }, `the credential state "stored"`},
		"empty state":    {func(f *aipG06Fake) { f.stats = `{"credential": {"state": ""}}` }, `the credential state ""`},
		"no credential":  {func(f *aipG06Fake) { f.stats = `{"turns": {}}` }, "has no credential.state"},
		"stats not JSON": {func(f *aipG06Fake) { f.stats = `ok` }, "is not JSON"},
		"forward fails": {func(f *aipG06Fake) {
			f.forwardErr = errors.New("[region-a] kubectl port-forward axon/axon printed no local address")
		}, "printed no local address"},
		"document in Secret": {func(f *aipG06Fake) {
			f.secrets = `{"items": [{"metadata": {"name": "axon-credential"}, "data": {"document": "e30="}}]}`
		}, "rests in a Secret object: axon-credential/document"},
		"tool file in Secret": {func(f *aipG06Fake) {
			f.secrets = `{"items": [{"metadata": {"name": "axon-claude-auth"}, "data": {".credentials.json": "e30="}}]}`
		}, "axon-claude-auth/.credentials.json"},
		"Secrets not JSON":   {func(f *aipG06Fake) { f.secrets = `[` }, "are not JSON"},
		"dial Pod not Ready": {func(f *aipG06Fake) { f.dial.waitErr = errors.New("timed out waiting for the condition") }, "is not Ready"},
		"API server unreachable": {func(f *aipG06Fake) {
			f.dial.answers["kubernetes.default.svc.cluster.local:443"] = DialTimedOut
		}, "want allow; the dial of kubernetes.default.svc.cluster.local:443 timed-out"},
		"model port open": {func(f *aipG06Fake) {
			f.dial.answers["axon.axon.svc.cluster.local:3000"] = DialConnected
		}, "want deny; the dial of axon.axon.svc.cluster.local:3000 connected"},
		"model port refused": {func(f *aipG06Fake) {
			f.dial.answers["axon.axon.svc.cluster.local:3000"] = DialRefused
		}, "was refused"},
		"replica in another region": {func(f *aipG06Fake) {
			f.deployment["region-b"] = `{"metadata": {"generation": 2}, "spec": {"replicas": 1}, "status": {"observedGeneration": 2, "replicas": 1, "readyReplicas": 1}}`
		}, "asks for 1 replicas, want 0"},
		"another region not current": {func(f *aipG06Fake) {
			f.deployment["region-b"] = `{"metadata": {"generation": 2}, "spec": {"replicas": 0}, "status": {"observedGeneration": 1}}`
		}, "is not current"},
		"another region keeps a Pod": {func(f *aipG06Fake) {
			f.deployment["region-b"] = `{"metadata": {"generation": 2}, "spec": {"replicas": 0}, "status": {"observedGeneration": 2, "replicas": 1}}`
		}, "has 1 replicas, 0 ready, want 0"},
		"no Service in another region": {func(f *aipG06Fake) { f.services["region-b"] = false }, "get service axon: NotFound"},
	} {
		t.Run(name, func(t *testing.T) {
			f := aipG06Good(t)
			c.change(f)
			aipG06Run(t, f, twoRegions(), c.want)
		})
	}
}

// TestAipG06Check_NoRegion: a run without a region is a failure, never a pass.
func TestAipG06Check_NoRegion(t *testing.T) {
	aipG06Run(t, aipG06Good(t), nil, "the run has no region")
}
