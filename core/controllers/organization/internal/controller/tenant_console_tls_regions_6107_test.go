// tenant_console_tls_regions_6107_test.go — #6107. A secondary region's
// kubeconfig that EXISTS but cannot yield a client is a missing artifact, not
// a transient blip.
//
// THE DEFECT
// ----------
// #6027 closed the door where the #5359 bridge Secret is ABSENT: two witnesses
// declare how many remote regions the Sovereign has, and a shortfall against
// the wired-key count is reported as UNWIRED, which verifyProvisioned turns
// into a NOT-provisioned Organization.
//
// The door where the Secret is PRESENT but its value is unusable was still
// open, and the partition that decides which door a region goes through tested
// only emptiness:
//
//	if region == "" || len(v) == 0 { continue }
//	regions = append(regions, region)
//
// A 95-byte contextless kubeconfig stub — the shape #6054 and #6104 exist to
// stop being WRITTEN, and the shape already on hw293's PVC — is non-empty, so
// it entered `regions`. Two things then followed, in order:
//
//  1. the shortfall check `expectedRemotes > len(regions)` became `1 > 1`,
//     which is FALSE, so no shortfall was reported at all; and
//  2. the client build failed a few lines later and the region landed in
//     Unreachable.
//
// Those two buckets are deliberately not equivalent. Unwired feeds
// out.Missing; Unreachable feeds out.Unverifiable; and
// `complete()` is `len(p.Missing) == 0` — Unverifiable does not hold an
// Organization back. So the Organization read FULLY PROVISIONED while its
// `console-https-<slug>` / `console-http-<slug>` pair had never been written
// to region B, and one shared console EIP round-robins both regions' envoy.
// That is the silent degradation tenant_console_tls_regions.go's own header
// declares impossible under "WHY IT CANNOT DEGRADE SILENTLY".
//
// WHY THIS IS STRUCTURAL AND NOT TRANSIENT
// ----------------------------------------
// Measured in-package, no network and no apiserver: a contextless stub and an
// apiVersion/kind-only document both fail `RESTConfigFromKubeConfig` with
// "invalid configuration: no configuration has been provided", while a
// complete credential-free kubeconfig parses and yields its Host. The verdict
// is a property of the BYTES, so requeueing can never change it — which is the
// struct's own definition of Unwired. `client.New` on the same config was
// measured lazy (no discovery call), so a genuine live-build failure remains a
// separate, genuinely transient signal and stays in Unreachable.
//
// WHAT IS ASSERTED HERE
// ---------------------
// Every case asserts on the VALUE the resolution produced, and the suite
// carries two controls that share the suspect property — a NON-EMPTY value in
// the bridge Secret — but must answer the other way, so the new constraint
// cannot pass by having widened the arithmetic:
//
//   - case 1 (the defect): a stub value holds the Organization back and names
//     the region plus the parse reason.
//   - case 2 (CONTROL): a COMPLETE credential-free kubeconfig, equally
//     non-empty, in the same fixture, still resolves to a writable target and
//     reports no shortfall.
//   - case 3 (CONTROL): a genuinely ABSENT key still produces the #6027
//     message with `wired region keys: none`, so the new wording did not
//     swallow the case it was built on.
//
// Refs #6015 #6027 #6104 #6107.
package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stubSecondaryKubeconfig is the hw293 shape: valid YAML, a cluster server,
// and NO contexts, users or CA data. It carries no credential material of any
// kind — the point of the fixture is precisely that there is none to carry.
const stubSecondaryKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://10.0.0.1:6443
  name: r
`

// completeSecondaryKubeconfig is the CONTROL value: same non-emptiness, same
// bridge-Secret key shape, but it satisfies the shared usability contract.
//
// The `users:` entry is load-bearing and is why the contract is deliberately
// STRICTER than "RESTConfigFromKubeConfig returned no error". A kubeconfig
// with a context but no user parses happily and builds an ANONYMOUS client —
// one that reaches the peer apiserver as `system:anonymous` and is refused 403
// on every write. That is the same silent-success class this whole chain
// exists to kill: a client that builds and can never write the listener. The
// first draft of this fixture omitted `users:` and the control caught it,
// which is the reason the control is here.
//
// The user entry carries NO credential material — an empty `user: {}` is
// enough to make the section non-empty, so nothing secret-shaped enters a
// public repository.
const completeSecondaryKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://10.0.0.1:6443
    insecure-skip-tls-verify: true
  name: r
users:
- name: u
  user: {}
contexts:
- context:
    cluster: r
    user: u
  name: c
current-context: c
`

// seedBridgeSecret writes the #5359 bridge Secret with one secondary-region
// key carrying exactly the supplied bytes, and drops the RegionClientBuilder
// seam so the PRODUCTION resolution path runs — the parse under test is the
// real one, not a fake injected by the fixture.
func seedBridgeSecret(t *testing.T, f multiRegionFixture, kubeconfig string) {
	t.Helper()
	bridge := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      consoleSecondaryKubeconfigSecretDefaultName,
			Namespace: consoleSecondaryKubeconfigSecretDefaultNamespace,
		},
		Data: map[string][]byte{secondaryRegionKey + ".yaml": []byte(kubeconfig)},
	}
	if err := f.r.Create(context.Background(), bridge); err != nil {
		t.Fatalf("seed secondary-region kubeconfig bridge: %v", err)
	}
	f.r.RegionClientBuilder = nil
}

