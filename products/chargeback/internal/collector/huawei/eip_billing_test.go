package huawei

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Elastic-IP billing shape (#6867).
//
// The collector used to bill EVERY address on its PROVISIONED bandwidth —
// bandwidth_size × hours — because the lister never read the charge mode.
// That is right for a fixed-bandwidth address and it is why bandwidth
// dominates the bill, but it is badly wrong for the two shapes it cannot
// see: an address the cloud bills by TRAFFIC reserves no pipe at all, and
// several addresses sharing ONE pipe each report the whole pipe's size, so
// the same reservation is billed once per address.

const (
	bandwidthsJSON = `{"bandwidths":[
	 {"id":"bw-ded","name":"catalyst-a-bw","size":100,"share_type":"PER","charge_mode":"bandwidth","status":"NORMAL",
	  "publicip_info":[{"publicip_id":"eip-1","publicip_address":"10.0.0.1"}]},
	 {"id":"bw-metered","name":"catalyst-b-bw","size":300,"share_type":"PER","charge_mode":"traffic","status":"NORMAL",
	  "publicip_info":[{"publicip_id":"eip-2","publicip_address":"10.0.0.2"}]},
	 {"id":"bw-shared","name":"catalyst-shared-bw","size":300,"share_type":"WHOLE","charge_mode":"bandwidth","status":"NORMAL",
	  "publicip_info":[{"publicip_id":"eip-3","publicip_address":"10.0.0.3"},{"publicip_id":"eip-4","publicip_address":"10.0.0.4"},{"publicip_id":"eip-5","publicip_address":"10.0.0.5"}]}
	]}`
	// Every address reports bandwidth_size 300 for the shared pipe — that
	// is the trap: read address by address it looks like 900 Mbps.
	publicIPsJSON = `{"publicips":[
	 {"id":"eip-1","public_ip_address":"10.0.0.1","bandwidth_id":"bw-ded","bandwidth_size":100,"bandwidth_name":"catalyst-a-bw","status":"ACTIVE","create_time":"2026-09-01 00:00:00","type":"5_bgp"},
	 {"id":"eip-2","public_ip_address":"10.0.0.2","bandwidth_id":"bw-metered","bandwidth_size":300,"bandwidth_name":"catalyst-b-bw","status":"ACTIVE","create_time":"2026-09-01 00:00:00","type":"5_bgp"},
	 {"id":"eip-3","public_ip_address":"10.0.0.3","bandwidth_id":"bw-shared","bandwidth_size":300,"bandwidth_name":"catalyst-shared-bw","status":"ACTIVE","create_time":"2026-09-01 00:00:00","type":"5_bgp"},
	 {"id":"eip-4","public_ip_address":"10.0.0.4","bandwidth_id":"bw-shared","bandwidth_size":300,"bandwidth_name":"catalyst-shared-bw","status":"ACTIVE","create_time":"2026-09-01 00:00:00","type":"5_bgp"},
	 {"id":"eip-5","public_ip_address":"10.0.0.5","bandwidth_id":"bw-shared","bandwidth_size":300,"bandwidth_name":"catalyst-shared-bw","status":"ACTIVE","create_time":"2026-09-01 00:00:00","type":"5_bgp"}
	]}`
	notPublishedJSON = `{"error_code":"APIGW.0101","error_msg":"The API does not exist or has not been published in the environment"}`
)

// eipClient serves the publicips listing, and serves the bandwidths listing
// only when withBandwidths — a gateway that does not publish it answers the
// same 404 APIGW.0101 the real one does.
func eipClient(t *testing.T, withBandwidths bool) *Client {
	t.Helper()
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bandwidths"):
			if !withBandwidths {
				w.WriteHeader(404)
				w.Write([]byte(notPublishedJSON))
				return
			}
			w.Write([]byte(bandwidthsJSON))
		case strings.HasSuffix(r.URL.Path, "/publicips"):
			w.Write([]byte(publicIPsJSON))
		default:
			w.WriteHeader(404)
			w.Write([]byte(notPublishedJSON))
		}
	})
	return c
}

