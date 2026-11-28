package orgdelete

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func orgCR(name, tenantID string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("orgs.openova.io/v1")
	u.SetKind("Organization")
	u.SetName(name)
	if tenantID != "" {
		u.SetLabels(map[string]string{TenantIDLabel: tenantID})
	}
	return u
}

func fakeDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{organizations: "OrganizationList"}, objs...)
}

// writeJSON stands in for the handler's writer: status and body as given.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// platformCR is the Sovereign's own Organization (ensureSpineOrganization):
// labelled openova.io/scope: platform, a name that passes the slug check.
func platformCR(name, tenantID string) *unstructured.Unstructured {
	u := orgCR(name, tenantID)
	labels := u.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["openova.io/scope"] = "platform"
	u.SetLabels(labels)
	return u
}

// The id names a CR a tenant door created, by its tenant-id label or by its
// name; never a CR without a tenant id or of scope platform.
func TestNames(t *testing.T) {
	list := []unstructured.Unstructured{*orgCR("zeta", "t-1"), *orgCR("acme", "t-1"), *orgCR("other", "t-2"), *orgCR("t-1x", ""),
		*platformCR("hw292-omani-works", ""), *platformCR("plat", "t-p")}
	for _, c := range []struct {
		id   string
		want []string
	}{
		{"t-1", []string{"acme", "zeta"}},
		{" t-2 ", []string{"other"}},
		{"ACME", []string{"acme"}},
		{"t-3", nil},
		{"", nil},
		{"t-1x", nil},
		{"hw292-omani-works", nil},
		{"t-p", nil},
		{"plat", nil},
	} {
		if got := Names(list, c.id); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Names(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// Every answer of Unrecorded, each with what it deleted.
func TestUnrecorded(t *testing.T) {
	refused := apierrors.NewForbidden(schema.GroupResource{Group: "orgs.openova.io", Resource: "organizations"}, "acme", errors.New("RBAC: delete denied"))
	listDenied := func(text string) *dynamicfake.FakeDynamicClient {
		d := fakeDyn(orgCR("acme", "t-1"))
		d.PrependReactor("list", "organizations", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(text)
		})
		return d
	}
	long := strings.Repeat("x", 600)
	for _, c := range []struct {
		name      string
		id        string
		dyn       dynamic.Interface
		clientErr error
		delErr    error
		status    int
		errField  string
		detail    string
		deleted   []store.OrganizationProvisionRecord
	}{
		{name: "deletes the CR the tenant id names", id: "t-1", dyn: fakeDyn(orgCR("acme", "t-1"), orgCR("other", "t-2")),
			status: http.StatusNoContent, deleted: []store.OrganizationProvisionRecord{{OrganizationID: "t-1", Subdomain: "acme"}}},
		{name: "deletes the CR the slug names", id: "other", dyn: fakeDyn(orgCR("acme", "t-1"), orgCR("other", "t-2")),
			status: http.StatusNoContent, deleted: []store.OrganizationProvisionRecord{{OrganizationID: "other", Subdomain: "other"}}},
		{name: "no CR is named", id: "t-9", dyn: fakeDyn(orgCR("acme", "t-1")), status: http.StatusNotFound, errField: "org-tenant-not-found"},
		{name: "no in-cluster client", id: "t-1", clientErr: errors.New("in-cluster config unavailable"), status: http.StatusNotFound, errField: "org-tenant-not-found"},
		{name: "the Sovereign's own Organization is not named", id: "hw292-omani-works", dyn: fakeDyn(platformCR("hw292-omani-works", "")),
			status: http.StatusNotFound, errField: "org-tenant-not-found"},
		{name: "the list is refused", id: "t-1", dyn: listDenied("apiserver unavailable"), status: http.StatusBadGateway, errField: "organization-cr-read-failed",
			detail: "apiserver unavailable"},
		{name: "the list is refused with a long text", id: "t-1", dyn: listDenied(long), status: http.StatusBadGateway, errField: "organization-cr-read-failed",
			detail: long[:512]},
		{name: "the delete is refused", id: "t-1", dyn: fakeDyn(orgCR("acme", "t-1")), delErr: refused, status: http.StatusBadGateway, errField: "organization-cr-delete-failed",
			detail: refused.Error(), deleted: []store.OrganizationProvisionRecord{{OrganizationID: "t-1", Subdomain: "acme"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var deleted []store.OrganizationProvisionRecord
			del := func(_ context.Context, rec store.OrganizationProvisionRecord) error {
				deleted = append(deleted, rec)
				return c.delErr
			}
			client := func() (dynamic.Interface, error) { return c.dyn, c.clientErr }
			w := httptest.NewRecorder()
			Unrecorded(context.Background(), w, c.id, client, del, writeJSON)
			if w.Code != c.status {
				t.Fatalf("status %d, want %d (body %s)", w.Code, c.status, w.Body.String())
			}
			if !reflect.DeepEqual(deleted, c.deleted) {
				t.Fatalf("deleted %v, want %v", deleted, c.deleted)
			}
			if c.errField == "" {
				if w.Body.Len() != 0 {
					t.Fatalf("204 carries a body: %s", w.Body.String())
				}
				return
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", w.Body.String(), err)
			}
			if body["error"] != c.errField {
				t.Fatalf("error %q, want %q (body %v)", body["error"], c.errField, body)
			}
			if body["detail"] != c.detail {
				t.Fatalf("detail %q, want the apiserver's text %q", body["detail"], c.detail)
			}
		})
	}
}
