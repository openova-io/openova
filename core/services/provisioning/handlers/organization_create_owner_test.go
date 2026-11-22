// organization_create_owner_test.go — a paid order always ends in its own
// Organization.
//
// Three ways the creation path used to end a paid order somewhere else:
//
//  1. A reused name: the Organization `<slug>` of a previous (cancelled)
//     customer still exists, the POST answers 409, and the 409 was taken
//     as "the same tenant.created redelivered" without reading whose
//     Organization it is. The new customer was bound to the old one.
//  2. A permanent refusal (422: the Organization kind refuses a reserved
//     slug or a too-long name) was Nak'd as if transient, redelivered until
//     MaxDeliver and then dropped with no failure event.
//  3. A catalog outage at creation stamped plan "s" on a paid Organization
//     for good, because the creation path acked with the guessed plan.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPIServer answers the handler's in-cluster API requests (Handler.k8sDo).
// GETs are answered from gets by path (404 when absent); every POST is
// recorded and answered with post.
type fakeAPIServer struct {
	mu    sync.Mutex
	gets  map[string]fakeAPIResponse
	post  fakeAPIResponse
	posts []string
	reqs  []string // every request, "METHOD path"
}

// setGet replaces the answer to GET path (the object changed between reads).
func (f *fakeAPIServer) setGet(path string, r fakeAPIResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets == nil {
		f.gets = map[string]fakeAPIResponse{}
	}
	f.gets[path] = r
}

func (f *fakeAPIServer) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

type fakeAPIResponse struct {
	status int
	body   string
}

func (f *fakeAPIServer) do(method, path string, body []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, method+" "+path)
	var r fakeAPIResponse
	switch method {
	case http.MethodPost:
		f.posts = append(f.posts, string(body))
		r = f.post
	default:
		var ok bool
		if r, ok = f.gets[path]; !ok {
			r = fakeAPIResponse{status: http.StatusNotFound, body: `{"kind":"Status","reason":"NotFound"}`}
		}
	}
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 400 {
		return []byte(r.body), fmt.Errorf("k8s %s %s: status %d: %s", method, path, r.status, r.body)
	}
	return []byte(r.body), nil
}

func (f *fakeAPIServer) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

const orgPathAcme = "/apis/orgs.openova.io/v1/organizations/acme"

// existingOrg renders the Organization `acme` as the apiserver returns it.
func existingOrg(tenantID string, terminating bool) string {
	meta := map[string]any{
		"name":   "acme",
		"labels": map[string]any{"openova.io/tenant-id": tenantID},
	}
	if terminating {
		meta["deletionTimestamp"] = "2026-11-22T08:00:00Z"
	}
	b, _ := json.Marshal(map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   meta,
		"spec":       map[string]any{"slug": "acme"},
	})
	return string(b)
}

func newOwnerTestHandler(api *fakeAPIServer) (*Handler, *recordingPublisher) {
	pub := &recordingPublisher{}
	return &Handler{Producer: pub, k8sDo: api.do}, pub
}

func acmeOrder(tenantID string) tenantCreatedPayload {
	return tenantCreatedPayload{
		ID:         tenantID,
		Slug:       "acme",
		Name:       "Acme",
		OwnerEmail: "owner@acme.omani.homes",
		PlanID:     "m",
	}
}

const alreadyExists = `{"kind":"Status","reason":"AlreadyExists","code":409}`

