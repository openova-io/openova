package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleVersion_AlwaysJSON pins the wire shape — keys MUST stay
// stable so the QA matrix + monitoring dashboards keep working across
// releases.
func TestHandleVersion_AlwaysJSON(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	h.HandleVersion(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var resp VersionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\nbody=%s", err, w.Body.String())
	}
	if resp.Service != "catalyst-api" {
		t.Errorf("Service = %q, want catalyst-api", resp.Service)
	}
	// SHA + Version + Go must always be non-empty (defaults are "dev"
	// / "0.0.0" / runtime.Version()) so dashboards never see blanks.
	if resp.SHA == "" {
		t.Errorf("SHA must be non-empty (default: dev)")
	}
	if resp.Version == "" {
		t.Errorf("Version must be non-empty (default: 0.0.0)")
	}
	if resp.Go == "" {
		t.Errorf("Go must be non-empty (runtime.Version())")
	}
}

// TestHandleVersion_EnvOverride asserts the env-var truth override
// works — the chart can inject CATALYST_BUILD_SHA via the deployment
// template and that value wins over the ldflag default.
func TestHandleVersion_EnvOverride(t *testing.T) {
	t.Setenv("CATALYST_BUILD_SHA", "abc1234")
	t.Setenv("CATALYST_BUILD_VERSION", "1.2.3")
	t.Setenv("CATALYST_CHART_VERSION", "0.5.0")

	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	h.HandleVersion(w, r)

	var resp VersionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.SHA != "abc1234" {
		t.Errorf("SHA = %q, want abc1234 (env override)", resp.SHA)
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3 (env override)", resp.Version)
	}
	if resp.ChartVersion != "0.5.0" {
		t.Errorf("ChartVersion = %q, want 0.5.0 (env override)", resp.ChartVersion)
	}
}

// TestHandleVersion_TrimsWhitespace covers the trailing-newline case
// — env values injected via downward API frequently arrive with
// trailing whitespace; the handler must trim before reporting.
func TestHandleVersion_TrimsWhitespace(t *testing.T) {
	t.Setenv("CATALYST_BUILD_SHA", "  cafef00d\n")

	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	h.HandleVersion(w, r)

	var resp VersionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.SHA != "cafef00d" {
		t.Errorf("SHA = %q, want cafef00d (trimmed)", resp.SHA)
	}
}
