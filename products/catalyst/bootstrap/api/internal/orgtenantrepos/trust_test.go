package orgtenantrepos

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

const testLocal = "oci://registry.hw.test/openova-io"

// The trust reference sits right under each url, the bytes step 06's
// inject_trust_ref writes (06-helmrepository-patches-job.yaml: the url line,
// then `certSecretRef:` and `  name: <ref>` at the url's indent), so step 06
// re-injecting into this file changes nothing.
func TestRenderPutsTheTrustReferenceUnderEveryURL(t *testing.T) {
	got := Render(testLocal, TrustSecretName)
	want := "  url: " + testLocal + "\n  certSecretRef:\n    name: cutover-harbor-ca\n  secretRef:\n    name: ghcr-pull\n"
	if n := strings.Count(got, want); n != 6 {
		t.Fatalf("want the trust reference under all 6 urls, found %d:\n%s", n, got)
	}
	if n := strings.Count(Render(testLocal, ""), "  certSecretRef:\n"); n != 0 {
		t.Errorf("no trust: want no certSecretRef, found %d", n)
	}
	if Render(GHCRBase, TrustSecretName) != Template {
		t.Error("the public catalog takes no trust reference: Render must be Template")
	}
}

// step06Secret is a Secret as step 06's Phase 0.5 writes it (labelled, ca.crt
// only), named name in ns and created at minute min.
func step06Secret(name, ns string, min int) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels:            map[string]string{"app.kubernetes.io/managed-by": "self-sovereign-cutover", "bp.openova.io/cutover-step": "helmrepository-patches"},
			CreationTimestamp: metav1.NewTime(time.Date(2026, 11, 21, 4, min, 0, 0, time.UTC))},
		Data: map[string][]byte{"ca.crt": []byte("-----BEGIN CERTIFICATE-----")},
	}
}

func TestTrustFollowsTheStep06Secret(t *testing.T) {
	ctx := context.Background()
	if got, err := Trust(ctx, kfake.NewSimpleClientset(step06Secret(TrustSecretName, TrustSecretNamespace, 5))); got != TrustSecretName || err != nil {
		t.Errorf("Secret present: %q, %v; want %q", got, err, TrustSecretName)
	}
	// helmRepositoryTrust.destSecretName overridden: on main the blocks named
	// no Secret at all, and step 08, which judges the url only, passed them.
	if got, err := Trust(ctx, kfake.NewSimpleClientset(step06Secret("sovereign-registry-ca", TrustSecretNamespace, 5))); got != "sovereign-registry-ca" || err != nil {
		t.Errorf("overridden name: %q, %v; want sovereign-registry-ca", got, err)
	}
	if got, _ := Trust(ctx, kfake.NewSimpleClientset(step06Secret("old-ca", TrustSecretNamespace, 3), step06Secret("new-ca", TrustSecretNamespace, 7))); got != "new-ca" {
		t.Errorf("two step-06 Secrets (an override between runs): %q, want the newest", got)
	}
	if got, err := Trust(ctx, kfake.NewSimpleClientset()); got != "" || err != nil {
		t.Errorf("Secret absent (step 06 not there yet, trust off, CA unreadable): %q, %v; want none", got, err)
	}
	if got, _ := Trust(ctx, kfake.NewSimpleClientset(step06Secret(TrustSecretName, "vcluster-platform", 5))); got != "" {
		t.Errorf("a step-06 Secret outside %s (the loft trust copy) is not the trust Secret: %q", TrustSecretNamespace, got)
	}
	noCA := step06Secret("ghcr-pull-copy", TrustSecretNamespace, 5)
	noCA.Data = map[string][]byte{".dockerconfigjson": []byte("{}")}
	if got, _ := Trust(ctx, kfake.NewSimpleClientset(noCA)); got != "" {
		t.Errorf("a labelled Secret without ca.crt is not a trust Secret: %q", got)
	}
	unlabelled := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: TrustSecretName, Namespace: TrustSecretNamespace}}
	if got, _ := Trust(ctx, kfake.NewSimpleClientset(unlabelled)); got != "" {
		t.Errorf("a Secret step 06 did not write is not the trust Secret: %q", got)
	}
	broken := kfake.NewSimpleClientset()
	broken.PrependReactor("list", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the apiserver is unreachable")
	})
	if got, err := Trust(ctx, broken); got != TrustSecretName || err == nil {
		t.Errorf("unreadable: %q, %v; want %q with the error", got, err, TrustSecretName)
	}
}

