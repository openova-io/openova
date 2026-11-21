package huawei

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// The deployment of these tests: Sovereign t1.omani.works, deployment 0123abcd99.
const (
	bdFQDN, bdDep, bdBucket = "t1.omani.works", "0123abcd99", "catalyst-t1-omani-works-0123abcd"
	bdLB, bdNAT             = "catalyst-t1-omani-works-elb-primary", "catalyst-t1-omani-works-b-nat"
)

type bdRule struct {
	kind, gateway string
	deleted       bool
	lag           int // reads that still list the rule after its delete
}

// bdCloud: load balancer lb1 with listeners l1 and l2 whose pools are set (a
// delete is refused with a conflict while the pool is set); gateway gwb with
// rules s1, s2 (rotated, listed once after its delete) and d1; lb9, gw9 and
// rule s9 of another Sovereign, s9 in every unfiltered rule listing.
type bdCloud struct {
	t         *testing.T
	mu        sync.Mutex
	lbs, nats map[string]string    // id -> name
	listeners map[string][2]string // id -> load balancer, default pool
	rules     map[string]*bdRule
	failRead  bool // reads of listeners and rule listings answer 500
	conflicts int
	log       []string
}

func newBDCloud(t *testing.T) *bdCloud {
	return &bdCloud{t: t,
		lbs:       map[string]string{"lb1": bdLB, "lb9": "catalyst-t2-omani-works-elb-primary"},
		nats:      map[string]string{"gwb": bdNAT, "gw9": "catalyst-t2-omani-works-b-nat"},
		listeners: map[string][2]string{"l1": {"lb1", "p1"}, "l2": {"lb1", "p2"}, "l9": {"lb9", "p9"}},
		rules: map[string]*bdRule{"s1": {kind: "snat_rules", gateway: "gwb"}, "s2": {kind: "snat_rules", gateway: "gwb", lag: 1},
			"d1": {kind: "dnat_rules", gateway: "gwb"}, "s9": {kind: "snat_rules", gateway: "gw9"}},
	}
}

