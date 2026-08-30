package huawei

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
)

// newTestClient points every service at one httptest server.
func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *metrics.Registry) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	reg := metrics.New()
	c := NewClient(srv.URL+"/%s/%s", false, 5*time.Second, reg)
	return c, reg
}

func TestVerifyClassifiesGatewayResponses(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		wantCode     string
		unauthorized bool
		notPublished bool
	}{
		{"ok", 200, `{"count":13,"servers":[]}`, "", false, false},
		{"not-published", 404, `{"error_code":"APIGW.0101","error_msg":"The API does not exist or has not been published in the environment"}`, "APIGW.0101", false, true},
		{"no-auth", 401, `{"error_code":"APIGW.0301","error_msg":"Incorrect IAM authentication information: verify aksk signature fail"}`, "APIGW.0301", true, false},
		{"service-unauthorized", 401, `{"error":{"code":"EPS.0003","message":"policy doesn't allow ecs:cloudServers:list"}}`, "EPS.0003", true, false},
		{"forbidden-plain", 403, `forbidden`, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/ecs/me-east-215/v1/pid/cloudservers/detail") || r.URL.Query().Get("limit") != "1" {
					t.Errorf("unexpected verify request %s %s", r.Method, r.URL.String())
				}
				if r.Header.Get("Authorization") == "" || r.Header.Get("X-Sdk-Date") == "" {
					t.Error("request not signed")
				}
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			})
			err := client.Verify(context.Background(), Credentials{AccessKey: "ak", SecretKey: "sk", ProjectID: "pid"}, "me-east-215")
			if c.status == 200 {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			var ge *GatewayError
			if !errors.As(err, &ge) {
				t.Fatalf("expected GatewayError, got %T %v", err, err)
			}
			if ge.Code != c.wantCode || ge.Unauthorized() != c.unauthorized || ge.NotPublished() != c.notPublished {
				t.Fatalf("got %+v", ge)
			}
		})
	}
}

func TestListECSPaginatesAndMapsFields(t *testing.T) {
	pages := map[string]string{
		"1": `{"count":3,"servers":[{"id":"s1","name":"web-1","status":"ACTIVE","created":"2026-08-01T10:00:00Z","flavor":{"id":"s6.large.2","name":"s6.large.2","vcpus":"2","ram":"4096"}},{"id":"s2","name":"db-1","status":"SHUTOFF","created":"2026-08-02T00:00:00Z","flavor":{"id":"c7.xlarge.2","name":"","vcpus":4,"ram":8192}}]}`,
		"2": `{"count":3,"servers":[{"id":"s3","name":"x","status":"ACTIVE","created":"2026-08-03T00:00:00Z","flavor":{"id":"f","name":"f"}}]}`,
	}
	calls := 0
	client, reg := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(pages[r.URL.Query().Get("offset")]))
	})
	// Two pages: the first must be a full page for pagination to continue.
	old := pageLimitForTest(2)
	defer old()
	rs, err := client.ListECS(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || calls != 2 {
		t.Fatalf("got %d resources over %d calls", len(rs), calls)
	}
	if rs[0].Attrs["flavor"] != "s6.large.2" || rs[0].Attrs["vcpus"] != int64(2) || rs[0].Attrs["ram_mb"] != int64(4096) || rs[0].Created.IsZero() {
		t.Fatalf("ecs attrs = %+v", rs[0])
	}
	if rs[1].Attrs["flavor"] != "c7.xlarge.2" || rs[1].Status != "SHUTOFF" || rs[1].Attrs["vcpus"] != int64(4) {
		t.Fatalf("ecs fallback flavor = %+v", rs[1])
	}
	if reg.Get("chargeback_cloud_api_calls_total", map[string]string{"service": "ecs", "status": "200"}) != 2 {
		t.Fatal("api call counter not incremented")
	}
}

