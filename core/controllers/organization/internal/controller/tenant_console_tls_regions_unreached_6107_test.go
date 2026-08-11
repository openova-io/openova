// tenant_console_tls_regions_unreached_6107_test.go — #6107, the REPORTING
// defect underneath the routing defect.
//
// THE DEFECT
// ----------
// consoleRegionResolution has always had two buckets for a region that did not
// get its listener, and they route to opposite verdicts:
//
//	Unwired      -> out.Missing        -> complete() == false  (holds the Org)
//	Unreachable  -> out.Unverifiable   -> complete() == true   (does not)
//
// `complete()` is `len(Missing) == 0`, so a region in Unreachable was, as far
// as every status surface is concerned, indistinguishable from a region that
// was written successfully.
//
// hw293 (dep a0077ba47e3720e5) is what that costs. Its bridge Secret carried a
// well-formed, credentialled kubeconfig — 219 bytes, sha256 881231f95cd49646…
// — naming `https://212.72.24.6:6443`, which is NEITHER region (A answers on
// .43, B on .25) and answers nothing at all. So:
//
//   - the shared usability contract passed it, and the region counted as WIRED
//     (`expectedRemotes > len(regions)` became 1 > 1, i.e. no shortfall);
//   - every write to it timed out and the region landed in Unreachable;
//   - all six Organizations read Ready/Reconciled for hours while region B's
//     `cilium-gateway-console` carried ZERO per-Org listeners — catalyst-api's
//     own emitter said so in the same log, `regions_declared=2
//     regions_written=1`, on every one of them.
//
// Reporting an Organization provisioned there is a verdict published from
// absent evidence: the pass neither wrote the pair in that region nor read one
// back.
//
// THE FIX, AND WHY IT IS NOT "EVERYTHING IS INCOMPLETE"
// -----------------------------------------------------
// Unreachable now feeds Missing — but the DENOMINATOR failures that used to
// share that bucket move to their own, Undecidable, and keep feeding
// Unverifiable. The two are different claims:
//
//	"I know this region exists and could not reach it"  -> no listener there
//	"I could not work out how many regions there are"   -> genuinely unknown
//
// Only the first is a missing artifact. Folding the second in would red-flag
// every Organization on a Sovereign whose witness read errored once, which is
// the over-correction the previous routing was (correctly) worried about — it
// just applied that worry to the wrong bucket.
//
// Missing is still a requeue, so a genuine apiserver blip clears on the next
// pass. What it no longer does is pass silently for hours.
//
// Refs #5246 #5511 #6015 #6027 #6107.
package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// deliveredRegionKubeconfig is a document that passes the bytes contract
// completely — five sections, resolvable current-context, bearer-shaped
// credential — for the region key the fixture declares. It carries no secret:
// its credential is the literal string `not-a-real-token`.
//
// It is what a MIS-delivery looks like from this controller's side. The
// controller has no cheap endpoint probe and deliberately claims no
// reachability verdict of its own; what it sees is a credential that resolves
// and a region it cannot reach, which is exactly the state under test.
const deliveredRegionKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.6:6443
    insecure-skip-tls-verify: true
  name: c
contexts:
- name: c
  context:
    cluster: c
    user: c
current-context: c
users:
- name: c
  user:
    token: not-a-real-token