func bdAnswer(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func bdNamed(m map[string]string, ids ...string) []map[string]string {
	out := []map[string]string{}
	for _, id := range ids {
		if name, ok := m[id]; ok {
			out = append(out, map[string]string{"id": id, "name": name})
		}
	}
	return out
}

func (c *bdCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, m := r.URL.Path, r.Method
	c.log = append(c.log, m+" "+p)
	seg := strings.Split(strings.Trim(p, "/"), "/")
	last := seg[len(seg)-1]
	l, isL := c.listeners[last]
	isL = isL && strings.Contains(p, "/elb/listeners/")
	if isL && l[0] != "lb1" && m != http.MethodGet {
		c.t.Errorf("%s on listener %s of another Sovereign", m, last)
	}
	switch {
	case m == http.MethodGet && strings.HasSuffix(p, "/elb/loadbalancers"):
		bdAnswer(w, 200, map[string]any{"loadbalancers": bdNamed(c.lbs, "lb1", "lb9"), "request_id": "r"})
	case m == http.MethodGet && strings.Contains(p, "/elb/loadbalancers/") && c.lbs[last] != "":
		ls := []map[string]string{}
		for _, id := range []string{"l1", "l2", "l9"} {
			if x, ok := c.listeners[id]; ok && x[0] == last {
				ls = append(ls, map[string]string{"id": id})
			}
		}
		bdAnswer(w, 200, map[string]any{"loadbalancer": map[string]any{"id": last, "listeners": ls}})
	case m == http.MethodGet && c.failRead && (strings.Contains(p, "/elb/listeners/") || strings.HasSuffix(p, "_rules")):
		bdAnswer(w, 500, map[string]string{"error_msg": "internal"})
	case m == http.MethodGet && isL:
		bdAnswer(w, 200, map[string]any{"listener": map[string]any{"id": last, "default_pool_id": l[1]}})
	case m == http.MethodPut && isL:
		if body, _ := io.ReadAll(r.Body); string(body) != `{"listener":{"default_pool_id":null}}` {
			c.t.Errorf("PUT listener %s with %s", last, body)
		}
		c.listeners[last] = [2]string{l[0], ""}
		bdAnswer(w, 200, map[string]any{"listener": map[string]any{"id": last}})
	case m == http.MethodDelete && isL && l[1] != "":
		c.conflicts++
		bdAnswer(w, 409, map[string]string{"error_msg": "The request could not be processed due to conflict in the request"})
	case m == http.MethodDelete && isL:
		delete(c.listeners, last)
		bdAnswer(w, 204, nil)
	case m == http.MethodGet && strings.HasSuffix(p, "/nat_gateways"):
		bdAnswer(w, 200, map[string]any{"nat_gateways": bdNamed(c.nats, "gwb", "gw9")})
	case m == http.MethodGet && (last == "snat_rules" || last == "dnat_rules"):
		list := []map[string]string{}
		for _, id := range []string{"s1", "s2", "d1", "s9"} {
			if x := c.rules[id]; x.kind == last && (!x.deleted || x.lag > 0) {
				if x.deleted {
					x.lag--
				}
				list = append(list, map[string]string{"id": id, "nat_gateway_id": x.gateway})
			}
		}
		bdAnswer(w, 200, map[string]any{last: list, "request_id": "r"})
	case m == http.MethodDelete && len(seg) >= 4 && seg[len(seg)-4] == "nat_gateways":
		x := c.rules[last]
		if x == nil || x.gateway != seg[len(seg)-3] || x.kind != seg[len(seg)-2] || x.gateway != "gwb" {
			c.t.Errorf("deleted %s: not a rule of the Sovereign's gateway on its own path", p)
		}
		if x == nil || x.deleted {
			bdAnswer(w, 404, nil)
			return
		}
		x.deleted = true
		bdAnswer(w, 204, nil)
	case m == http.MethodGet || m == http.MethodDelete && strings.Contains(p, "/elb/listeners/"):
		bdAnswer(w, 404, nil)
	case m == http.MethodPost && strings.HasSuffix(p, "/resource_instances/action"): // the purge's server listing by tag
		bdAnswer(w, 200, map[string]any{"resources": []any{}})
	default:
		c.t.Errorf("unexpected %s %s", m, p)
		bdAnswer(w, 405, nil)
	}
}

// bdServe points both seams at fakes (the API at cloud; the object store at
// an S3 fake that holds the deployment's bucket with three versions and an
// unfinished upload when bucket is true) and shrinks the waits.
func bdServe(t *testing.T, cloud *bdCloud, bucket bool) *obsFakeS3 {
	s3 := &obsFakeS3{bucket: bdBucket, exists: bucket}
	if bucket {
		s3.versions = []obsFakeVersion{{Key: "wal/1", VersionID: "v1"}, {Key: "wal/1", VersionID: "v2"}, {Key: "base/1", VersionID: "v3", IsDelete: true}}
		s3.uploads = []obsFakeUpload{{Key: "base/2", UploadID: "u1"}}
	}
	api, obs := httptest.NewTLSServer(cloud), httptest.NewTLSServer(s3.handler())
	origAPI, origOBS, origPoll, origL, origN := endpointFor, obsEndpointFor, beforeDestroyPoll, beforeDestroyListenerWait, beforeDestroyNATRuleWait
	endpointFor = func(string, string) string { return api.URL }
	obsEndpointFor = func(string) string { return strings.TrimPrefix(obs.URL, "https://") }
	beforeDestroyPoll, beforeDestroyListenerWait, beforeDestroyNATRuleWait = time.Millisecond, 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() {
		endpointFor, obsEndpointFor, beforeDestroyPoll, beforeDestroyListenerWait, beforeDestroyNATRuleWait = origAPI, origOBS, origPoll, origL, origN
		api.Close()
		obs.Close()
	})
	return s3
}

var bdCreds = providers.ProviderCreds{Raw: map[string]string{"access_key": "ak", "secret_key": "sk", "project_id": "proj"}}

func bdRun(t *testing.T, cloud *bdCloud, bucket bool) (*providers.WipeResult, *obsFakeS3) {
	s3 := bdServe(t, cloud, bucket)
	out := &providers.WipeResult{ProviderPurge: map[string][]string{}}
	removeBeforeDestroy(context.Background(), beforeDestroyCloud{client: httpClientFor(bdCreds), hw: hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"},
		region: defaultRegion, sovereignFQDN: bdFQDN, deployment: bdDep, progress: func(string) {}}, out)
	return out, s3
}

var bdNone = map[string][]string{beforeDestroyBuckets: {}, beforeDestroyListeners: {}, beforeDestroyNATRules: {}}

// The steps remove the bucket, then the two listeners, then the three rules
// of the deployment, and the report names each; nothing of another
// Sovereign is touched.
func TestRemoveBeforeDestroy_RemovesTheThreeAndNamesThem(t *testing.T) {
	cloud := newBDCloud(t)
	out, s3 := bdRun(t, cloud, true)
	want := map[string][]string{beforeDestroyBuckets: {bdBucket}, beforeDestroyListeners: {bdLB + "/l1", bdLB + "/l2"},
		beforeDestroyNATRules: {bdNAT + "/snat_rules/s1", bdNAT + "/dnat_rules/d1", bdNAT + "/snat_rules/s2"}}
	if !reflect.DeepEqual(out.ProviderPurge, want) || len(out.Errors) != 0 {
		t.Fatalf("report %v, errors %v; want %v and no error", out.ProviderPurge, out.Errors, want)
	}
	if s3.exists || len(s3.versions) != 0 || len(s3.deletedKeys) != 3 || len(s3.abortedUps) != 1 {
		t.Errorf("bucket exists %v, %d versions left, %d deleted, %d uploads aborted; want gone, 0, 3, 1",
			s3.exists, len(s3.versions), len(s3.deletedKeys), len(s3.abortedUps))
	}
	if cloud.listeners["l9"] != [2]string{"lb9", "p9"} || cloud.conflicts != 0 || cloud.rules["s9"].deleted {
		t.Errorf("listener l9 %v, conflicts %d, rule s9 deleted %v; want l9 untouched, no conflict, s9 kept", cloud.listeners["l9"], cloud.conflicts, cloud.rules["s9"].deleted)
	}
	// One listener at a time: l1 reads as gone before l2 is touched.
	at := func(suffix string, lastOne bool) int {
		n := -1
		for i, l := range cloud.log {
			if strings.HasSuffix(l, suffix) && (n < 0 || lastOne) {
				n = i
			}
		}
		return n
	}
	if g, f := at("/listeners/l1", true), at("/listeners/l2", false); g < 0 || f < 0 || g > f || !strings.HasPrefix(cloud.log[g], "GET ") {
		t.Errorf("last request on l1 at %d, first on l2 at %d; want l1 read back as gone first (log %v)", g, f, cloud.log)
	}
}

