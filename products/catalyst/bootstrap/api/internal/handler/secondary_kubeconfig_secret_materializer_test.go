// secondary_kubeconfig_secret_materializer_test.go — #6027.
//
// What these pin, in the order a reader should judge them:
//
//  1. THE PRODUCER EXISTS OUTSIDE runCutover. Before this change
//     materializeSecondaryKubeconfigsSecret had exactly one production call
//     site, in cutover.go — counted by TestSecondaryKubeconfigSecret_HasA-
//     ProducerOutsideCutover, which is the guard that fails if someone later
//     folds the loop back into the cutover path.
//
//  2. THE VALUE, NOT THE PRESENCE. A Secret carrying the 95-byte hw293 stub
//     is worth nothing: the organization-controller's newRegionClient runs
//     clientcmd on it, fails, and records the region Unreachable — while the
//     Secret sits there looking complete. So the acceptance test parses the
//     delivered bytes back into a rest.Config, and a second test proves the
//     stub is REFUSED rather than materialized.
//
//  3. THE DECISIVE CONTROL. A genuinely single-region Sovereign must produce
//     no Secret and must not error. A producer that always writes one would
//     pass every test above and still be wrong.
//
// Refs #6015 #6027.

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
	"k8s.io/client-go/tools/clientcmd"
)

// materializerFixture wires the chroot-shaped fixture used by the #5359 suite
// to a Handler whose cutoverDeps factory returns ONE stable fake clientset, so
// a test can drive the production loop and then inspect what it wrote.
func materializerFixture(t *testing.T, regions int) (*Handler, *fakek8s.Clientset, string) {
	t.Helper()
	h, deps, dir := newSecondaryKubeconfigFixture(t, regions)
	fake := deps.core.(*fakek8s.Clientset)
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) { return deps, nil })
	h.secondaryKubeconfigSecretInterval = 5 * time.Millisecond
	return h, fake, dir
}

