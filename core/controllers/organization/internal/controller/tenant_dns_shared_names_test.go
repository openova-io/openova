package controller

import (
	"context"
	"slices"
	"testing"

	"github.com/go-logr/logr"

	"github.com/openova-io/openova/core/controllers/pkg/orgdns"
)

// TestReconcileTenantDNS_WritesTheSharedNames asserts the writer and the wipe
// of the parent's engine name the same record sets: the controller writes, and
// deletes, exactly orgdns.RecordSetNames of the Organization, the function the
// wipe (products/catalyst/bootstrap/api/internal/handler/wipe_org_dns.go)
// releases them with.
func TestReconcileTenantDNS_WritesTheSharedNames(t *testing.T) {
	t.Parallel()
	org := orgWithTenantPublic("omani.homes", "shop")
	want := orgdns.RecordSetNames(tenantDNSOrg(org))
	if len(want) != 2 {
		t.Fatalf("orgdns.RecordSetNames = %q, want two names", want)
	}

	for _, step := range []string{"write", "teardown"} {
		var cap capturedPatch
		srv := newPDNSStub(t, &cap)
		r := &Reconciler{
			Log:                 logr.Discard(),
			PoolPowerDNSURL:     srv.URL,
			PoolPowerDNSAPIKey:  "central-pool-key",
			TenantConsoleLBIPv4: "212.72.24.33",
		}
		var err error
		if step == "write" {
			_, err = r.reconcileTenantDNS(context.Background(), org)
		} else {
			_, err = r.teardownTenantDNS(context.Background(), org)
		}
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		rrsets, _ := cap.body["rrsets"].([]any)
		got := make([]string, 0, len(rrsets))
		for _, rs := range rrsets {
			got = append(got, rs.(map[string]any)["name"].(string))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s names = %q, want orgdns.RecordSetNames = %q", step, got, want)
		}
	}
}
