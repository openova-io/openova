// continuum_dr_synthesis_5731_test.go — #5731 / #5728 regression guard.
//
// FOUR production `synthesize*` helpers fabricated DR state that nobody
// measured, on precisely the branches that fire when the cluster is
// unreachable or the Continuum CR is absent — i.e. the conditions under
// which a human reaches for failover:
//
//	synthesizedSwitchoverCompleted (continuum.go)        status:completed, duration 60s
//	synthesizedEnrichedContinuum   (continuum_extras.go) phase:Healthy, walLagSeconds:2
//	synthesizedFleetItem           (continuum_extras.go) Phase:Healthy, Healthy:true
//	synthesizedSwitchoverPreview   (continuum_extras.go) Promotable:true, BlockingChecks:[]
//
// They composed a FALSE-CONFIDENCE LOOP — preview said "safe to fail
// over" → switchover said "completed" → fleet said "healthy" → the
// enriched GET said "healthy, 2s lag". Each was fabricated
// independently and they AGREED with each other, so cross-checking one
// against another could not detect it. Hence one guard covering all
// four: fixing any subset leaves the loop intact.
//
// The rule under test: a synthesized/fallback response may never carry
// a health verdict, a lag figure, a duration, a completion status, or an
// empty blocking-checks list. If the state could not be read, say so.
//
// ── Guard vs control ────────────────────────────────────────────────
//
// The `_Honest*` tests are the GUARD: every one of them goes RED on the
// pre-fix tree (verified, output pasted in the PR).
//
// The `_Control*` tests are the CONTROL: they run the SAME handlers
// against a real Continuum CR and assert the real values still flow, so
// the fix cannot degrade into "always error". They are deliberately
// written to pass on BOTH trees — they accept any 2xx for the
// switchover rather than pinning 200-vs-202 — because a control that
// only passes after the fix is a second guard, not a control.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// registerContinuumHonestyRoutes wires every Continuum surface this
// guard exercises. The singular `/continuum/` aliases are the paths
// cmd/api/main.go registers and the UI + matrix runner hit.
func registerContinuumHonestyRoutes(r chi.Router, h *Handler) {
	r.Post("/api/v1/sovereigns/{id}/continuum/{name}/switchover", h.HandleContinuumSwitchoverRequest)
	r.Post("/api/v1/sovereigns/{id}/continuum/{name}/switchover/preview", h.HandleContinuumSwitchoverPreview)
	r.Get("/api/v1/sovereigns/{id}/continuum/{name}", h.HandleContinuumGetEnriched)
	r.Put("/api/v1/sovereigns/{id}/continuum/{name}", h.HandleContinuumPut)
	r.Get("/api/v1/fleet/continuum", h.HandleFleetContinuum)
}

