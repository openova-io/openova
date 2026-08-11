// secondary_kubeconfig_misdelivery_6107_test.go — the THIRD bridge state.
//
// THE MEASURED ARTEFACT (hw293, dep a0077ba47e3720e5, 2026-08-11)
// ---------------------------------------------------------------
//
//	/var/lib/catalyst/kubeconfigs/                    EMPTY — region B was never delivered
//	Secret catalyst/cutover-secondary-kubeconfigs
//	  annotations: deployment-id=a0077ba47e3720e5, materialized=2026-08-11T01:46:02Z
//	  me-east-215-b-1.yaml   219 bytes   sha256 881231f95cd49646…
//	  server: https://212.72.24.6:6443
//	catalyst-system/sovereign-fqdn  configuredRegions=
//	  hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod
//
//	catalyst-api, every 60 seconds, for hours:
//	  03:06:40 "accepting as-is" keys=1 expected=1
//	  03:07:40 "accepting as-is" keys=1 expected=1
//	  03:08:40 "accepting as-is" keys=1 expected=1
//	and, in the same log, for all six Organizations:
//	  "this Sovereign DECLARES more regions than carry a per-Org console
//	   listener" regions_declared=2 regions_written=1 regions=host
//
// `212.72.24.6` is neither region. Region A's apiserver answers on
// `212.72.24.43` and region B's on `212.72.24.25`; each proves its own
// identity through its serving certificate's SANs
// (`…-me-east-215-a-cp1-…` / `…-me-east-215-b-cp1-…`, one shared k3s CA).
// `212.72.24.6` answers nothing: no ICMP, no :443, 12s timeout on :6443.
//
// WHY THE MERGED FIXES WERE INERT HERE
// ------------------------------------
// #6054 stopped a credential-less shell being RECEIVED. #6112/#6116 stopped
// one being FORWARDED and CREATED. #6121 gave writer and reader one usability
// contract. Every one of them measures the same thing: can these bytes build a
// client. This value can. It is not a delivery all the same, and the arm that
// let it stand is the #5488 recovery arm — which short-circuits
// materializeSecondaryKubeconfigsSecret BEFORE its write. So the mis-delivered
// value is STICKY: a correct delivery landing later cannot displace it,
// because the pass that would write it returns before the write. That is what
// makes this the state in which the merged fixes cannot act.
//
// WHAT IS ASSERTED, AND THE CONTROLS
// ----------------------------------
// Acceptance now requires CORROBORATION — provenance (files on disk) OR proof
// (the endpoint answers) — and every case below flips exactly one of those
// inputs against a control that must answer the other way:
//
//   - MisDeliveredEndpointIsNotRecovery — the live state. REFUSED.
//   - ProvenEndpointIsRecovery — CONTROL, same document shape, live endpoint.
//     ACCEPTED, so the rule is not "refuse everything".
//   - OnDiskProvenanceNeedsNoProbe — CONTROL, the genuine hw291 #5488 case.
//     ACCEPTED without the oracle being consulted at all.
//   - AbsentSecretStillAborts — CONTROL, the #5359/#5488 message is unchanged
//     for the case it was written for.
//   - MisDeliveryIsReportedNotMaterialized — the operator-visible consequence:
//     the level-triggered loop must settle on `incomplete:` naming the refused
//     endpoint, not on `materialized:1`. Its second half is the convergence
//     control — a real delivery still lands.
//   - VacuityOfTheEndpointProof — flipping only the endpoint changes the
//     verdict.
//
// Refs #5359 #5488 #6015 #6027 #6104 #6107 #6112 #6116.
package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/core/controllers/pkg/kubeconfig"
)

// hw293DeliveredValue is the live Secret value, byte-for-byte. It is
// `completeKubeconfigSameCluster` without its trailing newline — 219 bytes
// against the fixture's 220 — which is exactly how the live artefact reads.
// The equality is asserted, not assumed, by TestHw293_DeliveredValueIsThe
// ControlFixture below: the document a live Sovereign accepted as its region-B
// credential is this repository's own "usable" control.
var hw293DeliveredValue = strings.TrimSuffix(completeKubeconfigSameCluster, "\n")

// hw293DeadEndpointHost is the host that value names. Nothing answers there.
const hw293DeadEndpointHost = "212.72.24.6"