func listEIPs(t *testing.T, withBandwidths bool) map[string]Resource {
	t.Helper()
	rs, err := eipClient(t, withBandwidths).ListEIP(context.Background(),
		Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "me-east-215")
	if err != nil {
		t.Fatalf("ListEIP: %v", err)
	}
	byID := map[string]Resource{}
	for _, r := range rs {
		byID[r.ID] = r
	}
	return byID
}

// reservedMbps is the total bandwidth the price book would bill across the
// whole listing: Σ over resources of the eip.bandwidth_mbps multiplier.
func reservedMbps(rs map[string]Resource) float64 {
	var total float64
	for _, r := range rs {
		for _, sku := range SKUsFor(r.Kind, r.Attrs, "") {
			if sku.Name == SKUEIPBandwidth {
				total += sku.Multiplier
			}
		}
	}
	return total
}

func skuNames(kind string, attrs map[string]any) []string {
	var out []string
	for _, s := range SKUsFor(kind, attrs, "") {
		out = append(out, s.Name)
	}
	return out
}

// TestEIPChargeModeAndShareTypeRoundTrip: the billing shape lives on the
// BANDWIDTH, not on the address, so the lister has to join the two. What it
// captures is what every later decision is driven off.
func TestEIPChargeModeAndShareTypeRoundTrip(t *testing.T) {
	rs := listEIPs(t, true)
	for _, tc := range []struct {
		id, chargeMode, shareType, bandwidthID string
	}{
		{"eip-1", ChargeModeBandwidth, ShareTypePer, "bw-ded"},
		{"eip-2", ChargeModeTraffic, ShareTypePer, "bw-metered"},
		{"eip-3", ChargeModeBandwidth, ShareTypeWhole, "bw-shared"},
		{"eip-5", ChargeModeBandwidth, ShareTypeWhole, "bw-shared"},
	} {
		r, ok := rs[tc.id]
		if !ok {
			t.Fatalf("%s not listed", tc.id)
		}
		if got := str(r.Attrs[attrChargeMode]); got != tc.chargeMode {
			t.Fatalf("%s charge mode = %q, want %q", tc.id, got, tc.chargeMode)
		}
		if got := str(r.Attrs[attrShareType]); got != tc.shareType {
			t.Fatalf("%s share type = %q, want %q", tc.id, got, tc.shareType)
		}
		if got := BandwidthIDOf(r.Attrs); got != tc.bandwidthID {
			t.Fatalf("%s bandwidth id = %q, want %q", tc.id, got, tc.bandwidthID)
		}
	}
	// Exactly one shared pipe becomes a resource of its own; the two
	// dedicated ones do not, because their address already carries them.
	var shared []Resource
	for _, r := range rs {
		if r.Kind == KindBandwidth {
			shared = append(shared, r)
		}
	}
	if len(shared) != 1 || shared[0].ID != "bw-shared" || shared[0].Name != "catalyst-shared-bw" {
		t.Fatalf("shared-pipe resources = %+v, want exactly bw-shared", shared)
	}
	if got := num(shared[0].Attrs["bandwidth_mbps"]); got != 300 {
		t.Fatalf("shared pipe size = %v, want 300", got)
	}
	if got := num(shared[0].Attrs["public_ip_count"]); got != 3 {
		t.Fatalf("shared pipe address count = %v, want 3", got)
	}
	// The pipe's name is what ScopeMatcher attributes an address by (#6859),
	// so the synthetic resource must carry it or it lands out of scope.
	if !(ScopeMatcher{Token: "catalyst-shared"}).Enabled() {
		t.Fatal("scope matcher disabled")
	}
	in, out := (ScopeMatcher{Token: "catalyst-shared"}).Partition([]Resource{shared[0]})
	if len(in) != 1 || len(out) != 0 {
		t.Fatalf("the shared pipe is not attributable by name: in=%d out=%d", len(in), len(out))
	}
}

