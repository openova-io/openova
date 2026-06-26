package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// orgWithTenantPublic builds a minimal Organization CR carrying a pool
// parentDomain so reconcileTenantDNS engages (the funnel-created shape).
func orgWithTenantPublic(parent, sub string) *orgapi.Organization {
	o := &orgapi.Organization{}
	o.Name = "f4179done"
	o.Spec.Slug = "f4179done"
	o.Spec.TenantPublic = orgapi.OrganizationTenantPublic{
		ParentDomain: parent,
		Subdomain:    sub,
	}
	return o
}

// captured records the single PATCH the reconciler issues so the test can
// assert URL + body byte-for-byte.
type capturedPatch struct {
	path   string
	apiKey string
	body   map[string]any
}

func newPDNSStub(t *testing.T, cap *capturedPatch) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method: got %s, want PATCH", r.Method)
		}
		cap.path = r.URL.Path
		cap.apiKey = r.Header.Get("X-API-Key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cap.body)
		w.WriteHeader(http.StatusNoContent) // PowerDNS PATCH returns 204
	}))
}

// TestReconcileTenantDNS_WritesConsoleAndWildcard is the #4236 close-gate unit:
// a funnel-created Org with a pool parentDomain MUST upsert console.<slug>.<pool>
// + *.<slug>.<pool> A-records pointing at the console-ELB IP, via PATCH REPLACE
// against the CENTRAL pdns zone, authenticated with the pool key.
func TestReconcileTenantDNS_WritesConsoleAndWildcard(t *testing.T) {
	t.Parallel()
	var cap capturedPatch
	srv := newPDNSStub(t, &cap)
	defer srv.Close()

	r := &Reconciler{
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     srv.URL,
		PoolPowerDNSAPIKey:  "central-pool-key",
		TenantConsoleLBIPv4: "212.72.24.33",
	}

	changed, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "f4179done"))
	if err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true (records written)")
	}

	// Zone targeted is the POOL parent (omani.works.), not the Sovereign FQDN.
	if want := "/api/v1/servers/localhost/zones/omani.works."; cap.path != want {
		t.Errorf("PATCH path: got %q, want %q", cap.path, want)
	}
	if cap.apiKey != "central-pool-key" {
		t.Errorf("X-API-Key: got %q, want central-pool-key", cap.apiKey)
	}

	rrsets, _ := cap.body["rrsets"].([]any)
	if len(rrsets) != 2 {
		t.Fatalf("rrsets: got %d, want 2 (console + wildcard)", len(rrsets))
	}
	names := map[string]map[string]any{}
	for _, rs := range rrsets {
		m := rs.(map[string]any)
		names[m["name"].(string)] = m
	}
	for _, wantName := range []string{"console.f4179done.omani.works.", "*.f4179done.omani.works."} {
		m, ok := names[wantName]
		if !ok {
			t.Fatalf("missing rrset %q (have %v)", wantName, keysOf(names))
		}
		if m["type"] != "A" {
			t.Errorf("%s type: got %v, want A", wantName, m["type"])
		}
		if m["changetype"] != "REPLACE" {
			t.Errorf("%s changetype: got %v, want REPLACE (idempotent upsert)", wantName, m["changetype"])
		}
		recs := m["records"].([]any)
		if len(recs) != 1 || recs[0].(map[string]any)["content"] != "212.72.24.33" {
			t.Errorf("%s records: got %v, want [212.72.24.33]", wantName, recs)
		}
	}
}

// TestReconcileTenantDNS_FallsBackToPrimaryLB locks the single-LB / pre-#4053
// path: with no dedicated console IP, the primary LB IP targets the records so
// they still resolve.
func TestReconcileTenantDNS_FallsBackToPrimaryLB(t *testing.T) {
	t.Parallel()
	var cap capturedPatch
	srv := newPDNSStub(t, &cap)
	defer srv.Close()

	r := &Reconciler{
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     srv.URL,
		PoolPowerDNSAPIKey:  "k",
		TenantPrimaryLBIPv4: "77.42.11.95", // console IP empty → fallback
	}
	if _, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "acme")); err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	rrsets, _ := cap.body["rrsets"].([]any)
	if len(rrsets) == 0 {
		t.Fatalf("expected rrsets written via fallback IP")
	}
	got := rrsets[0].(map[string]any)["records"].([]any)[0].(map[string]any)["content"]
	if got != "77.42.11.95" {
		t.Errorf("fallback target: got %v, want 77.42.11.95", got)
	}
}

