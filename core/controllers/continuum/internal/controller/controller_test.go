package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// ── fake Git (compare-and-swap on a per-file version) ─────────────────────

type fakeStore struct {
	mu      sync.Mutex
	files   map[string][]byte
	ver     map[string]int
	commits []string // "<path> <message>"
}

func newFakeStore() *fakeStore {
	return &fakeStore{files: map[string][]byte{}, ver: map[string]int{}}
}

func (s *fakeStore) Read(_ context.Context, _ Repo, path string) ([]byte, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.files[path]
	if !ok {
		return nil, "", false, nil
	}
	return append([]byte(nil), d...), fmt.Sprintf("v%d", s.ver[path]), true, nil
}

func (s *fakeStore) Write(_ context.Context, _ Repo, path string, data []byte, sha, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.files[path]
	cur := ""
	if exists {
		cur = fmt.Sprintf("v%d", s.ver[path])
	}
	if sha != cur {
		return ErrConflict
	}
	s.files[path] = append([]byte(nil), data...)
	s.ver[path]++
	s.commits = append(s.commits, path+" "+msg)
	return nil
}

// ── fake regions: Flux + CNPG applying the Git files ───────────────────────

type liveDB struct {
	primary  bool
	demTok   string
	lastProm string
}

type sim struct {
	store *fakeStore
	dbs   map[string]*liveDB // region → wordpress CNPG member
	down  map[string]bool
}

func (s *sim) fields(region string) regionfile.TransitionFields {
	d, _, ok, _ := s.store.Read(context.Background(), Repo{}, regionfile.AppPath(region, "wp"))
	if !ok {
		return regionfile.TransitionFields{}
	}
	objs, _ := regionfile.Parse(d)
	f, _, _ := regionfile.ReadTransitionFields(objs)
	return f
}

// step applies each region's file to its live state (Flux + CNPG).
func (s *sim) step(t *testing.T) {
	for r, db := range s.dbs {
		if s.down[r] {
			continue
		}
		f := s.fields(r)
		want := f.Primary == r
		switch {
		case db.primary && !want:
			db.primary, db.demTok = false, "tok-"+r
		case !db.primary && want:
			for pr, pdb := range s.dbs {
				if pr != r && pdb.primary && !s.down[pr] {
					t.Fatalf("%s promoted while %s is still primary", r, pr)
				}
			}
			ok := f.PromotionToken == ""
			for pr, pdb := range s.dbs {
				if pr != r && pdb.demTok != "" && pdb.demTok == f.PromotionToken {
					ok = true
				}
			}
			if ok {
				db.primary, db.lastProm, db.demTok = true, f.PromotionToken, ""
			}
		}
	}
}

func (s *sim) status(region string) transition.MemberStatus {
	db := s.dbs[region]
	return transition.MemberStatus{
		Observed: true, AppliedPrimary: s.fields(region).Primary, ReleaseReady: true,
		DB: transition.DBStatus{Found: true, Cluster: "wordpress-db-" + region, Primary: db.primary, Ready: true,
			DemotionToken: db.demTok, LastPromotionToken: db.lastProm, Phase: "Cluster in healthy state"},
	}
}

type simLocal struct {
	s      *sim
	region string
}

func (l simLocal) Observe(_ context.Context, ts []observe.Target) map[string]transition.MemberStatus {
	out := map[string]transition.MemberStatus{}
	for _, t := range ts {
		out[t.Key()] = l.s.status(l.region)
	}
	return out
}

type simPeers struct{ s *sim }

func (p simPeers) Fetch(_ context.Context, region string, ts []observe.Target) (map[string]transition.MemberStatus, error) {
	if p.s.down[region] {
		return nil, fmt.Errorf("region %s unreachable", region)
	}
	return simLocal{p.s, region}.Observe(context.Background(), ts), nil
}

// ── fixtures ───────────────────────────────────────────────────────────────

const regionA, regionB = "hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod"

func continuumCR(desired string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "dr.openova.io/v1", "kind": "Continuum",
		"metadata": map[string]interface{}{"name": "dr-wp", "namespace": "acme"},
		"spec": map[string]interface{}{
			"applicationRef":       "acme/wp",
			"desiredPrimaryRegion": desired,
			"regions":              []interface{}{regionA, regionB},
			"transition": map[string]interface{}{
				"repo":     map[string]interface{}{"owner": "acme", "name": "catalyst-tenant", "branch": "main"},
				"app":      "wp",
				"stateful": true,
			},
		},
	}}
}

func regionFile(t *testing.T, self, primary, desired string) []byte {
	peer := regionB
	if self == regionB {
		peer = regionA
	}
	hr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]interface{}{"name": "wp", "namespace": "acme"},
		"spec": map[string]interface{}{"values": map[string]interface{}{"openova": map[string]interface{}{"topology": map[string]interface{}{
			"mode": "active-hot-standby", "self": self, "primary": primary, "peers": []interface{}{peer},
		}}}},
	}}
	b, err := regionfile.Marshal([]*unstructured.Unstructured{hr, continuumCR(desired)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newDyn() *dynfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		ContinuumGVR:           "ContinuumList",
		observe.ApplicationGVR: "ApplicationList",
	}, continuumCR(regionA))
}