// hw293RegionBHost is region B's real apiserver, proven by its certificate.
const hw293RegionBHost = "212.72.24.25"

// genuineRegionBKubeconfig is the CONTROL document: identical in every
// property that made the live value pass the usability contract — five
// sections, resolvable current-context, bearer-shaped credential, same
// brevity — and differing ONLY in the endpoint it names.
var genuineRegionBKubeconfig = strings.Replace(
	hw293DeliveredValue, hw293DeadEndpointHost, hw293RegionBHost, 1)

// hw293MisdeliveryFixture is recordlessChrootFixture WITHOUT the on-disk
// primary kubeconfig: on the live Sovereign the kubeconfigs directory was
// empty, which is precisely why there was no provenance to corroborate the
// Secret with. Returns the handler, the fake clientset, the deps, and a
// pointer to a counter the injected oracle increments.
func hw293MisdeliveryFixture(t *testing.T, endpointAnswers bool) (*Handler, *fakek8s.Clientset, *cutoverDeps, *int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	t.Setenv("CATALYST_CONFIGURED_REGIONS", "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod")

	h := NewWithPDM(silentLogger(), &fakePDM{}) // record-less, as measured
	fake := fakek8s.NewSimpleClientset()
	deps := &cutoverDeps{core: fake, ns: cutoverTestNS}
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) { return deps, nil })
	h.secondaryKubeconfigSecretInterval = 5 * time.Millisecond

	probes := 0
	h.secondaryEndpointProbe = func(host string) bool {
		probes++
		// The oracle answers for the real region only, exactly as a live TCP
		// probe did. A stub that ignored its argument would make every case
		// below pass for the wrong reason.
		return endpointAnswers && host == hw293RegionBHost
	}
	return h, fake, deps, &probes
}

func seedBridgeSecret6107(t *testing.T, fake *fakek8s.Clientset, value string) {
	t.Helper()
	if _, err := fake.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cutoverSecondaryKubeconfigsSecretName(),
			Namespace:   cutoverTestNS,
			Annotations: map[string]string{"catalyst.openova.io/deployment-id": "a0077ba47e3720e5"},
		},
		Data: map[string][]byte{"me-east-215-b-1.yaml": []byte(value)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed bridge Secret: %v", err)
	}
}

// TestHw293_DeliveredValueIsTheControlFixture pins the premise. If this fails,
// the fixture no longer reproduces the live artefact and every case below is
// about a document of this test's own invention.
func TestHw293_DeliveredValueIsTheControlFixture(t *testing.T) {
	if got := len(hw293DeliveredValue); got != 219 {
		t.Fatalf("live-value fixture is %d bytes, want 219 (the measured Secret value)", got)
	}
	if d := secondaryKubeconfigDefects(hw293DeliveredValue); len(d) != 0 {
		t.Fatalf("premise broken: the live value now fails the bytes contract (%v) — this whole file exists because it PASSES it", d)
	}
	if got := kubeconfig.Endpoint(hw293DeliveredValue); got != "https://"+hw293DeadEndpointHost+":6443" {
		t.Fatalf("live-value endpoint = %q, want the measured server URL", got)
	}
	// The control differs in the endpoint and NOTHING else.
	if len(genuineRegionBKubeconfig) != len(hw293DeliveredValue)+1 {
		t.Fatalf("control must differ from the live value only by the endpoint host (%d vs %d bytes)",
			len(genuineRegionBKubeconfig), len(hw293DeliveredValue))
	}
}

// TestSecondaryKubeconfigSecret_6107_MisDeliveredEndpointIsNotRecovery is the
// RED case. Before this change it returned (1, nil) — "accepting as-is".
func TestSecondaryKubeconfigSecret_6107_MisDeliveredEndpointIsNotRecovery(t *testing.T) {
	h, fake, deps, probes := hw293MisdeliveryFixture(t, true)
	seedBridgeSecret6107(t, fake, hw293DeliveredValue)

	if n, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
		cutoverSecondaryKubeconfigsSecretName(), 1, secondaryKubeconfigResolution{depID: "a0077ba47e3720e5"}); ok {
		t.Fatalf("the live hw293 Secret satisfied the #5488 recovery check (n=%d) — a well-formed, credentialled document naming an endpoint this deployment never answered on is a MIS-delivery, and accepting it makes it permanent", n)
	}
	if *probes == 0 {
		t.Fatal("the endpoint oracle was never consulted, so the refusal cannot have been about the endpoint")
	}

	// End to end: the whole materialization pass must abort, not report 1.
	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err == nil {
		t.Fatalf("materialization reported success (n=%d) over a mis-delivered credential — the region-B legs and the per-Org console listener would both silently no-op", n)
	}
}