// TestTrafficBilledAddressStopsBillingAReservationItNeverMade: the whole
// point of reading the charge mode. eip-2 reserves nothing — billing it 300
// Mbps an hour is not a rounding error, it is a charge the cloud never made.
func TestTrafficBilledAddressStopsBillingAReservationItNeverMade(t *testing.T) {
	rs := listEIPs(t, true)
	got := skuNames(KindEIP, rs["eip-2"].Attrs)
	if len(got) != 1 || got[0] != "eip" {
		t.Fatalf("traffic-billed address SKUs = %v, want only the hourly address fee", got)
	}
	// And the address that DOES reserve a pipe is untouched.
	want := []string{"eip", SKUEIPBandwidth}
	if got := skuNames(KindEIP, rs["eip-1"].Attrs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("bandwidth-billed address SKUs = %v, want %v", got, want)
	}
	// The traffic meter and the reservation are never both billable for one
	// address: the meter's SKU is written by the CES sampler, not here.
	for _, s := range SKUsFor(KindEIP, rs["eip-2"].Attrs, "") {
		if s.Name == SKUEIPTraffic {
			t.Fatal("the traffic meter must come from the monitoring sampler, not from the hourly reservation lines")
		}
	}
}

// TestThreeAddressesOnOneSharedPipeBillItOnce: the pipe is 300 Mbps. Read
// address by address — which is all the collector could do before — it bills
// 900.
func TestThreeAddressesOnOneSharedPipeBillItOnce(t *testing.T) {
	rs := listEIPs(t, true)
	var sharedTotal float64
	for _, id := range []string{"eip-3", "eip-4", "eip-5", "bw-shared"} {
		r := rs[id]
		for _, sku := range SKUsFor(r.Kind, r.Attrs, "") {
			if sku.Name == SKUEIPBandwidth {
				sharedTotal += sku.Multiplier
			}
		}
	}
	if sharedTotal != 300 {
		t.Fatalf("three addresses on one shared 300 Mbps pipe bill %v Mbps, want 300", sharedTotal)
	}
	// Across the whole listing: 100 for the dedicated pipe, 0 for the
	// traffic-billed one, 300 for the shared one.
	if got := reservedMbps(rs); got != 400 {
		t.Fatalf("total reserved bandwidth billed = %v Mbps, want 400", got)
	}
	// Each address still pays its own hourly address fee — the shared pipe
	// is a reservation, not a substitute for the addresses.
	fees := 0
	for _, r := range rs {
		for _, sku := range SKUsFor(r.Kind, r.Attrs, "") {
			if sku.Name == "eip" {
				fees++
			}
		}
	}
	if fees != 5 {
		t.Fatalf("hourly address fees = %d, want one per address (5)", fees)
	}
}

// TestGatewayWithoutBandwidthsKeepsTodaysReservationBilling: requirement 6.
// An older gateway that does not publish the bandwidths API reports no
// charge mode at all, and every address must go on billing its reserved size
// exactly as before — under-billing an address because its shape is unknown
// is as wrong as over-billing one.
func TestGatewayWithoutBandwidthsKeepsTodaysReservationBilling(t *testing.T) {
	rs := listEIPs(t, false)
	if len(rs) != 5 {
		t.Fatalf("listed %d resources, want the 5 addresses and no synthetic pipe", len(rs))
	}
	for id, r := range rs {
		if r.Kind != KindEIP {
			t.Fatalf("%s: a gateway with no bandwidths API cannot produce a %s resource", id, r.Kind)
		}
		if _, ok := r.Attrs[attrChargeMode]; ok {
			t.Fatalf("%s carries a charge mode the gateway never reported: %v", id, r.Attrs[attrChargeMode])
		}
		if BillsTraffic(r.Attrs) || SharesBandwidth(r.Attrs) {
			t.Fatalf("%s was guessed into a billing shape", id)
		}
	}
	// 100 + 300 + 3 × 300 — every address on its own provisioned size,
	// which is precisely what the collector billed before this change.
	if got := reservedMbps(rs); got != 1300 {
		t.Fatalf("total reserved bandwidth billed = %v Mbps, want 1300 (today's behaviour)", got)
	}
}

