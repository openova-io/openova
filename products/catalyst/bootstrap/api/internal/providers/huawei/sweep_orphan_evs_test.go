package huawei

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// neutralizeEVSThrottle zeroes the inter-delete spacing + backoff so the
// destructive-path tests run instantly. #5028 added a ≥~0.9s spacing and a
// 1s-base 429 backoff to protect against the HCS EVS hard rate-limit; tests
// exercise the LOGIC, not the wall-clock, so we drive both to ~0 and restore.
func neutralizeEVSThrottle(t *testing.T) {
	t.Helper()
	origSpacing, origBackoff, origRetries := evsDeleteSpacing, evsDelete429BaseBackoff, evsDelete429MaxRetries
	evsDeleteSpacing = 0
	evsDelete429BaseBackoff = 0
	t.Cleanup(func() {
		evsDeleteSpacing = origSpacing
		evsDelete429BaseBackoff = origBackoff
		evsDelete429MaxRetries = origRetries
	})
}

// fakeHCSEVS stands up a httptest server that mimics the EVS detail-list +
// per-volume DELETE endpoints, overrides the package `endpointFor` to route
// at it, and records every DELETE'd volume ID. Returns a restore func.
func fakeHCSEVS(t *testing.T, listBody string, deleted *[]string) func() {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cloudvolumes/detail"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(listBody))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/cloudvolumes/"):
			mu.Lock()
			parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
			*deleted = append(*deleted, parts[len(parts)-1])
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	return func() {
		endpointFor = orig
		srv.Close()
	}
}

// evsListFixture: one stranded orphan + one in-use + one bastion disk.
const evsListFixture = `{"volumes":[
  {"id":"vol-orphan-1","name":"pvc-aaaa-orphan","status":"available","attachments":[],"metadata":{}},
  {"id":"vol-inuse-2","name":"pvc-bbbb-inuse","status":"in-use","attachments":[{"server_id":"s1"}],"metadata":{}},
  {"id":"vol-bastion-3","name":"bastion-openova-root","status":"available","attachments":[],"metadata":{}}
]}`