// TestSecondaryKubeconfigSecret_6107_ProvenEndpointIsRecovery is the CONTROL
// that shares the suspect property: the same document shape, the same absence
// of on-disk provenance, the same declared region — and a live endpoint. It
// must still be accepted, or the fix has over-corrected into "nothing is ever
// recoverable".
func TestSecondaryKubeconfigSecret_6107_ProvenEndpointIsRecovery(t *testing.T) {
	h, fake, deps, probes := hw293MisdeliveryFixture(t, true)
	seedBridgeSecret6107(t, fake, genuineRegionBKubeconfig)

	n, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
		cutoverSecondaryKubeconfigsSecretName(), 1, secondaryKubeconfigResolution{depID: "a0077ba47e3720e5"})
	if !ok || n != 1 {
		t.Fatalf("a genuinely usable credential at a DECLARED region was refused: n=%d ok=%v", n, ok)
	}
	if *probes == 0 {
		t.Fatal("acceptance without consulting the oracle means the proof is decorative")
	}
}

// TestSecondaryKubeconfigSecret_6107_OnDiskProvenanceNeedsNoProbe is the other
// CONTROL: the genuine #5488 condition (hw291) — the files ARE on disk, the
// process merely lost its path map. Acceptance rests on provenance, and the
// oracle must not be consulted at all, so a peer region that is briefly down
// during a restart cannot flip a correct Secret to refused.
func TestSecondaryKubeconfigSecret_6107_OnDiskProvenanceNeedsNoProbe(t *testing.T) {
	// endpointAnswers=false: even a DEAD endpoint must be accepted here,
	// which is what proves provenance alone is sufficient.
	h, fake, deps, probes := hw293MisdeliveryFixture(t, false)
	seedBridgeSecret6107(t, fake, hw293DeliveredValue)

	res := secondaryKubeconfigResolution{
		depID: "a0077ba47e3720e5",
		paths: map[string]string{"me-east-215-b-1": "/var/lib/catalyst/kubeconfigs/a0077ba47e3720e5-me-east-215-b-1.yaml"},
	}
	n, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
		cutoverSecondaryKubeconfigsSecretName(), 1, res)
	if !ok || n != 1 {
		t.Fatalf("the hw291 #5488 condition (files on disk, path map lost) was refused: n=%d ok=%v", n, ok)
	}
	if *probes != 0 {
		t.Fatalf("the oracle was consulted %d time(s) despite on-disk provenance — a peer region that blips during a restart would then flip a correct Secret to refused", *probes)
	}

	// And the ambiguous-prefix shape counts as provenance too: files exist,
	// they just could not be attributed to a deployment.
	ambiguous := secondaryKubeconfigResolution{ambiguousPrefixes: []string{"aaa111", "bbb222"}}
	if _, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
		cutoverSecondaryKubeconfigsSecretName(), 1, ambiguous); !ok {
		t.Fatal("ambiguous on-disk prefixes are files this process could not attribute, not an undelivered region — the Secret must still win")
	}
}

// TestSecondaryKubeconfigSecret_6107_AbsentSecretStillAborts is the CONTROL
// for the case #5488 was actually written for: a genuinely ABSENT Secret still
// produces the original diagnosable abort, not the new mis-delivery message.
// Without it, "refuse more things" could be mistaken for a fix.
func TestSecondaryKubeconfigSecret_6107_AbsentSecretStillAborts(t *testing.T) {
	h, _, deps, _ := hw293MisdeliveryFixture(t, true)

	_, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err == nil {
		t.Fatal("a Sovereign declaring 2 regions with no kubeconfig and no Secret must abort")
	}
	if !strings.Contains(err.Error(), "#5359") {
		t.Errorf("the absent-Secret abort lost its #5359 contract reference: %s", err)
	}
	if strings.Contains(err.Error(), "MIS-delivery") {
		t.Errorf("an ABSENT Secret was described as a mis-delivered one: %s", err)
	}
}

