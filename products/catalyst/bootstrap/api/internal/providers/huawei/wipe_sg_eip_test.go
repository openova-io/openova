// #4046 — janitor SG-leak + EIP-egress cooldown preflight.
//
// Two durable fixes root-caused by a 2026-06-21 live Huawei audit:
//
//   FIX 1 — the Wipe() purge-retry break-condition used to sum ONLY the
//   three quota-relevant residual kinds (networks/nat_gateways/floating_
//   ips), so a security group that survived a 409-on-port-settle fell
//   outside the sum and the loop broke early → one region's SG leaked on
//   EVERY wipe. The break is now gated on the FULL zero-orphans verdict
//   (wipeRetryShouldBreak → out.VerifiedZeroOrphans).
//
//   FIX 2 — a freshly-released poisoned NAT EIP was not recorded with a
//   cooldown, so the immediate next rapid re-prov re-drew it and region-a
//   never bootstrapped (phase-1 timeout). Wipe() now records every released
//   EIP into the same TTL-bounded cooldown store the preflight reads.

package huawei

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// TestWipeRetryShouldBreak_SGResidualDoesNotBreak is the core FIX-1
// regression guard. Before #4046 the loop summed only networks +
// nat_gateways + floating_ips, so a surviving security group (the
// 409-on-port-settle case) yielded quotaResidual==0 and the loop BROKE,
// leaking the SG. The break must now refuse to exit while ANY residual —
// including a non-quota firewall/server/load_balancer/keypair — remains.
func TestWipeRetryShouldBreak_SGResidualDoesNotBreak(t *testing.T) {
	cases := []struct {
		name          string
		out           *providers.WipeResult
		wantCanBreak  bool
		failExplainer string
	}{
		{
			name: "SG residual only — must NOT break (the leak this fixes)",
			out: &providers.WipeResult{
				VerifiedZeroOrphans: false, // verifyZeroOrphans sets false whenever any kind survives
				ResidualOrphans: map[string][]string{
					"firewalls": {"catalyst-t99-omani-works-a-sg"},
				},
			},
			wantCanBreak:  false,
			failExplainer: "a surviving security group must keep the retry loop running (the #4046 leak)",
		},
		{
			name: "server residual only — must NOT break",
			out: &providers.WipeResult{
				VerifiedZeroOrphans: false,
				ResidualOrphans:     map[string][]string{"servers": {"catalyst-t99-omani-works-a-cp-1"}},
			},
			wantCanBreak:  false,
			failExplainer: "a surviving ECS must keep the retry loop running",
		},
		{
			name: "load_balancer residual only — must NOT break",
			out: &providers.WipeResult{
				VerifiedZeroOrphans: false,
				ResidualOrphans:     map[string][]string{"load_balancers": {"catalyst-t99-omani-works-elb"}},
			},
			wantCanBreak:  false,
			failExplainer: "a surviving ELB must keep the retry loop running",
		},
		{
			name: "keypair residual only — must NOT break",
			out: &providers.WipeResult{
				VerifiedZeroOrphans: false,
				ResidualOrphans:     map[string][]string{"keypairs": {"catalyst-t99-omani-works-key"}},
			},
			wantCanBreak:  false,
			failExplainer: "a surviving keypair must keep the retry loop running",
		},
		{
			name: "quota residual (VPC) — must NOT break (unchanged behaviour)",
			out: &providers.WipeResult{
				VerifiedZeroOrphans: false,
				ResidualOrphans:     map[string][]string{"networks": {"catalyst-t99-omani-works-vpc"}},
			},
			wantCanBreak:  false,
			failExplainer: "a surviving VPC must keep the retry loop running (pre-existing contract)",
		},
		{
			name: "zero orphans verified — MAY break",
			out: &providers.WipeResult{
				VerifiedZeroOrphans: true,
				ResidualOrphans:     nil, // verifyZeroOrphans nils the map on a clean scan
			},
			wantCanBreak:  true,
			failExplainer: "a genuinely clean account must allow the loop to exit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wipeRetryShouldBreak(tc.out)
			if got != tc.wantCanBreak {
				t.Fatalf("wipeRetryShouldBreak = %v, want %v — %s", got, tc.wantCanBreak, tc.failExplainer)
			}
		})
	}
}

// TestWipeRetryShouldBreak_NilSafe — a nil result must not panic; there is
// nothing to retry against, so the loop is allowed to stop.
func TestWipeRetryShouldBreak_NilSafe(t *testing.T) {
	if !wipeRetryShouldBreak(nil) {
		t.Fatal("nil WipeResult should allow break (nothing to retry)")
	}
}

