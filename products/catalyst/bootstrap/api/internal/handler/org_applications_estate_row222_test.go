// org_applications_estate_row222_test.go — UAT row 222 (Refs #3988).
//
// Clause: "The agent-created application converges and appears in the user's
// Org (chat-driven app creation works end-to-end)."
//
// The Org-scoped estate this exercises is the surface that clause names.
// `create_application` (products/openova-mcp/internal/tools/catalogue.go:373)
// POSTs to catalyst-api, which writes an Application CR into the Org namespace
// (applications.go:547) — and GET /api/v1/org/applications is the ONLY reader
// that can see it. Its two sibling passes are keyed on HelmReleases and on
// per-org-app HTTPRoutes; neither can ever surface an Application CR.
//
// # The defect
//
// That pass discarded the List error at the `if`, so a failed List (Application
// CRD unregistered, RBAC denial on apps.openova.io, apiserver timeout) produced
// a 200 whose `apps` was assembled from the other two passes alone. The grid
// rendered plausibly and merely lacked the agent's Application — which is
// byte-identical, on the wire and on screen, to "the agent's app never
// converged". That is the exact conflation #6249/#6251 named and fixed on the
// Sovereign estate (sovereign.go:786); the Org-scoped handler was never
// enrolled, and it is the one this row walks.
//
// # Why the remedy differs from the Sovereign path's
//
// There, the fix SUPPRESSES the HelmRelease fallback once the Application
// estate is authoritative. Here that would be wrong: an Org legitimately owns
// HR-only apps with no Application CR (agenity-demo) and funnel purchases that
// deploy neither a CR nor an HR. Suppressing those would blank a correct
// estate. So the failure is REPORTED rather than compensated — every pass still
// runs, and the response states plainly that the Application-keyed half is
// missing.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

var errForbiddenRow222 = errors.New(
	"applications.apps.openova.io is forbidden: User cannot list resource in the namespace")

const (
	row222Host  = "console.demo.omani.homes"
	row222OrgNS = "org-7283eb4a-19e5-4e86-9066-d4aa26762064"
)

// row222AgentApp is the Application CR an agent-driven create_application
// leaves behind: written into the Org namespace, blueprintRef + environmentRef
// set, phase Ready once the application-controller has converged it.
func row222AgentApp(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(row222OrgNS)
	u.SetName(name)
	u.Object["spec"] = map[string]interface{}{
		"environmentRef": "demo-prod",
		"blueprintRef": map[string]interface{}{
			"name":    "bp-wordpress",
			"version": "0.4.23",
		},
	}
	u.Object["status"] = map[string]interface{}{"phase": "Ready"}
	return u
}

// row222Handler wires the Org-applications handler onto a fake dynamic client.
// `breakApplicationList` installs a reactor that fails the Application LIST the
// same way an unregistered CRD or an RBAC denial would.
func row222Handler(t *testing.T, breakApplicationList bool, objs ...*unstructured.Unstructured) *Handler {
	t.Helper()
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:                  row222Host,
		TenantID:              "7283eb4a-19e5-4e86-9066-d4aa26762064",
		TenantKind:            store.TenantKindOrg,
		KeycloakRealmURL:      "https://keycloak.demo.omani.homes/realms/org-demo",
		KeycloakClientID:      "catalyst-ui",
		OrganizationNamespace: row222OrgNS,
		OrgKeycloakRealmName:  "org-demo",
	}); err != nil {
		t.Fatalf("put demo: %v", err)
	}
	h.SetTenantRegistry(reg)

	gvrToList := map[schema.GroupVersionResource]string{
		applicationGVR:       "ApplicationList",
		helmReleaseGVR:       "HelmReleaseList",
		httpRouteGVR:         "HTTPRouteList",
		fluxKustomizationGVR: "KustomizationList",
	}
	runtimeObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		runtimeObjs = append(runtimeObjs, o)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), gvrToList, runtimeObjs...)

	if breakApplicationList {
		dyn.PrependReactor("list", "applications",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(
					schema.GroupResource{Group: "apps.openova.io", Resource: "applications"},
					"", errForbiddenRow222)
			})
	}
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
	})
	return h
}