func writeKubeconfigFile(t *testing.T, dir, depID, region, body string) {
	t.Helper()
	path := filepath.Join(dir, depID+"-"+region+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func getBridgeSecret(t *testing.T, fake *fakek8s.Clientset) (*corev1.Secret, error) {
	t.Helper()
	return fake.CoreV1().Secrets(cutoverTestNS).Get(context.Background(),
		cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
}

func waitForBridgeSecret(t *testing.T, fake *fakek8s.Clientset, d time.Duration) *corev1.Secret {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if sec, err := getBridgeSecret(t, fake); err == nil {
			return sec
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. The producer exists outside runCutover.
// ---------------------------------------------------------------------------

// TestSecondaryKubeconfigSecret_HasAProducerOutsideCutover states the root
// cause as a number. With one production call site — cutover.go's — the Secret
// could only ever exist on a Sovereign that had already run the operator-gated
// cutover, which is why hw293 held none in either region while its peer
// region's apiserver was healthy the whole time.
func TestSecondaryKubeconfigSecret_HasAProducerOutsideCutover(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	const call = "h.materializeSecondaryKubeconfigsSecret("
	sites := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(name)
		if rerr != nil {
			continue
		}
		if n := strings.Count(string(raw), call); n > 0 {
			sites[name] = n
		}
	}
	if len(sites) == 0 {
		t.Fatalf("no production call site of materializeSecondaryKubeconfigsSecret at all — the Secret has no producer")
	}
	outside := 0
	for file, n := range sites {
		if file != "cutover.go" {
			outside += n
		}
	}
	if outside == 0 {
		t.Fatalf("materializeSecondaryKubeconfigsSecret is called ONLY from cutover.go (%v) — "+
			"the peer-region credential Secret exists only after an operator fires the cutover, "+
			"so a 2-region Sovereign that has not cut over cannot write per-Org console listeners "+
			"into its own region B (#6027)", sites)
	}
}

// TestSecondaryKubeconfigSecret_IsStartedByMain is the wiring guard. Every
// other test here calls RunSecondaryKubeconfigSecretMaterializer directly, so
// all of them stay green if the `go h.Run…` line is deleted from main() and
// the loop never runs in production — the exact "guard that cannot fail"
// shape. main() is package main and is not drivable from here, so the spawn is
// asserted at the source level.
func TestSecondaryKubeconfigSecret_IsStartedByMain(t *testing.T) {
	const mainPath = "../../cmd/api/main.go"
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read %s: %v", mainPath, err)
	}
	const spawn = "go h.RunSecondaryKubeconfigSecretMaterializer("
	if !strings.Contains(string(raw), spawn) {
		t.Fatalf("%s does not spawn the secondary-kubeconfig Secret materializer (%q) — "+
			"the loop exists but nothing starts it, so the Secret still has no producer "+
			"outside runCutover on a real Sovereign (#6027)", mainPath, spawn)
	}
	// Vacuity arm: the same read must be able to MISS something, or a typo in
	// the needle above would make this test pass against any file at all.
	if strings.Contains(string(raw), "go h.RunSecondaryKubeconfigSecretMaterializerThatDoesNotExist(") {
		t.Fatal("control needle matched — the containment check is not discriminating")
	}
}

// ---------------------------------------------------------------------------
// 2. The value, not the presence.
// ---------------------------------------------------------------------------

// TestSecondaryKubeconfigSecret_MaterializesPreCutover is the acceptance unit:
// a 2-region Sovereign that has NEVER run a cutover ends up holding a peer-
// region credential that actually builds a client.
func TestSecondaryKubeconfigSecret_MaterializesPreCutover(t *testing.T) {
	h, fake, dir := materializerFixture(t, 2)
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b", completeKubeconfigSameCluster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.RunSecondaryKubeconfigSecretMaterializer(ctx)

	sec := waitForBridgeSecret(t, fake, 3*time.Second)
	if sec == nil {
		t.Fatalf("no %s Secret after a level-triggered pass on a 2-region Sovereign — "+
			"the organization-controller still has no credential for region B and its per-Org "+
			"console listener pair can only ever land in region A (#6027)",
			cutoverSecondaryKubeconfigsSecretName())
	}

	// ASSERT ON THE VALUE. A key is not a credential: the 95-byte hw293 stub
	// satisfied every presence check ever applied to this object and still
	// could not produce a client.
	raw, ok := sec.Data["me-east-215-b.yaml"]
	if !ok {
		t.Fatalf("Secret carries no key for region me-east-215-b; keys=%v", secretDataKeys(sec))
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		t.Fatalf("the materialized %d-byte value cannot build a client: %v — presence without usability is the defect, not the fix", len(raw), err)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		t.Fatalf("materialized kubeconfig built a rest.Config with no Host")
	}
	if len(cfg.BearerToken) == 0 && cfg.CertData == nil && cfg.AuthProvider == nil && cfg.ExecProvider == nil {
		t.Fatalf("materialized kubeconfig carries no credential at all — Host alone cannot authenticate")
	}
}

// TestSecondaryKubeconfigSecret_StubIsRefused is the read-side twin of #6054.
// #6054 stops a credential-less document at the delivery endpoint, but hw293's
// 95-byte stub was already on the PVC when that gate shipped, and the
// materializer's only content check was len(raw) != 0. Measured before this
// change: n=1, err=nil, and a 95-byte value in the Secret that
// clientcmd.RESTConfigFromKubeConfig rejects.
func TestSecondaryKubeconfigSecret_StubIsRefused(t *testing.T) {
	h, fake, dir := materializerFixture(t, 2)
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b", hw293StubKubeconfig)

	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), &cutoverDeps{core: fake, ns: cutoverTestNS})
	if err == nil {
		t.Fatalf("a %d-byte credential-less kubeconfig was ACCEPTED (n=%d) — every consumer would then read region B as credentialled while no client can be built from it", len(hw293StubKubeconfig), n)
	}
	for _, want := range []string{"contexts", "users", "current-context", "95 bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rejection message does not name %q, so an operator cannot tell WHAT is missing: %s", want, err)
		}
	}
	if _, gerr := getBridgeSecret(t, fake); gerr == nil {
		t.Fatalf("the unusable document was still written to the Secret — a refusal that persists its own reject is not a refusal")
	}

	// VACUITY ARM — the same fixture with the same cluster block, complete,
	// must be accepted. A gate that cannot pass is as useless as one that
	// cannot fail.
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b", completeKubeconfigSameCluster)
	if n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), &cutoverDeps{core: fake, ns: cutoverTestNS}); err != nil || n != 1 {
		t.Fatalf("the complete control was refused: n=%d err=%v", n, err)
	}
}