// TestBandwidthsFailureFailsTheKindRatherThanErasingThePipes: a rejected
// credential or a transient 500 must NOT read as "this project has no
// bandwidths". That reading would mark every shared pipe deleted and stop
// billing it, and nothing would look wrong.
func TestBandwidthsFailureFailsTheKindRatherThanErasingThePipes(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bandwidths") {
			w.WriteHeader(401)
			w.Write([]byte(`{"error_code":"APIGW.0301","error_msg":"Incorrect IAM authentication information"}`))
			return
		}
		w.Write([]byte(publicIPsJSON))
	})
	if _, err := c.ListEIP(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r"); err == nil {
		t.Fatal("a rejected bandwidths call must fail the kind, not silently drop every shared pipe")
	} else if !strings.Contains(err.Error(), "APIGW.0301") {
		t.Fatalf("unexpected error: %v", err)
	}
	// And the failure marks BOTH kinds the lister produces, or the caller's
	// "nothing listed" check against SupportedKinds could never fire.
	all, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error_code":"APIGW.0301","error_msg":"Incorrect IAM authentication information"}`))
	})
	_, failed := all.ListAll(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r")
	if failed[KindEIP] == nil || failed[KindBandwidth] == nil {
		t.Fatalf("failed kinds = %v, want both eip and bandwidth", failed)
	}
	if len(failed) != len(SupportedKinds()) {
		t.Fatalf("failed=%d, registry=%d — total rejection would not be detected", len(failed), len(SupportedKinds()))
	}
}

// TestTrafficGBStatesItsConversion: up_stream is a per-period BYTE TOTAL,
// not a cumulative counter, so the hour is the sum divided by 10^9. A
// gateway that answers with only a mean of the per-minute totals is
// multiplied by the 60 raw intervals in the hour. Reading a mean as a total
// under-reports by a factor of 60 on the one meter charged per gigabyte.
func TestTrafficGBStatesItsConversion(t *testing.T) {
	// 2 GB out in the hour, reported as a sum of bytes.
	if got := TrafficGB(Datapoint{Sum: 2e9, Unit: "byte"}); got != 2 {
		t.Fatalf("sum of 2e9 bytes = %v GB, want 2", got)
	}
	// The same hour reported as the mean of its 60 per-minute totals.
	if got := TrafficGB(Datapoint{Average: 2e9 / 60, Unit: "byte"}); got < 1.999999 || got > 2.000001 {
		t.Fatalf("mean of the per-minute totals = %v GB, want 2", got)
	}
	if got := TrafficGB(Datapoint{}); got != 0 {
		t.Fatalf("an empty datapoint = %v GB, want 0", got)
	}
	// Decimal GB, not GiB: 2^30 here would under-report every hour by ~7 %.
	if bytesPerGB != 1e9 {
		t.Fatalf("bytesPerGB = %v; network traffic is sold per 10^9 bytes", bytesPerGB)
	}
	// The direction is pinned: outbound is the billed one, inbound is free.
	if MetricOutboundTraffic != "up_stream" {
		t.Fatalf("traffic metric = %q, want up_stream (outbound)", MetricOutboundTraffic)
	}
}

// TestOutboundTrafficHourlyAsksForTheOutboundSumPerHour pins the query the
// collector actually sends: the direction, the namespace, the dimension and
// the aggregation are all part of the meter's definition.
func TestOutboundTrafficHourlyAsksForTheOutboundSumPerHour(t *testing.T) {
	var q url.Values
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write([]byte(`{"datapoints":[],"metric_name":"up_stream"}`))
	})
	from := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if _, err := c.OutboundTrafficHourly(context.Background(), Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r", "bw-1", from, from.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"namespace":   "SYS.VPC",
		"metric_name": "up_stream",
		"dim.0":       "bandwidth_id,bw-1",
		"period":      "3600",
		"filter":      "sum",
	} {
		if got := q.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
}