// TestReconcileTenantDNS_NoOpPaths covers every skip: empty parentDomain (legacy
// Org), unwired writer (no URL/key), and no IP. None of these write or error —
// they must NOT wedge the Org reconcile.
func TestReconcileTenantDNS_NoOpPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		r    *Reconciler
		org  *orgapi.Organization
	}{
		{
			name: "empty parentDomain (legacy single-domain Org)",
			r:    &Reconciler{Log: logr.Discard(), PoolPowerDNSURL: "http://x", PoolPowerDNSAPIKey: "k", TenantConsoleLBIPv4: "1.2.3.4"},
			org:  orgWithTenantPublic("", ""),
		},
		{
			name: "writer unwired (no pdns url/key)",
			r:    &Reconciler{Log: logr.Discard(), TenantConsoleLBIPv4: "1.2.3.4"},
			org:  orgWithTenantPublic("omani.works", "acme"),
		},
		{
			name: "no console IP",
			r:    &Reconciler{Log: logr.Discard(), PoolPowerDNSURL: "http://x", PoolPowerDNSAPIKey: "k"},
			org:  orgWithTenantPublic("omani.works", "acme"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := tc.r.reconcileTenantDNS(context.Background(), tc.org)
			if err != nil {
				t.Errorf("expected nil err (no-op, not a wedge), got %v", err)
			}
			if changed {
				t.Errorf("expected changed=false (no write)")
			}
		})
	}
}

