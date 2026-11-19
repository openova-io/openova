package huawei

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// sweepCall is what a stand-in sweep received.
type sweepCall struct {
	kind, region string
	protected    map[string]struct{}
	destructive  bool
}

// standInSweepKinds replaces sweepKinds with one stand-in per kind that
// records its call and returns count; the kind named in fail also errors.
func standInSweepKinds(t *testing.T, count int, fail string) *[]sweepCall {
	t.Helper()
	orig := sweepKinds
	t.Cleanup(func() { sweepKinds = orig })
	var calls []sweepCall
	var fake []sweepKind
	for _, k := range orig {
		name := k.name
		fake = append(fake, sweepKind{name: name, budget: k.budget, sweep: func(_ *Provider, _ context.Context, _, _, _, region string, protected map[string]struct{}, destructive bool, _ func(string)) (int, error) {
			calls = append(calls, sweepCall{kind: name, region: region, protected: protected, destructive: destructive})
			if name == fail {
				return count, errors.New("list failed")
			}
			return count, nil
		}})
	}
	sweepKinds = fake
	return &calls
}

var sweepCreds = providers.ProviderCreds{Raw: map[string]string{"access_key": "ak", "secret_key": "sk", "project_id": "proj"}}

// TestSweepKinds_TheFiveSweeps — the kind names are the janitor's log keys
// (orphanEIPsDeleted ... orphanOBSBucketsWouldReap, #5545): they must not change.
func TestSweepKinds_TheFiveSweeps(t *testing.T) {
	want := []string{"EIPs", "Keypairs", "VPCs", "EVS", "OBSBuckets"}
	if got := New().SweepKinds(); !slices.Equal(got, want) {
		t.Fatalf("SweepKinds() = %v, want %v", got, want)
	}
}

// TestSweepOrphans_EveryKindGetsTheInput — each kind runs once, in order,
// with the protected prefixes, the destructive gate and the region of the
// input; a failing kind does not stop the next and its error is returned.
func TestSweepOrphans_EveryKindGetsTheInput(t *testing.T) {
	calls := standInSweepKinds(t, 2, "VPCs")
	protected := map[string]struct{}{"live0001": {}}
	res, err := New().SweepOrphans(context.Background(), providers.SweepInput{
		Creds: sweepCreds, CloudRegion: "region-x", ProtectedIDs: protected, Destructive: true,
	})
	if err == nil || !strings.Contains(err.Error(), "VPCs: list failed") {
		t.Fatalf("err = %v, want the VPCs error", err)
	}
	var kinds []string
	for _, c := range *calls {
		kinds = append(kinds, c.kind)
		if c.region != "region-x" || !c.destructive {
			t.Errorf("%s: region=%q destructive=%v, want region-x and true", c.kind, c.region, c.destructive)
		}
		if _, ok := c.protected["live0001"]; !ok || len(c.protected) != 1 {
			t.Errorf("%s: protected = %v, want exactly live0001", c.kind, c.protected)
		}
	}
	if !slices.Equal(kinds, New().SweepKinds()) {
		t.Fatalf("kinds run = %v, want every kind once, in order", kinds)
	}
	for _, k := range New().SweepKinds() {
		if res.Deleted[k] != 2 {
			t.Errorf("Deleted[%s] = %d, want 2", k, res.Deleted[k])
		}
	}
}

