package controller

// A cancelled marketplace tenant's Organization ENDS: on
// provision.tenant_removed the bridge deletes the Organization CR minted for
// that tenant, stamped with the cancellation instant, and never touches
// another Organization (organization_end.go).
//
// On main nothing consumed provision.tenant_removed and nothing deleted the
// CR: the CR stayed Ready with its finalizer 17 hours after the cancellation
// (two-region environment, 22 Nov 2026). These tests do not compile there.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

func endingFixture(t *testing.T, funcs interceptor.Funcs, orgs ...*orgapi.Organization) (*NATSBridge, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := orgapi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := make([]client.Object, 0, len(orgs))
	for _, o := range orgs {
		objs = append(objs, o)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&orgapi.Organization{}).
		WithObjects(objs...).
		WithInterceptorFuncs(funcs).
		Build()
	return &NATSBridge{Client: cl, Log: testr.New(t)}, cl
}

func mintedOrg(slug, source, tenantID string) *orgapi.Organization {
	o := &orgapi.Organization{}
	o.SetName(slug)
	o.Spec.Slug = slug
	o.SetLabels(map[string]string{marketplaceSourceLabel: source, tenantIDLabel: tenantID})
	o.SetFinalizers([]string{TenantNetworkingFinalizer})
	return o
}

func tenantRemoved(t *testing.T, id, slug string, endedAt time.Time) *natsbus.Event {
	t.Helper()
	data, err := json.Marshal(map[string]string{"id": id, "slug": slug, "ended_at": endedAt.UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	return &natsbus.Event{ID: "ev-" + slug, Type: "provision.tenant_removed", Source: "provisioning", TenantID: id, Timestamp: endedAt.Add(3 * time.Second), Data: data}
}

func getOrg(t *testing.T, c client.Client, name string) *orgapi.Organization {
	t.Helper()
	var o orgapi.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &o); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return &o
}

func TestHandleTenantRemoved_EndsTheOrganizationMintedForTheTenant(t *testing.T) {
	ctx := context.Background()
	b, cl := endingFixture(t, interceptor.Funcs{},
		mintedOrg("g01-cel26uza", marketplaceSource, "4ef578b9"),
		// Another Organization whose name starts with the same characters.
		mintedOrg("g01-cel26uza-2", marketplaceSource, "09e427af"))
	cancelled := time.Date(2026, 11, 22, 1, 15, 45, 0, time.UTC)

	if err := b.HandleTenantRemoved(ctx, tenantRemoved(t, "4ef578b9", "g01-cel26uza", cancelled)); err != nil {
		t.Fatalf("HandleTenantRemoved: %v", err)
	}
	ended := getOrg(t, cl, "g01-cel26uza")
	if ended.DeletionTimestamp.IsZero() {
		t.Fatal("the Organization of the cancelled tenant was not deleted: it keeps running in every region and keeps being metered")
	}
	if got := ended.Annotations[EndedAtAnnotation]; got != cancelled.Format(time.RFC3339) {
		t.Errorf("%s = %q, want the cancellation instant %s", EndedAtAnnotation, got, cancelled.Format(time.RFC3339))
	}
	sib := getOrg(t, cl, "g01-cel26uza-2")
	if !sib.DeletionTimestamp.IsZero() || sib.Annotations[EndedAtAnnotation] != "" {
		t.Fatal("another Organization with a similar name was ended")
	}

	// A redelivery (and the controller's own tenant.deleted echo coming back
	// as a second tenant_removed) changes nothing.
	rv := ended.ResourceVersion
	if err := b.HandleTenantRemoved(ctx, tenantRemoved(t, "4ef578b9", "g01-cel26uza", cancelled.Add(17*time.Hour))); err != nil {
		t.Fatalf("second run: %v", err)
	}
	again := getOrg(t, cl, "g01-cel26uza")
	if again.ResourceVersion != rv || again.Annotations[EndedAtAnnotation] != cancelled.Format(time.RFC3339) {
		t.Errorf("second run changed the Organization (rv %s -> %s, ended-at %q)", rv, again.ResourceVersion, again.Annotations[EndedAtAnnotation])
	}
}

func TestHandleTenantRemoved_NeverEndsAnotherOrganization(t *testing.T) {
	cases := []struct {
		name string
		org  *orgapi.Organization
		id   string
	}{
		{name: "same slug minted for another tenant", org: mintedOrg("acme-x", marketplaceSource, "tenant-9"), id: "tenant-1"},
		{name: "minted by the sovereign-admin door", org: mintedOrg("acme-x", "org-tenant-funnel", "tenant-1"), id: "tenant-1"},
		{name: "the controller's own tenant.deleted echo (id is the slug)", org: mintedOrg("acme-x", marketplaceSource, "tenant-1"), id: "acme-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, cl := endingFixture(t, interceptor.Funcs{}, tc.org)
			if err := b.HandleTenantRemoved(context.Background(), tenantRemoved(t, tc.id, "acme-x", time.Now())); err != nil {
				t.Fatal(err)
			}
			o := getOrg(t, cl, "acme-x")
			if !o.DeletionTimestamp.IsZero() || o.Annotations[EndedAtAnnotation] != "" {
				t.Fatal("an Organization that was not minted for the removed tenant was ended")
			}
		})
	}
}

func TestHandleTenantRemoved_AbsentIsDone(t *testing.T) {
	b, _ := endingFixture(t, interceptor.Funcs{})
	if err := b.HandleTenantRemoved(context.Background(), tenantRemoved(t, "tenant-1", "gone-org", time.Now())); err != nil {
		t.Fatalf("an absent Organization must be acknowledged, got %v", err)
	}
}

func TestHandleTenantRemoved_RefusedDeleteIsNamedAndRedelivered(t *testing.T) {
	deny := interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*orgapi.Organization); ok {
				return apierrors.NewForbidden(schema.GroupResource{Group: "orgs.openova.io", Resource: "organizations"}, obj.GetName(),
					errFake("serviceaccount cannot delete organizations"))
			}
			return c.Delete(ctx, obj, opts...)
		},
	}
	b, cl := endingFixture(t, deny, mintedOrg("acme", marketplaceSource, "tenant-1"))
	if err := b.HandleTenantRemoved(context.Background(), tenantRemoved(t, "tenant-1", "acme", time.Now())); err == nil {
		t.Fatal("a refused delete was acknowledged: nothing would retry it")
	}
	o := getOrg(t, cl, "acme")
	var found bool
	for _, c := range o.Status.Conditions {
		if c.Type == OrganizationRemovedCondition && c.Status == "False" && c.Reason == RemovalIncompleteReason && strings.Contains(c.Message, "forbidden") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s/%s condition names the refused delete; conditions=%+v", OrganizationRemovedCondition, RemovalIncompleteReason, o.Status.Conditions)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