func ownerCtxReq(req *http.Request) *http.Request {
	return req.WithContext(withClaims(req.Context(),
		&auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
}

// fleetProbe5731 — the fleet envelope decoded from the WIRE rather than
// through the handler's own Go type. Deliberate: this guard must
// COMPILE against the pre-fix tree so it can be run there and observed
// going red. Referencing `continuumFleetResponse.Unreachable` (a field
// the fix adds) would turn a red assertion into a build error, which
// proves nothing.
type fleetProbe5731 struct {
	Items []struct {
		Sovereign     string  `json:"sovereign"`
		Name          string  `json:"name"`
		Namespace     string  `json:"namespace"`
		PrimaryRegion string  `json:"primaryRegion"`
		Phase         string  `json:"phase"`
		WALLagSeconds float64 `json:"walLagSeconds"`
		Healthy       bool    `json:"healthy"`
	} `json:"items"`
	Total       int      `json:"total"`
	Unreachable []string `json:"unreachable"`
}

// forbiddenCompletionTokens — the literals a switchover response may
// carry ONLY when a promotion was actually observed. `60` is checked
// separately because it can legitimately appear inside a timestamp.
var forbiddenCompletionTokens = []string{
	"completed",
	"durationSeconds",
	"lastSwitchoverDuration",
	`"applied":true`,
}

func assertNoFabricatedCompletion(t *testing.T, label string, body []byte) {
	t.Helper()
	for _, tok := range forbiddenCompletionTokens {
		if bytes.Contains(body, []byte(tok)) {
			t.Errorf("%s: body reports %q for a switchover that was never attempted; got %s",
				label, tok, string(body))
		}
	}
}

// ── GUARD 1 — switchover against an UNREGISTERED deployment ─────────
//
// Pre-fix: HandleContinuumSwitchoverRequest called
// synthesizedSwitchoverCompleted() and answered 200 with
// status:"completed", duration 60s, fromRegion "fsn1".
func TestSwitchover_HonestWhenDeploymentUnregistered_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory()
	h.dynamicFactory = factory
	// NOTE: no installUserAccessDeployment — the id is unknown.

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/dep-never-registered/continuum/cont-x/switchover", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("unregistered deployment: got 2xx %d, want non-2xx — nothing was attempted; body=%s",
			rec.Code, rec.Body.String())
	}
	assertNoFabricatedCompletion(t, "unregistered deployment", rec.Body.Bytes())
	// The Hetzner/QA constants the synthesizer emitted from a Huawei
	// Sovereign.
	for _, leak := range []string{"fsn1", "hz-hel-rtz-prod", "qa-omantel"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Errorf("unregistered deployment: body leaked hardcoded %q; got %s",
				leak, rec.Body.String())
		}
	}
}

// ── GUARD 2 — switchover when the cluster client CANNOT BE BUILT ────
//
// This is the outage case: the control plane cannot reach the
// Sovereign. Pre-fix it was the second synthesizedSwitchoverCompleted()
// call site and returned 200 "completed".
func TestSwitchover_HonestWhenClientUnavailable_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-5731-noclient")

	body, _ := json.Marshal(continuumSwitchoverRequest{TargetRegion: "me-east-215-b"})
	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-x/switchover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("unreachable cluster: got 2xx %d, want non-2xx — no switchover was attempted; body=%s",
			rec.Code, rec.Body.String())
	}
	assertNoFabricatedCompletion(t, "unreachable cluster", rec.Body.Bytes())
	// A fabricated duration is the token TC-312's must_contain demanded.
	if bytes.Contains(rec.Body.Bytes(), []byte(`"duration":60`)) ||
		bytes.Contains(rec.Body.Bytes(), []byte(`"durationSeconds":60`)) {
		t.Errorf("unreachable cluster: body reports a 60s switchover duration; got %s",
			rec.Body.String())
	}
	for _, leak := range []string{"fsn1", "hz-hel-rtz-prod", "qa-omantel"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Errorf("unreachable cluster: body leaked hardcoded %q; got %s",
				leak, rec.Body.String())
		}
	}
}

// ── GUARD 3 — enriched GET with NO Continuum CR ─────────────────────
//
// Pre-fix: 200 OK with phase:"Healthy", walLagSeconds:2,
// lastSwitchoverDurationSeconds:45, currentPrimary:"fsn1" and
// dnsObservation:"lua:pdm-1.openova.io" — a healthy DR record for a
// Continuum that does not exist, naming another cloud's regions and the
// mothership, on a cut-over Sovereign.
func TestGetEnriched_HonestWhenCRAbsent_5728(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory() // cluster answers; no CR
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5728-nocr")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-absent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("absent CR: got 2xx %d, want non-2xx — there is no DR record to report; body=%s",
			rec.Code, rec.Body.String())
	}
	// No health verdict, no lag, no duration.
	for _, tok := range []string{"phase", "walLagSeconds", "lastSwitchoverDuration", "Healthy"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(tok)) {
			t.Errorf("absent CR: body carries %q for a Continuum that does not exist; got %s",
				tok, rec.Body.String())
		}
	}
	// No region string, and never the mothership hostname.
	for _, leak := range []string{"fsn1", "hz-hel-rtz-prod", "qa-omantel", "pdm-1.openova.io"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Errorf("absent CR: body leaked hardcoded %q; got %s", leak, rec.Body.String())
		}
	}
}