// TestSecondaryKubeconfigSecret_6107_MisDeliveryIsReportedNotMaterialized is
// the operator-visible consequence, and it is what makes the refusal worth
// shipping rather than merely correct.
//
// The level-triggered materializer reports one state per pass, and it logs
// only on a CHANGE — so whatever it settles on is what an operator sees for as
// long as the condition lasts. While the arm accepted, it settled on
// `materialized:1`: a Sovereign whose region B held no credential at all
// reported, once, that the peer-region credential was in place, and then went
// quiet. That is the same substitution one layer up in the reporting stack.
//
// The second half is the convergence CONTROL: refusing must not wedge the
// Sovereign. The moment a real file is delivered, the same loop materializes
// it and the state moves to `materialized:1` with region B's real endpoint in
// the Secret.
func TestSecondaryKubeconfigSecret_6107_MisDeliveryIsReportedNotMaterialized(t *testing.T) {
	h, fake, _, _ := hw293MisdeliveryFixture(t, true)
	seedBridgeSecret6107(t, fake, hw293DeliveredValue)

	h.reconcileSecondaryKubeconfigsSecret(context.Background())
	h.secondaryKubeconfigSecretStateMu.Lock()
	state := h.secondaryKubeconfigSecretState
	h.secondaryKubeconfigSecretStateMu.Unlock()
	if !strings.HasPrefix(state, "incomplete:") {
		t.Fatalf("the materializer settled on %q over a mis-delivered credential — a Sovereign whose region B holds no usable credential reported that one was in place, and the loop logs only on a state CHANGE, so that is what an operator sees until something else moves", state)
	}
	if !strings.Contains(state, hw293DeadEndpointHost) {
		t.Errorf("the reported state does not name the endpoint that was refused, so an operator cannot act on it: %q", state)
	}

	// CONVERGENCE CONTROL — the refusal must not wedge the Sovereign.
	dir := os.Getenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR")
	for stem, body := range map[string]string{
		"a0077ba47e3720e5":                 genuineRegionBKubeconfig,
		"a0077ba47e3720e5-me-east-215-b-1": genuineRegionBKubeconfig,
	} {
		if err := os.WriteFile(filepath.Join(dir, stem+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.reconcileSecondaryKubeconfigsSecret(context.Background())
	h.secondaryKubeconfigSecretStateMu.Lock()
	state = h.secondaryKubeconfigSecretState
	h.secondaryKubeconfigSecretStateMu.Unlock()
	if state != "materialized:1" {
		t.Fatalf("after a real delivery landed the materializer state is %q, want materialized:1 — the refusal wedged convergence", state)
	}
	sec, err := getBridgeSecret(t, fake)
	if err != nil {
		t.Fatalf("get bridge Secret: %v", err)
	}
	if got := kubeconfig.Endpoint(string(sec.Data["me-east-215-b-1.yaml"])); got != "https://"+hw293RegionBHost+":6443" {
		t.Fatalf("materialized endpoint = %q, want region B's real apiserver", got)
	}
}

// TestSecondaryKubeconfigSecret_6107_VacuityOfTheEndpointProof is the VACUITY
// CHECK. Every green assertion above would also pass against an arm that
// accepted unconditionally. This one flips ONE input — the endpoint host, one
// substring of one fixture — and requires the verdict to change.
func TestSecondaryKubeconfigSecret_6107_VacuityOfTheEndpointProof(t *testing.T) {
	verdict := func(value string) bool {
		h, fake, deps, _ := hw293MisdeliveryFixture(t, true)
		seedBridgeSecret6107(t, fake, value)
		_, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
			cutoverSecondaryKubeconfigsSecretName(), 1, secondaryKubeconfigResolution{depID: "a0077ba47e3720e5"})
		return ok
	}
	accepted := verdict(genuineRegionBKubeconfig)
	refused := verdict(hw293DeliveredValue)
	if !accepted {
		t.Fatal("the control was refused — the gate cannot pass")
	}
	if refused {
		t.Fatal("VACUITY: swapping the endpoint host changed nothing — the gate cannot fail")
	}
}