func TestTwoRegions_PlannedMove_OneActorPerTransition(t *testing.T) {
	store := newFakeStore()
	store.files[regionfile.AppPath(regionA, "wp")] = regionFile(t, regionA, regionA, regionB)
	store.files[regionfile.AppPath(regionB, "wp")] = regionFile(t, regionB, regionA, regionB)
	s := &sim{store: store, dbs: map[string]*liveDB{regionA: {primary: true}, regionB: {}}, down: map[string]bool{}}

	clock := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	mk := func(region string) *Controller {
		return &Controller{Dyn: newDyn(), Store: store, Local: simLocal{s, region}, Peers: simPeers{s}, Region: region, Now: now}
	}
	ca, cb := mk(regionA), mk(regionB)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		// Both regions tick every round; order alternates.
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
	if !s.dbs[regionB].primary || s.dbs[regionA].primary {
		t.Fatalf("writer did not move to B: %+v %+v", s.dbs[regionA], s.dbs[regionB])
	}
	if s.dbs[regionB].lastProm != "tok-"+regionA {
		t.Fatalf("B must promote with A's demotion token, got %q", s.dbs[regionB].lastProm)
	}

	// Region-file commits: exactly two, in protocol order, all by ONE actor.
	var fileCommits []string
	actors := map[string]bool{}
	for _, c := range store.commits {
		if strings.HasPrefix(c, "regions/") {
			fileCommits = append(fileCommits, strings.Fields(c)[0])
			actors[strings.SplitN(strings.SplitN(c, "continuum(", 2)[1], ")", 2)[0]] = true
		}
	}
	want := []string{regionfile.AppPath(regionA, "wp"), regionfile.AppPath(regionB, "wp")}
	if strings.Join(fileCommits, " ") != strings.Join(want, " ") {
		t.Fatalf("region-file commits\n got %v\nwant %v", fileCommits, want)
	}
	if len(actors) != 1 {
		t.Fatalf("region files were committed by %d actors (%v); the Git lease must fence to one", len(actors), actors)
	}

	// The lease is released and the record says Converged.
	ld, _, _, _ := store.Read(ctx, Repo{}, regionfile.LeasePath("acme--dr-wp"))
	lease, err := transition.ParseLease(ld)
	if err != nil || lease.Holder != "" || lease.Transition == nil || lease.Transition.Outcome != transition.OutcomeConverged {
		t.Fatalf("lease after transition: %+v %v", lease, err)
	}

	// Each region's own Continuum CR carries the D7 activity shape.
	for _, c := range []*Controller{ca, cb} {
		cr, err := c.Dyn.Resource(ContinuumGVR).Namespace("acme").Get(ctx, "dr-wp", metav1GetOptions())
		if err != nil {
			t.Fatal(err)
		}
		phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
		acts, _, _ := unstructured.NestedSlice(cr.Object, "status", "activity")
		if phase != "Healthy" || len(acts) != 4 {
			t.Fatalf("%s status: phase=%s activity=%d", c.Region, phase, len(acts))
		}
		for _, a := range acts {
			am := a.(map[string]interface{})
			for _, k := range []string{"id", "phase", "member", "state", "startedAt", "finishedAt", "message"} {
				if _, ok := am[k]; !ok {
					t.Fatalf("activity entry lacks %q: %v", k, am)
				}
			}
			if am["state"] != "succeeded" {
				t.Fatalf("entry not succeeded: %v", am)
			}
		}
	}
}

func TestNotGitDelivered_ReportedNeverActed(t *testing.T) {
	legacy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "dr.openova.io/v1", "kind": "Continuum",
		"metadata": map[string]interface{}{"name": "dr-cnpg-pair", "namespace": "cnpg"},
		"spec":     map[string]interface{}{"applicationRef": "cnpg/cnpg-pair", "primaryRegion": regionA},
	}}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		ContinuumGVR: "ContinuumList", observe.ApplicationGVR: "ApplicationList",
	}, legacy)
	store := newFakeStore()
	c := &Controller{Dyn: dyn, Store: store, Local: simLocal{&sim{store: store, dbs: map[string]*liveDB{}}, regionA}, Region: regionA}
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.commits) != 0 {
		t.Fatalf("a non-Git Continuum must never cause a commit: %v", store.commits)
	}
	cr, _ := dyn.Resource(ContinuumGVR).Namespace("cnpg").Get(context.Background(), "dr-cnpg-pair", metav1GetOptions())
	phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	if phase != "Unknown" {
		t.Fatalf("want Unknown, got %q", phase)
	}
}