// TestCreateOrganizationCR_ReusedSlugOfAnotherCustomerIsRefused: the 409 names
// an Organization labelled with a DIFFERENT tenant. The order must not be acked
// as a redelivery; it is refused loudly (provision.org_create_failed).
func TestCreateOrganizationCR_ReusedSlugOfAnotherCustomerIsRefused(t *testing.T) {
	api := &fakeAPIServer{
		post: fakeAPIResponse{status: http.StatusConflict, body: alreadyExists},
		gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", false)}},
	}
	h, pub := newOwnerTestHandler(api)

	if err := h.createOrganizationCR(context.Background(), acmeOrder("tenant-new")); err != nil {
		t.Fatalf("a slug held by another customer is permanent, want an ack with a failure event, got error %v", err)
	}
	ev := pub.byType("provision.org_create_failed")
	if ev == nil {
		t.Fatalf("the new customer was silently bound to the previous customer's Organization: no provision.org_create_failed event (events=%d)", len(pub.events()))
	}
	if pub.byType("provision.org_created") != nil {
		t.Fatalf("provision.org_created published for an Organization that belongs to another customer")
	}
	var data map[string]string
	_ = json.Unmarshal(ev.Data, &data)
	if data["slug"] != "acme" || !strings.Contains(data["reason"], "another") {
		t.Fatalf("failure event does not name the cause: %v", data)
	}
	if strings.Contains(data["reason"], "tenant-previous") {
		t.Fatalf("failure event leaks the previous customer's id: %q", data["reason"])
	}
}

// TestCreateOrganizationCR_RedeliveryOfTheSameCustomerIsAcked is the control:
// the 409 names an Organization of THIS tenant, so it is a redelivery.
func TestCreateOrganizationCR_RedeliveryOfTheSameCustomerIsAcked(t *testing.T) {
	api := &fakeAPIServer{
		post: fakeAPIResponse{status: http.StatusConflict, body: alreadyExists},
		gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-new", false)}},
	}
	h, pub := newOwnerTestHandler(api)

	if err := h.createOrganizationCR(context.Background(), acmeOrder("tenant-new")); err != nil {
		t.Fatalf("a redelivery must be acked, got %v", err)
	}
	if ev := pub.byType("provision.org_create_failed"); ev != nil {
		t.Fatalf("a redelivery of the same customer's order was refused: %s", ev.Data)
	}
}

// TestCreateOrganizationCR_TerminatingPredecessorIsRetried: the previous
// customer's Organization is being removed. The order must be redelivered (Nak)
// so it creates its own Organization once the old one is gone — neither acked
// onto the dying one nor refused for good.
func TestCreateOrganizationCR_TerminatingPredecessorIsRetried(t *testing.T) {
	api := &fakeAPIServer{
		post: fakeAPIResponse{status: http.StatusConflict, body: alreadyExists},
		gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", true)}},
	}
	h, pub := newOwnerTestHandler(api)

	if err := h.createOrganizationCR(context.Background(), acmeOrder("tenant-new")); err == nil {
		t.Fatalf("an order whose name is held by a Terminating Organization was acked; it must be redelivered until that Organization is gone")
	}
	if ev := pub.byType("provision.org_create_failed"); ev != nil {
		t.Fatalf("a transient (Terminating predecessor) was reported as a permanent failure: %s", ev.Data)
	}
}

// TestCreateOrganizationCR_PermanentRefusalIsReported: a 422 from the
// Organization kind (reserved slug, name over its maxLength) can never succeed
// on redelivery. It is acked with provision.org_create_failed, not Nak'd until
// JetStream drops it.
func TestCreateOrganizationCR_PermanentRefusalIsReported(t *testing.T) {
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			api := &fakeAPIServer{post: fakeAPIResponse{
				status: status,
				body:   `{"kind":"Status","reason":"Invalid","message":"spec.slug: Invalid value: \"huawei\""}`,
			}}
			h, pub := newOwnerTestHandler(api)

			if err := h.createOrganizationCR(context.Background(), acmeOrder("tenant-new")); err != nil {
				t.Fatalf("a permanent refusal (%d) was returned for redelivery: %v", status, err)
			}
			if pub.byType("provision.org_create_failed") == nil {
				t.Fatalf("a permanent refusal (%d) published no provision.org_create_failed", status)
			}
		})
	}
}

// TestCreateOrganizationCR_TransientRefusalIsRetried is the control: a 5xx is
// still redelivered.
func TestCreateOrganizationCR_TransientRefusalIsRetried(t *testing.T) {
	api := &fakeAPIServer{post: fakeAPIResponse{status: http.StatusServiceUnavailable, body: "unavailable"}}
	h, pub := newOwnerTestHandler(api)
	if err := h.createOrganizationCR(context.Background(), acmeOrder("tenant-new")); err == nil {
		t.Fatalf("a 503 was acked; it must be redelivered")
	}
	if pub.byType("provision.org_create_failed") != nil {
		t.Fatalf("a 503 was reported as a permanent failure")
	}
}