func TestListOtherKindsMapFields(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/cloudvolumes/detail"):
			w.Write([]byte(`{"volumes":[{"id":"v1","name":"pvc-1","size":100,"volume_type":"SSD","status":"in-use","created_at":"2026-08-01T00:00:00.000000","attachments":[{"server_id":"s1","device":"/dev/vdb"}]}]}`))
		case strings.Contains(r.URL.Path, "/publicips"):
			w.Write([]byte(`{"publicips":[{"id":"e1","public_ip_address":"10.0.0.1","bandwidth_size":5,"status":"ACTIVE","create_time":"2026-08-01 00:00:00"}]}`))
		case strings.Contains(r.URL.Path, "/elb/loadbalancers"):
			w.Write([]byte(`{"loadbalancers":[{"id":"l1","name":"lb","created_at":"2026-08-01T00:00:00Z","provisioning_status":"ACTIVE"}],"page_info":{"next_marker":""}}`))
		case strings.Contains(r.URL.Path, "/nat_gateways"):
			w.Write([]byte(`{"nat_gateways":[{"id":"n1","name":"nat","spec":"1","status":"ACTIVE","created_at":"2026-08-01 00:00:00.418723"}]}`))
		case strings.Contains(r.URL.Path, "/cloudservers/detail"):
			w.Write([]byte(`{"servers":[]}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"error_code":"APIGW.0101","error_msg":"not published"}`))
		}
	})
	rs, failed := client.ListAll(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r")
	if len(failed) != 0 {
		t.Fatalf("failed kinds: %v", failed)
	}
	byKind := map[string]Resource{}
	for _, r := range rs {
		byKind[r.Kind] = r
	}
	if v := byKind[KindEVS]; v.Attrs["size_gb"] != 100 || v.Attrs["attached_to"] != "s1" || v.Created.IsZero() {
		t.Fatalf("evs = %+v", v)
	}
	if e := byKind[KindEIP]; e.Attrs["bandwidth_mbps"] != 5 || e.Name != "10.0.0.1" || e.Created.IsZero() {
		t.Fatalf("eip = %+v", e)
	}
	if l := byKind[KindELB]; l.Name != "lb" || l.Created.IsZero() {
		t.Fatalf("elb = %+v", l)
	}
	if n := byKind[KindNAT]; n.Attrs["spec"] != "1" || n.Created.IsZero() {
		t.Fatalf("nat = %+v", n)
	}
}

func TestListAllReportsFailedKindsWithoutDroppingOthers(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/nat_gateways") {
			w.WriteHeader(404)
			w.Write([]byte(`{"error_code":"APIGW.0101","error_msg":"not published"}`))
			return
		}
		w.Write([]byte(`{"servers":[{"id":"s1","name":"a","status":"ACTIVE","flavor":{"name":"f"}}],"volumes":[],"publicips":[],"loadbalancers":[]}`))
	})
	rs, failed := client.ListAll(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r")
	if len(rs) != 1 || rs[0].Kind != KindECS {
		t.Fatalf("resources = %+v", rs)
	}
	var ge *GatewayError
	if !errors.As(failed[KindNAT], &ge) || !ge.NotPublished() {
		t.Fatalf("nat failure = %v", failed[KindNAT])
	}
}

// TestSecretNeverAppearsInErrorsOrLogs is the "no secret in logs" assertion
// for the cloud client: a rejected credential produces an error and a log line
// that carry the gateway code but never the secret key.
func TestSecretNeverAppearsInErrorsOrLogs(t *testing.T) {
	const secret = "TESTSECRETKEY-not-real-9f8e7d6c5b4a"
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo everything the request carried, the way a chatty gateway might.
		w.WriteHeader(401)
		w.Write([]byte(`{"error_code":"APIGW.0301","error_msg":"verify aksk signature fail, canonical_request: ` + r.Header.Get("Authorization") + `"}`))
	})
	var logbuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logbuf, nil))
	err := client.Verify(context.Background(), Credentials{AccessKey: "AKTEST", SecretKey: secret, ProjectID: "pid"}, "r")
	if err == nil {
		t.Fatal("expected error")
	}
	logger.Warn("verification failed", "error", err)
	for name, s := range map[string]string{"error": err.Error(), "log": logbuf.String()} {
		if strings.Contains(s, secret) {
			t.Fatalf("%s leaks the secret key: %s", name, s)
		}
		if !strings.Contains(s, "APIGW.0301") {
			t.Fatalf("%s lacks the gateway code: %s", name, s)
		}
	}
	// Transport errors are sanitized too.
	if got := sanitizeErr(errors.New("dial https://x?sk="+secret), Credentials{SecretKey: secret}); strings.Contains(got.Error(), secret) {
		t.Fatal("sanitizeErr left the secret in place")
	}
}

