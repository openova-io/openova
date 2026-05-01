package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── splitFQDN — pure parsing ──────────────────────────────────────────

func TestSplitFQDN(t *testing.T) {
	cases := []struct {
		in        string
		wantPool  string
		wantSub   string
		wantOk    bool
	}{
		{"omantel.omani.works", "omani.works", "omantel", true},
		{"acme.example.openova.io", "example.openova.io", "acme", true},
		{"OMANTEL.OMANI.WORKS", "omani.works", "omantel", true}, // normalised lower-case
		{" omantel.omani.works ", "omani.works", "omantel", true}, // trimmed
		{"openova.io", "io", "openova", true}, // 2-label is parseable; managed-domain check filters later
		{"single", "", "", false},
		{".leading-dot", "", "", false},
		{"trailing-dot.", "", "", false},
		{"", "", "", false},
		{"   ", "", "", false},
	}
	for _, c := range cases {
		gotPool, gotSub, gotOk := splitFQDN(c.in)
		if gotPool != c.wantPool || gotSub != c.wantSub || gotOk != c.wantOk {
			t.Errorf("splitFQDN(%q) = (%q, %q, %v); want (%q, %q, %v)",
				c.in, gotPool, gotSub, gotOk, c.wantPool, c.wantSub, c.wantOk)
		}
	}
}

// ── graceHoursFromEnv — env-var override ──────────────────────────────

func TestGraceHoursFromEnv_Default(t *testing.T) {
	t.Setenv("POOL_FORCE_RELEASE_GRACE_HOURS", "")
	if got := graceHoursFromEnv(); got != defaultForceReleaseGraceHours {
		t.Errorf("default = %d; want %d", got, defaultForceReleaseGraceHours)
	}
}

func TestGraceHoursFromEnv_Override(t *testing.T) {
	t.Setenv("POOL_FORCE_RELEASE_GRACE_HOURS", "168") // 7 days
	if got := graceHoursFromEnv(); got != 168 {
		t.Errorf("override = %d; want 168", got)
	}
}

func TestGraceHoursFromEnv_RejectsNegative(t *testing.T) {
	t.Setenv("POOL_FORCE_RELEASE_GRACE_HOURS", "-1")
	if got := graceHoursFromEnv(); got != defaultForceReleaseGraceHours {
		t.Errorf("negative = %d; want default %d", got, defaultForceReleaseGraceHours)
	}
}

func TestGraceHoursFromEnv_RejectsZero(t *testing.T) {
	t.Setenv("POOL_FORCE_RELEASE_GRACE_HOURS", "0")
	if got := graceHoursFromEnv(); got != defaultForceReleaseGraceHours {
		t.Errorf("zero = %d; want default %d", got, defaultForceReleaseGraceHours)
	}
}

func TestGraceHoursFromEnv_RejectsGarbage(t *testing.T) {
	t.Setenv("POOL_FORCE_RELEASE_GRACE_HOURS", "thirty-days")
	if got := graceHoursFromEnv(); got != defaultForceReleaseGraceHours {
		t.Errorf("garbage = %d; want default %d", got, defaultForceReleaseGraceHours)
	}
}

// ── ForceRelease — gates that don't need a live store ─────────────────