// ── GUARD 3b — enriched GET when the CLIENT cannot be built ─────────
func TestGetEnriched_HonestWhenClientUnavailable_5728(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-5728-noclient")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-x", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unreachable cluster: got %d, want 503 (distinct from 404 'no such Continuum' — "+
			"they imply different operator actions); body=%s", rec.Code, rec.Body.String())
	}
	for _, tok := range []string{"walLagSeconds", "Healthy", "fsn1", "pdm-1.openova.io"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(tok)) {
			t.Errorf("unreachable cluster: body carries %q; got %s", tok, rec.Body.String())
		}
	}
}

// ── GUARD 3c — PUT when the cluster could not be reached ────────────
//
// Pre-fix: enrichSynthesizedWithPut() echoed the caller's rpo/rto back
// at 200, so an operator was told their DR policy had been persisted
// when nothing had been written.
func TestPut_HonestWhenClientUnavailable_5728(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-5728-put")

	body := []byte(`{"spec":{"rpoSeconds":15,"rtoSeconds":45}}`)
	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-x", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("PUT against unreachable cluster: got 2xx %d, want non-2xx — nothing was persisted; body=%s",
			rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"rpoSeconds":15`)) {
		t.Errorf("PUT against unreachable cluster: echoed the requested policy back as if stored; got %s",
			rec.Body.String())
	}
}

// ── GUARD 4 — fleet listing when NO Continuum can be read ───────────
//
// The fleet page is what a sovereign-admin scans to find which
// Sovereign needs attention. Pre-fix it appended a synthesized
// `cont-omantel` row with Healthy:true whenever items was empty —
// including when every Sovereign was unreadable.
func TestFleetContinuum_HonestWhenUnreadable_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-5731-fleet")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/continuum", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("fleet status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetProbe5731
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Assert on the VALUE: a health verdict for a Continuum nobody read.
	for _, item := range resp.Items {
		if item.Healthy {
			t.Errorf("fleet row %q reports Healthy:true for a Continuum that could not be read; row=%+v",
				item.Name, item)
		}
		if item.Phase == "Healthy" {
			t.Errorf("fleet row %q reports Phase:Healthy without reading a CR; row=%+v",
				item.Name, item)
		}
	}
	if len(resp.Items) != 0 {
		t.Errorf("fleet returned %d row(s) with no readable Continuum anywhere; items=%+v",
			len(resp.Items), resp.Items)
	}
	// An empty list must not read as "no DR configured" — name what
	// could not be read.
	if len(resp.Unreachable) == 0 || !contains5731(resp.Unreachable, dep.ID) {
		t.Errorf("fleet must name the Sovereigns it could not read; unreachable=%v want to include %q",
			resp.Unreachable, dep.ID)
	}
	for _, leak := range []string{"fsn1", "qa-omantel", "cont-omantel"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Errorf("fleet body leaked hardcoded %q; got %s", leak, rec.Body.String())
		}
	}
}

// ── GUARD 5 — switchover PREVIEW when nothing could be checked ──────
//
// This is the PRE-FLIGHT SAFETY CHECK the confirm button is gated on.
// Pre-fix it answered Promotable:true with an EMPTY BlockingChecks list
// when it had run zero checks. An empty list is the dangerous value: it
// reads as "every precondition passed", so this asserts on the VALUE,
// not on the presence of the key.
func TestSwitchoverPreview_HonestWhenClientUnavailable_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-5731-preview-noclient")

	body, _ := json.Marshal(continuumSwitchoverPreviewRequest{TargetRegion: "me-east-215-b"})
	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-x/switchover/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	var resp continuumSwitchoverPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if resp.Promotable {
		t.Errorf("preview reports promotable:true having run zero checks (cluster unreachable); body=%s",
			rec.Body.String())
	}
	if len(resp.BlockingChecks) == 0 {
		t.Errorf("preview returned an EMPTY blockingChecks list having run zero checks — "+
			"an empty list reads as 'all preconditions passed'; body=%s", rec.Body.String())
	}
	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("preview: got 2xx %d, want non-2xx when no precondition could be checked; body=%s",
			rec.Code, rec.Body.String())
	}
	for _, leak := range []string{"fsn1", "hz-hel-rtz-prod", "qa-omantel"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Errorf("preview leaked hardcoded %q; got %s", leak, rec.Body.String())
		}
	}
}

// ── GUARD 5b — preview when the CR is absent ────────────────────────
func TestSwitchoverPreview_HonestWhenCRAbsent_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory() // cluster answers; no CR
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5731-preview-nocr")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-absent/switchover/preview", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	var resp continuumSwitchoverPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if resp.Promotable {
		t.Errorf("preview reports promotable:true with no Continuum CR to check; body=%s",
			rec.Body.String())
	}
	if len(resp.BlockingChecks) == 0 {
		t.Errorf("preview returned an EMPTY blockingChecks list with no CR to check; body=%s",
			rec.Body.String())
	}
	if resp.CurrentLagSec > 0 {
		t.Errorf("preview reports a %v s replication lag it never measured; body=%s",
			resp.CurrentLagSec, rec.Body.String())
	}
}

// ── GUARD 6 — the four must not AGREE on a fabricated green ─────────
//
// The composite assertion. On one unreachable Sovereign, walk the whole
// DR decision path in the order an operator would during an outage and
// assert that NOT ONE of the four steps returns an optimistic verdict.
// Pre-fix all four returned 200 and agreed: promotable → completed →
// healthy → healthy, which is why cross-checking could not detect it.
func TestDRDecisionPath_NoMutuallyConsistentFabrication_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-5731-loop")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, ownerCtxReq(req))
		return rec
	}

	base := "/api/v1/sovereigns/" + dep.ID + "/continuum/cont-x"
	steps := []struct {
		label string
		rec   *httptest.ResponseRecorder
	}{
		{"1. preview (safe to fail over?)", do(http.MethodPost, base+"/switchover/preview")},
		{"2. switchover (did it happen?)", do(http.MethodPost, base+"/switchover")},
		{"3. fleet view (is the estate fine?)", do(http.MethodGet, "/api/v1/fleet/continuum")},
		{"4. enriched GET (healthy replicas?)", do(http.MethodGet, base)},
	}

	optimistic := []string{
		`"promotable":true`,
		`"status":"completed"`,
		`"healthy":true`,
		`"phase":"Healthy"`,
	}
	for _, s := range steps {
		for _, tok := range optimistic {
			if bytes.Contains(s.rec.Body.Bytes(), []byte(tok)) {
				t.Errorf("%s answered %s on an UNREACHABLE Sovereign; body=%s",
					s.label, tok, s.rec.Body.String())
			}
		}
		if strings.Contains(s.rec.Body.String(), "fsn1") {
			t.Errorf("%s emitted a hardcoded Hetzner region; body=%s", s.label, s.rec.Body.String())
		}
	}
}

// ── CONTROL A — a genuine Continuum still returns its REAL values ───
//
// Green on BOTH trees: the CR exists, so the live path runs either way.
// The fixture deliberately uses `hz-fsn-rtz-prod`, which differs from
// the deleted `continuumDefaultPrimary` ("fsn1") — so this asserts the
// value came from the CR, not from a surviving constant.
func TestGetEnriched_ControlLiveCRStillReturnsRealValues_5728(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	_ = unstructured.SetNestedField(cr.Object, float64(7), "status", "walLagSeconds")
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5728-control")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-wp?namespace=acme", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("CONTROL: a live Continuum must still return 200; got %d body=%s",
			rec.Code, rec.Body.String())
	}
	var resp continuumEnrichedGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "dr-wp" {
		t.Errorf("CONTROL name: got %q want dr-wp", resp.Name)
	}
	if resp.PrimaryRegion != "hz-fsn-rtz-prod" {
		t.Errorf("CONTROL primaryRegion: got %q want hz-fsn-rtz-prod (read off the CR)", resp.PrimaryRegion)
	}
	if resp.WALLagSeconds != 7 {
		t.Errorf("CONTROL walLagSeconds: got %v want 7 (read off status, the fix must not blank real telemetry)",
			resp.WALLagSeconds)
	}
}

// ── CONTROL B — a genuine switchover still succeeds ─────────────────
//
// Green on BOTH trees: accepts any 2xx (the pre-fix tree answers 200,
// the fixed tree 202) and asserts the effect that actually matters —
// the CR was patched. Proves the fix did not degrade into "always
// error".
func TestSwitchover_ControlLiveCRStillSucceeds_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5731-control-sw")

	body, _ := json.Marshal(continuumSwitchoverRequest{TargetRegion: "hz-hel-rtz-prod"})
	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-wp/switchover?namespace=acme", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("CONTROL: a genuine switchover must still succeed; got %d body=%s",
			rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"applied":true`)) {
		t.Errorf("CONTROL: a genuine switchover must report applied:true; got %s", rec.Body.String())
	}
	got, err := client.Resource(ContinuumGVR()).Namespace("acme").
		Get(context.Background(), "dr-wp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CONTROL re-fetch: %v", err)
	}
	requested, _, _ := unstructured.NestedBool(got.Object, "spec", "switchover", "requested")
	if !requested {
		t.Error("CONTROL: spec.switchover.requested was not patched — the fix broke the real path")
	}
	target, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "targetRegion")
	if target != "hz-hel-rtz-prod" {
		t.Errorf("CONTROL: spec.switchover.targetRegion got %q want hz-hel-rtz-prod", target)
	}
}

