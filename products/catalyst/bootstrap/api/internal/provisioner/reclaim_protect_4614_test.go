package provisioner

import "testing"

// TestReclaimProtectSet_ProtectsAllLiveDeployments is the #4614 regression:
// the in-band VPC-quota reclaim must protect EVERY live deployment, not just
// the firing prov. On 2026-06-28 the reclaim's protect-set held only the
// firing prov's own prefix, so SweepOrphanVPCs treated the live production
// omantel.biz VPC (a different prefix sharing the kom4dc project) as a
// reclaimable orphan and cascade-deleted all 12 production nodes.
func TestReclaimProtectSet_ProtectsAllLiveDeployments(t *testing.T) {
	orig := ActiveDepPrefixesHook
	t.Cleanup(func() { ActiveDepPrefixesHook = orig })

	// Simulate the handler's #4454 allowlist: two OTHER live deployments
	// (the production Sovereign + a sibling) share the project.
	ActiveDepPrefixesHook = func() map[string]struct{} {
		return map[string]struct{}{"91dc0591": {}, "5150f960": {}}
	}

	got := reclaimProtectSet("aaaaaaaa1111222233334444")

	// The firing prov AND both live deployments must all be protected.
	for _, want := range []string{"aaaaaaaa", "91dc0591", "5150f960"} {
		if _, ok := got[want]; !ok {
			t.Errorf("reclaimProtectSet missing %q — its VPC would be reaped by the in-band reclaim (the #4614 production-delete fault)", want)
		}
	}
	if len(got) != 3 {
		t.Errorf("reclaimProtectSet = %d prefixes, want 3 (firing prov + 2 live)", len(got))
	}
}

// TestReclaimProtectSet_NilHookProtectsFiringProv guards the CI / no-handler
// path: with no ActiveDepPrefixesHook wired, the reclaim still protects the
// firing prov's own (not-yet-created) VPCs — the pre-#4614 baseline, never
// weaker.
func TestReclaimProtectSet_NilHookProtectsFiringProv(t *testing.T) {
	orig := ActiveDepPrefixesHook
	t.Cleanup(func() { ActiveDepPrefixesHook = orig })
	ActiveDepPrefixesHook = nil

	got := reclaimProtectSet("bbbbbbbb5555666677778888")
	if _, ok := got["bbbbbbbb"]; !ok {
		t.Error("reclaimProtectSet must still protect the firing prov when the hook is nil")
	}
	if len(got) != 1 {
		t.Errorf("reclaimProtectSet (nil hook) = %d prefixes, want 1", len(got))
	}
}

// TestReclaimProtectSet_ShortDeploymentID tolerates a malformed/short
// deployment ID without panicking (the < 8-char guard) and still folds in
// the live-deployment allowlist.
func TestReclaimProtectSet_ShortDeploymentID(t *testing.T) {
	orig := ActiveDepPrefixesHook
	t.Cleanup(func() { ActiveDepPrefixesHook = orig })
	ActiveDepPrefixesHook = func() map[string]struct{} { return map[string]struct{}{"91dc0591": {}} }

	got := reclaimProtectSet("short")
	if _, ok := got["91dc0591"]; !ok {
		t.Error("reclaimProtectSet must protect the live deployment even when the firing prov ID is too short to add")
	}
	if len(got) != 1 {
		t.Errorf("reclaimProtectSet (short ID) = %d prefixes, want 1 (only the live deployment)", len(got))
	}
}