// TestConsoleRegionTargets_UnusableSecondaryKubeconfigIsUnwired_6107 is the
// defect: a bridge Secret whose only value is a contextless stub must hold the
// Organization back, not read as a wired region whose client happens to be
// having a bad day.
func TestConsoleRegionTargets_UnusableSecondaryKubeconfigIsUnwired_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions
	seedBridgeSecret(t, f, stubSecondaryKubeconfig)

	res := f.r.consoleRegionTargets(ctx)

	// Precondition: the stub cannot become a writable target either way. If
	// this ever fails the rest of the case is meaningless.
	if len(res.Targets) != 1 || !res.Targets[0].Host {
		t.Fatalf("precondition: a stub kubeconfig must not yield a writable target, got %d targets", len(res.Targets))
	}

	joined := strings.Join(res.Unwired, " | ")
	if len(res.Unwired) == 0 {
		t.Fatalf("a %d-byte contextless kubeconfig stub was counted as a WIRED region — "+
			"the shortfall check saw 1 declared remote against 1 wired key, reported nothing, and the "+
			"region degraded into Unreachable, which only requeues. unwired=%v unreachable=%v",
			len(stubSecondaryKubeconfig), res.Unwired, res.Unreachable)
	}
	// Assert on the VALUE: the message has to name the region whose credential
	// is unusable and why, or it repeats the very claim that is false here —
	// that there is "no kubeconfig" for a key that plainly exists.
	if !strings.Contains(joined, secondaryRegionKey) {
		t.Fatalf("the shortfall message does not name the region whose kubeconfig is unusable: %q", joined)
	}
	// The message must name WHAT is missing, in the shared contract's own
	// vocabulary, or an operator cannot tell an unusable credential from an
	// absent one — and cannot diff the rejected document against a healthy one.
	for _, section := range []string{"contexts", "current-context", "users"} {
		if !strings.Contains(joined, section) {
			t.Fatalf("the shortfall message does not name the missing %q section: %q", section, joined)
		}
	}
	if !strings.Contains(joined, "cannot produce a client") {
		t.Fatalf("the shortfall message does not say the credential is unusable, only that it is absent: %q", joined)
	}

	// The consumer half, and the whole point: the Organization must NOT read
	// fully provisioned. Its per-Org listener pair was never written to the
	// declared secondary region.
	if _, err := f.r.ensureConsoleOrgListener(ctx, f.r.Client, f.names); err != nil {
		t.Fatalf("write the host-region listener: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)

	got := f.r.verifyProvisioned(ctx, f.org)
	if got.complete() {
		t.Fatalf("Organization reads FULLY PROVISIONED while region %q holds an unusable kubeconfig and "+
			"carries zero per-Org listeners — the console EIP round-robins both regions, so a share of "+
			"customer TLS to %q resets. missing=%v unverifiable=%v",
			secondaryRegionKey, f.names.WildcardHost, got.Missing, got.Unverifiable)
	}
}

// TestConsoleRegionTargets_CompleteSecondaryKubeconfigStaysWired_6107 is the
// CONTROL for the case above. It shares the suspect property — a non-empty
// value under the same bridge-Secret key — and must answer the other way.
//
// Without it, the case above would also pass if the change had simply started
// treating EVERY secondary region as unwired, which would red-flag every
// correctly-wired Sovereign on the estate.
func TestConsoleRegionTargets_CompleteSecondaryKubeconfigStaysWired_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions
	seedBridgeSecret(t, f, completeSecondaryKubeconfig)

	res := f.r.consoleRegionTargets(ctx)

	if len(res.Unwired) != 0 {
		t.Fatalf("a COMPLETE secondary kubeconfig was reported unwired — the check is discriminating on "+
			"non-emptiness or on nothing at all, rather than on whether the bytes yield a client: %v", res.Unwired)
	}
	if len(res.Unreachable) != 0 {
		t.Fatalf("a COMPLETE secondary kubeconfig was reported unreachable: %v", res.Unreachable)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("want host + secondary targets, got %d", len(res.Targets))
	}
	if res.Targets[1].Region != secondaryRegionKey || res.Targets[1].Host {
		t.Fatalf("second target is not the secondary region: %+v", res.Targets[1].Region)
	}
}

// TestConsoleRegionTargets_AbsentKeyKeepsTheNoneWording_6107 is the second
// CONTROL: the #6027 case this change sits next to must be untouched. An
// ABSENT key still reports the shortfall with `wired region keys: none`, and
// must NOT acquire a parse reason it has no business carrying.
func TestConsoleRegionTargets_AbsentKeyKeepsTheNoneWording_6107(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions
	f.r.RegionClientBuilder = nil

	res := f.r.consoleRegionTargets(ctx)

	joined := strings.Join(res.Unwired, " | ")
	if len(res.Unwired) == 0 {
		t.Fatalf("#6027 regression: a 2-region Sovereign with no bridge Secret reported no shortfall")
	}
	if !strings.Contains(joined, "wired region keys: none") {
		t.Fatalf("the absent-key message lost its `none` wording: %q", joined)
	}
	if strings.Contains(joined, "cannot produce a client") {
		t.Fatalf("the absent-key message claims a credential is UNUSABLE, but there were no bytes at all — "+
			"the two states must stay distinguishable: %q", joined)
	}
	if !strings.Contains(joined, "present but unusable: none") {
		t.Fatalf("the absent-key message does not state that nothing was present-but-unusable: %q", joined)
	}

	// And it must still hold the Organization back — an absent credential was
	// already a missing artifact before #6107 and must remain one.
	if _, err := f.r.ensureConsoleOrgListener(ctx, f.r.Client, f.names); err != nil {
		t.Fatalf("write the host-region listener: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)
	if got := f.r.verifyProvisioned(ctx, f.org); got.complete() {
		t.Fatalf("#6027 regression: an ABSENT secondary kubeconfig no longer holds the Organization back. missing=%v", got.Missing)
	}
}