// A read that fails is never taken for gone: the step names what it could
// not remove and reports nothing for it, and the next step still runs.
func TestRemoveBeforeDestroy_AFailedReadIsNotGone(t *testing.T) {
	cloud := newBDCloud(t)
	cloud.failRead = true
	out, _ := bdRun(t, cloud, false)
	text := strings.Join(out.Errors, "\n")
	if !reflect.DeepEqual(out.ProviderPurge, bdNone) || len(out.Errors) != 2 {
		t.Fatalf("report %v, errors %v; want %v and one error per stopped step", out.ProviderPurge, out.Errors, bdNone)
	}
	for _, w := range []string{beforeDestroyListeners + ": listener l1 of " + bdLB + ": still present", "listener l2 of " + bdLB + ": still present",
		beforeDestroyNATRules + ": NAT gateway " + bdNAT + ": its rules could not be read"} {
		if !strings.Contains(text, w) {
			t.Errorf("errors %q; want one containing %q", text, w)
		}
	}
}

// A deployment that holds none of the three: nothing is written, and each
// step is still named, with an empty list.
func TestRemoveBeforeDestroy_NothingToRemove(t *testing.T) {
	cloud := newBDCloud(t)
	cloud.lbs, cloud.nats = map[string]string{}, map[string]string{}
	out, _ := bdRun(t, cloud, false)
	if !reflect.DeepEqual(out.ProviderPurge, bdNone) || len(out.Errors) != 0 || strings.Contains(strings.Join(cloud.log, " "), "DELETE") {
		t.Fatalf("report %v, errors %v, requests %v; want %v, no error and no delete", out.ProviderPurge, out.Errors, cloud.log, bdNone)
	}
}

// Wipe runs the steps before the destroy: when the destroy starts, the
// bucket, the listeners and the rules are gone, and the destroy finishes.
func TestWipe_RemovesBeforeTheDestroy(t *testing.T) {
	cloud := newBDCloud(t)
	s3 := bdServe(t, cloud, true)
	calls, orig := 0, tofuDestroy
	tofuDestroy = func(_ context.Context, req provisioner.Request, _ chan<- provisioner.Event) error {
		calls++
		cloud.mu.Lock()
		defer cloud.mu.Unlock()
		_, l1 := cloud.listeners["l1"]
		_, l2 := cloud.listeners["l2"]
		if req.DeploymentID != bdDep || l1 || l2 || !cloud.rules["s1"].deleted || !cloud.rules["s2"].deleted || !cloud.rules["d1"].deleted || s3.exists {
			t.Errorf("when the destroy of %s starts: listeners %v, rules s1 %v s2 %v d1 %v deleted, bucket there %v; want all gone", req.DeploymentID,
				cloud.listeners, cloud.rules["s1"].deleted, cloud.rules["s2"].deleted, cloud.rules["d1"].deleted, s3.exists)
		}
		delete(cloud.lbs, "lb1") // the destroy removes the rest
		delete(cloud.nats, "gwb")
		return nil
	}
	t.Cleanup(func() { tofuDestroy = orig })
	out, err := New().Wipe(context.Background(), providers.WipeSpec{DeploymentID: bdDep, SovereignFQDN: bdFQDN, Creds: bdCreds}, nil)
	if err != nil || calls != 1 || !out.TofuDestroyed || strings.Contains(strings.Join(out.Errors, "\n"), "before_destroy_") {
		t.Fatalf("Wipe error %v, destroy calls %d, report %+v; want nil, 1, the destroy finished and no step error", err, calls, out)
	}
	if p := out.ProviderPurge; !reflect.DeepEqual(p[beforeDestroyBuckets], []string{bdBucket}) || len(p[beforeDestroyListeners]) != 2 || len(p[beforeDestroyNATRules]) != 3 {
		t.Errorf("report %v; want the bucket, 2 listeners and 3 rules", p)
	}
}
