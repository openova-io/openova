package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Tests for #5501 — `POST /api/v1/organizations` answered HTTP 202 with
// `state:"done"`, all six provisioning steps `"done"`, in ZERO seconds, over a
// Sovereign that had no namespace, no vCluster and an empty `status.phase`
// (walked live on hw291, 2026-07-29). Unlike every other fake-green in this
// codebase — a display reading stale data — this one FABRICATED SUCCESS AT
// CREATION on the write path.
//
// Anti-theater: every case is proven in BOTH directions. The fresh-create
// assertions fail against the pre-fix code (which reported done/6-done), and
// the observed-Ready control assertions pin that an Organization whose
// boundary IS up still reports done — a fix that simply inverted the bug
// ("never report success") would satisfy the first half and break the second.

// newOrgPipelineHandlerWithCRs wires the FULL Organization provisioning
// pipeline (gitops/dns/keycloak/registry stubs — the same fakes the #804
// suite uses) AND a fake dynamic client pre-seeded with the supplied
// Organization CRs, so the create path can be exercised against a cluster
// whose boundary state we control.
func newOrgPipelineHandlerWithCRs(t *testing.T, crs ...*unstructured.Unstructured) (*Handler, *dynamicfake.FakeDynamicClient) {
	t.Helper()

	// The pipeline's step-7 console-TLS finaliser polls the console Gateway
	// for listener admission and spends its whole 60s budget against a fake
	// client that has no Gateway. Shrink it the way org_console_tls_test.go
	// does, or five creates alone would push the package past its 10m
	// deadline.
	prevBudget, prevInterval := orgConsoleListenerAdmitBudget, orgConsoleListenerAdmitPollInterval
	orgConsoleListenerAdmitBudget = 50 * time.Millisecond
	orgConsoleListenerAdmitPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		orgConsoleListenerAdmitBudget, orgConsoleListenerAdmitPollInterval = prevBudget, prevInterval
	})

	dir := t.TempDir()
	tenantStore, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("tenant store: %v", err)
	}
	registry, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("tenant registry: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetOrganizationDeps(OrganizationDeps{
		Store:            tenantStore,
		GitOps:           &fakeGitOps{},
		DNS:              &fakeDNS{},
		KeycloakClients:  &fakeKCClients{},
		Events:           &fakeTenantEmitter{},
		TenantRegistry:   registry,
		OTECHFQDN:        "otech.example",
		OTECHIngressIPv4: "192.0.2.10",
		MaxRetryCount:    5,
	})

	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		organizationGVR(): "OrganizationList",
	}
	objs := make([]runtime.Object, 0, len(crs))
	for _, c := range crs {
		objs = append(objs, c)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, objs...)
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
	})
	return h, dyn
}

// orgCRHostNsReady builds a host-namespace-tier Organization CR (customer +
// plan s — the tier the walked hw291 Org actually was) whose boundary the
// org-controller has reported READY via the top-level Ready condition. A
// host-ns Org never gets a `status.vcluster` block (#5489), so the condition
// is the only readiness signal — the fixture deliberately omits the block.
func orgCRHostNsReady(slug string) *unstructured.Unstructured {
	cr := orgReadyCR(slug, strings.ToUpper(slug), "", "owner@"+slug+".test", "")
	cr.Object["status"] = map[string]any{
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Provisioned"},
		},
	}
	return cr
}

func postCreateOrg(t *testing.T, h *Handler, body string) (*httptest.ResponseRecorder, orgTenantResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)
	var got orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode create response: %v body=%s", err, w.Body.String())
	}
	return w, got
}

