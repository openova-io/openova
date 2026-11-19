package retrycall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// TestLogRecordsOrNot: a zero Log never recorded (nil), a started one holds
// an empty list, Add appends one entry for a target and none for "", and a
// Log that never recorded records from its first entry.
func TestLogRecordsOrNot(t *testing.T) {
	var never Log
	if never.List() != nil {
		t.Fatalf("a zero Log must list nil, got %#v", never.List())
	}
	var started Log
	started.Start()
	if got := started.List(); got == nil || len(got) != 0 {
		t.Fatalf("a started Log must list an empty list, got %#v", got)
	}
	if started.Add(RouteReconciler, ReconcilerTarget("suspend", "HelmRelease", "flux-system", "bp-x"), "op") || len(started.List()) != 0 {
		t.Fatalf("a suspend is not a retry call, got %#v", started.List())
	}
	if !started.Add(RoutePhase, "phase tofu-apply", "op") || len(started.List()) != 1 {
		t.Fatalf("one retry call must be one entry, got %#v", started.List())
	}
	if !never.Add(RouteJob, "job install-x", "op") || len(never.List()) != 1 {
		t.Fatalf("a Log that never recorded must record its first entry, got %#v", never.List())
	}
	if raw, _ := json.Marshal(store.Record{}); !strings.Contains(string(raw), `"retryCalls":null`) {
		t.Fatalf("a record without a list must write retryCalls null, got %s", raw)
	}
}

// TestUnionKeepsEveryEntryOnce: the union holds each entry once, oldest
// first, and is nil only when both lists are nil.
func TestUnionKeepsEveryEntryOnce(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)
	a := store.RetryCall{At: t0, Route: RoutePhase, Target: "phase tofu-apply", By: "op"}
	b := store.RetryCall{At: t0.Add(time.Minute), Route: RouteJob, Target: "job install-x", By: "op"}
	if Union(nil, nil) != nil {
		t.Fatalf("the union of two lists never recorded must stay nil")
	}
	if got := Union(nil, []store.RetryCall{}); got == nil || len(got) != 0 {
		t.Fatalf("the union with an empty list must be empty, not nil, got %#v", got)
	}
	got := Union([]store.RetryCall{b, a}, []store.RetryCall{a})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("want [a b], got %#v", got)
	}
}

// TestMergeOnlyParam: Merge adds what the held Log lacks, gives the record
// the union, and reports whether the import asks for the list only.
func TestMergeOnlyParam(t *testing.T) {
	var held Log
	held.Start()
	held.Add(RouteJob, "job install-x", "op")
	rec := store.Record{RetryCalls: []store.RetryCall{{At: time.Now().UTC(), Route: RoutePhase, Target: "phase cilium", By: "op"}}}
	if held.Merge(httptest.NewRequest(http.MethodPost, "/import", nil), &rec) {
		t.Fatalf("an import without %s must not be list-only", OnlyParam)
	}
	if len(held.List()) != 2 || len(rec.RetryCalls) != 2 {
		t.Fatalf("want 2 entries on both sides, got %d and %d", len(held.List()), len(rec.RetryCalls))
	}
	if !held.Merge(httptest.NewRequest(http.MethodPost, "/import?"+OnlyParam+"=true", nil), &rec) || len(held.List()) != 2 {
		t.Fatalf("a list-only import of entries already held must add none, got %#v", held.List())
	}
}

func TestCaller(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if got := Caller(r); got != "operator" {
		t.Errorf("no session: got %q", got)
	}
	r.Header.Set("X-User-Email", "header@example.org")
	if got := Caller(r); got != "header@example.org" {
		t.Errorf("header only: got %q", got)
	}
	r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{Sub: "sub-1"}))
	if got := Caller(r); got != "sub-1" {
		t.Errorf("claims with a subject: got %q", got)
	}
	r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{Sub: "sub-1", Email: "owner@example.org"}))
	if got := Caller(r); got != "owner@example.org" {
		t.Errorf("claims with an email: got %q", got)
	}
}

