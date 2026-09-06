package huawei

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Every kind the collector lists must be reachable through SupportedKinds().
// Two places used to hold a hand-written copy of this list — the deletion
// sweep and the "nothing listed" check — and both silently broke when kinds
// were added (#6853). This pins the single-source property.
func TestSupportedKindsCoversEveryLister(t *testing.T) {
	if len(SupportedKinds()) != len(kindListers) {
		t.Fatalf("SupportedKinds()=%d but %d listers registered", len(SupportedKinds()), len(kindListers))
	}
	seen := map[string]bool{}
	for _, k := range SupportedKinds() {
		if seen[k] {
			t.Fatalf("duplicate kind %q in the registry", k)
		}
		seen[k] = true
	}
	// The original five must never be dropped by a refactor.
	for _, k := range []string{KindECS, KindEVS, KindEIP, KindELB, KindNAT} {
		if !seen[k] {
			t.Fatalf("kind %q disappeared from the registry", k)
		}
	}
	// And the extended set must be present, or coverage silently regresses.
	for _, k := range []string{KindRDS, KindDDS, KindGaussDB, KindCBR, KindCCE,
		KindVPC, KindDNS, KindWAF, KindIMS, KindAS, KindVPCEP} {
		if !seen[k] {
			t.Fatalf("extended kind %q is not registered — resources of that kind bill zero", k)
		}
	}
}

// When EVERY kind fails the source must surface the error, whatever the kind
// count is. The old `len(failed) == 5` silently reported a source with dead
// credentials as healthy once more kinds existed — it stopped billing and
// nothing looked wrong.
func TestTotalRejectionIsDetectedWhateverTheKindCount(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error_code":"APIGW.0301","error_msg":"Incorrect IAM authentication information"}`))
	})
	_, failed := client.ListAll(context.Background(),
		Credentials{AccessKey: "a", SecretKey: "b", ProjectID: "pid"}, "r")

	if len(failed) != len(SupportedKinds()) {
		t.Fatalf("failed=%d, registry=%d — the 'nothing listed' comparison would not fire",
			len(failed), len(SupportedKinds()))
	}
	for _, err := range failed {
		if !strings.Contains(err.Error(), "APIGW.0301") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