// TestCreateOrganization_FreshOrgIsNotTerminal_5501 is the issue's explicit
// task: a freshly-created Organization must report a NON-terminal state and
// non-"done" steps until the substrate is actually observed.
//
// The pipeline mints the Organization CR inside this very request, so the CR
// exists with NO status — precisely the hw291 shape. Pre-fix this returned
// `state:"done"` + six "done" steps; the fix holds it at tenant_registered
// with the substrate-side steps reporting the unobserved boundary.
func TestCreateOrganization_FreshOrgIsNotTerminal_5501(t *testing.T) {
	h, dyn := newOrgPipelineHandlerWithCRs(t)

	w, got := postCreateOrg(t, h, `{
		"subdomain":    "uatcorp",
		"admin_email":  "admin@uatcorp.test",
		"company_name": "UAT Corp",
		"domain_mode":  "free-subdomain"
	}`)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: want 202 got %d body=%s", w.Code, w.Body.String())
	}
	// 202 is correct — the resource IS being created. The body is what lied.
	if got.State == store.STSDone {
		t.Fatalf("fresh create must NOT report terminal success: state=%q — no namespace, no vCluster and no status.phase exist yet (#5501)", got.State)
	}
	if got.State != store.STSTenantRegistered {
		t.Errorf("fresh create should hold at the highest NON-terminal state; want %q got %q (lastError=%q)",
			store.STSTenantRegistered, got.State, got.LastError)
	}
	if got.Steps.BPCharts == "done" {
		t.Errorf("steps.bp_charts must not claim done over a boundary that has not been observed: got %q (#5501)", got.Steps.BPCharts)
	}
	if got.Steps.BPCharts != "pending" {
		t.Errorf("steps.bp_charts: want pending (boundary unobserved) got %q", got.Steps.BPCharts)
	}

	// Raw-JSON check — the struct decode cannot see an omitted key, and the
	// #5489 vcluster-step omission rides omitempty.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	var steps map[string]string
	if err := json.Unmarshal(raw["steps"], &steps); err != nil {
		t.Fatalf("decode steps: %v", err)
	}
	if _, present := steps["vcluster"]; present {
		t.Errorf("namespace-tier Org must not carry a vcluster step at all (#5489), got %v", steps)
	}
	doneCount := 0
	for _, v := range steps {
		if v == "done" {
			doneCount++
		}
	}
	if doneCount == len(steps) {
		t.Errorf("every step reported done in zero seconds — the exact #5501 defect; steps=%v", steps)
	}

	// Vacuity control: the pipeline really did run (the CR was minted), so
	// the non-terminal state above is a HONEST read of an unready boundary,
	// not a pipeline that silently did nothing.
	if _, err := dyn.Resource(organizationGVR()).Get(context.Background(), "uatcorp", metav1.GetOptions{}); err != nil {
		t.Fatalf("precondition: the pipeline must still mint the Organization CR: %v", err)
	}
	if got.CommitSHA == "" {
		t.Errorf("precondition: the GitOps overlay must still be committed (commit_sha empty)")
	}
}

// TestCreateOrganization_ObservedReadyBoundary_ReportsDone_5501 is the
// CONTROL direction: an Organization whose boundary the org-controller has
// already reported Ready must STILL report done with a fully green timeline.
// Without this case the fix could have been "never report success", which
// would break every genuinely-provisioned Org.
func TestCreateOrganization_ObservedReadyBoundary_ReportsDone_5501(t *testing.T) {
	// The CR exists and is Ready BEFORE the create (same-slug re-provision:
	// ensureOrganizationCR treats AlreadyExists as success and leaves the
	// controller-owned status untouched).
	h, _ := newOrgPipelineHandlerWithCRs(t, orgCRHostNsReady("acme"))

	w, got := postCreateOrg(t, h, `{
		"subdomain":   "acme",
		"admin_email": "admin@acme.test",
		"domain_mode": "free-subdomain"
	}`)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: want 202 got %d body=%s", w.Code, w.Body.String())
	}
	if got.State != store.STSDone {
		t.Fatalf("an Org whose boundary is OBSERVED Ready must still report done, got %q (lastError=%q)", got.State, got.LastError)
	}
	for name, step := range map[string]string{
		"bp_charts":        got.Steps.BPCharts,
		"dns":              got.Steps.DNS,
		"certs":            got.Steps.Certs,
		"keycloak_clients": got.Steps.KeycloakClients,
		"registry":         got.Steps.Registry,
	} {
		if step != "done" {
			t.Errorf("completed Org: steps.%s want done got %q", name, step)
		}
	}
}

// TestOrganization_HeldRecordPromotedWhenBoundaryTurnsReady_5501 proves the
// held record is not STRANDED: once the org-controller reports the boundary
// Ready, the NATS-driven reconciler promotes the same record to done. This is
// what makes the honest non-terminal answer safe — the caller's Organization
// still completes, it just completes when it is true rather than when it is
// submitted.
func TestOrganization_HeldRecordPromotedWhenBoundaryTurnsReady_5501(t *testing.T) {
	h, dyn := newOrgPipelineHandlerWithCRs(t)

	_, created := postCreateOrg(t, h, `{
		"subdomain":   "latecorp",
		"admin_email": "admin@latecorp.test",
		"domain_mode": "free-subdomain"
	}`)
	if created.State == store.STSDone {
		t.Fatalf("precondition: fresh create must be non-terminal, got %q", created.State)
	}

	// The org-controller finishes authoring the boundary and stamps Ready.
	ctx := context.Background()
	cr, err := dyn.Resource(organizationGVR()).Get(ctx, "latecorp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get minted CR: %v", err)
	}
	cr.Object["status"] = map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
	}
	if _, err := dyn.Resource(organizationGVR()).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("stamp Ready on CR: %v", err)
	}

	// The reconciler walks every non-terminal row.
	h.ReconcileAllPending(ctx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/"+created.OrganizationID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", created.OrganizationID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.HandleGetOrganization(w, req)
	var after orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.State != store.STSDone {
		t.Fatalf("after the boundary is observed Ready the record must promote to done, got %q (lastError=%q)", after.State, after.LastError)
	}
	if after.Steps.BPCharts != "done" {
		t.Errorf("promoted Org: steps.bp_charts want done got %q", after.Steps.BPCharts)
	}
}