// TestSecondaryKubeconfigSecret_StubInSecretIsNotAcceptedAsRecovery covers the
// #5488 recovery arm, which is the one that runs when the process cannot
// re-derive its on-disk paths — i.e. exactly when nothing else would catch a
// bad value. It counted any non-empty key toward the expected total.
func TestSecondaryKubeconfigSecret_StubInSecretIsNotAcceptedAsRecovery(t *testing.T) {
	h, fake, _ := materializerFixture(t, 2) // 2 regions expected, NOTHING on disk
	deps := &cutoverDeps{core: fake, ns: cutoverTestNS}
	// This test isolates the CREDENTIAL axis, so the endpoint axis is held
	// satisfied: the probe proves whatever host the fixtures name. Endpoint
	// proof has its own test — TestSecondaryKubeconfigSecret_6107_* — and
	// leaving it unproven here would let this case pass for the wrong reason.
	h.secondaryEndpointProbe = func(string) bool { return true }
	res := secondaryKubeconfigResolution{depID: "dep5359"}

	if _, err := fake.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverSecondaryKubeconfigsSecretName(), Namespace: cutoverTestNS},
		Data:       map[string][]byte{"me-east-215-b.yaml": []byte(hw293StubKubeconfig)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if n, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
		cutoverSecondaryKubeconfigsSecretName(), 1, res); ok {
		t.Fatalf("a Secret whose only key is the 95-byte stub satisfied the recovery check (n=%d) — the count was of keys, not of credentials", n)
	}

	// Vacuity arm: the same Secret carrying a COMPLETE document is accepted.
	sec, _ := getBridgeSecret(t, fake)
	sec.Data["me-east-215-b.yaml"] = []byte(completeKubeconfigSameCluster)
	if _, err := fake.CoreV1().Secrets(cutoverTestNS).Update(context.Background(), sec, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n, ok, _ := h.acceptMaterializedSecondaryKubeconfigsSecret(context.Background(), deps,
		cutoverSecondaryKubeconfigsSecretName(), 1, res); !ok || n != 1 {
		t.Fatalf("the complete control was refused by the recovery check: n=%d ok=%v", n, ok)
	}
}

// ---------------------------------------------------------------------------
// 2b. The hw293 state: a chroot with NO deployment record.
// ---------------------------------------------------------------------------

// recordlessChrootFixture reproduces hw293 dep a0077ba47e3720e5 as measured:
// `restored deployments from PVC count=0` (handover never fired, so no record
// was ever imported) while the `sovereign-fqdn` ConfigMap declares two regions
// via CATALYST_CONFIGURED_REGIONS. The primary kubeconfig is on disk beside the
// secondary, which is what lets the on-disk prefix derivation name the region.
func recordlessChrootFixture(t *testing.T) (*Handler, *fakek8s.Clientset, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	t.Setenv("CATALYST_CONFIGURED_REGIONS", "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod")

	h := NewWithPDM(silentLogger(), &fakePDM{}) // deployments map deliberately EMPTY
	fake := fakek8s.NewSimpleClientset()
	deps := &cutoverDeps{core: fake, ns: cutoverTestNS}
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) { return deps, nil })
	h.secondaryKubeconfigSecretInterval = 5 * time.Millisecond

	if err := os.WriteFile(filepath.Join(dir, "a0077ba47e3720e5.yaml"),
		[]byte(completeKubeconfigSameCluster), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, fake, dir
}

