package orgtenantrepos

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
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

func trustSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: TrustSecretName, Namespace: TrustSecretNamespace}}
}

func TestTrustFollowsTheStep06Secret(t *testing.T) {
	ctx := context.Background()
	if got, err := Trust(ctx, kfake.NewSimpleClientset(trustSecret())); got != TrustSecretName || err != nil {
		t.Errorf("Secret present: %q, %v; want %q", got, err, TrustSecretName)
	}
	if got, err := Trust(ctx, kfake.NewSimpleClientset()); got != "" || err != nil {
		t.Errorf("Secret absent (step 06 not there yet, trust off, CA unreadable): %q, %v; want none", got, err)
	}
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: TrustSecretName, Namespace: "kube-system"}}
	if got, _ := Trust(ctx, kfake.NewSimpleClientset(other)); got != "" {
		t.Errorf("a Secret of that name outside %s is not the trust Secret: %q", TrustSecretNamespace, got)
	}
	broken := kfake.NewSimpleClientset()
	broken.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the apiserver is unreachable")
	})
	if got, err := Trust(ctx, broken); got != TrustSecretName || err == nil {
		t.Errorf("unreadable: %q, %v; want %q with the error", got, err, TrustSecretName)
	}
}

func TestFileLooksUpTrustOnlyOffThePublicCatalog(t *testing.T) {
	ctx := context.Background()
	calls := 0
	lookup := func(ref string, err error) TrustFunc {
		return func(context.Context) (string, error) { calls++; return ref, err }
	}
	env := envOf(map[string]string{"SOVEREIGN_FQDN": "hw.test"})
	if got := File(ctx, env, map[string]string{StatusKeyRegistries: "v1"}, lookup(TrustSecretName, nil), nil); got != Template || calls != 0 {
		t.Errorf("not pivoted: want Template and no lookup, got %d lookups:\n%s", calls, got)
	}
	pivoted := map[string]string{StatusKeyRegistries: "v2"}
	if got := File(ctx, env, pivoted, lookup(TrustSecretName, nil), nil); got != Render(testLocal, TrustSecretName) || calls != 1 {
		t.Errorf("pivoted, Secret present: %d lookups:\n%s", calls, got)
	}
	if got := File(ctx, env, pivoted, lookup("", nil), nil); got != Render(testLocal, "") {
		t.Errorf("pivoted, Secret absent:\n%s", got)
	}
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	if got := File(ctx, env, pivoted, lookup(TrustSecretName, errors.New("forbidden")), log); got != Render(testLocal, TrustSecretName) {
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