// TestSweepOrphanEVS_LogOnlyReapsNothing — #4466 key contract. With
// destructive=false the sweep must issue ZERO deletes (log-only) even
// though one volume is a genuine orphan. The returned count reflects the
// would-reap so an operator sees what the destructive pass would do.
func TestSweepOrphanEVS_LogOnlyReapsNothing(t *testing.T) {
	var deleted []string
	restore := fakeHCSEVS(t, evsListFixture, &deleted)
	defer restore()

	p := New()
	n, err := p.SweepOrphanEVS(context.Background(), "ak", "sk", "proj", "me-east-215",
		map[string]struct{}{}, false /*destructive*/, nil)
	if err != nil {
		t.Fatalf("SweepOrphanEVS: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("log-only mode issued %d DELETE(s) (%v) — must delete NOTHING", len(deleted), deleted)
	}
	if n != 1 {
		t.Fatalf("would-reap count = %d, want 1 (the single genuine orphan)", n)
	}
}

// TestSweepOrphanEVS_DestructiveReapsOrphanOnly — with the flag armed the
// sweep deletes ONLY the genuine orphan; the in-use + bastion volumes are
// never touched.
func TestSweepOrphanEVS_DestructiveReapsOrphanOnly(t *testing.T) {
	neutralizeEVSThrottle(t)
	var deleted []string
	restore := fakeHCSEVS(t, evsListFixture, &deleted)
	defer restore()

	p := New()
	n, err := p.SweepOrphanEVS(context.Background(), "ak", "sk", "proj", "me-east-215",
		map[string]struct{}{}, true /*destructive*/, nil)
	if err != nil {
		t.Fatalf("SweepOrphanEVS: %v", err)
	}
	if n != 1 || len(deleted) != 1 || deleted[0] != "vol-orphan-1" {
		t.Fatalf("destructive sweep deleted=%v (n=%d), want exactly [vol-orphan-1]", deleted, n)
	}
}

// TestSweepOrphanEVS_ActiveDepNeverReaped — a detached orphan whose
// metadata carries an in-flight deployment-ID prefix is protected EVEN with
// the destructive flag set.
func TestSweepOrphanEVS_ActiveDepNeverReaped(t *testing.T) {
	neutralizeEVSThrottle(t)
	listBody := fmt.Sprintf(`{"volumes":[
	  {"id":"vol-live-1","name":"pvc-live","status":"available","attachments":[],"metadata":{"cluster_id":"catalyst-x-%s-cluster"}}
	]}`, "5b413990")
	var deleted []string
	restore := fakeHCSEVS(t, listBody, &deleted)
	defer restore()

	p := New()
	n, err := p.SweepOrphanEVS(context.Background(), "ak", "sk", "proj", "me-east-215",
		map[string]struct{}{"5b413990": {}}, true /*destructive*/, nil)
	if err != nil {
		t.Fatalf("SweepOrphanEVS: %v", err)
	}
	if n != 0 || len(deleted) != 0 {
		t.Fatalf("active-dep volume was reaped (deleted=%v, n=%d) — must be protected regardless of flag", deleted, n)
	}
}

// TestIsReclaimableOrphanEVS locks the #4466 detached-EVS-orphan
// match/protection contract:
//   - only CSI `pvc-*` volumes are ever in scope (system / bastion disks
//     never carry that prefix → hard-protected),
//   - only genuinely-detached volumes (status=available + zero
//     attachments) are eligible (an in-use / attaching volume is live),
//   - protect-by-default: a volume whose owning-cluster metadata carries an
//     in-flight deployment-ID prefix is protected even when momentarily
//     detached.
func TestIsReclaimableOrphanEVS(t *testing.T) {
	// One in-flight deployment protected by its 8-char ID prefix.
	active := map[string]struct{}{
		"5b413990": {},
	}

	cases := []struct {
		name        string
		vol         string
		status      string
		attachments int
		metadata    map[string]string
		want        bool
	}{
		// Reclaimable: detached CSI volume, no active-dep metadata.
		{
			name:        "stranded detached pvc from a self-reaped node",
			vol:         "pvc-aaaaaaaa-1111-2222-3333-444444444444",
			status:      "available",
			attachments: 0,
			metadata:    map[string]string{"cluster_id": "wiped-cluster-aabbccdd"},
			want:        true,
		},
		{
			name:        "detached pvc with no metadata at all",
			vol:         "pvc-bbbbbbbb-1111-2222-3333-444444444444",
			status:      "available",
			attachments: 0,
			metadata:    nil,
			want:        true,
		},

		// Protected: in-use / non-detached volume must never be swept.
		{
			name:        "in-use attached pvc",
			vol:         "pvc-cccccccc-1111-2222-3333-444444444444",
			status:      "in-use",
			attachments: 1,
			metadata:    nil,
			want:        false,
		},
		{
			name:        "available status but still carries an attachment",
			vol:         "pvc-dddddddd-1111-2222-3333-444444444444",
			status:      "available",
			attachments: 1,
			metadata:    nil,
			want:        false,
		},
		{
			name:        "mid-attach (creating) volume",
			vol:         "pvc-eeeeeeee-1111-2222-3333-444444444444",
			status:      "creating",
			attachments: 0,
			metadata:    nil,
			want:        false,
		},

		// Protected: belongs to a live deployment (metadata prefix match).
		{
			name:        "detached but owned by an in-flight dep (node mid-roll)",
			vol:         "pvc-ffffffff-1111-2222-3333-444444444444",
			status:      "available",
			attachments: 0,
			metadata:    map[string]string{"cluster_id": "catalyst-omantel-biz-5b413990-cluster"},
			want:        false,
		},

		// Hard-protected: not a CSI volume / bastion.
		{
			name:        "bastion system disk",
			vol:         "bastion-openova-root",
			status:      "available",
			attachments: 0,
			metadata:    nil,
			want:        false,
		},
		{
			name:        "catalyst control-plane root disk (not a pvc-)",
			vol:         "catalyst-omantel-biz-aabbccdd-cp-root",
			status:      "available",
			attachments: 0,
			metadata:    nil,
			want:        false,
		},
		{
			name:        "empty name",
			vol:         "",
			status:      "available",
			attachments: 0,
			metadata:    nil,
			want:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isReclaimableOrphanEVS(tc.vol, tc.status, tc.attachments, tc.metadata, active)
			if got != tc.want {
				t.Fatalf("isReclaimableOrphanEVS(%q, %q, %d) = %v, want %v",
					tc.vol, tc.status, tc.attachments, got, tc.want)
			}
		})
	}
}

// TestIsReclaimableOrphanEVS_NoActiveProvs proves the post-wipe sweep
// (empty active set) reclaims every detached pvc-* while still hard-
// protecting in-use volumes and non-CSI / bastion disks.
func TestIsReclaimableOrphanEVS_NoActiveProvs(t *testing.T) {
	empty := map[string]struct{}{}
	if !isReclaimableOrphanEVS("pvc-stranded-vol", "available", 0, nil, empty) {
		t.Fatal("with no in-flight provs every detached pvc- volume must be reclaimable")
	}
	if isReclaimableOrphanEVS("pvc-stranded-vol", "in-use", 1, nil, empty) {
		t.Fatal("an in-use volume must stay protected even with an empty active set")
	}
	if isReclaimableOrphanEVS("bastion-openova-root", "available", 0, nil, empty) {
		t.Fatal("a bastion disk must stay protected even with an empty active set")
	}
}

// fakeHCS429EVS stands up an EVS DELETE endpoint that returns HTTP 429 for the
// first `fail429` DELETE calls (any volume), then 202. Records the attempt
// count so a test can assert the #5028 backoff retried the right number of
// times. Returns a restore func.
func fakeHCS429EVS(t *testing.T, fail429 int, attempts *int32) func() {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/cloudvolumes/") {
			n := atomic.AddInt32(attempts, 1)
			if int(n) <= fail429 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	return func() {
		endpointFor = orig
		srv.Close()
	}
}