// TestSecondaryKubeconfigSecret_RecordlessChrootStillMaterializes is the test
// that decides whether this change reaches hw293 at all.
//
// resolveCutoverDeployment returns nil there. Deriving the expected region
// count from the record alone would hand the completeness check a denominator
// of zero — a guard that cannot fail — and the producer would conclude,
// correctly by its own arithmetic, that a 2-region Sovereign has nothing to
// materialize. The count therefore comes from CATALYST_CONFIGURED_REGIONS,
// which the IaC writes at provision time and which is independent of both the
// deployment store and the k8scache.
func TestSecondaryKubeconfigSecret_RecordlessChrootStillMaterializes(t *testing.T) {
	h, fake, dir := recordlessChrootFixture(t)
	if h.resolveCutoverDeployment() != nil {
		t.Fatalf("fixture is not record-less")
	}
	writeKubeconfigFile(t, dir, "a0077ba47e3720e5", "me-east-215-b-1", completeKubeconfigSameCluster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.RunSecondaryKubeconfigSecretMaterializer(ctx)

	sec := waitForBridgeSecret(t, fake, 3*time.Second)
	if sec == nil {
		t.Fatalf("a chroot with NO deployment record and CATALYST_CONFIGURED_REGIONS declaring 2 regions "+
			"produced no %s Secret — the region count was taken from the record, which is the one source "+
			"that is blank on exactly the Sovereign that needs this (#6027)",
			cutoverSecondaryKubeconfigsSecretName())
	}
	raw, ok := sec.Data["me-east-215-b-1.yaml"]
	if !ok {
		t.Fatalf("Secret carries no key for the derived region; keys=%v", secretDataKeys(sec))
	}
	if _, err := clientcmd.RESTConfigFromKubeConfig(raw); err != nil {
		t.Fatalf("materialized value cannot build a client: %v", err)
	}
	if got := sec.Annotations["catalyst.openova.io/deployment-id"]; got != "a0077ba47e3720e5" {
		t.Errorf("deployment-id annotation = %q, want the prefix derived from disk", got)
	}
}

// TestSecondaryKubeconfigSecret_RecordlessChrootWithNoUsableFileWritesNothing
// is the same fixture minus a usable peer kubeconfig — the hw293 state as it
// stands today, where the only region-B document ever delivered was the
// 95-byte stub. The pass must produce nothing and must not reap an existing
// Secret, because "I cannot see the credential" is not "the credential is
// wrong".
func TestSecondaryKubeconfigSecret_RecordlessChrootWithNoUsableFileWritesNothing(t *testing.T) {
	h, fake, dir := recordlessChrootFixture(t)
	writeKubeconfigFile(t, dir, "a0077ba47e3720e5", "me-east-215-b-1", hw293StubKubeconfig)

	if _, err := fake.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverSecondaryKubeconfigsSecretName(), Namespace: cutoverTestNS},
		Data:       map[string][]byte{"me-east-215-b-1.yaml": []byte(completeKubeconfigSameCluster)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed a GOOD Secret from a prior pass: %v", err)
	}

	h.reconcileSecondaryKubeconfigsSecret(context.Background())

	sec, err := getBridgeSecret(t, fake)
	if err != nil {
		t.Fatalf("a pass that could not read a usable kubeconfig DELETED the good Secret a prior pass "+
			"had materialized: %v — an unreadable peer is a wait, not a teardown", err)
	}
	if !strings.Contains(string(sec.Data["me-east-215-b-1.yaml"]), "fake-token") {
		t.Fatalf("the good Secret value was overwritten by the stub")
	}
}

// ---------------------------------------------------------------------------
// 3. The decisive control.
// ---------------------------------------------------------------------------

// TestSecondaryKubeconfigSecret_SingleRegionProducesNothing is the control that
// makes every test above mean something. A producer that always writes a Secret
// would satisfy the acceptance case and be indistinguishable from the defect.
// A one-region Sovereign must end the pass with NO Secret and NO error, and the
// loop must stay alive rather than wedging on the absence.
func TestSecondaryKubeconfigSecret_SingleRegionProducesNothing(t *testing.T) {
	h, fake, dir := materializerFixture(t, 1)

	// A stray file on disk must not conjure a secondary region either: the
	// expected count comes from the deployment SPEC.
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b", completeKubeconfigSameCluster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.RunSecondaryKubeconfigSecretMaterializer(ctx)
	time.Sleep(120 * time.Millisecond) // ~24 ticks at the 5ms test interval

	if sec, err := getBridgeSecret(t, fake); err == nil {
		t.Fatalf("a SINGLE-region Sovereign produced a secondary-kubeconfig Secret (keys=%v) — "+
			"a producer that always writes one is not a fix", secretDataKeys(sec))
	}

	// …and it did not fail, either: the state the loop settled in is the
	// single-region one, not an error.
	h.secondaryKubeconfigSecretStateMu.Lock()
	state := h.secondaryKubeconfigSecretState
	h.secondaryKubeconfigSecretStateMu.Unlock()
	if state != "single-region" {
		t.Fatalf("single-region loop settled in state %q, want %q — the absence of a peer region is a normal outcome, not a failure", state, "single-region")
	}
}