// TestSampleCESSplitsBillableTrafficFromObservedTraffic: the two shapes
// write DIFFERENT SKUs. The traffic-billed address writes the billable
// meter; the reservation-billed one writes a never-rated metric, which is
// what keeps "either the reservation or the traffic, never both" true even
// after the operator puts a rate on the meter.
func TestSampleCESSplitsBillableTrafficFromObservedTraffic(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/metric-data") {
			w.WriteHeader(404)
			w.Write([]byte(notPublishedJSON))
			return
		}
		if r.URL.Query().Get("metric_name") != MetricOutboundTraffic {
			w.Write([]byte(`{"datapoints":[]}`))
			return
		}
		hour := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
		json.NewEncoder(w).Encode(map[string]any{"datapoints": []map[string]any{
			{"sum": 3e9, "timestamp": hour.UnixMilli(), "unit": "byte"},
		}, "metric_name": MetricOutboundTraffic})
	}))
	t.Cleanup(srv.Close)

	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	sealed, _ := keys.Seal([]byte(testSecret))
	repo := newFakeRepo()
	repo.addSource("src-eip", "cust", "0123456789abcdef0123456789abcdef", "cred", sealed)
	col := &Collector{
		Store: repo, Keys: keys, Metrics: metrics.New(),
		Client:          NewClient(srv.URL+"/%s/%s", false, 5*time.Second, metrics.New()),
		CollectInterval: 15 * time.Minute, CTSInterval: 5 * time.Minute, CESInterval: time.Hour,
		Now: func() time.Time { return time.Date(2026, 9, 10, 11, 30, 0, 0, time.UTC) },
	}

	seen := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	items := []store.InventoryUpsert{
		{ResourceID: "eip-1", Kind: KindEIP, Name: "10.0.0.1", SeenAt: seen, Attrs: map[string]any{
			attrBandwidthID: "bw-ded", attrChargeMode: ChargeModeBandwidth, attrShareType: ShareTypePer, "bandwidth_mbps": 100, "status": "ACTIVE"}},
		{ResourceID: "eip-2", Kind: KindEIP, Name: "10.0.0.2", SeenAt: seen, Attrs: map[string]any{
			attrBandwidthID: "bw-metered", attrChargeMode: ChargeModeTraffic, attrShareType: ShareTypePer, "bandwidth_mbps": 300, "status": "ACTIVE"}},
		{ResourceID: "eip-3", Kind: KindEIP, Name: "10.0.0.3", SeenAt: seen, Attrs: map[string]any{
			attrBandwidthID: "bw-shared", attrChargeMode: ChargeModeBandwidth, attrShareType: ShareTypeWhole, "bandwidth_mbps": 300, "status": "ACTIVE"}},
		{ResourceID: "bw-shared", Kind: KindBandwidth, Name: "catalyst-shared-bw", SeenAt: seen, Attrs: map[string]any{
			attrBandwidthID: "bw-shared", attrChargeMode: ChargeModeBandwidth, attrShareType: ShareTypeWhole, "bandwidth_mbps": 300, "status": "NORMAL"}},
		{ResourceID: "eip-9", Kind: KindEIP, Name: "10.0.0.9", SeenAt: seen, Attrs: map[string]any{"bandwidth_mbps": 5, "status": "ACTIVE"}},
	}
	if _, err := repo.UpsertInventory(ctx, "src-eip", items); err != nil {
		t.Fatal(err)
	}
	if err := col.SampleCES(ctx, repo.source("src-eip"), col.now()); err != nil {
		t.Fatal(err)
	}

	got := map[string]store.UsageRecord{}
	for _, r := range repo.usage {
		got[r.ResourceID+"|"+r.SKU] = r
	}
	if r, ok := got["eip-2|"+SKUEIPTraffic]; !ok {
		t.Fatalf("the traffic-billed address wrote no billable meter: %v", keysOf(got))
	} else if string(r.Quantity) != "3.000000" || r.Unit != UnitEIPTraffic {
		t.Fatalf("billable traffic = %s %s, want 3.000000 gb", r.Quantity, r.Unit)
	}
	if r, ok := got["eip-1|"+SKUEIPTrafficObserved]; !ok {
		t.Fatalf("the reservation-billed address wrote no observed metric: %v", keysOf(got))
	} else if string(r.Quantity) != "3.000000" || r.Unit != UnitEIPTrafficObserved {
		t.Fatalf("observed traffic = %s %s", r.Quantity, r.Unit)
	}
	// A reservation-billed address never writes the billable meter, or a
	// rate on that meter would bill it twice.
	if _, ok := got["eip-1|"+SKUEIPTraffic]; ok {
		t.Fatal("a reservation-billed address wrote the BILLABLE traffic meter — it would then bill both")
	}
	// The shared pipe is metered once, on the pipe; the addresses hanging
	// off it do not each report the whole pipe's traffic as their own.
	if _, ok := got["bw-shared|"+SKUEIPTrafficObserved]; !ok {
		t.Fatalf("the shared pipe wrote no traffic: %v", keysOf(got))
	}
	for _, sku := range []string{SKUEIPTraffic, SKUEIPTrafficObserved} {
		if _, ok := got["eip-3|"+sku]; ok {
			t.Fatal("an address on a shared pipe reported the pipe's traffic as its own")
		}
	}
	// An address whose pipe the gateway never identified cannot be sampled,
	// and is left billing its reservation.
	for _, sku := range []string{SKUEIPTraffic, SKUEIPTrafficObserved} {
		if _, ok := got["eip-9|"+sku]; ok {
			t.Fatal("an address with no bandwidth id was sampled against an unknown pipe")
		}
	}
}