// TestDeleteEVSVolume_429Backoff — #5028 core: the HCS EVS API rate-limits
// HARD (429). deleteEVSVolume MUST retry-with-backoff on 429 up to the budget
// and give up gracefully (returning the residual 429) rather than treating the
// first 429 as a fatal failure. Table-tests the attempt accounting with the
// wall-clock backoff neutralised.
func TestDeleteEVSVolume_429Backoff(t *testing.T) {
	cases := []struct {
		name         string
		fail429      int
		maxRetries   int
		wantAttempts int32
		wantStatus   int
	}{
		{"succeeds on first try", 0, 5, 1, http.StatusAccepted},
		{"429 twice then 202 (retries succeed)", 2, 5, 3, http.StatusAccepted},
		{"429 forever exhausts the budget", 99, 3, 4 /*1 initial + 3 retries*/, http.StatusTooManyRequests},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origBackoff, origRetries := evsDelete429BaseBackoff, evsDelete429MaxRetries
			evsDelete429BaseBackoff = 0 // no wall-clock in tests
			evsDelete429MaxRetries = tc.maxRetries
			defer func() {
				evsDelete429BaseBackoff = origBackoff
				evsDelete429MaxRetries = origRetries
			}()

			var attempts int32
			restore := fakeHCS429EVS(t, tc.fail429, &attempts)
			defer restore()

			client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": "me-east-215"}})
			status, err := deleteEVSVolume(context.Background(), client,
				hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"}, "me-east-215", "proj", "vol-x")
			if err != nil {
				t.Fatalf("deleteEVSVolume: %v", err)
			}
			if status != tc.wantStatus {
				t.Fatalf("final status = %d, want %d", status, tc.wantStatus)
			}
			if got := atomic.LoadInt32(&attempts); got != tc.wantAttempts {
				t.Fatalf("DELETE attempts = %d, want %d (429-retry budget not honoured)", got, tc.wantAttempts)
			}
		})
	}
}

// TestSweepOrphanEVS_ThrottlesConsecutiveDeletes proves the #5028 throttle is
// actually applied on the destructive path: two genuine orphans reaped with a
// 50ms spacing must take at least one spacing interval (un-throttled the two
// DELETEs are sub-millisecond). Without the throttle a 332-volume backlog
// 429s wholesale — this is the regression guard for that.
func TestSweepOrphanEVS_ThrottlesConsecutiveDeletes(t *testing.T) {
	origSpacing, origBackoff := evsDeleteSpacing, evsDelete429BaseBackoff
	evsDeleteSpacing = 50 * time.Millisecond
	evsDelete429BaseBackoff = 0
	defer func() {
		evsDeleteSpacing = origSpacing
		evsDelete429BaseBackoff = origBackoff
	}()

	listBody := `{"volumes":[
	  {"id":"vol-o1","name":"pvc-o1","status":"available","attachments":[],"metadata":{}},
	  {"id":"vol-o2","name":"pvc-o2","status":"available","attachments":[],"metadata":{}}
	]}`
	var deleted []string
	restore := fakeHCSEVS(t, listBody, &deleted)
	defer restore()

	p := New()
	start := time.Now()
	n, err := p.SweepOrphanEVS(context.Background(), "ak", "sk", "proj", "me-east-215",
		map[string]struct{}{}, true /*destructive*/, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SweepOrphanEVS: %v", err)
	}
	if n != 2 || len(deleted) != 2 {
		t.Fatalf("reaped n=%d deleted=%v, want exactly 2", n, deleted)
	}
	if elapsed < evsDeleteSpacing {
		t.Fatalf("2-delete sweep took %s — the #5028 throttle was not applied (want ≥ %s)", elapsed, evsDeleteSpacing)
	}
}

// TestSweepOrphanEVS_ThrottleRespectsContext proves the throttle honours the
// wipe/janitor deadline: with a long spacing and an already-cancelled context,
// the sweep returns ctx.Err() immediately without deleting anything (rather
// than blocking the whole spacing interval).
func TestSweepOrphanEVS_ThrottleRespectsContext(t *testing.T) {
	origSpacing := evsDeleteSpacing
	evsDeleteSpacing = time.Hour // would block forever if the ctx weren't honoured
	defer func() { evsDeleteSpacing = origSpacing }()

	listBody := `{"volumes":[
	  {"id":"vol-o1","name":"pvc-o1","status":"available","attachments":[],"metadata":{}}
	]}`
	var deleted []string
	restore := fakeHCSEVS(t, listBody, &deleted)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	p := New()
	_, err := p.SweepOrphanEVS(ctx, "ak", "sk", "proj", "me-east-215",
		map[string]struct{}{}, true /*destructive*/, nil)
	if err == nil {
		t.Fatal("expected ctx.Err() from the throttle wait, got nil")
	}
	if len(deleted) != 0 {
		t.Fatalf("cancelled sweep deleted %v — must stop before the DELETE", deleted)
	}
}
