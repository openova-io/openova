package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// hw307 2026-11-06: the Topology tab asks for `dr-bp-cnpg-pair`; the live CR
// is cnpg-pair-bp-cnpg-pair-continuum (ns cnpg) with applicationRef
// catalyst-platform, so the lookup fell to source:"pending" and the DR panel +
// switchover never rendered. It is resolved by its blueprint label.
func TestReplicationStatus_ResolvesByBlueprintLabel_hw307(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("cnpg-pair-bp-cnpg-pair-continuum", "cnpg",
		"catalyst-platform", "hw-me-east-215-a-rtz-prod", []string{"hw-me-east-215-b-rtz-prod"})
	cr.SetLabels(map[string]string{"catalyst.openova.io/blueprint": "bp-cnpg-pair"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetContinuumClock(func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) })
	dep := installUserAccessDeployment(t, h, "dep-hw307-dr")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/"+dep.ID+"/continuum/dr-bp-cnpg-pair/replication-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp continuumReplicationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "live" || resp.Continuum != "cnpg-pair-bp-cnpg-pair-continuum" || resp.Namespace != "cnpg" {
		t.Fatalf("got source=%q continuum=%q ns=%q, want live / cnpg-pair-bp-cnpg-pair-continuum / cnpg", resp.Source, resp.Continuum, resp.Namespace)
	}
}

// Two CRs carrying the same blueprint label is ambiguous: never guess.
func TestFindContinuumByBlueprintLabel_AmbiguousReturnsNil(t *testing.T) {
	a := newContinuumUnstructured("x", "ns1", "other", "r1", nil)
	a.SetLabels(map[string]string{"catalyst.openova.io/blueprint": "bp-dup"})
	b := newContinuumUnstructured("y", "ns2", "other", "r1", nil)
	b.SetLabels(map[string]string{"catalyst.openova.io/blueprint": "bp-dup"})
	_, client := fakeContinuumDynamicFactory(a, b)
	if got := findContinuumCRByApplicationRef(t.Context(), client, "bp-dup"); got != nil {
		t.Fatalf("ambiguous blueprint label resolved to %s, want nil", got.GetName())
	}
}
