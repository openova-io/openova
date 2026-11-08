package suite

import (
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

func TestPickOwnOrg(t *testing.T) {
	items := []OrgItem{{Subdomain: "acme", Kind: "customer"}, {Subdomain: "hw307-omani-works", Kind: "internal"}}
	o, err := PickOwnOrg(items, "", "hw307-omani-works")
	if err != nil || o.Subdomain != "hw307-omani-works" {
		t.Fatalf("got %v %v", o, err)
	}
	if _, err := PickOwnOrg(items, "nope", ""); err == nil {
		t.Fatal("unknown explicit org accepted — the suite would fabricate a reference")
	}
	two := append(items, OrgItem{Subdomain: "ops", Kind: "internal"})
	if _, err := PickOwnOrg(two, "", "x"); err == nil {
		t.Fatal("ambiguous internal orgs resolved silently")
	}
	if o, _ := PickOwnOrg(two, "", "hw307-omani-works"); o.Subdomain != "hw307-omani-works" {
		t.Fatal("dashed-FQDN tie-break failed")
	}
}

func TestPickEnvironment(t *testing.T) {
	envs := []EnvInfo{
		{Name: "o-prod", Org: "o", Placement: "single-region", Regions: 1},
		{Name: "o-cp", Org: "o", Placement: "multi-region", Regions: 2},
		{Name: "p-cp", Org: "p", Placement: "multi-region", Regions: 2},
	}
	e, err := PickEnvironment(envs, "o", "")
	if err != nil || e.Name != "o-cp" {
		t.Fatalf("got %v %v", e, err)
	}
	if _, err := PickEnvironment(envs, "o", "p-cp"); err == nil {
		t.Fatal("another Org's Environment accepted")
	}
	if _, err := PickEnvironment(envs, "q", ""); err == nil {
		t.Fatal("Org without a multi-region Environment resolved")
	}
}

func TestDNSServesRegion(t *testing.T) {
	m, err := ParseRegionIPs("a=1.1.1.1,1.1.1.2; b=2.2.2.2")
	if err != nil {
		t.Fatal(err)
	}
	if err := DNSServesRegion([]string{"2.2.2.2"}, m, "b"); err != nil {
		t.Fatalf("correct answer refused: %v", err)
	}
	if err := DNSServesRegion([]string{"1.1.1.1"}, m, "b"); err == nil {
		t.Fatal("answer in the OTHER region accepted")
	}
	if err := DNSServesRegion([]string{"2.2.2.2", "9.9.9.9"}, m, "b"); err == nil {
		t.Fatal("stray answer accepted")
	}
	if err := DNSServesRegion(nil, m, "b"); err == nil {
		t.Fatal("empty answer accepted")
	}
	shared := map[string][]string{"a": {"5.5.5.5"}, "b": {"5.5.5.5"}}
	if err := DNSServesRegion([]string{"5.5.5.5"}, shared, "b"); err == nil || !strings.Contains(err.Error(), "share") {
		t.Fatalf("overlapping region IPs must be undecidable, got %v", err)
	}
	if _, err := ParseRegionIPs("garbage"); err == nil {
		t.Fatal("bad region-ips accepted")
	}
}

func TestClusterHealthyAndDeclaredWriter(t *testing.T) {
	ok := kube.Object{"metadata": map[string]any{"name": "db"},
		"spec":   map[string]any{"instances": float64(2), "replica": map[string]any{"primary": "db", "self": "db"}},
		"status": map[string]any{"readyInstances": float64(2), "phase": "Cluster in healthy state", "currentPrimary": "db-1"}}
	if h, d := ClusterHealthy(ok); !h {
		t.Fatalf("healthy cluster refused: %s", d)
	}
	if !DeclaredWriter(ok) {
		t.Fatal("primary==self is the writer")
	}
	bad := kube.Object{"spec": map[string]any{"instances": float64(2), "replica": map[string]any{"primary": "x", "self": "db"}},
		"status": map[string]any{"readyInstances": float64(1), "phase": "Cluster in healthy state", "currentPrimary": "db-1"}}
	if h, _ := ClusterHealthy(bad); h {
		t.Fatal("1/2 ready accepted as healthy")
	}
	if DeclaredWriter(bad) {
		t.Fatal("primary!=self declared writer")
	}
	legacy := kube.Object{"spec": map[string]any{"replica": map[string]any{"enabled": true}}}
	if DeclaredWriter(legacy) {
		t.Fatal("replica.enabled=true declared writer")
	}
}

func TestMovePrimary(t *testing.T) {
	ts := []Target{{Region: "A", Cluster: "ca", Role: "Primary"}, {Region: "B", Cluster: "cb", Role: "Standby", StandbyType: "Hot"}}
	out, err := MovePrimary(ts, "B")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Role != "Standby" || out[0].StandbyType != "Hot" || out[1].Role != "Primary" || out[1].StandbyType != "" {
		t.Fatalf("wrong targets %+v", out)
	}
	if ts[0].Role != "Primary" {
		t.Fatal("input mutated")
	}
	if _, err := MovePrimary(ts, "C"); err == nil {
		t.Fatal("move to a non-target region accepted")
	}
}

func TestFindTransition(t *testing.T) {
	since := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	ts := func(m int) *string {
		s := since.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano)
		return &s
	}
	jobs := []Job{
		{ID: "d:tt-old", JobName: "topology-transition-accept-1", Kind: TransitionKind, Type: "group", AppID: "accept-1", Status: "succeeded", StartedAt: ts(-60)},
		{ID: "d:tt", JobName: "topology-transition-accept-1", Kind: TransitionKind, Type: "group", AppID: "accept-1", Status: "succeeded", StartedAt: ts(1)},
		{ID: "d:tt-1", JobName: "traffic-off", Kind: TransitionKind, ParentID: "d:tt", Status: "succeeded"},
		{ID: "d:tt-2", JobName: "data-primary", Kind: TransitionKind, ParentID: "d:tt", Status: "succeeded"},
		{ID: "d:other", JobName: "install-x", Kind: "install", AppID: "accept-1", Status: "succeeded"},
	}
	v := FindTransition(jobs, "accept-1", since)
	if !v.Done || v.Parent != "d:tt" || len(v.Steps) != 2 {
		t.Fatalf("complete record not found: %+v", v)
	}
	jobs[3].Status = "running"
	if v := FindTransition(jobs, "accept-1", since); v.Done {
		t.Fatal("a running step accepted as complete")
	}
	noSteps := []Job{jobs[1]}
	if v := FindTransition(noSteps, "accept-1", since); v.Done {
		t.Fatal("a record without step rows accepted")
	}
	if v := FindTransition(jobs[:1], "accept-1", since); v.Done || v.Parent != "" {
		t.Fatal("a PREVIOUS move's record accepted for this move")
	}
	if v := FindTransition(jobs, "accept-2", since); v.Done {
		t.Fatal("another app's record accepted")
	}
}

