package huawei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

const testSecret = "TESTSECRET-0000-never-real-1111"

// fakeGateway imitates the Kom4DC per-service endpoints with a mutable
// inventory so the tests can add, stop and delete resources between ticks.
type fakeGateway struct {
	mu        sync.Mutex
	servers   []map[string]any
	volumes   []map[string]any
	traces    []map[string]any
	rejectAll bool
	calls     map[string]int
}

func (g *fakeGateway) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.calls == nil {
			g.calls = map[string]int{}
		}
		if r.Header.Get("Authorization") == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "SDK-HMAC-SHA256 Access=AKTEST,") {
			t.Errorf("unsigned request: %s", r.URL.Path)
		}
		if g.rejectAll {
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error_code":"APIGW.0301","error_msg":"verify aksk signature fail, canonical_request: %s"}`, r.Header.Get("Authorization"))
			return
		}
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/cloudservers/detail"):
			g.calls["ecs"]++
			json.NewEncoder(w).Encode(map[string]any{"count": len(g.servers), "servers": g.servers})
		case strings.Contains(p, "/cloudvolumes/detail"):
			g.calls["evs"]++
			json.NewEncoder(w).Encode(map[string]any{"volumes": g.volumes})
		case strings.Contains(p, "/publicips"):
			g.calls["eip"]++
			json.NewEncoder(w).Encode(map[string]any{"publicips": []map[string]any{{"id": "eip-1", "public_ip_address": "10.1.1.1", "bandwidth_size": 5, "status": "ACTIVE", "create_time": "2026-08-30 00:00:00"}}})
		case strings.Contains(p, "/elb/loadbalancers"):
			g.calls["elb"]++
			json.NewEncoder(w).Encode(map[string]any{"loadbalancers": []map[string]any{{"id": "elb-1", "name": "lb", "created_at": "2026-08-30T00:00:00Z"}}, "page_info": map[string]any{"next_marker": ""}})
		case strings.Contains(p, "/nat_gateways"):
			g.calls["nat"]++
			json.NewEncoder(w).Encode(map[string]any{"nat_gateways": []map[string]any{{"id": "nat-1", "name": "nat", "spec": "1", "status": "ACTIVE", "created_at": "2026-08-30 00:00:00.000000"}}})
		case strings.Contains(p, "/traces"):
			g.calls["cts"]++
			json.NewEncoder(w).Encode(map[string]any{"traces": g.traces, "meta_data": map[string]any{"count": len(g.traces), "marker": ""}})
		case strings.Contains(p, "/metric-data"):
			g.calls["ces"]++
			json.NewEncoder(w).Encode(map[string]any{"datapoints": []map[string]any{{"average": 12.5, "timestamp": time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC).UnixMilli(), "unit": "%"}}, "metric_name": "cpu_util"})
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"error_code":"APIGW.0101","error_msg":"not published"}`))
		}
	}
}

func server(id, flavor, status string) map[string]any {
	return map[string]any{"id": id, "name": "vm-" + id, "status": status, "created": "2026-08-30T00:00:00Z",
		"flavor": map[string]any{"id": flavor, "name": flavor, "vcpus": "2", "ram": "4096"}}
}

func setupCollector(t *testing.T) (*Collector, *fakeGateway, store.CostSource, *store.Store) {
	t.Helper()
	st := testdb.Open(t)
	ctx := context.Background()
	gw := &fakeGateway{}
	for i := 1; i <= 13; i++ {
		gw.servers = append(gw.servers, server(fmt.Sprintf("srv-%02d", i), "s6.large.2", "ACTIVE"))
	}
	gw.servers[12]["status"] = "SHUTOFF" // srv-13 is powered off
	gw.volumes = []map[string]any{
		{"id": "vol-1", "name": "data-1", "size": 100, "volume_type": "SSD", "status": "in-use", "created_at": "2026-08-30T00:00:00.000000", "attachments": []map[string]any{{"server_id": "srv-01"}}},
		{"id": "vol-2", "name": "data-2", "size": 40, "volume_type": "SAS", "status": "in-use", "created_at": "2026-08-30T00:00:00.000000", "attachments": []map[string]any{{"server_id": "srv-13"}}},
	}
	srv := httptest.NewServer(gw.handler(t))
	t.Cleanup(srv.Close)

	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{9}, 32))
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "coll", Name: "Collector Co", AdminEmail: "a@coll.example", StartDate: "2026-08-30"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCustomerStatus(ctx, c.ID, "active"); err != nil {
		t.Fatal(err)
	}
	enc, _ := keys.Seal([]byte(testSecret))
	cred, err := st.CreateCredential(ctx, c.ID, "AKTEST", enc)
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-215", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSourceCredential(ctx, src.ID, cred.ID)
	_ = st.SetSourceVerified(ctx, src.ID, "")
	src, _ = st.GetSource(ctx, store.OperatorScope, src.ID)

	col := &Collector{
		Store:           st,
		Client:          NewClient(srv.URL+"/%s/%s", false, 5*time.Second, metrics.New()),
		Keys:            keys,
		Metrics:         metrics.New(),
		CollectInterval: 15 * time.Minute,
		CTSInterval:     5 * time.Minute,
		CESInterval:     time.Hour,
	}
	return col, gw, src, st
}

func sumQty(t *testing.T, recs []store.UsageRecord, sku string) (float64, int) {
	t.Helper()
	var total float64
	n := 0
	for _, r := range recs {
		if r.SKU != sku {
			continue
		}
		var q float64
		fmt.Sscanf(string(r.Quantity), "%g", &q)
		total += q
		n++
	}
	return total, n
}

func TestIntegrationCollectorFillsInventoryAndUsage(t *testing.T) {
	col, gw, src, st := setupCollector(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	col.Now = func() time.Time { return now }

	if err := col.CollectSource(ctx, src, now); err != nil {
		t.Fatalf("collect: %v", err)
	}
	inv, _ := st.ListInventory(ctx, src.ID)
	if len(inv) != 18 {
		t.Fatalf("inventory = %d, want 18 (13 ECS + 2 EVS + EIP + ELB + NAT)", len(inv))
	}
	ecs := 0
	for _, it := range inv {
		if it.Kind == KindECS {
			ecs++
		}
	}
	if ecs != 13 {
		t.Fatalf("ecs inventory = %d", ecs)
	}
	// Window: start_date 2026-08-30 00:00 → now 2026-08-31 12:00 = 36 hours.
	recs, _ := st.ListUsageRecords(ctx, src.ID, "srv-01")
	if q, n := sumQty(t, recs, "ecs.s6.large.2"); q != 36 || n != 36 {
		t.Fatalf("srv-01 instance-hours = %v over %d records", q, n)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "vol-1")
	if q, _ := sumQty(t, recs, "evs.ssd.gb"); q != 3600 {
		t.Fatalf("vol-1 gb-hours = %v", q)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "vol-2")
	if q, _ := sumQty(t, recs, "evs.hdd.gb"); q != 1440 {
		t.Fatalf("vol-2 gb-hours = %v", q)
	}
	if labelOf(recs[0], "server_status") != "SHUTOFF" || labelOf(recs[0], "attached_to") != "srv-13" {
		t.Fatalf("volume attached to a stopped server not labelled: %s", recs[0].Labels)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "eip-1")
	if q, _ := sumQty(t, recs, "eip"); q != 36 {
		t.Fatalf("eip hours = %v", q)
	}
	if q, _ := sumQty(t, recs, "eip.bandwidth_mbps"); q != 180 {
		t.Fatalf("eip bandwidth mbps-hours = %v", q)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "nat-1")
	if q, _ := sumQty(t, recs, "nat.1"); q != 36 {
		t.Fatalf("nat hours = %v", q)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "srv-13")
	if labelOf(recs[0], "status") != "SHUTOFF" {
		t.Fatalf("stopped server not labelled: %s", recs[0].Labels)
	}
	rat, _ := st.UsageForRating(ctx, src.CustomerID, now.Add(-48*time.Hour), now)
	var stoppedECS, stoppedEVS string
	for _, r := range rat {
		switch r.SKU {
		case "ecs.s6.large.2":
			stoppedECS = string(r.StoppedQuantity)
			if r.ResourceCount != 13 || string(r.Quantity) != "468.000000" {
				t.Fatalf("ecs aggregate = %+v", r)
			}
		case "evs.hdd.gb":
			stoppedEVS = string(r.StoppedQuantity)
		}
	}
	if stoppedECS != "36.000000" || stoppedEVS != "1440.000000" {
		t.Fatalf("stopped split ecs=%s evs=%s", stoppedECS, stoppedEVS)
	}
	total, _ := st.UsageCount(ctx, src.ID)

	// Second tick 15 minutes later: the partial hour appears, nothing else
	// duplicates; a third identical tick changes nothing.
	src, _ = st.GetSource(ctx, store.OperatorScope, src.ID)
	if src.LastCollectedAt == nil || !src.LastCollectedAt.Equal(now) {
		t.Fatalf("last_collected_at = %v", src.LastCollectedAt)
	}
	now2 := now.Add(15 * time.Minute)
	col.Now = func() time.Time { return now2 }
	if err := col.CollectSource(ctx, src, now2); err != nil {
		t.Fatal(err)
	}
	total2, _ := st.UsageCount(ctx, src.ID)
	// 13 ECS + 2 EVS + EIP(2 skus) + ELB + NAT = 19 new partial-hour rows.
	if total2 != total+19 {
		t.Fatalf("records after tick 2 = %d, want %d", total2, total+19)
	}
	src, _ = st.GetSource(ctx, store.OperatorScope, src.ID)
	if err := col.CollectSource(ctx, src, now2); err != nil {
		t.Fatal(err)
	}
	if total3, _ := st.UsageCount(ctx, src.ID); total3 != total2 {
		t.Fatalf("re-run not idempotent: %d != %d", total3, total2)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "srv-01")
	last := recs[len(recs)-1]
	if string(last.Quantity) != "0.250000" || !last.WindowEnd.Equal(now2) {
		t.Fatalf("partial hour = %+v", last)
	}

	// A server disappears: it is marked deleted and stops accruing.
	gw.mu.Lock()
	gw.servers = gw.servers[:12]
	gw.mu.Unlock()
	now3 := now.Add(30 * time.Minute)
	src, _ = st.GetSource(ctx, store.OperatorScope, src.ID)
	if err := col.CollectSource(ctx, src, now3); err != nil {
		t.Fatal(err)
	}
	it, _ := st.GetInventoryItem(ctx, src.ID, "srv-13")
	if it.DeletedAt == nil || !it.DeletedAt.Equal(now3) {
		t.Fatalf("srv-13 deleted_at = %v", it.DeletedAt)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "srv-13")
	if q, _ := sumQty(t, recs, "ecs.s6.large.2"); q != 36.5 {
		t.Fatalf("deleted server hours = %v, want 36.5", q)
	}
	if col.Metrics.Get("chargeback_usage_records_written_total", nil) == 0 {
		t.Fatal("usage metric not incremented")
	}
}

func TestIntegrationCTSCorrectsBoundariesAndCESSamples(t *testing.T) {
	col, gw, src, st := setupCollector(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	col.Now = func() time.Time { return now }
	// srv-03 reports no creation time: first_seen is the observation time
	// until the audit trail supplies the exact createServer timestamp.
	gw.mu.Lock()
	gw.servers[2]["created"] = ""
	gw.mu.Unlock()
	if err := col.CollectSource(ctx, src, now); err != nil {
		t.Fatal(err)
	}
	if recs, _ := st.ListUsageRecords(ctx, src.ID, "srv-03"); len(recs) != 0 {
		t.Fatalf("srv-03 accrued before its creation time was known: %d records", len(recs))
	}
	stopAt := time.Date(2026, 8, 31, 10, 30, 0, 0, time.UTC)
	createAt := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)
	gw.mu.Lock()
	gw.traces = []map[string]any{
		{"trace_id": "t-stop", "trace_name": "stopServer", "resource_id": "srv-02", "resource_type": "ecs", "trace_status": "normal", "code": "200", "time": stopAt.UnixMilli()},
		{"trace_id": "t-create", "trace_name": "createServer", "resource_id": "srv-03", "resource_type": "ecs", "trace_status": "normal", "code": "200", "time": createAt.UnixMilli()},
		{"trace_id": "t-ignored", "trace_name": "listServers", "resource_id": "srv-04", "trace_status": "normal", "time": stopAt.UnixMilli()},
	}
	gw.mu.Unlock()
	src, _ = st.GetSource(ctx, store.OperatorScope, src.ID)
	if err := col.PollCTS(ctx, src, now); err != nil {
		t.Fatalf("poll cts: %v", err)
	}
	// srv-02: hour 10 is split at 10:30 into ACTIVE and SHUTOFF halves.
	recs, _ := st.ListUsageRecords(ctx, src.ID, "srv-02")
	var seen []string
	for _, r := range recs {
		if r.WindowStart.Hour() == 10 && r.WindowStart.Day() == 31 {
			seen = append(seen, fmt.Sprintf("%s|%s|%s|%s", r.WindowStart.Format("15:04"), r.Quantity, labelOf(r, "status"), r.RawRef))
		}
	}
	want := []string{"10:00|0.500000|ACTIVE|t-stop", "10:30|0.500000|SHUTOFF|t-stop"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("hour 10 for srv-02 = %v, want %v", seen, want)
	}
	if q, _ := sumQty(t, recs, "ecs.s6.large.2"); q != 36 {
		t.Fatalf("srv-02 total hours after correction = %v", q)
	}
	// srv-03: first_seen moved back from the 2026-08-31 12:00 observation to
	// the exact 2026-08-30 06:00 creation → 30 hours accrue that were unknown
	// before the audit trail was read.
	it, _ := st.GetInventoryItem(ctx, src.ID, "srv-03")
	if !it.FirstSeen.Equal(createAt) {
		t.Fatalf("srv-03 first_seen = %v", it.FirstSeen)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "srv-03")
	if q, _ := sumQty(t, recs, "ecs.s6.large.2"); q != 30 {
		t.Fatalf("srv-03 hours after create correction = %v, want 30", q)
	}
	if labelOf(recs[0], "status") != "ACTIVE" || recs[0].RawRef != "t-create" {
		t.Fatalf("srv-03 corrected record = %+v", recs[0])
	}
	// The next snapshot observes SHUTOFF for srv-02 and adopts the CTS time.
	gw.mu.Lock()
	gw.servers[1]["status"] = "SHUTOFF"
	gw.mu.Unlock()
	now2 := now.Add(15 * time.Minute)
	src, _ = st.GetSource(ctx, store.OperatorScope, src.ID)
	if err := col.CollectSource(ctx, src, now2); err != nil {
		t.Fatal(err)
	}
	it, _ = st.GetInventoryItem(ctx, src.ID, "srv-02")
	var attrs map[string]any
	_ = json.Unmarshal(it.Attrs, &attrs)
	trs := transitionsFrom(attrs)
	if len(trs) != 2 || !trs[1].At.Equal(stopAt) || trs[1].Source != "cts" {
		t.Fatalf("srv-02 transitions = %+v", trs)
	}
	// CES sample writes the informational SKU.
	if err := col.SampleCES(ctx, src, now2); err != nil {
		t.Fatal(err)
	}
	recs, _ = st.ListUsageRecords(ctx, src.ID, "srv-01")
	if q, n := sumQty(t, recs, SKUCPUUtil); n != 1 || q != 12.5 {
		t.Fatalf("cpu_util = %v over %d", q, n)
	}
}

func labelOf(r store.UsageRecord, key string) string {
	var m map[string]any
	_ = json.Unmarshal(r.Labels, &m)
	return str(m[key])
}

// TestIntegrationCollectorFailureNeverStoresOrLogsSecret drives the pass
// through a gateway that echoes the request into its error message and
// asserts the secret key reaches neither last_error nor the log.
func TestIntegrationCollectorFailureNeverStoresOrLogsSecret(t *testing.T) {
	col, gw, src, st := setupCollector(t)
	ctx := context.Background()
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	gw.mu.Lock()
	gw.rejectAll = true
	gw.mu.Unlock()
	col.Now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	col.CollectAll(ctx)
	got, _ := st.GetSource(ctx, store.OperatorScope, src.ID)
	if got.LastError == nil || !strings.Contains(*got.LastError, "APIGW.0301") {
		t.Fatalf("last_error = %v", got.LastError)
	}
	if strings.Contains(*got.LastError, testSecret) {
		t.Fatal("secret stored in last_error")
	}
	if strings.Contains(logbuf.String(), testSecret) {
		t.Fatalf("secret in logs: %s", logbuf.String())
	}
	if !strings.Contains(logbuf.String(), "APIGW.0301") {
		t.Fatalf("failure not logged: %s", logbuf.String())
	}
	if !col.inBackoff(src.ID) {
		t.Fatal("failed source not in backoff")
	}
	if col.Metrics.Get("chargeback_collect_runs_total", map[string]string{"result": "error"}) != 1 {
		t.Fatal("error counter not incremented")
	}
	// While in backoff the source is skipped entirely.
	before := gw.calls["ecs"]
	col.CollectAll(ctx)
	if gw.calls["ecs"] != before {
		t.Fatal("backoff not honoured")
	}
	// Inventory attrs and usage labels never carry the secret either.
	inv, _ := st.ListInventory(ctx, src.ID)
	for _, it := range inv {
		if strings.Contains(string(it.Attrs), testSecret) {
			t.Fatal("secret in inventory attrs")
		}
	}
}