// TestSecondaryKubeconfigSecret_SingleRegionReapsStaleSecret keeps the #5359
// reap: a Sovereign that USED to be multi-region must lose the Secret, or the
// cutover chart's region-B legs would fire against a region that is gone.
func TestSecondaryKubeconfigSecret_SingleRegionReapsStaleSecret(t *testing.T) {
	h, fake, _ := materializerFixture(t, 1)
	if _, err := fake.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverSecondaryKubeconfigsSecretName(), Namespace: cutoverTestNS},
		Data:       map[string][]byte{"gone-region.yaml": []byte(completeKubeconfigSameCluster)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.reconcileSecondaryKubeconfigsSecret(context.Background())
	if _, err := getBridgeSecret(t, fake); err == nil {
		t.Fatalf("stale Secret survived a single-region pass")
	}
}

// ---------------------------------------------------------------------------
// Steady state.
// ---------------------------------------------------------------------------

// TestSecondaryKubeconfigSecret_ConvergedPassWritesNothing protects the
// consumer this whole change exists to serve. The organization-controller keys
// its per-region client cache on this Secret's resourceVersion, so an
// unconditional Update on every pass would discard every secondary-region
// client and re-run discovery against the peer apiserver once per Org per
// tick. Measured before the convergence check: 3 identical passes → 3 writes.
func TestSecondaryKubeconfigSecret_ConvergedPassWritesNothing(t *testing.T) {
	h, fake, dir := materializerFixture(t, 2)
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b", completeKubeconfigSameCluster)
	deps := &cutoverDeps{core: fake, ns: cutoverTestNS}

	const passes = 5
	for i := 0; i < passes; i++ {
		if _, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	writes := 0
	for _, a := range fake.Actions() {
		if a.GetResource().Resource == "secrets" && (a.GetVerb() == "create" || a.GetVerb() == "update") {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("%d identical passes produced %d writes, want 1 — a churning Secret invalidates the "+
			"organization-controller's per-region client cache on every tick", passes, writes)
	}

	// Vacuity arm: a CHANGED kubeconfig must still be written, or the check
	// above would be satisfied by a producer that never updates anything.
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b",
		strings.Replace(completeKubeconfigSameCluster, "token: fake-token", "token: rotated-token", 1))
	if _, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps); err != nil {
		t.Fatalf("post-rotation pass: %v", err)
	}
	sec, err := getBridgeSecret(t, fake)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(string(sec.Data["me-east-215-b.yaml"]), "rotated-token") {
		t.Fatalf("a ROTATED kubeconfig was not propagated — the convergence check is swallowing real changes")
	}
}

// TestSecondaryKubeconfigSecret_DeliveryKicksTheMaterializer pins the fast
// path: the endpoint that lands a peer-region kubeconfig asks for a pass, so
// the credential is in-cluster about a second after delivery instead of on the
// next tick. Uses a long interval so a pass within the deadline can only have
// come from the kick.
func TestSecondaryKubeconfigSecret_DeliveryKicksTheMaterializer(t *testing.T) {
	h, fake, _ := materializerFixture(t, 2)
	h.secondaryKubeconfigSecretInterval = time.Hour
	h.k8sCache = newK8sCacheWithClusters(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.RunSecondaryKubeconfigSecretMaterializer(ctx)
	time.Sleep(30 * time.Millisecond) // first pass runs, finds nothing, then blocks

	if _, err := getBridgeSecret(t, fake); err == nil {
		t.Fatalf("Secret existed before any kubeconfig was delivered")
	}

	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "dep5359",
		"regionKey":      "me-east-215-b",
		"kubeconfigYaml": completeKubeconfigSameCluster,
	})
	if rec.Code != 201 {
		t.Fatalf("delivery POST status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	if sec := waitForBridgeSecret(t, fake, 3*time.Second); sec == nil {
		t.Fatalf("the materializer did not run within 3s of a delivery while its tick is 1h — " +
			"the delivery endpoint is not kicking it, so the credential would wait a full interval (#6027)")
	}
}

// TestSecondaryKubeconfigSecret_MothershipDoesNotRun keeps the loop off the
// side that has no cutover namespace and no deployment record of its own.
func TestSecondaryKubeconfigSecret_MothershipDoesNotRun(t *testing.T) {
	h, fake, dir := materializerFixture(t, 2)
	writeKubeconfigFile(t, dir, "dep5359", "me-east-215-b", completeKubeconfigSameCluster)
	t.Setenv("SOVEREIGN_FQDN", "") // mothership

	done := make(chan struct{})
	go func() { h.RunSecondaryKubeconfigSecretMaterializer(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("the materializer kept running on the mothership")
	}
	if _, err := getBridgeSecret(t, fake); err == nil {
		t.Fatalf("the mothership wrote a cutover-namespace Secret")
	}
}