func TestFinalizeNeverGreensUnmeasured(t *testing.T) {
	r := &Run{Mode: "run", Steps: []*Step{{Journey: "D0", Status: Pass}, {Journey: "J1", Status: Pass}}}
	r.Finalize([]string{"J1", "J2"})
	if r.Result != Fail || r.Journeys["J2"] != Skip {
		t.Fatalf("a requested journey with no steps read green: %+v", r.Journeys)
	}
	r = &Run{Mode: "run", Steps: []*Step{{Journey: "D0", Status: Pass}, {Journey: "J1", Status: Pass}, {Journey: "J1", Status: Fail}}}
	r.Finalize([]string{"J1"})
	if r.Result != Fail || r.Journeys["J1"] != Fail {
		t.Fatal("a failed step did not fail its journey")
	}
	r = &Run{Mode: "run", Steps: []*Step{{Journey: "D0", Status: Pass}, {Journey: "J1", Status: Pass}}}
	r.Finalize([]string{"J1"})
	if r.Result != Pass {
		t.Fatal("CONTROL: an all-pass run must pass")
	}
	r = &Run{Mode: "discover", Steps: []*Step{{Journey: "D0", Status: Pass}}}
	r.Finalize(nil)
	if len(r.Journeys) != 0 {
		t.Fatal("discover must never certify a journey")
	}
}
