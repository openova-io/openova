// org_console_tls_net191_test.go — NET-191. The per-Org console listener
// pair that the catalyst-api door applies admits HTTPRoutes only from the
// Organization's own namespace and from catalyst-system, where the console
// route of the Organization lives; never from every namespace.

package handler

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// net191Admission is the allowedRoutes the pair must carry, written out in
// full: a selector on the built-in name label with exactly these two values in
// this order (the org-controller twin writes the same JSON).
func net191Admission(orgNS string) string {
	return `{"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
		`{"key":"kubernetes.io/metadata.name","operator":"In","values":["` + orgNS + `","catalyst-system"]}]}}}`
}

// appliedListenerAdmissions runs ensureOrgConsoleListener against a fake
// console Gateway and returns the allowedRoutes of each listener in the
// recorded server-side-apply body, as JSON ("absent" when it has none).
func appliedListenerAdmissions(t *testing.T, names orgConsoleTLSNames) map[string]string {
	t.Helper()
	dyn := fakeDynForConsoleTLS(t)
	// The fake dynamic client does not merge server-side apply (see
	// TestEnsureOrgConsoleListener_RidesApexPorts); the recorded patch body is
	// what the door sends to the Gateway.
	_ = ensureOrgConsoleListener(context.Background(), dyn, names, store.OrganizationProvisionRecord{})
	var patched []byte
	for _, a := range dyn.Actions() {
		if a.GetResource() == consoleGatewayGVR && a.GetVerb() == "patch" {
			if pa, ok := a.(interface{ GetPatch() []byte }); ok {
				patched = pa.GetPatch()
			}
		}
	}
	if len(patched) == 0 {
		t.Fatal("no server-side-apply patch recorded against the console gateway")
	}
	var body struct {
		Spec struct {
			Listeners []map[string]any `json:"listeners"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(patched, &body); err != nil {
		t.Fatalf("decode patch: %v\n%s", err, patched)
	}
	out := map[string]string{}
	for _, l := range body.Spec.Listeners {
		name, _ := l["name"].(string)
		ar, has := l["allowedRoutes"]
		if !has {
			out[name] = "absent"
			continue
		}
		b, err := json.Marshal(ar)
		if err != nil {
			t.Fatalf("marshal allowedRoutes of %s: %v", name, err)
		}
		out[name] = string(b)
	}
	return out
}

func TestEnsureOrgConsoleListener_NET191_PairAdmitsOnlyItsOrganization(t *testing.T) {
	internal := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "orgs.openova.io/v1alpha1", "kind": "Organization",
		"metadata": map[string]interface{}{"name": "finance"},
		"spec":     map[string]interface{}{"slug": "finance", "kind": "internal"},
	}}
	internalRec, ok := orgConsoleTLSRecordFromOrgCR(internal, "hw307.omani.works")
	if !ok {
		t.Fatal("orgConsoleTLSRecordFromOrgCR skipped the internal Organization")
	}
	cases := []struct {
		name  string
		rec   store.OrganizationProvisionRecord
		orgNS string
	}{
		{"BSS door record", store.OrganizationProvisionRecord{
			Subdomain: "nstar", DomainMode: store.OrganizationDomainFreeSubdomain, ParentDomain: "omani.homes",
		}, "nstar"},
		{"record of an internal Organization CR", internalRec, "finance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, ok := resolveOrgConsoleTLSNames(tc.rec)
			if !ok {
				t.Fatal("resolveOrgConsoleTLSNames returned !ok")
			}
			got := appliedListenerAdmissions(t, names)
			if len(got) != 2 {
				t.Fatalf("patch must carry exactly the per-Org pair, got %d listeners %v", len(got), got)
			}
			want := net191Admission(tc.orgNS)
			for _, n := range []string{"console-https-" + tc.orgNS, "console-http-" + tc.orgNS} {
				if got[n] != want {
					t.Errorf("%s allowedRoutes:\n got %s\nwant %s", n, got[n], want)
				}
			}
		})
	}
}