// ── CONTROL C — the fleet still lists a real Continuum ──────────────
//
// Green on BOTH trees.
func TestFleetContinuum_ControlLiveCRStillListed_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	_ = installUserAccessDeployment(t, h, "dep-5731-control-fleet")

	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/continuum", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("CONTROL fleet status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetProbe5731
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Fatalf("CONTROL: a live Continuum must still be listed; body=%s", rec.Body.String())
	}
	var found bool
	for _, it := range resp.Items {
		if it.Name == "dr-wp" {
			found = true
			if it.PrimaryRegion != "hz-fsn-rtz-prod" {
				t.Errorf("CONTROL fleet primaryRegion: got %q want hz-fsn-rtz-prod (read off the CR)",
					it.PrimaryRegion)
			}
		}
	}
	if !found {
		t.Errorf("CONTROL: fleet did not list the live Continuum dr-wp; items=%+v", resp.Items)
	}
}

// ── CONTROL D — a live preview still runs its real checks ───────────
//
// Green on BOTH trees.
func TestSwitchoverPreview_ControlLiveCRStillPreviews_5731(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5731-control-preview")

	body, _ := json.Marshal(continuumSwitchoverPreviewRequest{TargetRegion: "hz-hel-rtz-prod"})
	r := chi.NewRouter()
	registerContinuumHonestyRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-wp/switchover/preview?namespace=acme",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, ownerCtxReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("CONTROL preview status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumSwitchoverPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CurrentPrimary != "hz-fsn-rtz-prod" {
		t.Errorf("CONTROL preview currentPrimary: got %q want hz-fsn-rtz-prod (read off the CR)",
			resp.CurrentPrimary)
	}
	if !resp.Promotable {
		t.Errorf("CONTROL: a healthy live pair must still preview as promotable; body=%s",
			rec.Body.String())
	}
}

func contains5731(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
