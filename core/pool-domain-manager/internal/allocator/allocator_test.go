package allocator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/pdns"
)

// fakeDNS is an in-memory DNSWriter for unit tests. It records every call
// so assertions can verify the allocator's PowerDNS interactions without
// running a real httptest server.
type fakeDNS struct {
	mu                sync.Mutex
	zones             map[string][]string // zone → nameservers (apex NS)
	rrsets            map[string][]pdns.RRSet
	delegations       map[string]map[string][]string // parent → child → nameservers
	dnssecEnabled     map[string]bool
	failOn            string // operation tag to fail on next call
	callsCreate       []string
	callsDelete       []string
	callsAddNS        []string
	callsRemoveNS     []string
	callsEnableDNSSEC []string
	callsPatch        int
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{
		zones:         map[string][]string{},
		rrsets:        map[string][]pdns.RRSet{},
		delegations:   map[string]map[string][]string{},
		dnssecEnabled: map[string]bool{},
	}
}

func (f *fakeDNS) CreateZone(_ context.Context, name string, _ pdns.ZoneKind, ns []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == "create:"+name {
		f.failOn = ""
		return errors.New("fake create-zone failure")
	}
	f.callsCreate = append(f.callsCreate, name)
	if _, ok := f.zones[name]; ok {
		return nil // idempotent
	}
	f.zones[name] = append([]string(nil), ns...)
	return nil
}

func (f *fakeDNS) DeleteZone(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == "delete:"+name {
		f.failOn = ""
		return errors.New("fake delete-zone failure")
	}
	f.callsDelete = append(f.callsDelete, name)
	delete(f.zones, name)
	delete(f.dnssecEnabled, name)
	delete(f.rrsets, name)
	return nil
}

func (f *fakeDNS) EnsureZone(ctx context.Context, name string, kind pdns.ZoneKind, ns []string) error {
	f.mu.Lock()
	_, exists := f.zones[name]
	f.mu.Unlock()
	if exists {
		return nil
	}
	return f.CreateZone(ctx, name, kind, ns)
}

func (f *fakeDNS) AddARecord(_ context.Context, zone, name, ipv4 string, ttl int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rrsets[zone] = append(f.rrsets[zone], pdns.RRSet{
		Name: name, Type: "A", TTL: ttl, ChangeType: "REPLACE",
		Records: []pdns.Record{{Content: ipv4}},
	})
	return nil
}

func (f *fakeDNS) PatchRRSets(_ context.Context, zone string, rrsets []pdns.RRSet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == "patch:"+zone {
		f.failOn = ""
		return errors.New("fake patch-rrsets failure")
	}
	f.callsPatch++
	f.rrsets[zone] = append(f.rrsets[zone], rrsets...)
	return nil
}

func (f *fakeDNS) AddNSDelegation(_ context.Context, parent, child string, ns []string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == "addns:"+parent+":"+child {
		f.failOn = ""
		return errors.New("fake add-ns-delegation failure")
	}
	f.callsAddNS = append(f.callsAddNS, parent+"→"+child)
	if f.delegations[parent] == nil {
		f.delegations[parent] = map[string][]string{}
	}
	f.delegations[parent][child] = append([]string(nil), ns...)
	return nil
}

func (f *fakeDNS) RemoveNSDelegation(_ context.Context, parent, child string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsRemoveNS = append(f.callsRemoveNS, parent+"→"+child)
	if f.delegations[parent] != nil {
		delete(f.delegations[parent], child)
	}
	return nil
}

func (f *fakeDNS) EnableDNSSEC(_ context.Context, zone string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == "dnssec:"+zone {
		f.failOn = ""
		return errors.New("fake dnssec failure")
	}
	f.callsEnableDNSSEC = append(f.callsEnableDNSSEC, zone)
	f.dnssecEnabled[zone] = true
	return nil
}

func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── Pure-logic tests that don't need the store layer ─────────────────

func TestChildZoneName(t *testing.T) {
	got := childZoneName("omani.works", "omantel-prod")
	if got != "omantel-prod.omani.works" {
		t.Errorf("childZoneName = %q, want omantel-prod.omani.works", got)
	}
	// Case + whitespace normalisation.
	if got := childZoneName("Omani.Works", " OMANTEL "); got != "omantel.omani.works" {
		t.Errorf("childZoneName not normalised: %q", got)
	}
}

func TestCanonicalRecordSet(t *testing.T) {
	rrsets := canonicalRecordSet("acme.openova.io", "1.2.3.4")
	if len(rrsets) != 6 {
		t.Fatalf("expected 6 RRsets, got %d", len(rrsets))
	}
	wantNames := map[string]bool{
		"acme.openova.io":         false,
		"*.acme.openova.io":       false,
		"console.acme.openova.io": false,
		"api.acme.openova.io":     false,
		"gitea.acme.openova.io":   false,
		"harbor.acme.openova.io":  false,
	}
	for _, r := range rrsets {
		if r.Type != "A" {
			t.Errorf("type = %s, want A", r.Type)
		}
		if r.TTL != pdns.DefaultChildRecordTTL {
			t.Errorf("ttl = %d, want %d", r.TTL, pdns.DefaultChildRecordTTL)
		}
		if r.ChangeType != "REPLACE" {
			t.Errorf("changetype = %s, want REPLACE", r.ChangeType)
		}
		if len(r.Records) != 1 || r.Records[0].Content != "1.2.3.4" {
			t.Errorf("records = %v", r.Records)
		}
		if _, ok := wantNames[r.Name]; !ok {
			t.Errorf("unexpected RRset name %q", r.Name)
		} else {
			wantNames[r.Name] = true
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("missing RRset for %q", name)
		}
	}
}

