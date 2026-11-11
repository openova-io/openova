package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/observe"
	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// A PLATFORM pair (bp-cnpg-pair, installed by bootstrap-kit slot 16b) is
// delivered from the Sovereign region repo: its region file holds the carrier
// ConfigMap the slot HelmRelease reads through valuesFrom, plus its Continuum.
// The same phase machine must move it with the same 3-commit protocol.

const platformFQDN = "hw307.omani.works"

var platformLayout = regionfile.PlatformLayout(platformFQDN)

func platformCR(desired string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "dr.openova.io/v1", "kind": "Continuum",
		"metadata": map[string]interface{}{"name": "dr-cnpg-pair", "namespace": "cnpg"},
		"spec": map[string]interface{}{
			"applicationRef":       "cnpg/cnpg-pair",
			"desiredPrimaryRegion": desired,
			"regions":              []interface{}{regionA, regionB},
			"transition": map[string]interface{}{
				"repo": map[string]interface{}{
					"owner": "openova", "name": "sovereign-regions", "branch": "main",
					"path": platformLayout.Root, "folder": platformLayout.Folder,
				},
				"app":         "cnpg-pair",
				"stateful":    true,
				"helmRelease": map[string]interface{}{"name": "bp-cnpg-pair", "namespace": "flux-system"},
			},
		},
	}}
}