func keysOf(m map[string]store.UsageRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSampledSKUNamesAgreeWithTheStore: the collector writes these names and
// the store's aggregates filter on them. They are declared in both packages
// (the store cannot import the collector), so the one thing that keeps them
// from drifting is this assertion.
func TestSampledSKUNamesAgreeWithTheStore(t *testing.T) {
	if SKUCPUUtil != store.SKUCPUUtil {
		t.Fatalf("cpu-util SKU: collector %q vs store %q", SKUCPUUtil, store.SKUCPUUtil)
	}
	if SKUEIPTrafficObserved != store.SKUEIPTrafficObserved {
		t.Fatalf("observed-traffic SKU: collector %q vs store %q", SKUEIPTrafficObserved, store.SKUEIPTrafficObserved)
	}
	if SKUEIPTraffic != store.SKUEIPTraffic {
		t.Fatalf("traffic meter SKU: collector %q vs store %q", SKUEIPTraffic, store.SKUEIPTraffic)
	}
	// The two the collector writes as metrics must be excluded from cost;
	// the billable meter must NOT be, or it could never be rated.
	if !store.IsMetricSKU(SKUCPUUtil) || !store.IsMetricSKU(SKUEIPTrafficObserved) {
		t.Fatal("a sampled metric is not excluded from the cost aggregates")
	}
	if store.IsMetricSKU(SKUEIPTraffic) {
		t.Fatal("the billable traffic meter is excluded from cost — it could never be rated")
	}
}

// TestEIPBandwidthSKUNameIsUnchanged: renaming this SKU would orphan every
// rate the operator has already entered, and every historical record.
func TestEIPBandwidthSKUNameIsUnchanged(t *testing.T) {
	if SKUEIPBandwidth != "eip.bandwidth_mbps" || UnitEIPBandwidth != "mbps-hour" {
		t.Fatalf("reservation SKU = %q %q", SKUEIPBandwidth, UnitEIPBandwidth)
	}
	if SKUEIPTraffic != "eip.traffic_gb" || UnitEIPTraffic != "gb" {
		t.Fatalf("traffic meter = %q %q", SKUEIPTraffic, UnitEIPTraffic)
	}
}