// TestReconcileTenantDNS_PatchErrorSurfaces locks that a PowerDNS non-2xx is
// surfaced as an error so the caller requeues (rather than silently dropping the
// record).
func TestReconcileTenantDNS_PatchErrorSurfaces(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"zone not found"}`))
	}))
	defer srv.Close()

	r := &Reconciler{
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     srv.URL,
		PoolPowerDNSAPIKey:  "k",
		TenantConsoleLBIPv4: "212.72.24.33",
	}
	_, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "acme"))
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("expected HTTP 422 surfaced as error, got %v", err)
	}
}

// secretScheme registers core types (Secret) so the fake client can serve the
// live pool-key read in the #4290/#4179 self-heal tests.
func secretScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return s
}

// TestReconcileTenantDNS_SelfHealsViaLiveSecretRead is the #4290/#4179 regression
// lock: when the env-frozen pool key is EMPTY (the org-controller Pod started
// before bp-reflector filled the bridged Secret) but the Secret now carries the
// key, the reconciler must READ IT LIVE and write the per-Org A-records — instead
// of staying have_key:false forever. This is the actual fresh-prov defect: the
// CATALYST_POOL_POWERDNS_API_KEY secretKeyRef binds once at Pod start, so an
// empty env never self-heals without this live read.
func TestReconcileTenantDNS_SelfHealsViaLiveSecretRead(t *testing.T) {
	t.Parallel()
	var cap capturedPatch
	srv := newPDNSStub(t, &cap)
	defer srv.Close()

	// The bridged Secret the reflector lands AFTER Pod start.
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-powerdns-api-credentials",
			Namespace: "catalyst-system",
		},
		Data: map[string][]byte{"api-key": []byte("reflected-pool-key")},
	}
	cl := fake.NewClientBuilder().WithScheme(secretScheme(t)).WithObjects(sec).Build()

	r := &Reconciler{
		Client:              cl,
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     srv.URL,
		PoolPowerDNSAPIKey:  "", // env frozen EMPTY (pre-reflection Pod start)
		TenantConsoleLBIPv4: "212.72.24.33",
		// Secret name/namespace default to pool-powerdns-api-credentials /
		// catalyst-system — match the fixture above without setting them.
	}

	changed, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "f4179done"))
	if err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true — live Secret read should have supplied the key")
	}
	if cap.apiKey != "reflected-pool-key" {
		t.Errorf("X-API-Key: got %q, want reflected-pool-key (proves the LIVE read was used, not the empty env)", cap.apiKey)
	}
	rrsets, _ := cap.body["rrsets"].([]any)
	if len(rrsets) != 2 {
		t.Fatalf("rrsets: got %d, want 2 (console + wildcard)", len(rrsets))
	}
}

// TestReconcileTenantDNS_SelfHealsViaSourceSecretRead is the #4475 §2 regression
// lock: when the env-frozen pool key is EMPTY and the bridged DESTINATION Secret
// (`catalyst-system/pool-powerdns-api-credentials`) is STILL ABSENT (bp-reflector
// never re-emitted after the late source-create), but the reflector SOURCE Secret
// (`cert-manager/powerdns-api-credentials`) now carries the key, the reconciler
// must FALL BACK to reading the SOURCE directly and write the per-Org A-records —
// sidestepping the stuck reflector entirely. Without this, console.<slug>.<pool>
// stays NXDOMAIN forever even though the central key is present in cert-manager.
func TestReconcileTenantDNS_SelfHealsViaSourceSecretRead(t *testing.T) {
	t.Parallel()
	var cap capturedPatch
	srv := newPDNSStub(t, &cap)
	defer srv.Close()

	// ONLY the reflector SOURCE exists (cert-manager). The bridged destination
	// (catalyst-system/pool-powerdns-api-credentials) is deliberately absent —
	// the reflector never re-emitted.
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "powerdns-api-credentials",
			Namespace: "cert-manager",
		},
		Data: map[string][]byte{"api-key": []byte("source-pool-key")},
	}
	cl := fake.NewClientBuilder().WithScheme(secretScheme(t)).WithObjects(src).Build()

	r := &Reconciler{
		Client:              cl,
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     srv.URL,
		PoolPowerDNSAPIKey:  "", // env frozen EMPTY (pre-reflection Pod start)
		TenantConsoleLBIPv4: "212.72.24.33",
		// Destination + source secret name/namespace default to the #4218 chart
		// PULL-stub values — match the fixture above without setting them.
	}

	changed, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "f4475done"))
	if err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true — SOURCE Secret read should have supplied the key when the bridged destination was absent")
	}
	if cap.apiKey != "source-pool-key" {
		t.Errorf("X-API-Key: got %q, want source-pool-key (proves the SOURCE-secret fallback was used, not the missing destination)", cap.apiKey)
	}
	rrsets, _ := cap.body["rrsets"].([]any)
	if len(rrsets) != 2 {
		t.Fatalf("rrsets: got %d, want 2 (console + wildcard)", len(rrsets))
	}
}

// TestReconcileTenantDNS_PrefersDestinationOverSource locks the source ordering:
// when BOTH the bridged destination AND the reflector source carry a key, the
// reconciler must use the DESTINATION (the controller's own namespace, the
// steady-state path) — the source read is a fallback only.
func TestReconcileTenantDNS_PrefersDestinationOverSource(t *testing.T) {
	t.Parallel()
	var cap capturedPatch
	srv := newPDNSStub(t, &cap)
	defer srv.Close()

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-powerdns-api-credentials", Namespace: "catalyst-system"},
		Data:       map[string][]byte{"api-key": []byte("destination-key")},
	}
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "powerdns-api-credentials", Namespace: "cert-manager"},
		Data:       map[string][]byte{"api-key": []byte("source-key")},
	}
	cl := fake.NewClientBuilder().WithScheme(secretScheme(t)).WithObjects(dst, src).Build()

	r := &Reconciler{
		Client:              cl,
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     srv.URL,
		PoolPowerDNSAPIKey:  "",
		TenantConsoleLBIPv4: "212.72.24.33",
	}
	if _, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "f4475pref")); err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if cap.apiKey != "destination-key" {
		t.Errorf("X-API-Key: got %q, want destination-key (the bridged destination must be preferred over the source)", cap.apiKey)
	}
}

// TestReconcileTenantDNS_LoudFailWhenKeyMissing locks the loud-fail guard: when
// the pool URL is configured (this Sovereign EXPECTS a central pool write) but
// the key is absent from both env AND the live Secret, the reconciler must return
// an ERROR so the controller requeues until the reflector lands the key — NOT a
// silent no-op that leaves console.<slug>.<pool> NXDOMAIN forever.
func TestReconcileTenantDNS_LoudFailWhenKeyMissing(t *testing.T) {
	t.Parallel()
	// Empty client (no bridged Secret yet) + configured pool URL + empty env key.
	cl := fake.NewClientBuilder().WithScheme(secretScheme(t)).Build()
	r := &Reconciler{
		Client:              cl,
		Log:                 logr.Discard(),
		PoolPowerDNSURL:     "https://pdns.openova.io",
		PoolPowerDNSAPIKey:  "", // env frozen empty
		TenantConsoleLBIPv4: "212.72.24.33",
	}
	changed, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.works", "f4179done"))
	if err == nil {
		t.Fatalf("expected a loud error (requeue) when pool URL set but key missing, got nil")
	}
	if changed {
		t.Errorf("expected changed=false when no write happened")
	}
	if !strings.Contains(err.Error(), "pool API key is empty") {
		t.Errorf("error should name the empty-key cause, got %q", err.Error())
	}
}