// The name Trust answers when the cluster cannot be read is the chart's
// default: the test reads the cutover chart's values file in this checkout.
func TestTrustSecretNameIsTheChartDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean("../../../../../../platform/self-sovereign-cutover/chart/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		HelmRepositoryTrust struct {
			DestSecretName string `json:"destSecretName"`
		} `json:"helmRepositoryTrust"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if got := values.HelmRepositoryTrust.DestSecretName; got != TrustSecretName {
		t.Errorf("chart helmRepositoryTrust.destSecretName = %q, TrustSecretName = %q: change both", got, TrustSecretName)
	}
}

func TestFileLooksUpTrustOnlyOffThePublicCatalog(t *testing.T) {
	ctx := context.Background()
	calls, strips := 0, 0
	lookup := func(ref string, err error) TrustFunc {
		return func(context.Context) (string, error) { calls++; return ref, err }
	}
	held, stripped := stripLookup(true, nil, &strips), stripLookup(false, nil, &strips)
	env := envOf(map[string]string{"SOVEREIGN_FQDN": "hw.test"})
	if got := File(ctx, env, map[string]string{StatusKeyRegistries: "v1"}, held, lookup(TrustSecretName, nil), nil); got != Template || calls != 0 {
		t.Errorf("not pivoted: want Template and no lookup, got %d lookups:\n%s", calls, got)
	}
	if got := File(ctx, env, map[string]string{StatusKeyRegistries: "v2"}, held, lookup(TrustSecretName, nil), nil); got != Template || calls != 0 || strips != 1 {
		t.Errorf("pivoted, step 06 before its strip: want Template and no trust lookup, got %d lookups, %d of the strip:\n%s", calls, strips, got)
	}
	failedAfterStrip := map[string]string{StatusKeyRegistries: "v2", StatusKeySourcesPivoted: "failed"}
	if got := File(ctx, env, failedAfterStrip, stripped, lookup(TrustSecretName, nil), nil); got != Render(testLocal, TrustSecretName) || calls != 1 {
		t.Errorf("step 06 failed after its strip: want the own source with the trust reference, %d lookups:\n%s", calls, got)
	}
	pivoted := usableStatus
	if got := File(ctx, env, pivoted, nil, lookup(TrustSecretName, nil), nil); got != Render(testLocal, TrustSecretName) || calls != 2 {
		t.Errorf("pivoted, Secret present: %d lookups:\n%s", calls, got)
	}
	if got := File(ctx, env, pivoted, nil, lookup("", nil), nil); got != Render(testLocal, "") {
		t.Errorf("pivoted, Secret absent:\n%s", got)
	}
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	if got := File(ctx, env, pivoted, nil, lookup(TrustSecretName, errors.New("forbidden")), log); got != Render(testLocal, TrustSecretName) {
		t.Errorf("lookup error: the reference must be kept:\n%s", got)
	}
	if !strings.Contains(logged.String(), "forbidden") {
		t.Errorf("lookup error not logged: %q", logged.String())
	}
}

func TestNeedsRewrite(t *testing.T) {
	withTrust := Render(testLocal, TrustSecretName)
	for _, tc := range []struct {
		name, doc, want string
		rewrite         bool
	}{
		{"the public catalog", Template, withTrust, true},
		{"own registry without the reference the Secret now allows", Render(testLocal, ""), withTrust, true},
		{"already right", withTrust, withTrust, false},
		{"own registry, Secret still absent", Render(testLocal, ""), Render(testLocal, ""), false},
		{"a reference is never removed", withTrust, Render(testLocal, ""), false},
	} {
		if got := NeedsRewrite([]byte(tc.doc), tc.want); got != tc.rewrite {
			t.Errorf("%s: NeedsRewrite = %v, want %v", tc.name, got, tc.rewrite)
		}
	}
}