func row222Get(t *testing.T, h *Handler) (int, sovereignAppsResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/applications", nil)
	req.Header.Set("X-Tenant-Host", row222Host)
	req = req.WithContext(context.Background())
	rec := httptest.NewRecorder()
	h.HandleOrgApplications(rec, req)

	var resp sovereignAppsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, resp
}

// TestRow222_AgentApplicationAppearsInTheOrgEstate — the positive half: a
// converged Application CR in the Org namespace IS the Org's estate.
func TestRow222_AgentApplicationAppearsInTheOrgEstate(t *testing.T) {
	h := row222Handler(t, false, row222AgentApp("agent-shop"))

	code, resp := row222Get(t, h)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.ApplicationEstateUnreadable {
		t.Error("applicationEstateUnreadable set on a healthy List — the flag would " +
			"cry wolf on every normal read")
	}

	var found *sovereignAppItem
	for i := range resp.Apps {
		if resp.Apps[i].ID == "agent-shop" {
			found = &resp.Apps[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the agent-created Application is absent from the Org estate: %+v", resp.Apps)
	}
	if found.Status != "installed" {
		t.Errorf("status = %q, want installed for a phase=Ready Application", found.Status)
	}
	if found.Blueprint != "bp-wordpress" {
		t.Errorf("blueprint = %q, want bp-wordpress", found.Blueprint)
	}
}

// TestRow222_UnreadableApplicationEstateIsReported — the defect's own shape.
//
// The List fails, so `apps` CANNOT contain the agent's Application. Before the
// fix the response was an unremarkable 200 and the absence read as "the app
// never converged". Now the response says the Application-keyed half is
// missing, so the two situations are distinguishable.
func TestRow222_UnreadableApplicationEstateIsReported(t *testing.T) {
	h := row222Handler(t, true, row222AgentApp("agent-shop"))

	code, resp := row222Get(t, h)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the other estate passes still serve)", code)
	}
	if !resp.ApplicationEstateUnreadable {
		t.Fatal("the Application CR List FAILED and the response did not say so — " +
			"an operator reading this estate cannot tell \"this Org has no " +
			"Applications\" from \"we could not ask\", which is precisely the " +
			"conclusion UAT row 222 has to adjudicate")
	}
	// The absence itself is the premise of the test, not the assertion — state
	// it so a future reader knows the flag is describing a real omission.
	for _, a := range resp.Apps {
		if a.ID == "agent-shop" {
			t.Fatal("an Application surfaced despite a failed List — the fixture no " +
				"longer reproduces the situation the flag describes")
		}
	}
}

// TestRow222_UnreadableEstateStillServesTheOtherPasses is the CONTROL that
// shares the suspect property: the same failed Application List, but the Org
// also owns an HR-only app.
//
// It pins the deliberate divergence from the Sovereign path. Had this been
// fixed by copying #6251's remedy — skip the HelmRelease pass — an Org whose
// Agenity workspace is HR-only would have lost its only card at exactly the
// moment the Application estate was unreadable. That would trade one silent
// wrong answer for another.
func TestRow222_UnreadableEstateStillServesTheOtherPasses(t *testing.T) {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace(row222OrgNS)
	hr.SetName("agenity-demo")
	hr.Object["spec"] = map[string]interface{}{
		"releaseName": "agenity-demo",
		"chart": map[string]interface{}{
			"spec": map[string]interface{}{"chart": "bp-agenity"},
		},
	}

	h := row222Handler(t, true, hr)

	code, resp := row222Get(t, h)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !resp.ApplicationEstateUnreadable {
		t.Error("the failed Application List must still be reported")
	}
	var sawAgenity bool
	for _, a := range resp.Apps {
		if a.ID == "agenity-demo" {
			sawAgenity = true
		}
	}
	if !sawAgenity {
		t.Fatal("the HelmRelease-only app vanished when the Application List failed — " +
			"reporting the gap must not also blank the estate")
	}
}