`

// seedDeliveredBridgeSecret writes the bridge Secret with a USABLE value under
// the fixture's declared secondary region key — i.e. the region WILL be
// counted as wired, which is what makes Unreachable (not Unwired) the bucket
// under test.
func seedDeliveredBridgeSecret(t *testing.T, f multiRegionFixture) {
	t.Helper()
	bridge := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      consoleSecondaryKubeconfigSecretDefaultName,
			Namespace: consoleSecondaryKubeconfigSecretDefaultNamespace,
		},
		Data: map[string][]byte{secondaryRegionKey + ".yaml": []byte(deliveredRegionKubeconfig)},
	}
	if err := f.r.Create(context.Background(), bridge); err != nil {
		t.Fatalf("seed bridge Secret: %v", err)
	}
}

// errOnGetClient fails Get for ONE object key and delegates everything else,
// so a single witness can be made unreadable without disturbing the rest of
// the fixture.
type errOnGetClient struct {
	client.Client
	failNS, failName string
	err              error
}

func (c errOnGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Namespace == c.failNS && key.Name == c.failName {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// TestVerifyProvisioned_UnreachedSecondaryRegionHoldsTheOrgBack_6107 is the
// RED case. Before this change verifyProvisioned put the region in
// Unverifiable and complete() returned true.
func TestVerifyProvisioned_UnreachedSecondaryRegionHoldsTheOrgBack_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions
	seedDeliveredBridgeSecret(t, f)
	// The endpoint is in neither region, so no client can be used against it.
	// The builder failing is this fixture's stand-in for that dial timing out.
	f.r.RegionClientBuilder = func([]byte) (client.Client, error) {
		return nil, errors.New("dial tcp 212.72.24.6:6443: i/o timeout")
	}

	res := f.r.consoleRegionTargets(ctx)

	// Precondition — the region must land in Unreachable, not Unwired. If it
	// were Unwired this case would pass for the reason #6107's first rung
	// already covers, and would assert nothing new.
	if len(res.Unwired) != 0 {
		t.Fatalf("precondition: the value is usable, so the region must be WIRED; unwired=%v", res.Unwired)
	}
	if len(res.Unreachable) == 0 {
		t.Fatalf("precondition: a region whose client cannot be built must land in Unreachable; got targets=%d", len(res.Targets))
	}
	if len(res.Undecidable) != 0 {
		t.Fatalf("a region-level failure was filed as a DENOMINATOR failure: %v", res.Undecidable)
	}

	// Write the host-region listener so nothing else can be the cause.
	if _, err := f.r.ensureConsoleOrgListener(ctx, f.r.Client, f.names); err != nil {
		t.Fatalf("write the host-region listener: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)

	got := f.r.verifyProvisioned(ctx, f.org)
	if got.complete() {
		t.Fatalf("Organization reads FULLY PROVISIONED while a declared secondary region was never reached — "+
			"this pass neither wrote the per-Org pair there nor read one back, and the console EIP round-robins "+
			"both regions, so a share of customer TLS to %q resets. missing=%v unverifiable=%v",
			f.names.WildcardHost, got.Missing, got.Unverifiable)
	}
	joined := strings.Join(got.Missing, " | ")
	if !strings.Contains(joined, secondaryRegionKey) {
		t.Fatalf("the Missing entry does not name the region that was not reached: %q", joined)
	}
	if !strings.Contains(joined, "unreached") {
		t.Fatalf("the Missing entry does not distinguish an UNREACHED region from an absent artifact: %q", joined)
	}
}

// TestVerifyProvisioned_ReachedSecondaryRegionStaysComplete_6107 is the
// CONTROL that shares the suspect property. Same fixture, same declared
// region, same non-empty bridge value — and a region client that works. The
// Organization must still read complete, or the fix has over-corrected into
// "everything is incomplete".
func TestVerifyProvisioned_ReachedSecondaryRegionStaysComplete_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions
	seedDeliveredBridgeSecret(t, f)
	f.r.RegionClientBuilder = func([]byte) (client.Client, error) { return f.region, nil }

	res := f.r.consoleRegionTargets(ctx)
	if len(res.Unwired) != 0 || len(res.Unreachable) != 0 || len(res.Undecidable) != 0 {
		t.Fatalf("a usable credential at a declared region reported a gap: unwired=%v unreachable=%v undecidable=%v",
			res.Unwired, res.Unreachable, res.Undecidable)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("targets = %d, want 2 (host + the wired secondary)", len(res.Targets))
	}

	if _, err := f.r.reconcileTenantConsoleTLS(ctx, f.org); err != nil {
		t.Fatalf("the up-path failed against a reachable secondary region: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)
	syncGatewayStatus(t, f.region)

	got := f.r.verifyProvisioned(ctx, f.org)
	if !got.complete() {
		t.Fatalf("an Organization whose listener pair reached EVERY declared region reads incomplete — "+
			"the fix has over-corrected. missing=%v unverifiable=%v", got.Missing, got.Unverifiable)
	}
}

// TestVerifyProvisioned_AbsentKeyStillMissing_6107 is the CONTROL for the case
// the previous rungs were built on: a genuinely ABSENT secondary key still
// lands in Missing with the #6027 shortfall wording. Widening Unreachable must
// not have swallowed it.
func TestVerifyProvisioned_AbsentKeyStillMissing_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions

	res := f.r.consoleRegionTargets(ctx)
	if len(res.Unwired) == 0 {
		t.Fatal("a declared secondary region with no bridge key must still be UNWIRED")
	}
	joined := strings.Join(res.Unwired, " | ")
	if !strings.Contains(joined, "wired region keys: none") {
		t.Fatalf("the #6027 shortfall wording was lost: %q", joined)
	}
	if got := f.r.verifyProvisioned(ctx, f.org); got.complete() {
		t.Fatalf("an Organization with no credential for a declared region reads complete: missing=%v", got.Missing)
	}
}

// TestVerifyProvisioned_UndecidableCountIsUnverifiable_6107 is the
// ANTI-OVER-CORRECTION control, and the reason Undecidable exists as its own
// field. A witness this pass could not READ leaves the expected region count
// unknown — a claim about the denominator, not about any artifact. It must
// requeue and say so, never red-flag every Organization on the Sovereign.
func TestVerifyProvisioned_UndecidableCountIsUnverifiable_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	// Single-region topology: with the mesh witness readable there would be no
	// remote at all, so anything reported here comes from the unreadable
	// witness and nothing else.
	f.r.ConfiguredRegions = "hw-me-east-215-a-rtz-prod"
	f.r.Client = errOnGetClient{
		Client:   f.r.Client,
		failNS:   consoleClusterMeshSecretDefaultNamespace,
		failName: consoleClusterMeshSecretDefaultName,
		err:      errors.New("etcdserver: request timed out"),
	}

	res := f.r.consoleRegionTargets(ctx)
	if len(res.Undecidable) == 0 {
		t.Fatal("an unreadable region witness must be reported as an UNDECIDABLE count")
	}
	if len(res.Unreachable) != 0 {
		t.Fatalf("a denominator failure was filed as an unreached REGION, which now holds every Org back: %v", res.Unreachable)
	}

	if _, err := f.r.ensureConsoleOrgListener(ctx, f.r.Client, f.names); err != nil {
		t.Fatalf("write the host-region listener: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)

	got := f.r.verifyProvisioned(ctx, f.org)
	if len(got.Unverifiable) == 0 {
		t.Fatal("the undecidable count never reached the postcondition report")
	}
	for _, m := range got.Missing {
		if strings.Contains(m, "request timed out") {
			t.Fatalf("a witness read error was turned into a MISSING artifact — every Organization on the Sovereign would red-flag on one bad Get: %q", m)
		}
	}
	if !got.complete() {
		t.Fatalf("an unreadable witness held the Organization back as if an artifact were absent: missing=%v", got.Missing)
	}
}

// TestVerifyProvisioned_UnreachedVacuity_6107 is the VACUITY CHECK. Every
// green assertion above would also hold against an implementation that never
// produced a Missing entry for a region. This flips ONE input — whether the
// region client builds — inside one fixture shape, and requires the verdict to
// change.
func TestVerifyProvisioned_UnreachedVacuity_6107(t *testing.T) {
	ctx := context.Background()

	verdict := func(buildFails bool) provisioningPostconditions {
		f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
		f.r.ConfiguredRegions = hw293ConfiguredRegions
		seedDeliveredBridgeSecret(t, f)
		if buildFails {
			f.r.RegionClientBuilder = func([]byte) (client.Client, error) {
				return nil, errors.New("dial tcp 212.72.24.6:6443: i/o timeout")
			}
		} else {
			f.r.RegionClientBuilder = func([]byte) (client.Client, error) { return f.region, nil }
		}
		if _, err := f.r.reconcileTenantConsoleTLS(ctx, f.org); err != nil && !buildFails {
			t.Fatalf("up-path failed on the reachable arm: %v", err)
		}
		syncGatewayStatus(t, f.r.Client)
		syncGatewayStatus(t, f.region)
		return f.r.verifyProvisioned(ctx, f.org)
	}

	reachable := verdict(false)
	unreached := verdict(true)
	if !reachable.complete() {
		t.Fatalf("control arm is not complete, so the comparison proves nothing: missing=%v", reachable.Missing)
	}
	if unreached.complete() {
		t.Fatal("VACUITY: flipping the region client from working to failing changed nothing — the new assertion cannot fail")
	}
	if len(unreached.Missing) == len(reachable.Missing) {
		t.Fatalf("VACUITY: both arms report %d missing artifacts", len(reachable.Missing))
	}
}