func platformFile(t *testing.T, self, primary, desired string) []byte {
	peer := regionB
	if self == regionB {
		peer = regionA
	}
	cm, err := regionfile.TopologyValuesConfigMap(regionfile.TopologyValuesConfigMapName("bp-cnpg-pair"), "flux-system",
		map[string]string{"catalyst.openova.io/app": "cnpg-pair"},
		map[string]interface{}{"openova": map[string]interface{}{"topology": map[string]interface{}{
			"mode": "active-hot-standby", "self": self, "primary": primary, "peers": []interface{}{peer},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := regionfile.Marshal([]*unstructured.Unstructured{cm, platformCR(desired)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// platformSim is two regions' Flux (valuesFrom → Helm) + CNPG applying the
// platform region files. It records the targets the controller asked about.
type platformSim struct {
	store   *fakeStore
	dbs     map[string]*liveDB
	asked   map[string]string // region → last target's HelmRelease
	applied map[string]string // region → openova.topology.primary Helm applied
}

func (s *platformSim) fields(region string) regionfile.TransitionFields {
	d, _, ok, _ := s.store.Read(context.Background(), Repo{}, platformLayout.AppPath(region, "cnpg-pair"))
	if !ok {
		return regionfile.TransitionFields{}
	}
	objs, _ := regionfile.Parse(d)
	f, _, _ := regionfile.ReadTransitionFields(objs)
	return f
}

func (s *platformSim) step(t *testing.T) {
	for r, db := range s.dbs {
		f := s.fields(r)
		s.applied[r] = f.Primary // the chart's Cluster annotation follows the applied values
		want := f.Primary == r
		switch {
		case db.primary && !want:
			db.primary, db.demTok = false, "tok-"+r
		case !db.primary && want:
			for pr, pdb := range s.dbs {
				if pr != r && pdb.primary {
					t.Fatalf("%s promoted while %s is still primary (split brain)", r, pr)
				}
			}
			for pr, pdb := range s.dbs {
				if pr != r && pdb.demTok != "" && pdb.demTok == f.PromotionToken {
					db.primary, db.lastProm, db.demTok = true, f.PromotionToken, ""
				}
			}
		}
	}
}

type platformLocal struct {
	s      *platformSim
	region string
}

func (l platformLocal) Observe(_ context.Context, ts []observe.Target) map[string]transition.MemberStatus {
	out := map[string]transition.MemberStatus{}
	for _, t := range ts {
		l.s.asked[l.region] = t.HelmRelease
		db := l.s.dbs[l.region]
		out[t.Key()] = transition.MemberStatus{
			// The slot HelmRelease's spec.values carry no contract (valuesFrom);
			// the applied primary is the Cluster annotation.
			Observed: true, AppliedPrimary: l.s.applied[l.region], ReleaseReady: true,
			DB: transition.DBStatus{Found: true, Cluster: "cnpg-pair-" + l.region, Primary: db.primary, Ready: true,
				DemotionToken: db.demTok, LastPromotionToken: db.lastProm, TopologyPrimary: l.s.applied[l.region]},
		}
	}
	return out
}

type platformPeers struct{ s *platformSim }

func (p platformPeers) Fetch(_ context.Context, region string, ts []observe.Target) (map[string]transition.MemberStatus, error) {
	return platformLocal{p.s, region}.Observe(context.Background(), ts), nil
}

func TestPlatformPair_TwoRegions_ThreeCommitProtocol(t *testing.T) {
	store := newFakeStore()
	// hw307 today: cnpg-pair's writer is region B; the operator sets
	// Primary=A in the Topology tab (desired A in every region file).
	store.files[platformLayout.AppPath(regionA, "cnpg-pair")] = platformFile(t, regionA, regionB, regionA)
	store.files[platformLayout.AppPath(regionB, "cnpg-pair")] = platformFile(t, regionB, regionB, regionA)
	s := &platformSim{store: store,
		dbs:     map[string]*liveDB{regionA: {}, regionB: {primary: true}},
		asked:   map[string]string{},
		applied: map[string]string{regionA: regionB, regionB: regionB},
	}
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	mk := func(region string) *Controller {
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			ContinuumGVR: "ContinuumList", observe.ApplicationGVR: "ApplicationList",
		}, platformCR(regionA))
		return &Controller{Dyn: dyn, Store: store, Local: platformLocal{s, region}, Peers: platformPeers{s}, Region: region, Now: now}
	}
	ca, cb := mk(regionA), mk(regionB)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		first, second := ca, cb
		if i%2 == 1 {
			first, second = cb, ca
		}
		if err := first.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if err := second.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		s.step(t)
		clock = clock.Add(10 * time.Second)
	}
	if !s.dbs[regionA].primary || s.dbs[regionB].primary {
		t.Fatalf("writer did not move to A: A=%+v B=%+v", s.dbs[regionA], s.dbs[regionB])
	}
	if s.dbs[regionA].lastProm != "tok-"+regionB {
		t.Fatalf("A must promote with B's demotion token, got %q", s.dbs[regionA].lastProm)
	}
	for _, r := range []string{regionA, regionB} {
		if s.asked[r] != "flux-system/bp-cnpg-pair" {
			t.Fatalf("region %s observed HelmRelease %q; a platform member is observed through its slot HelmRelease", r, s.asked[r])
		}
	}

	// Phase commits: old primary's file (traffic-off), then the new primary's
	// file with the token (data-primary) — exactly two, in that order, one actor,
	// all inside the Sovereign region repo's platform layout.
	var fileCommits []string
	actors := map[string]bool{}
	for _, c := range store.commits {
		path := strings.Fields(c)[0]
		if strings.Contains(path, "/regions/") {
			fileCommits = append(fileCommits, path)
			actors[strings.SplitN(strings.SplitN(c, "continuum(", 2)[1], ")", 2)[0]] = true
		} else if !strings.HasPrefix(path, platformLayout.Root+"/continuum/transitions/") {
			t.Fatalf("commit outside the platform layout: %s", c)
		}
	}
	want := []string{platformLayout.AppPath(regionB, "cnpg-pair"), platformLayout.AppPath(regionA, "cnpg-pair")}
	if fmt.Sprint(fileCommits) != fmt.Sprint(want) {
		t.Fatalf("region-file commits\n got %v\nwant %v", fileCommits, want)
	}
	if len(actors) != 1 {
		t.Fatalf("region files were committed by %d actors (%v)", len(actors), actors)
	}
	// The data-primary commit carried the token in A's carrier ConfigMap only.
	if f := s.fields(regionA); f.Primary != regionA || f.PromotionToken != "tok-"+regionB {
		t.Fatalf("A's file after the move: %+v", f)
	}
	if f := s.fields(regionB); f.Primary != regionA || f.PromotionToken != "" {
		t.Fatalf("B's file after the move: %+v", f)
	}
	ld, _, _, _ := store.Read(ctx, Repo{}, platformLayout.LeasePath("cnpg--dr-cnpg-pair"))
	lease, err := transition.ParseLease(ld)
	if err != nil || lease.Holder != "" || lease.Transition == nil || lease.Transition.Outcome != transition.OutcomeConverged {
		t.Fatalf("lease after transition: %+v %v", lease, err)
	}
	for _, c := range []*Controller{ca, cb} {
		cr, err := c.Dyn.Resource(ContinuumGVR).Namespace("cnpg").Get(ctx, "dr-cnpg-pair", metav1GetOptions())
		if err != nil {
			t.Fatal(err)
		}
		phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
		prim, _, _ := unstructured.NestedString(cr.Object, "status", "primaryRegion")
		if phase != "Healthy" || prim != regionA {
			t.Fatalf("%s: Continuum status phase=%q primaryRegion=%q", c.Region, phase, prim)
		}
	}
}