func newReleaseTestHandler() *Handler {
	return &Handler{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func doForceRelease(t *testing.T, h *Handler, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/force-release", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ForceRelease(rec, req)
	return rec
}

func TestForceRelease_RequiresConfirmHeader(t *testing.T) {
	h := newReleaseTestHandler()
	rec := doForceRelease(t, h, nil, ForceReleaseRequest{
		SovereignFQDN:  "ghost.omani.works",
		Reason:         "customer unreachable since 2026-03-15",
		GraceConfirmed: true,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (missing X-Force-Release-Confirm)", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "missing-confirm-header" {
		t.Errorf("error = %q; want missing-confirm-header", resp["error"])
	}
}

func TestForceRelease_RequiresReason(t *testing.T) {
	h := newReleaseTestHandler()
	rec := doForceRelease(t, h, map[string]string{"X-Force-Release-Confirm": "yes"}, ForceReleaseRequest{
		SovereignFQDN:  "ghost.omani.works",
		Reason:         "",
		GraceConfirmed: true,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (missing reason)", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "missing-reason" {
		t.Errorf("error = %q; want missing-reason", resp["error"])
	}
}

func TestForceRelease_RequiresGraceConfirmed(t *testing.T) {
	h := newReleaseTestHandler()
	rec := doForceRelease(t, h, map[string]string{"X-Force-Release-Confirm": "yes"}, ForceReleaseRequest{
		SovereignFQDN:  "ghost.omani.works",
		Reason:         "customer unreachable",
		GraceConfirmed: false,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (grace not confirmed)", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "grace-not-confirmed" {
		t.Errorf("error = %q; want grace-not-confirmed", resp["error"])
	}
}

func TestForceRelease_RejectsInvalidFQDN(t *testing.T) {
	h := newReleaseTestHandler()
	rec := doForceRelease(t, h, map[string]string{"X-Force-Release-Confirm": "yes"}, ForceReleaseRequest{
		SovereignFQDN:  "single",
		Reason:         "test",
		GraceConfirmed: true,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (invalid fqdn)", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "invalid-fqdn" {
		t.Errorf("error = %q; want invalid-fqdn", resp["error"])
	}
}

func TestForceRelease_RejectsUnmanagedPool(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "openova.io,omani.works")
	// Force the dynadot package to re-read the env on next IsManagedDomain
	// call. Since the dynadot package may cache, we accept whichever fall-
	// through it computes — the test only asserts on the response shape.
	h := newReleaseTestHandler()
	rec := doForceRelease(t, h, map[string]string{"X-Force-Release-Confirm": "yes"}, ForceReleaseRequest{
		SovereignFQDN:  "ghost.example.com", // example.com is NOT in the managed list
		Reason:         "test",
		GraceConfirmed: true,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (unmanaged pool)", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "unsupported-pool" {
		t.Errorf("error = %q; want unsupported-pool", resp["error"])
	}
}

// ── SovereignRelease — pure JSON / FQDN gates ─────────────────────────

func doSovereignRelease(t *testing.T, h *Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/release", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.SovereignRelease(rec, req)
	return rec
}

func TestSovereignRelease_RejectsInvalidJSON(t *testing.T) {
	h := newReleaseTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/release", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	h.SovereignRelease(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", rec.Code)
	}
}

func TestSovereignRelease_RejectsInvalidFQDN(t *testing.T) {
	h := newReleaseTestHandler()
	rec := doSovereignRelease(t, h, SovereignReleaseRequest{SovereignFQDN: "single"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422 (invalid fqdn)", rec.Code)
	}
}

func TestSovereignRelease_BYOReturnsNotFound(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "openova.io,omani.works")
	h := newReleaseTestHandler()
	// example.com is not in the managed list → BYO path → 404 with detail
	// indicating "no allocation to release".
	rec := doSovereignRelease(t, h, SovereignReleaseRequest{SovereignFQDN: "acme.example.com"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (BYO)", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp["detail"], "BYO") {
		t.Errorf("detail = %q; want BYO-shaped", resp["detail"])
	}
}

// ── DNS resolver swap — happy path only ───────────────────────────────

func TestDNSResolverSwap(t *testing.T) {
	original := dnsResolver
	defer func() { dnsResolver = original }()

	called := false
	dnsResolver = func(ctx context.Context, host string) ([]net.IP, error) {
		called = true
		return nil, errors.New("nxdomain")
	}
	if _, err := dnsResolver(context.Background(), "ghost.omani.works"); err == nil {
		t.Fatalf("expected error from stub resolver")
	}
	if !called {
		t.Fatalf("stub resolver was not invoked")
	}
}

// ── Time helper for completeness ──────────────────────────────────────

func TestSovereignRelease_RequestRoundTrip(t *testing.T) {
	// Marshal/unmarshal the request body shape so a future field
	// rename doesn't silently break the wire contract.
	in := SovereignReleaseRequest{SovereignFQDN: "omantel.omani.works"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SovereignReleaseRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SovereignFQDN != in.SovereignFQDN {
		t.Errorf("round-trip lost FQDN: %q -> %q", in.SovereignFQDN, out.SovereignFQDN)
	}
}

func TestForceReleaseRequest_RoundTrip(t *testing.T) {
	in := ForceReleaseRequest{
		SovereignFQDN:  "ghost.omani.works",
		Reason:         "customer unreachable since " + time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Format("2006-01-02"),
		GraceConfirmed: true,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ForceReleaseRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SovereignFQDN != in.SovereignFQDN || out.Reason != in.Reason || out.GraceConfirmed != in.GraceConfirmed {
		t.Errorf("round-trip lost data: %+v -> %+v", in, out)
	}
}

// Confirm the io import is used so the linter is happy in case the
// captureLogger path above changes.
var _ io.Reader = strings.NewReader("")