// TestSweepOrphans_RegionFromTheCredentialsThenTheDefault — the handler
// passes no region literal: the adapter reads the credentials' region and
// falls back to its own default.
func TestSweepOrphans_RegionFromTheCredentialsThenTheDefault(t *testing.T) {
	calls := standInSweepKinds(t, 0, "")
	withRegion := providers.ProviderCreds{Raw: map[string]string{"access_key": "ak", "secret_key": "sk", "project_id": "proj", "region": "region-y"}}
	if _, err := New().SweepOrphans(context.Background(), providers.SweepInput{Creds: withRegion, ProtectedIDs: map[string]struct{}{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := New().SweepOrphans(context.Background(), providers.SweepInput{Creds: sweepCreds, ProtectedIDs: map[string]struct{}{}}); err != nil {
		t.Fatal(err)
	}
	n := len(sweepKinds)
	if got := (*calls)[0].region; got != "region-y" {
		t.Errorf("region = %q, want the credentials' region-y", got)
	}
	if got := (*calls)[n].region; got != defaultRegion {
		t.Errorf("region = %q, want the default %q", got, defaultRegion)
	}
}

// TestSweepOrphans_RefusesBeforeTheCloud — without protected prefixes or
// without credentials no sweep runs.
func TestSweepOrphans_RefusesBeforeTheCloud(t *testing.T) {
	calls := standInSweepKinds(t, 1, "")
	if _, err := New().SweepOrphans(context.Background(), providers.SweepInput{Creds: sweepCreds, Destructive: true}); err == nil {
		t.Error("a nil ProtectedIDs was accepted")
	}
	if _, err := New().SweepOrphans(context.Background(), providers.SweepInput{ProtectedIDs: map[string]struct{}{}}); err == nil {
		t.Error("missing credentials were accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("%d sweep(s) ran after a refusal", len(*calls))
	}
}

// evsAttachedFixture: the wiped deployment's detached volume, a detached
// volume of a protected neighbour, and two volumes attached to a server
// (one of them claiming status available).
const evsAttachedFixture = `{"volumes":[
  {"id":"vol-own-detached","name":"pvc-own","status":"available","attachments":[],"metadata":{"cluster":"catalyst-x-wiped001-c"}},
  {"id":"vol-neighbour","name":"pvc-neighbour","status":"available","attachments":[],"metadata":{"cluster":"catalyst-y-live0001-c"}},
  {"id":"vol-attached-inuse","name":"pvc-attached","status":"in-use","attachments":[{"server_id":"s1"}],"metadata":{}},
  {"id":"vol-attached-available","name":"pvc-attached-2","status":"available","attachments":[{"server_id":"s2"}],"metadata":{}}
]}`

// TestSweepOrphans_NeverDeletesAnAttachedVolume — the orphan sweep, armed
// destructive with nothing protected, deletes only the detached volumes; a
// volume attached to a server is never deleted.
func TestSweepOrphans_NeverDeletesAnAttachedVolume(t *testing.T) {
	neutralizeEVSThrottle(t)
	var deleted []string
	defer fakeHCSEVS(t, evsAttachedFixture, &deleted)()
	orig := sweepKinds
	t.Cleanup(func() { sweepKinds = orig })
	for _, k := range orig {
		if k.name == "EVS" {
			sweepKinds = []sweepKind{k}
		}
	}
	res, err := New().SweepOrphans(context.Background(), providers.SweepInput{Creds: sweepCreds, ProtectedIDs: map[string]struct{}{}, Destructive: true})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	if want := []string{"vol-neighbour", "vol-own-detached"}; !slices.Equal(deleted, want) || res.Deleted["EVS"] != 2 {
		t.Fatalf("deleted = %v (count %d), want %v: an attached volume was deleted or a detached one kept", deleted, res.Deleted["EVS"], want)
	}
}

// TestWipeVolumeStep_NeverDeletesAnAttachedVolume — the post-destroy step
// of Wipe deletes the wiped deployment's detached volume only: never an
// attached volume, never a protected neighbour's, and nothing at all
// without protected prefixes.
func TestWipeVolumeStep_NeverDeletesAnAttachedVolume(t *testing.T) {
	neutralizeEVSThrottle(t)
	var deleted []string
	defer fakeHCSEVS(t, evsAttachedFixture, &deleted)()
	hw := hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"}
	spec := providers.WipeSpec{DeploymentID: "wiped001cafe0000", SovereignFQDN: "x.omani.works",
		ProtectedIDs: map[string]struct{}{"wiped001": {}, "live0001": {}}}
	if n := New().reapDetachedVolumes(context.Background(), spec, hw, defaultRegion, func(string) {}); n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
	if !slices.Equal(deleted, []string{"vol-own-detached"}) {
		t.Fatalf("deleted = %v, want only vol-own-detached", deleted)
	}
	if _, ok := spec.ProtectedIDs["wiped001"]; !ok {
		t.Error("the caller's protected set was changed")
	}
	deleted = nil
	spec.ProtectedIDs = nil
	if n := New().reapDetachedVolumes(context.Background(), spec, hw, defaultRegion, func(string) {}); n != 0 || len(deleted) != 0 {
		t.Fatalf("nil ProtectedIDs reaped %d, deleted %v; want nothing", n, deleted)
	}
}

// TestHasLiveInfrastructure — a server with the deployment's name prefix
// is alive, another deployment's is not, and missing credentials are an
// error that reports alive.
func TestHasLiveInfrastructure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[{"id":"s1","name":"catalyst-x-omani-works-live0001-cp-0","status":"ACTIVE"}]}`))
	}))
	defer srv.Close()
	orig := endpointFor
	endpointFor = func(string, string) string { return srv.URL }
	defer func() { endpointFor = orig }()

	p := New()
	if alive, err := p.HasLiveInfrastructure(context.Background(), "live0001aaaa", "x.omani.works", sweepCreds); err != nil || !alive {
		t.Errorf("own server: alive=%v err=%v, want true, nil", alive, err)
	}
	if alive, err := p.HasLiveInfrastructure(context.Background(), "gone0001aaaa", "x.omani.works", sweepCreds); err != nil || alive {
		t.Errorf("other deployment's server: alive=%v err=%v, want false, nil", alive, err)
	}
	if alive, err := p.HasLiveInfrastructure(context.Background(), "live0001aaaa", "x.omani.works", providers.ProviderCreds{}); err == nil || !alive {
		t.Errorf("no credentials: alive=%v err=%v, want true and an error", alive, err)
	}
}
