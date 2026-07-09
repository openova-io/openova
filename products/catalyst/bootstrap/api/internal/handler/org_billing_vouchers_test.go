package handler

// #4914 — the catalyst-api console BSS voucher-issue proxy must enforce the
// #3376 DoD-6 voucher-code strength policy at the edge (reusing the shared
// core/services/shared/voucher validator), not only rely on the upstream
// billing service. These tests pin the three contract cases:
//
//   - weak CUSTOM code   → 4xx `voucher-code-too-weak`, upstream NOT hit.
//   - strong CUSTOM code → 200, forwarded to upstream verbatim.
//   - omitted code       → 200 (auto-generate path), forwarded untouched.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issueVoucherUpstream stands up a stub Organization gateway that records
// whether the issue endpoint was reached and echoes back a 200 with the
// forwarded body, so a test can assert both the status the FE sees AND
// whether the proxy short-circuited at the edge.
func issueVoucherUpstream(t *testing.T, hit *bool, gotBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/billing/vouchers/issue" {
			t.Errorf("upstream path = %q, want /api/billing/vouchers/issue", r.URL.Path)
		}
		*hit = true
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// Mirror the billing service's OK shape closely enough for the FE.
		_, _ = w.Write([]byte(`{"code":"OK","credit_omr":5,"active":true}`))
	}))
}

func TestHandleIssueOrgBillingVoucher_WeakCustomCodeRejectedAtEdge(t *testing.T) {
	var hit bool
	var gotBody string
	upstream := issueVoucherUpstream(t, &hit, &gotBody)
	defer upstream.Close()
	t.Setenv(orgGatewayURLEnv, upstream.URL)

	// 7 chars — below the 12-char floor → rejected before any upstream hop.
	h := &Handler{orgJWTSecret: orgBillingTestSecret}
	r := withSovereignAdminClaims(httptest.NewRequest(
		http.MethodPost, "/api/v1/org/billing/vouchers/issue",
		strings.NewReader(`{"code":"WEAK123","credit_omr":5}`)))
	w := httptest.NewRecorder()
	h.HandleIssueOrgBillingVoucher(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	// writeJSON enriches 4xx bodies with a numeric `status` field, so decode
	// into a loose map and assert the `error` discriminator.
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, w.Body.String())
	}
	if resp["error"] != "voucher-code-too-weak" {
		t.Errorf("error = %v, want voucher-code-too-weak", resp["error"])
	}
	if hit {
		t.Errorf("upstream was reached — weak code must be rejected at the console edge, not forwarded")
	}
}

func TestHandleIssueOrgBillingVoucher_LowEntropyCustomCodeRejectedAtEdge(t *testing.T) {
	var hit bool
	var gotBody string
	upstream := issueVoucherUpstream(t, &hit, &gotBody)
	defer upstream.Close()
	t.Setenv(orgGatewayURLEnv, upstream.URL)

	// 12 chars but a single repeated character → below the distinct-char
	// floor → rejected at the edge.
	h := &Handler{orgJWTSecret: orgBillingTestSecret}
	r := withSovereignAdminClaims(httptest.NewRequest(
		http.MethodPost, "/api/v1/org/billing/vouchers/issue",
		strings.NewReader(`{"code":"AAAAAAAAAAAA","credit_omr":5}`)))
	w := httptest.NewRecorder()
	h.HandleIssueOrgBillingVoucher(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if hit {
		t.Errorf("upstream reached — low-entropy code must be rejected at the edge")
	}
}

func TestHandleIssueOrgBillingVoucher_StrongCustomCodeForwarded(t *testing.T) {
	var hit bool
	var gotBody string
	upstream := issueVoucherUpstream(t, &hit, &gotBody)
	defer upstream.Close()
	t.Setenv(orgGatewayURLEnv, upstream.URL)

	// 15 chars, many distinct → clears the strength floor → forwarded.
	h := &Handler{orgJWTSecret: orgBillingTestSecret}
	r := withSovereignAdminClaims(httptest.NewRequest(
		http.MethodPost, "/api/v1/org/billing/vouchers/issue",
		strings.NewReader(`{"code":"STRONGVOUCHER42","credit_omr":5}`)))
	w := httptest.NewRecorder()
	h.HandleIssueOrgBillingVoucher(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !hit {
		t.Fatalf("upstream was NOT reached — a strong custom code must be forwarded")
	}
	if !strings.Contains(gotBody, "STRONGVOUCHER42") {
		t.Errorf("upstream body = %q, want it to carry the forwarded code STRONGVOUCHER42", gotBody)
	}
}

func TestHandleIssueOrgBillingVoucher_OmittedCodeAutoGenForwarded(t *testing.T) {
	var hit bool
	var gotBody string
	upstream := issueVoucherUpstream(t, &hit, &gotBody)
	defer upstream.Close()
	t.Setenv(orgGatewayURLEnv, upstream.URL)

	// No `code` field → the auto-generate path. The edge gate MUST NOT run;
	// the billing service mints a high-entropy code server-side.
	h := &Handler{orgJWTSecret: orgBillingTestSecret}
	r := withSovereignAdminClaims(httptest.NewRequest(
		http.MethodPost, "/api/v1/org/billing/vouchers/issue",
		strings.NewReader(`{"credit_omr":5}`)))
	w := httptest.NewRecorder()
	h.HandleIssueOrgBillingVoucher(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (auto-gen path); body=%s", w.Code, w.Body.String())
	}
	if !hit {
		t.Fatalf("upstream was NOT reached — the omitted-code auto-generate path must be forwarded")
	}
}