// TestOrgCreateResponse_NeverPublishesZeroTimestamps_5501 — `created_at` /
// `updated_at` were Go zero values (`0001-01-01T00:00:00Z`) on the create
// response because the store stamped them on a by-value copy. A zero
// timestamp serializes as a valid RFC-3339 instant, so every consumer reads
// it as a real measurement. Asserted on the SERIALIZED JSON, not the struct:
// the wire is what lied.
func TestOrgCreateResponse_NeverPublishesZeroTimestamps_5501(t *testing.T) {
	h, _ := newOrgPipelineHandlerWithCRs(t)

	w, got := postCreateOrg(t, h, `{
		"subdomain":   "stamped",
		"admin_email": "admin@stamped.test",
		"domain_mode": "free-subdomain"
	}`)

	if body := w.Body.String(); strings.Contains(body, "0001-01-01") {
		t.Fatalf("response published a Go zero timestamp as a measurement (#5501/#5477): %s", body)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("created_at is the Go zero value")
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("updated_at is the Go zero value")
	}

	// Vacuity control: the keys must actually be PRESENT and parse — an
	// omitted-everywhere timestamp would also pass the substring check.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, key := range []string{"created_at", "updated_at"} {
		if _, present := raw[key]; !present {
			t.Errorf("%s must be present on a record whose timestamps ARE known", key)
		}
	}

	// The list view reads the same record back off disk — both views must
	// agree, which is what exposed the defect live (GET had real timestamps,
	// POST did not).
	lw := httptest.NewRecorder()
	h.HandleListOrganizations(lw, httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil))
	if body := lw.Body.String(); strings.Contains(body, "0001-01-01") {
		t.Errorf("list view published a Go zero timestamp: %s", body)
	}
}

// TestOrgCreateResponse_NamespaceTier_OmitsVClusterName_5501 is the #5489
// contract carried onto the create path: a namespace-isolated Org authors no
// vCluster, so the payload must not name one. Raw-JSON assertion — the field
// is omitempty, so a struct check alone could pass while the key still
// shipped as `"vcluster_name": ""`.
func TestOrgCreateResponse_NamespaceTier_OmitsVClusterName_5501(t *testing.T) {
	h, _ := newOrgPipelineHandlerWithCRs(t)

	w, got := postCreateOrg(t, h, `{
		"subdomain":   "nsonly",
		"admin_email": "admin@nsonly.test",
		"domain_mode": "free-subdomain"
	}`)

	if got.Isolation != "namespace" {
		t.Fatalf("fixture must be namespace-tier (customer + default plan s), got isolation=%q", got.Isolation)
	}
	if got.VClusterName != "" {
		t.Errorf("namespace-tier Org must not name a vCluster, got vcluster_name=%q", got.VClusterName)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, present := raw["vcluster_name"]; present {
		t.Errorf("vcluster_name key must be OMITTED for a namespace-tier Org, got %s", string(raw["vcluster_name"]))
	}
	// Vacuity control: the payload is not empty — the fields that DO describe
	// this Org's boundary are still present.
	if _, present := raw["tenant_namespace"]; !present {
		t.Errorf("tenant_namespace must still be reported")
	}
}

// TestVClusterName_MatchesOrgControllerAuthority_5501 — the API synthesized
// `vc-<slug>` while the CR the org-controller owns says the BARE SLUG
// (vclusterStatusFor stamps `status.vcluster.name = <slug>`). Two names for
// one object, and the one the API published named nothing that exists. The
// API must report the owner's name.
func TestVClusterName_MatchesOrgControllerAuthority_5501(t *testing.T) {
	t.Parallel()
	got := vclusterNameFor("vcluster", "uatcorp")
	if strings.HasPrefix(got, "vc-") {
		t.Errorf("vcluster_name must not be synthesized with a vc- prefix the CR does not use, got %q", got)
	}
	if got != "uatcorp" {
		t.Errorf("vcluster_name must match the org-controller's status.vcluster.name (the bare slug), want uatcorp got %q", got)
	}
	if ns := vclusterNameFor("namespace", "uatcorp"); ns != "" {
		t.Errorf("namespace tier authors no vCluster, want empty got %q", ns)
	}
}
