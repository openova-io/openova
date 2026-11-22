package orgend

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The two cancelled Organizations of 22 November 2026 had this shape: a CR the
// provisioning service minted (source tenant-created-event), no catalyst-api
// record, so the store handler answered 404 org-tenant-not-found.
func orgCR(name, source, tenantID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata": map[string]any{
			"name": name,
			"uid":  "uid-" + name,
			"labels": map[string]any{
				sourceLabel:   source,
				tenantIDLabel: tenantID,
			},
		},
		"spec": map[string]any{"slug": name},
	}}
}

func fakeDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{OrganizationGVR: "OrganizationList"}, objs...)
}

// storeOnly stands in for HandleDeleteOrganization on an id its store lacks.
type storeOnly struct{ calls int }

func (s *storeOnly) serve(w http.ResponseWriter, _ *http.Request) {
	s.calls++
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "org-tenant-not-found"})
}

func del(t *testing.T, dyn dynamic.Interface, next *storeOnly, id string) *httptest.ResponseRecorder {
	t.Helper()
	h := Delete(next.serve, func() (dynamic.Interface, error) { return dyn, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := chi.NewRouter()
	r.Delete("/api/v1/organizations/{id}", h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/organizations/"+id, nil))
	return w
}

func exists(t *testing.T, dyn dynamic.Interface, name string) bool {
	t.Helper()
	_, err := dyn.Resource(OrganizationGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get %s: %v", name, err)
	}
	return err == nil
}

func deletes(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "delete" {
			n++
		}
	}
	return n
}

// On main the platform's DELETE answered 404 for a marketplace Organization and
// removed nothing; it now deletes that CR (by tenant id and by slug), leaves an
// Organization with a similar name and the sovereign-admin door's CR alone, and
// a second call changes nothing.
func TestDelete_EndsTheMarketplaceOrganizationTheStoreDoesNotHold(t *testing.T) {
	for _, id := range []string{"b1d057b1-ed32-4143-9c23-fea4ecaa33ee", "g01-w3ojsz0a"} {
		t.Run(id, func(t *testing.T) {
			dyn := fakeDyn(
				orgCR("g01-w3ojsz0a", marketplaceSource, "b1d057b1-ed32-4143-9c23-fea4ecaa33ee"),
				orgCR("g01-w3ojsz0a-2", marketplaceSource, "4ef578b9-c346-4a9e-a8e1-fadd78d0df3d"),
				orgCR("g01-w3ojsz0", "org-tenant-funnel", "t-g01-w3ojsz0"),
			)
			next := &storeOnly{}
			if w := del(t, dyn, next, id); w.Code != http.StatusNoContent {
				t.Fatalf("DELETE %s: want 204, got %d %s", id, w.Code, w.Body.String())
			}
			if exists(t, dyn, "g01-w3ojsz0a") {
				t.Fatalf("DELETE %s answered but the Organization CR survives: its removal never starts", id)
			}
			for _, other := range []string{"g01-w3ojsz0a-2", "g01-w3ojsz0"} {
				if !exists(t, dyn, other) {
					t.Fatalf("DELETE %s removed another Organization, %s", id, other)
				}
			}
			if next.calls != 0 {
				t.Fatalf("the store handler was asked for a marketplace Organization (it answers 404)")
			}
			// The fake apiserver has no finalizer, so the CR is gone at once and
			// the id names nothing any more: the store handler answers 404.
			// (While the removal runs the CR is marked for deletion and the
			// answer is 204: TestDelete_RefusesAmbiguityAndReportsARefusedDelete.)
			before := deletes(dyn)
			if w := del(t, dyn, next, id); w.Code != http.StatusNotFound {
				t.Fatalf("second DELETE %s: want 404 for an Organization that is gone, got %d %s", id, w.Code, w.Body.String())
			}
			if deletes(dyn) != before || !exists(t, dyn, "g01-w3ojsz0a-2") || !exists(t, dyn, "g01-w3ojsz0") {
				t.Fatalf("a second DELETE %s changed something", id)
			}
		})
	}
}

// Every id that names no marketplace Organization goes to the store handler
// unchanged: the sovereign-admin door's own CR, an unknown id, no cluster.
func TestDelete_EveryOtherIDGoesToTheStoreHandler(t *testing.T) {
	dyn := fakeDyn(orgCR("acme", "org-tenant-funnel", "t-acme"))
	for _, id := range []string{"t-acme", "acme", "nobody", "Not_A_Slug!"} {
		next := &storeOnly{}
		if w := del(t, dyn, next, id); w.Code != http.StatusNotFound || next.calls != 1 {
			t.Fatalf("DELETE %s: want the store handler's 404, got %d (store calls %d)", id, w.Code, next.calls)
		}
	}
	if deletes(dyn) != 0 || !exists(t, dyn, "acme") {
		t.Fatalf("a DELETE of a sovereign-admin door Organization bypassed its store handler")
	}
	next := &storeOnly{}
	h := Delete(next.serve, func() (dynamic.Interface, error) { return nil, errors.New("not in a cluster") }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := chi.NewRouter()
	r.Delete("/api/v1/organizations/{id}", h)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/v1/organizations/acme", nil))
	if next.calls != 1 {
		t.Fatalf("without a cluster client the store handler must serve the DELETE")
	}
}

// Nothing is deleted on a guess, a refused delete is the answer and not a log
// line, and an Organization already ending is not deleted again.
func TestDelete_RefusesAmbiguityAndReportsARefusedDelete(t *testing.T) {
	dyn := fakeDyn(orgCR("dup-a", marketplaceSource, "tid-dup"), orgCR("dup-b", marketplaceSource, "tid-dup"))
	if w := del(t, dyn, &storeOnly{}, "tid-dup"); w.Code != http.StatusConflict {
		t.Fatalf("two CRs with one tenant id: want 409, got %d", w.Code)
	}
	if deletes(dyn) != 0 {
		t.Fatalf("an ambiguous tenant id deleted an Organization")
	}

	dyn = fakeDyn(orgCR("held", marketplaceSource, "tid-held"))
	dyn.PrependReactor("delete", "organizations", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "orgs.openova.io", Resource: "organizations"}, "held", errors.New("no grant"))
	})
	if w := del(t, dyn, &storeOnly{}, "tid-held"); w.Code != http.StatusBadGateway {
		t.Fatalf("a refused delete: want 502, got %d %s", w.Code, w.Body.String())
	}

	ending := orgCR("ending", marketplaceSource, "tid-ending")
	now := metav1.Now()
	ending.SetDeletionTimestamp(&now)
	dyn = fakeDyn(ending)
	if w := del(t, dyn, &storeOnly{}, "tid-ending"); w.Code != http.StatusNoContent {
		t.Fatalf("an Organization already ending: want 204, got %d", w.Code)
	}
	if deletes(dyn) != 0 {
		t.Fatalf("an Organization already ending was deleted again")
	}
}