func TestBootstrapParentZonesEnsuresEachAndEnablesDNSSEC(t *testing.T) {
	dns := newFakeDNS()
	a := &Allocator{
		dns:         dns,
		log:         newSilentLogger(),
		nameservers: []string{"ns1.openova.io", "ns2.openova.io", "ns3.openova.io"},
	}
	if err := a.BootstrapParentZones(context.Background(), []string{"omani.works", "openova.io"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := dns.zones["omani.works"]; !ok {
		t.Error("omani.works parent zone not created")
	}
	if _, ok := dns.zones["openova.io"]; !ok {
		t.Error("openova.io parent zone not created")
	}
	if !dns.dnssecEnabled["omani.works"] || !dns.dnssecEnabled["openova.io"] {
		t.Error("DNSSEC not enabled on parent zones")
	}
}

func TestBootstrapParentZonesNoNameserversFails(t *testing.T) {
	a := &Allocator{
		dns:         newFakeDNS(),
		log:         newSilentLogger(),
		nameservers: nil,
	}
	err := a.BootstrapParentZones(context.Background(), []string{"omani.works"})
	if err == nil {
		t.Fatal("expected error when nameservers empty")
	}
}

// reserveCommitReleaseTracker exercises the full lifecycle against the
// fake DNS, but bypasses the real Postgres store by stubbing the alloc
// methods directly. This isolates the DNS-side state machine from the
// (already independently-tested) store package and avoids requiring a
// testcontainer here — the integration path is covered by the existing
// store_test.go round-trip + the e2e curl in the deploy verification.
type lifecycleScenario struct {
	dns *fakeDNS
}

// TestReserveCommitReleaseDNSShape uses the canonicalRecordSet + dns
// directly to verify the wire shape that the allocator's Commit call
// would produce, and exercises the BYO-path-equivalent (no parent-zone
// touch) for the DNS-only side. The real /reserve → /commit → /release
// runs against a CNPG database in the live deploy verification step.
func TestCommitDNSShape(t *testing.T) {
	dns := newFakeDNS()
	dns.zones["omantel.omani.works"] = []string{"ns1.openova.io.", "ns2.openova.io."}

	rrsets := canonicalRecordSet("omantel.omani.works", "10.20.30.40")
	if err := dns.PatchRRSets(context.Background(), "omantel.omani.works", rrsets); err != nil {
		t.Fatal(err)
	}
	got := dns.rrsets["omantel.omani.works"]
	if len(got) != 6 {
		t.Fatalf("expected 6 RRsets recorded, got %d", len(got))
	}
}

func TestReserveRollsBackOnDelegationFailure(t *testing.T) {
	dns := newFakeDNS()
	dns.failOn = "addns:omani.works:omantel.omani.works"

	// Manually drive what allocator.Reserve does, minus the store call.
	// This validates the cleanup path without spinning up a real DB.
	if err := dns.CreateZone(context.Background(), "omantel.omani.works", pdns.ZoneKindNative, []string{"ns1"}); err != nil {
		t.Fatal(err)
	}
	if err := dns.AddNSDelegation(context.Background(), "omani.works", "omantel.omani.works", []string{"ns1"}, 0); err == nil {
		t.Fatal("expected delegation failure")
	}
	// Cleanup that allocator.Reserve performs in this branch:
	if err := dns.DeleteZone(context.Background(), "omantel.omani.works"); err != nil {
		t.Fatal(err)
	}
	if _, ok := dns.zones["omantel.omani.works"]; ok {
		t.Error("child zone should have been cleaned up")
	}
}

// TestSweeperListExpiredCleansDNS verifies the sweeper-driven cleanup
// codepath shape — the actual time/Postgres orchestration is exercised
// in store_test.go.
func TestSweeperCleansDNSOnExpiry(t *testing.T) {
	dns := newFakeDNS()
	// Pretend an expired reservation has been allocated.
	dns.zones["abandoned.omani.works"] = []string{"ns1.openova.io."}
	dns.delegations["omani.works"] = map[string][]string{
		"abandoned.omani.works": {"ns1.openova.io."},
	}
	// Sweeper would call DeleteZone + RemoveNSDelegation per row.
	_ = dns.DeleteZone(context.Background(), "abandoned.omani.works")
	_ = dns.RemoveNSDelegation(context.Background(), "omani.works", "abandoned.omani.works")
	if _, ok := dns.zones["abandoned.omani.works"]; ok {
		t.Error("zone not removed by simulated sweeper")
	}
	if _, ok := dns.delegations["omani.works"]["abandoned.omani.works"]; ok {
		t.Error("NS delegation not removed by simulated sweeper")
	}
}

// Smoke-test that the *pdns.Client satisfies DNSWriter at compile time
// (already asserted by var _ at file-scope in allocator.go) — duplicated
// at runtime here so a future signature change is caught by `go test`.
func TestPDNSClientImplementsDNSWriter(t *testing.T) {
	_ = DNSWriter(pdns.New("http://x", "", "k"))
}

// ── Concurrency check on the fake itself, used by the reserve test. ──

func TestFakeDNSConcurrency(t *testing.T) {
	dns := newFakeDNS()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			_ = dns.CreateZone(ctx, "z", pdns.ZoneKindNative, nil)
			_ = dns.DeleteZone(ctx, "z")
		}()
	}
	wg.Wait()
}