// TestWipeRetryBreak_OldQuotaOnlyConditionWouldHaveLeaked documents — and
// pins — the precise defect: the OLD break condition (sum of the three
// quota kinds) would have returned 0 for an SG-only residual and broken the
// loop, leaking the SG. We reproduce that old sum here and assert it
// DISAGREES with the new predicate for exactly that input, so a future
// refactor can't silently regress back to the quota-only sum.
func TestWipeRetryBreak_OldQuotaOnlyConditionWouldHaveLeaked(t *testing.T) {
	out := &providers.WipeResult{
		VerifiedZeroOrphans: false,
		ResidualOrphans: map[string][]string{
			"firewalls": {"catalyst-t99-omani-works-a-sg"}, // the leaked SG
		},
	}

	// The OLD (buggy) break condition, reproduced verbatim.
	oldQuotaResidual := len(out.ResidualOrphans["networks"]) +
		len(out.ResidualOrphans["nat_gateways"]) +
		len(out.ResidualOrphans["floating_ips"])
	oldWouldBreak := oldQuotaResidual == 0

	newWouldBreak := wipeRetryShouldBreak(out)

	if !oldWouldBreak {
		t.Fatal("setup invalid: old quota-only sum should be 0 for an SG-only residual")
	}
	if newWouldBreak {
		t.Fatal("#4046 regression: new break predicate broke on an SG-only residual, same as the old leak")
	}
	if oldWouldBreak == newWouldBreak {
		t.Fatal("#4046: the new predicate must DIFFER from the old quota-only sum for an SG-only residual")
	}
}

// TestWipe_RecordsReleasedEIPsToCooldown is the FIX-2 contract at the store
// boundary: the addresses a wipe just released (out.ProviderPurge["floating
// _ips"] + any survived-but-soon-released residual) are recorded into the
// SAME cooldown blocklist the preflight reads, so the immediate next prov
// auto-avoids a freshly-freed (reputation-poisoned) EIP rather than re-
// drawing it and wedging region-a on the phase-1 timeout.
//
// We exercise the exact persistence call Wipe() makes (recordPoisonedEIPs)
// and assert the addresses surface through blocklist() on the next prov —
// the cross-prov cooldown loop — without needing to stand up the full HCS
// API (endpointFor targets real kom4dc hosts, unmockable here).
func TestWipe_RecordsReleasedEIPsToCooldown(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)
	// Pin the env blocklist empty so we only observe the cooldown effect.
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST", "")

	// Simulate the addresses a wipe released: 2 deleted + 1 that survived the
	// purge (recorded under ResidualOrphans, still in HCS, will free soon).
	released := []string{"212.72.24.91", "212.72.24.92"}
	survived := []string{"212.72.24.93"}

	// Reproduce the Wipe() cooldown step (provider.go) verbatim in spirit:
	// concat released + survived and record them with a wipe note.
	all := append(append([]string(nil), released...), survived...)
	now := time.Now()
	if err := recordPoisonedEIPs(all, "wipe:deadbeefcafef00d", now); err != nil {
		t.Fatalf("recordPoisonedEIPs (cooldown): %v", err)
	}

	// The IMMEDIATE next prov (same wall clock, well within TTL) must avoid
	// every released + survived address via the merged blocklist().
	bl := blocklist()
	for _, want := range append(append([]string(nil), released...), survived...) {
		if !bl[want] {
			t.Fatalf("#4046: freshly-released EIP %s not in next-prov cooldown blocklist (region-a would re-draw it): %v", want, bl)
		}
	}
}

// TestWipe_NoReleasedEIPsIsNoop guards the common clean-account case: a wipe
// that released no EIPs must not write a spurious cooldown file (the empty-
// input no-op that recordPoisonedEIPs already enforces, asserted here at the
// Wipe-intent level).
func TestWipe_NoReleasedEIPsIsNoop(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)

	// An empty release set (no floating_ips purged, no residual) → no-op.
	if err := recordPoisonedEIPs(nil, "wipe:empty", time.Now()); err != nil {
		t.Fatalf("empty cooldown record should be a no-op, got: %v", err)
	}
	// blocklist() must still resolve to just the seed (no learned poison).
	bl := blocklist()
	if len(bl) == 0 {
		t.Fatal("seed blocklist unexpectedly empty")
	}
	for _, seed := range []string{"212.72.24.48", "212.72.24.14"} {
		if !bl[seed] {
			t.Fatalf("seed %s missing after empty cooldown record: %v", seed, bl)
		}
	}
}