func catalogPlans(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/plans" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`[{"id":"6f1c2b9e-0000-4000-8000-00000000000l","slug":"l"}]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// postedPlanSlug decodes spec.planSlug of the one Organization POSTed.
func postedPlanSlug(t *testing.T, api *fakeAPIServer) string {
	t.Helper()
	if n := api.postCount(); n != 1 {
		t.Fatalf("POSTs = %d, want 1", n)
	}
	var cr struct {
		Spec struct {
			PlanSlug string `json:"planSlug"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(api.posts[0]), &cr); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	return cr.Spec.PlanSlug
}

// TestCreateOrganizationCR_CatalogOutageTakesThePlanFromTheSKU: the order
// carries a catalog plan UUID, the catalog cannot answer, and the order's own
// package sku is "plan.l". The Organization is created at plan l, never at a
// guessed "s" (nothing corrects spec.planSlug later).
func TestCreateOrganizationCR_CatalogOutageTakesThePlanFromTheSKU(t *testing.T) {
	cat := catalogPlans(t, http.StatusServiceUnavailable)
	api := &fakeAPIServer{post: fakeAPIResponse{status: http.StatusCreated, body: `{}`}}
	h, _ := newOwnerTestHandler(api)
	h.CatalogURL = cat.URL

	order := acmeOrder("tenant-new")
	order.PlanID = "6f1c2b9e-0000-4000-8000-00000000000l"
	order.PackageSKU = "plan.l"
	if err := h.createOrganizationCR(context.Background(), order); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := postedPlanSlug(t, api); got != "l" {
		t.Fatalf("a package-L order was created at plan %q during a catalog outage: the guess is permanent", got)
	}
}

// TestCreateOrganizationCR_CatalogOutageWithoutSKUIsRetried: neither the
// catalog nor the order can name the plan. The Organization is not created
// with a guess; the event is redelivered.
func TestCreateOrganizationCR_CatalogOutageWithoutSKUIsRetried(t *testing.T) {
	cat := catalogPlans(t, http.StatusServiceUnavailable)
	api := &fakeAPIServer{post: fakeAPIResponse{status: http.StatusCreated, body: `{}`}}
	h, _ := newOwnerTestHandler(api)
	h.CatalogURL = cat.URL

	order := acmeOrder("tenant-new")
	order.PlanID = "6f1c2b9e-0000-4000-8000-00000000000l"
	err := h.createOrganizationCR(context.Background(), order)
	if n := api.postCount(); n != 0 {
		t.Fatalf("the Organization was created during a catalog outage (%d POST, body %s): its plan is a guess", n, api.posts[0])
	}
	if err == nil {
		t.Fatalf("the order was acked during a catalog outage with no plan known: no Organization is ever created")
	}
}

// TestCreateOrganizationCR_CatalogAnswerStampsThePaidPlan is the control: the
// catalog answers, the UUID resolves, and the CR carries the paid plan.
func TestCreateOrganizationCR_CatalogAnswerStampsThePaidPlan(t *testing.T) {
	cat := catalogPlans(t, http.StatusOK)
	api := &fakeAPIServer{post: fakeAPIResponse{status: http.StatusCreated, body: `{}`}}
	h, _ := newOwnerTestHandler(api)
	h.CatalogURL = cat.URL

	order := acmeOrder("tenant-new")
	order.PlanID = "6f1c2b9e-0000-4000-8000-00000000000l"
	if err := h.createOrganizationCR(context.Background(), order); err != nil {
		t.Fatalf("create: %v", err)
	}
	if api.postCount() != 1 {
		t.Fatalf("POSTs = %d, want 1", api.postCount())
	}
	var cr struct {
		Spec struct {
			PlanSlug string `json:"planSlug"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(api.posts[0]), &cr); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if cr.Spec.PlanSlug != "l" {
		t.Fatalf("spec.planSlug = %q, want l", cr.Spec.PlanSlug)
	}
}
