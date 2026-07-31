package handler

// #5488 — cutover secondary-kubeconfigs pre-flight restart-recovery tests.
//
// hw291 (dep 2c2d746b578c636b): a catalyst-api restart wiped the process-
// local dep.secondaryKubeconfigPaths map AND left the pre-flight resolving
// a degraded record with an EMPTY ID, so the on-disk fallback globbed
// `<dir>/-<key>.yaml` (leading dash), matched nothing, and the run aborted
// with "expects 1 ... only 0 readable (missing/unreadable: )" — even
// though the cutover-secondary-kubeconfigs Secret was already correctly
// materialized by an earlier run. These tests pin the recovery contract
// BOTH ways:
//
//   PASS (recoverable / already-done):
//   1. Already-materialized Secret with >= expected non-empty keys →
//      pre-flight passes even with an empty paths map AND an empty dep.ID.
//   2. Empty dep.ID + exactly ONE deployment prefix on disk → the prefix
//      is derived from the files present and materialization proceeds.
//   3. Ambiguous on-disk prefixes but a satisfying Secret → the Secret
//      wins (never guess between prefixes).
//
//   ABORT (the #5359 fail-loud contract, now with honest messages):
//   4. Empty dep.ID + no Secret → diagnosable empty-ID message (#5488),
//      not the uninformative "(missing/unreadable: )".
//   5. Empty dep.ID + MULTIPLE on-disk prefixes + no Secret → abort
//      naming the ambiguity — never silently pick one.
//   6. Non-empty dep.ID but zero candidate paths → "no candidate paths
//      resolved", not an empty missing list.
//   7. Genuinely missing/unreadable kubeconfig file → abort naming the
//      missing key (the original #5359 message shape).
//   8. A Secret annotated for a DIFFERENT deployment is never accepted.
//
// Plus the population-side guards:
//   9. resolveCutoverDeployment prefers the record with a non-empty ID
//      over a degraded empty-ID record (sync.Map order-independent).
//  10. chrootEnsureDeployment refuses to mint a record for a blank id.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const fqdn5488 = "hw291.omani.works"

// newFixture5488 builds a chroot-shaped Handler + fake cutoverDeps with a
// single deployment record carrying the given ID (empty = the degraded
// post-restart record shape observed live on hw291). The record is stored
// under its ID as the map key, mirroring how the live poisoned record was
// stored under "".
func newFixture5488(t *testing.T, depID string, regions int) (*Handler, *cutoverDeps, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", fqdn5488)

	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: fqdn5488,
		},
	}
	for i := 0; i < regions; i++ {
		dep.Request.Regions = append(dep.Request.Regions, provisioner.RegionSpec{
			Provider:    "huawei",
			CloudRegion: "me-east-215",
		})
	}
	h.deployments.Store(dep.ID, dep)

	client := fakek8s.NewSimpleClientset()
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}
	return h, deps, dir
}

func seedMaterializedSecret5488(t *testing.T, deps *cutoverDeps, annotationDepID string, keys ...string) {
	t.Helper()
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte("apiVersion: v1\nkind: Config\nclusters: []\n")
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverSecondaryKubeconfigsSecretName(),
			Namespace: cutoverTestNS,
		},
		Data: data,
	}
	if annotationDepID != "" {
		sec.Annotations = map[string]string{"catalyst.openova.io/deployment-id": annotationDepID}
	}
	if _, err := deps.core.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
}

