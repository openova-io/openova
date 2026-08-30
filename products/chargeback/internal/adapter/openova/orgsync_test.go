package openova

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
)

func testKeys(t *testing.T) *crypto.Keyring {
	t.Helper()
	keys, err := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// orgUnstructured builds an Organization CR the way the apiserver would
// hand it to the dynamic informer.
func orgUnstructured(slug string, mutate func(spec map[string]any)) *unstructured.Unstructured {
	spec := map[string]any{
		"slug":         slug,
		"displayName":  "ACME Corp",
		"kind":         "customer",
		"tier":         "org",
		"billingMode":  "real",
		"sovereignRef": "t99.omani.works",
		"owners": []any{
			map[string]any{"email": "ceo@acme.example", "role": "owner"},
		},
	}
	if mutate != nil {
		mutate(spec)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   map[string]any{"name": slug},
		"spec":       spec,
	}}
}

type fakeVerifier struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (v *fakeVerifier) VerifyProject(_ context.Context, region, projectID, accessKey, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, region+"/"+projectID+"/"+accessKey)
	return v.err
}

func waitFor(t *testing.T, d time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestOrgSyncInformerCreatesAndSuspends drives the real list+watch against
// the fake dynamic client: an existing Organization becomes an active
// customer with its platform source; deleting the CR SUSPENDS the customer
// (never deletes — history is billing data).
func TestOrgSyncInformerCreatesAndSuspends(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{OrganizationGVR: "OrganizationList"},
		orgUnstructured("acme", nil))
	repo := newFakeRepo()
	s := &OrgSync{Dyn: dyn, Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New(), Resync: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitFor(t, 5*time.Second, "customer upserted from the Organization CR", func() bool {
		c, ok := repo.customerBySlug("acme")
		return ok && c.Status == "active"
	})
	c, _ := repo.customerBySlug("acme")
	if c.Kind != "organization" || c.BillingMode != "real" || c.AdminEmail != "ceo@acme.example" {
		t.Fatalf("customer = %+v", c)
	}
	if c.OrgSlug == nil || *c.OrgSlug != "acme" {
		t.Fatalf("org_slug = %v, want acme", c.OrgSlug)
	}
	waitFor(t, 5*time.Second, "verified platform source", func() bool {
		for _, src := range repo.sourcesOf(c.ID) {
			if src.Kind == SourceKindOrg && src.Status == "verified" {
				return true
			}
		}
		return false
	})

	if err := dyn.Resource(OrganizationGVR).Delete(ctx, "acme", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "customer suspended on Organization delete", func() bool {
		got, ok := repo.customerBySlug("acme")
		return ok && got.Status == "suspended"
	})
}

// TestSyncOrganizationCostSources: spec.costSources[] become cost_sources
// with the credential resolved read-only from the named Secret in the Org's
// host namespace, verified through the Verifier, and a resync never mints a
// second credential.
func TestSyncOrganizationCostSources(t *testing.T) {
	repo := newFakeRepo()
	keys := testKeys(t)
	core := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "acme", Name: "acme-huawei-aksk"},
		Data:       map[string][]byte{"aksk": []byte("AKTEST:SKSECRET")},
	})
	ver := &fakeVerifier{}
	s := &OrgSync{Core: core, Repo: repo, Keys: keys, Verifier: ver, Metrics: metrics.New()}
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["costSources"] = []any{
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-1",
				"credentialRef": map[string]any{"name": "acme-huawei-aksk", "key": "aksk"}},
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-2"},
		}
	})
	ctx := context.Background()
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, ok := repo.customerBySlug("acme")
	if !ok || c.Status != "active" {
		t.Fatalf("customer = %+v ok=%v", c, ok)
	}
	var declared, bare, platform int
	for _, src := range repo.sourcesOf(c.ID) {
		switch {
		case src.Kind == SourceKindOrg && src.Status == "verified":
			platform++
		case src.Kind == "huawei-project" && src.ProjectID == "proj-1":
			declared++
			if src.Status != "verified" || src.AccessKey != "AKTEST" {
				t.Fatalf("declared source = %+v", src)
			}
			sk, err := keys.Open(repo.credEnc[*src.CredentialID])
			if err != nil || string(sk) != "SKSECRET" {
				t.Fatalf("stored secret = %q err=%v", sk, err)
			}
		case src.Kind == "huawei-project" && src.ProjectID == "proj-2":
			bare++
			if src.Status != "pending" || src.CredentialID != nil {
				t.Fatalf("bare source must stay pending for UI credential entry: %+v", src)
			}
		}
	}
	if platform != 1 || declared != 1 || bare != 1 {
		t.Fatalf("sources platform=%d declared=%d bare=%d, want 1/1/1", platform, declared, bare)
	}
	if len(ver.calls) != 1 || ver.calls[0] != "me-east-215/proj-1/AKTEST" {
		t.Fatalf("verifier calls = %v", ver.calls)
	}

	// Resync: nothing changed → no new credential, no duplicate source.
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if len(repo.creds) != 1 {
		t.Fatalf("resync minted credentials: %d, want 1", len(repo.creds))
	}
	if n := len(repo.sourcesOf(c.ID)); n != 3 {
		t.Fatalf("resync duplicated sources: %d, want 3", n)
	}
}