func TestClassifyTrace(t *testing.T) {
	cases := []struct {
		name string
		in   Trace
		op   Op
		ok   bool
	}{
		{"create", Trace{TraceName: "createServer", ResourceID: "s1", TraceStatus: "normal", Code: "200", Time: 1756641600000}, OpCreate, true},
		{"delete", Trace{TraceName: "deleteVolume", ResourceID: "v1", TraceStatus: "normal", Code: "202"}, OpDelete, true},
		{"resize", Trace{TraceName: "resizeServer", ResourceID: "s1"}, OpResize, true},
		{"stop", Trace{TraceName: "stopServer", ResourceID: "s1", TraceStatus: "normal"}, OpStop, true},
		{"start", Trace{TraceName: "startServer", ResourceID: "s1", TraceStatus: "normal"}, OpStart, true},
		{"batch-stop", Trace{TraceName: "batchStopServers", ResourceID: "s1"}, "", false},
		{"failed", Trace{TraceName: "createServer", ResourceID: "s1", TraceStatus: "incident", Code: "500"}, "", false},
		{"unrelated", Trace{TraceName: "listServers", ResourceID: "s1"}, "", false},
		{"no-resource", Trace{TraceName: "createServer"}, "", false},
	}
	for _, c := range cases {
		ev, ok := ClassifyTrace(c.in)
		if ok != c.ok || ev.Op != c.op {
			t.Errorf("%s: got (%v, %v) want (%v, %v)", c.name, ev.Op, ok, c.op, c.ok)
		}
		if ok && c.in.Time != 0 && !ev.At.Equal(time.UnixMilli(c.in.Time).UTC()) {
			t.Errorf("%s: time not parsed: %v", c.name, ev.At)
		}
	}
}

func TestCPUUtilHourly(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("namespace") != "SYS.ECS" || q.Get("metric_name") != "cpu_util" || q.Get("dim.0") != "instance_id,s1" || q.Get("period") != "3600" || q.Get("filter") != "average" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"datapoints":[{"average":12.5,"timestamp":1756641600000,"unit":"%"}],"metric_name":"cpu_util"}`))
	})
	pts, err := client.CPUUtilHourly(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r", "s1", time.Now().Add(-time.Hour), time.Now())
	if err != nil || len(pts) != 1 || pts[0].Average != 12.5 {
		t.Fatalf("pts=%v err=%v", pts, err)
	}
}

func TestListTracesPaginates(t *testing.T) {
	old := pageLimitForTest(1)
	defer old()
	calls := 0
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("trace_type") != "system" {
			t.Errorf("trace_type missing: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("next") == "" {
			w.Write([]byte(`{"traces":[{"trace_id":"t1","trace_name":"createServer","resource_id":"s1","time":1756641600000}],"meta_data":{"count":1,"marker":"m1"}}`))
			return
		}
		w.Write([]byte(`{"traces":[{"trace_id":"t2","trace_name":"deleteServer","resource_id":"s2","time":1756645200000}],"meta_data":{"count":1,"marker":""}}`))
	})
	trs, err := client.ListTraces(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r", time.Now().Add(-time.Hour), time.Now())
	if err != nil || len(trs) != 2 || calls != 2 {
		t.Fatalf("traces=%d calls=%d err=%v", len(trs), calls, err)
	}
}