func writeKubeconfigFile5488(t *testing.T, dir, stem string) {
	t.Helper()
	path := filepath.Join(dir, stem+".yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\nclusters: []\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ── PASS side ────────────────────────────────────────────────────────────

// (a) The primary recovery path: the Secret already carries the expected
// keys, so the pre-flight passes even with an EMPTY dep.ID, an empty
// in-memory paths map, and nothing derivable on disk — the exact hw291
// post-restart state. Before the #5488 fallback change this returned the
// "(missing/unreadable: )" abort; reverting the change makes this fail.
func TestMaterializeSecondaryKubeconfigs_5488_AcceptsAlreadyMaterializedSecret(t *testing.T) {
	h, deps, _ := newFixture5488(t, "", 2)
	seedMaterializedSecret5488(t, deps, "", "me-east-215-b-1.yaml")

	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err != nil {
		t.Fatalf("pre-flight rejected an already-materialized Secret (the #5488 recoverable condition): %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1 (the accepted Secret's key count)", n)
	}
	// Acceptance is as-is: the Secret must not have been rewritten.
	sec, err := deps.core.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if _, ok := sec.Data["me-east-215-b-1.yaml"]; !ok || len(sec.Data) != 1 {
		t.Fatalf("accepted Secret was rewritten; got keys %v", secretDataKeys(sec))
	}
}

// Task 3: empty dep.ID but exactly ONE deployment prefix on disk (the
// primary <id>.yaml #5131 always materializes on a chroot, plus its
// secondary sibling) → the prefix is derived and materialization proceeds.
func TestMaterializeSecondaryKubeconfigs_5488_EmptyDepIDDerivesSinglePrefix(t *testing.T) {
	h, deps, dir := newFixture5488(t, "", 2)
	writeKubeconfigFile5488(t, dir, "2c2d746b578c636b")                 // primary
	writeKubeconfigFile5488(t, dir, "2c2d746b578c636b-me-east-215-b-1") // secondary

	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err != nil {
		t.Fatalf("materialize with derivable single prefix: %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	sec, err := deps.core.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if _, ok := sec.Data["me-east-215-b-1.yaml"]; !ok {
		t.Fatalf("secret missing me-east-215-b-1.yaml key; got keys %v", secretDataKeys(sec))
	}
}

// Ambiguous on-disk prefixes + a satisfying Secret → the Secret wins;
// the ambiguity must never be silently resolved by picking a prefix.
func TestMaterializeSecondaryKubeconfigs_5488_AmbiguousPrefixesSecretWins(t *testing.T) {
	h, deps, dir := newFixture5488(t, "", 2)
	writeKubeconfigFile5488(t, dir, "aaa111")
	writeKubeconfigFile5488(t, dir, "aaa111-region-x")
	writeKubeconfigFile5488(t, dir, "bbb222")
	writeKubeconfigFile5488(t, dir, "bbb222-region-y")
	seedMaterializedSecret5488(t, deps, "", "me-east-215-b-1.yaml")

	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err != nil {
		t.Fatalf("ambiguous prefixes with a satisfying Secret must pass via the Secret, got: %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	// The accepted Secret must be untouched — proof no prefix was guessed.
	sec, err := deps.core.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if len(sec.Data) != 1 {
		t.Fatalf("secret rewritten from a guessed prefix; got keys %v", secretDataKeys(sec))
	}
}

// ── ABORT side (fail-loud preserved, messages now diagnosable) ──────────

func TestMaterializeSecondaryKubeconfigs_5488_AbortMessages(t *testing.T) {
	cases := []struct {
		name        string
		depID       string
		setup       func(t *testing.T, h *Handler, deps *cutoverDeps, dir string)
		wantSubstrs []string
		banSubstrs  []string
	}{
		{
			// (b) Empty dep.ID, nothing on disk, no Secret → its own
			// diagnosable condition with an actionable recovery hint.
			// Before the #5488 change this produced the uninformative
			// "deployment  expects 1 ... (missing/unreadable: )" string;
			// reverting the change makes this case fail.
			name:  "empty depID no secret",
			depID: "",
			setup: func(t *testing.T, h *Handler, deps *cutoverDeps, dir string) {},
			wantSubstrs: []string{
				"EMPTY deployment id",
				"#5488",
				"#5359",
				"Recovery:",
			},
			banSubstrs: []string{"missing/unreadable: )"},
		},
		{
			// Empty dep.ID + MULTIPLE prefixes on disk + no Secret →
			// abort naming the ambiguity, never silently pick one.
			name:  "empty depID ambiguous prefixes",
			depID: "",
			setup: func(t *testing.T, h *Handler, deps *cutoverDeps, dir string) {
				writeKubeconfigFile5488(t, dir, "aaa111")
				writeKubeconfigFile5488(t, dir, "aaa111-region-x")
				writeKubeconfigFile5488(t, dir, "bbb222")
				writeKubeconfigFile5488(t, dir, "bbb222-region-y")
			},
			wantSubstrs: []string{
				"EMPTY deployment id",
				"refusing to guess",
				"aaa111",
				"bbb222",
				"#5488",
			},
			banSubstrs: []string{"missing/unreadable: )"},
		},
		{
			// Task 2: non-empty dep.ID but zero candidate paths — say
			// "no candidate paths resolved" instead of printing an empty
			// missing list.
			name:  "no candidate paths resolved",
			depID: "dep5488",
			setup: func(t *testing.T, h *Handler, deps *cutoverDeps, dir string) {},
			wantSubstrs: []string{
				"deployment dep5488",
				"no candidate kubeconfig paths resolved",
				"#5359",
			},
			banSubstrs: []string{"missing/unreadable: )"},
		},
		{
			// (c) The original #5359 contract: a candidate path exists
			// but the file is genuinely missing — abort naming the key.
			name:  "genuinely missing file names the key",
			depID: "dep5488",
			setup: func(t *testing.T, h *Handler, deps *cutoverDeps, dir string) {
				val, _ := h.deployments.Load("dep5488")
				dep := val.(*Deployment)
				dep.mu.Lock()
				dep.secondaryKubeconfigPaths = map[string]string{
					"me-east-215-b-1": filepath.Join(dir, "does-not-exist.yaml"),
				}
				dep.mu.Unlock()
			},
			wantSubstrs: []string{
				"deployment dep5488",
				"missing/unreadable",
				"me-east-215-b-1",
				"does-not-exist.yaml",
				"#5359",
			},
		},
		{
			// (8) A Secret materialized for a DIFFERENT deployment is
			// foreign — never accepted, the abort stands.
			name:  "foreign secret not accepted",
			depID: "dep5488",
			setup: func(t *testing.T, h *Handler, deps *cutoverDeps, dir string) {
				seedMaterializedSecret5488(t, deps, "someotherdep", "me-east-215-b-1.yaml")
			},
			wantSubstrs: []string{
				"deployment dep5488",
				"no candidate kubeconfig paths resolved",
				"#5359",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, deps, dir := newFixture5488(t, tc.depID, 2)
			tc.setup(t, h, deps, dir)

			_, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
			if err == nil {
				t.Fatalf("expected fail-loud abort (#5359), got nil")
			}
			msg := err.Error()
			for _, want := range tc.wantSubstrs {
				if !strings.Contains(msg, want) {
					t.Fatalf("abort message missing %q; got: %s", want, msg)
				}
			}
			for _, ban := range tc.banSubstrs {
				if strings.Contains(msg, ban) {
					t.Fatalf("abort message still carries the uninformative %q shape; got: %s", ban, msg)
				}
			}
		})
	}
}

// ── population-side guards ──────────────────────────────────────────────

// (9) resolveCutoverDeployment must prefer the fully-populated record over
// a degraded empty-ID record that matches the same SOVEREIGN_FQDN. Run
// several fresh handlers so a lucky sync.Map range order cannot mask a
// regression.
func TestResolveCutoverDeployment_5488_PrefersNonEmptyID(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", fqdn5488)
	for i := 0; i < 25; i++ {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		degraded := &Deployment{Request: provisioner.Request{SovereignFQDN: fqdn5488}}
		real := &Deployment{ID: "dep5488", Request: provisioner.Request{SovereignFQDN: fqdn5488}}
		h.deployments.Store("", degraded)
		h.deployments.Store(real.ID, real)

		got := h.resolveCutoverDeployment()
		if got == nil {
			t.Fatalf("iteration %d: resolveCutoverDeployment returned nil", i)
		}
		if got.ID != "dep5488" {
			t.Fatalf("iteration %d: resolved the degraded empty-ID record instead of the fully-populated one", i)
		}
	}
}

// resolveCutoverDeployment still returns the degraded record when it is
// the ONLY match — the caller diagnoses it (never a silent nil that would
// take the record-less warn path and skip the fail-loud check entirely).
func TestResolveCutoverDeployment_5488_DegradedOnlyMatchStillReturned(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", fqdn5488)
	h := NewWithPDM(silentLogger(), &fakePDM{})
	degraded := &Deployment{Request: provisioner.Request{SovereignFQDN: fqdn5488}}
	h.deployments.Store("", degraded)

	got := h.resolveCutoverDeployment()
	if got == nil {
		t.Fatalf("degraded-only match must still be returned for diagnosis, got nil")
	}
	if got.ID != "" {
		t.Fatalf("unexpected record resolved: %q", got.ID)
	}
}

// (10) chrootEnsureDeployment must never mint a record for a blank id —
// that is the #5488 empty-ID poisoning source.
func TestChrootEnsureDeployment_5488_EmptyIDReturnsNil(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", fqdn5488)
	h := NewWithPDM(silentLogger(), &fakePDM{})

	for _, id := range []string{"", "   "} {
		if dep := h.chrootEnsureDeployment(id); dep != nil {
			t.Fatalf("chrootEnsureDeployment(%q) minted a record (ID %q) — the #5488 poisoning source", id, dep.ID)
		}
	}
	if _, ok := h.deployments.Load(""); ok {
		t.Fatalf("a record was stored under the empty key")
	}
}