// TestSyncOrganizationFailedVerificationRedactsSecret: a failing declared
// credential flips the source to failed with the secret redacted from
// last_error, and the sync itself still succeeds (per-source isolation).
func TestSyncOrganizationFailedVerificationRedactsSecret(t *testing.T) {
	repo := newFakeRepo()
	core := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "acme", Name: "aksk"},
		Data:       map[string][]byte{"v": []byte(`{"accessKey":"AKX","secretKey":"SKX"}`)},
	})
	ver := &fakeVerifier{err: errors.New("gateway said no to SKX")}
	s := &OrgSync{Core: core, Repo: repo, Keys: testKeys(t), Verifier: ver, Metrics: metrics.New()}
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["costSources"] = []any{
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-9",
				"credentialRef": map[string]any{"name": "aksk", "key": "v"}},
		}
	})
	if err := s.SyncOrganization(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	for _, src := range repo.sourcesOf(c.ID) {
		if src.Kind != "huawei-project" {
			continue
		}
		if src.Status != "failed" || src.LastError == nil {
			t.Fatalf("source = %+v", src)
		}
		if strings.Contains(*src.LastError, "SKX") {
			t.Fatalf("last_error leaks the secret: %q", *src.LastError)
		}
	}
}

// TestReadOrgOwnerAndBillingDefaults: the owner-role email wins over the
// roster order and an absent billingMode falls back to showback.
func TestReadOrgOwnerAndBillingDefaults(t *testing.T) {
	org := orgUnstructured("bank", func(spec map[string]any) {
		spec["owners"] = []any{
			map[string]any{"email": "admin@bank.example", "role": "admin"},
			map[string]any{"email": "cto@bank.example", "role": "owner"},
		}
		delete(spec, "billingMode")
	})
	f, err := readOrg(org)
	if err != nil {
		t.Fatal(err)
	}
	if f.AdminEmail != "cto@bank.example" {
		t.Fatalf("admin email = %q, want the owner-role entry", f.AdminEmail)
	}
	if f.BillingMode != "showback" {
		t.Fatalf("billing mode = %q, want the showback floor", f.BillingMode)
	}

	// No roster at all → blank-pending.
	f2, err := readOrg(orgUnstructured("solo", func(spec map[string]any) { delete(spec, "owners") }))
	if err != nil {
		t.Fatal(err)
	}
	if f2.AdminEmail != "" {
		t.Fatalf("admin email = %q, want blank-pending", f2.AdminEmail)
	}
}

func TestParseAKSK(t *testing.T) {
	ak, sk, err := parseAKSK([]byte("AK1:SK1\n"))
	if err != nil || ak != "AK1" || string(sk) != "SK1" {
		t.Fatalf("colon form: %q/%q err=%v", ak, sk, err)
	}
	ak, sk, err = parseAKSK([]byte(`{"access_key":"AK2","secret_key":"SK2"}`))
	if err != nil || ak != "AK2" || string(sk) != "SK2" {
		t.Fatalf("snake json: %q/%q err=%v", ak, sk, err)
	}
	if _, _, err := parseAKSK([]byte("just-one-token")); err == nil {
		t.Fatal("want error for a value with no separator")
	}
}
