package huawei

import "testing"

// TestIsReclaimableOrphanOBSBucket locks the #4872 OBS-bucket orphan
// match/protection contract — the pure selection logic the project-wide
// SweepOrphanOBSBuckets reclaim relies on. OBS buckets carry no usable HCS
// tag (Wave 5.4 disabled tags) so the decision is driven purely by the
// deterministic `catalyst-<sovereign-dashed>-<dep-id-prefix>` name:
//   - only catalyst-prefixed buckets are ever in scope (operator- / bastion-
//     owned and any non-catalyst bucket are hard-protected),
//   - protect-by-default: a bucket whose name carries an in-flight
//     deployment-ID 8-char prefix belongs to a live/parked/failed dep and is
//     never swept; only a genuinely-wiped dep leaves its bucket reclaimable.
func TestIsReclaimableOrphanOBSBucket(t *testing.T) {
	// One in-flight deployment protected by its 8-char ID prefix.
	active := map[string]struct{}{
		"5b413990": {},
	}

	cases := []struct {
		name   string
		bucket string
		want   bool
	}{
		// Reclaimable: catalyst bucket from a wiped Sovereign, no active prefix.
		{
			name:   "orphan bucket from a wiped prov (hw01-era leak)",
			bucket: "catalyst-hw01-omani-works-aabbccdd",
			want:   true,
		},
		{
			name:   "legacy no-suffix catalyst bucket from a wiped prov",
			bucket: "catalyst-t38-omani-works",
			want:   true,
		},

		// Protected: belongs to the in-flight deployment (prefix match).
		{
			name:   "live deployment's active bucket",
			bucket: "catalyst-hw229-omani-works-5b413990",
			want:   false,
		},

		// Hard-protected: not a catalyst bucket.
		{
			name:   "operator-owned openbao snapshot bucket",
			bucket: "openbao-snapshots",
			want:   false,
		},
		{
			name:   "unrelated third-party bucket",
			bucket: "some-customer-data-lake",
			want:   false,
		},

		// Hard-protected: bastion, even if it somehow carried the prefix.
		{
			name:   "bastion-named bucket",
			bucket: "bastion-openova-backups",
			want:   false,
		},
		{
			name:   "catalyst-prefixed bastion bucket (defence in depth)",
			bucket: "catalyst-bastion-openova-state",
			want:   false,
		},

		{
			name:   "empty name",
			bucket: "",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isReclaimableOrphanOBSBucket(tc.bucket, active)
			if got != tc.want {
				t.Fatalf("isReclaimableOrphanOBSBucket(%q) = %v, want %v", tc.bucket, got, tc.want)
			}
		})
	}
}

// TestIsReclaimableOrphanOBSBucket_NoActiveProvs proves the post-wipe sweep
// (empty active set) reclaims every catalyst-prefixed bucket while still
// hard-protecting bastion / non-catalyst buckets.
func TestIsReclaimableOrphanOBSBucket_NoActiveProvs(t *testing.T) {
	empty := map[string]struct{}{}
	if !isReclaimableOrphanOBSBucket("catalyst-hw10-omani-works-deadbeef", empty) {
		t.Fatal("with no in-flight provs every catalyst-prefixed bucket must be reclaimable")
	}
	if isReclaimableOrphanOBSBucket("bastion-openova-backups", empty) {
		t.Fatal("a bastion bucket must stay protected even with an empty active set")
	}
	if isReclaimableOrphanOBSBucket("openbao-snapshots", empty) {
		t.Fatal("a non-catalyst bucket must stay protected even with an empty active set")
	}
}

// TestIsReclaimableOrphanOBSBucket_ProtectsEveryActivePrefix proves that when
// several deployments are in flight, a bucket matching ANY of their prefixes
// is protected (the sweep never races a concurrent prov's backup bucket).
func TestIsReclaimableOrphanOBSBucket_ProtectsEveryActivePrefix(t *testing.T) {
	active := map[string]struct{}{
		"11112222": {},
		"33334444": {},
	}
	if isReclaimableOrphanOBSBucket("catalyst-hw30-omani-works-33334444", active) {
		t.Fatal("a bucket carrying a second in-flight dep's prefix must be protected")
	}
	if !isReclaimableOrphanOBSBucket("catalyst-hw30-omani-works-99998888", active) {
		t.Fatal("a bucket carrying no active prefix must be reclaimable")
	}
}