// TestForward: the parent forwards only a record whose hand-over fired, that
// names a Sovereign, from an engine that is not that Sovereign.
func TestForward(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fired := time.Now().UTC()
	save := func(id, fqdn string, handedOver bool) {
		rec := store.Record{ID: id, Status: "ready", Request: store.RedactedRequest{SovereignFQDN: fqdn}, Result: &provisioner.Result{}}
		if handedOver {
			rec.Result.HandoverFiredAt = &fired
		}
		if err := st.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	save("before", "t99.omani.works", false)
	save("after", "t99.omani.works", true)
	save("nameless", "", true)
	wiped := store.Record{ID: "wiped", Status: "wiped", Request: store.RedactedRequest{SovereignFQDN: "t99.omani.works"},
		Result: &provisioner.Result{HandoverFiredAt: &fired}, RetryCalls: []store.RetryCall{{At: fired, Route: RoutePhase, Target: "phase x", By: "op"}}}
	if err := st.Save(wiped); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOVEREIGN_FQDN", "")
	if _, _, _, ok := Forward(nil, "after"); ok {
		t.Errorf("no store: forwarded")
	}
	for _, id := range []string{"before", "nameless", "unknown", "wiped"} {
		if _, _, _, ok := Forward(st, id); ok {
			t.Errorf("%s: forwarded", id)
		}
	}
	url, body, n, ok := Forward(st, "after")
	if !ok || n != 0 || url != "https://api.t99.omani.works/api/v1/internal/deployments/import?"+OnlyParam+"=true" || !strings.Contains(string(body), `"id":"after"`) {
		t.Errorf("after the hand-over: got %q %q %d %v", url, body, n, ok)
	}
	wiped.ID, wiped.Status = "listed", "ready"
	if err := st.Save(wiped); err != nil {
		t.Fatal(err)
	}
	if _, _, n, ok := Forward(st, "listed"); !ok || n != 1 {
		t.Errorf("a record that lists one entry: got n=%d ok=%v", n, ok)
	}
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	if _, _, _, ok := Forward(st, "after"); ok {
		t.Errorf("the Sovereign itself forwarded to itself")
	}
}

// TestDeliver: Deliver sends until the Sovereign took every entry and counts
// them, tries again after the export budget ran out, stops on a refusal with
// the entries unconfirmed, sends nothing once every entry is confirmed, runs
// one round at a time, and sends a call recorded during a round in the next.
func TestDeliver(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOVEREIGN_FQDN", "")
	fired := time.Now().UTC()
	var l Log
	l.Start()
	l.Add(RoutePhase, "phase tofu-apply", "op")
	save := func() {
		if err := st.Save(store.Record{ID: "dep", Status: "ready", Request: store.RedactedRequest{SovereignFQDN: "t99.omani.works"},
			Result: &provisioner.Result{HandoverFiredAt: &fired}, RetryCalls: l.List(), RetryCallsSent: l.Sent()}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	posts := 0
	deliver := func(post func() (bool, bool)) {
		posts = 0
		l.Deliver(st, "dep", func(string, []byte) (bool, bool) { posts++; return post() }, save)
	}

	deliver(func() (bool, bool) { return false, false }) // a 4xx
	if posts != 1 || l.Sent() != 0 {
		t.Fatalf("a refusal: posts=%d sent=%d, want 1 and 0", posts, l.Sent())
	}
	deliver(func() (bool, bool) { return posts == 3, posts < 3 }) // the budget runs out twice
	if rec, _ := st.Load("dep"); posts != 3 || l.Sent() != 1 || rec.RetryCallsSent != 1 {
		t.Fatalf("after two expired budgets: posts=%d sent=%d saved=%d, want 3, 1 and 1", posts, l.Sent(), rec.RetryCallsSent)
	}
	deliver(func() (bool, bool) { return true, false })
	if posts != 0 {
		t.Fatalf("every entry confirmed: %d posts, want none", posts)
	}

	l.Add(RouteJob, "job a: b", "op")
	save()
	deliver(func() (bool, bool) {
		if posts == 1 {
			l.Add(RouteJob, "job a: c", "op")
			save()
			l.Deliver(st, "dep", func(string, []byte) (bool, bool) { t.Error("a second round ran during a round"); return false, false }, save)
		}
		return true, false
	})
	if posts != 2 || l.Sent() != 3 {
		t.Fatalf("a call recorded during a round: posts=%d sent=%d, want 2 and 3", posts, l.Sent())
	}
}
