package huawei

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

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
