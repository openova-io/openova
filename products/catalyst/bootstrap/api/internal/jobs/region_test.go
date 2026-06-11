// region_test.go — multi-region job-labeling coverage (#3276).
//
// The Sovereign Jobs page must surface jobs from EVERY region,
// region-labeled, post-handover. The region is encoded two ways that
// MUST stay in agreement:
//   - the "<region>:<chart>" prefix on a secondary region's component
//     id (AppID / JobName), and
//   - the first-class Job.Region field the bridge now stamps from that
//     prefix.
//
// These tests lock in (a) the prefix → Region extraction and (b) the
// store's preservation of Region across a follow-up state-transition
// merge (without which the Region column would blank out on the first
// transition after the seed).
package jobs

import (
	"testing"
	"time"
)

func TestRegionFromComponent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare primary chart — no region", "cilium", ""},
		{"empty — no region", "", ""},
		{"leading colon — no region", ":cilium", ""},
		{"simple region prefix", "fsn1:cilium", "fsn1"},
		{"hyphenated region prefix (kom4dc)", "me-east-215-b-1:cilium", "me-east-215-b-1"},
		{"region prefix on multi-word chart", "hel1-2:self-sovereign-cutover", "hel1-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RegionFromComponent(tc.in); got != tc.want {
				t.Fatalf("RegionFromComponent(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUpsertJob_PreservesRegionAcrossTransition proves a follow-up
// UpsertJob whose Region is empty (the OnHelmReleaseEvent transition
// path before this fix, and any caller that doesn't recompute the
// region) does NOT blank out a previously-stamped Region. Without the
// mergeJob carry-forward the multi-region Region column would vanish on
// the first state change after the seed.
func TestUpsertJob_PreservesRegionAcrossTransition(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const (
		depID  = "depmerge"
		region = "me-east-215-b-1"
		comp   = region + ":cilium"
	)
	jobName := JobNamePrefix + comp
	now := time.Now().UTC()

	// Seed with Region set (the fan-out's write shape).
	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      jobName,
		AppID:        comp,
		Region:       region,
		Type:         JobTypeInstall,
		Status:       StatusRunning,
		StartedAt:    &now,
	}); err != nil {
		t.Fatalf("UpsertJob (seed): %v", err)
	}

	// Transition merge with Region="" (the bug-prone shape).
	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      jobName,
		AppID:        comp,
		Region:       "", // empty — must NOT clobber the seeded region
		Type:         JobTypeInstall,
		Status:       StatusSucceeded,
	}); err != nil {
		t.Fatalf("UpsertJob (transition): %v", err)
	}

	job, _, err := st.GetJob(depID, JobID(depID, jobName))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Region != region {
		t.Fatalf("Region clobbered on transition merge: got %q; want %q", job.Region, region)
	}
	if job.Status != StatusSucceeded {
		t.Fatalf("Status not advanced: got %q; want %q", job.Status, StatusSucceeded)
	}
}
