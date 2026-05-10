package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteJSON_ErrorBody_IncludesStatusCode verifies that error responses
// (status >= 400) always include the numeric HTTP status code in the body
// under the keys `status` (int) and `code` (string token), so matrix tests
// and debuggers that scan the body for the literal status code can find it.
//
// This is the seam fix for qa-loop iter-16: 13+ FAILs ("missing ['403']" /
// "missing ['401']") that asserted the literal token in the body even though
// the HTTP STATUS CODE itself was correct.
func TestWriteJSON_ErrorBody_IncludesStatusCode(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		in      any
		wantStr string // status as string token, e.g. "403"
	}{
		{
			name:    "403 Forbidden — map[string]string",
			code:    http.StatusForbidden,
			in:      map[string]string{"error": "forbidden"},
			wantStr: "403",
		},
		{
			name:    "401 Unauthorized — map[string]string",
			code:    http.StatusUnauthorized,
			in:      map[string]string{"error": "unauthenticated"},
			wantStr: "401",
		},
		{
			name:    "404 Not Found — map[string]any",
			code:    http.StatusNotFound,
			in:      map[string]any{"error": "not found"},
			wantStr: "404",
		},
		{
			name:    "500 Internal — nil body",
			code:    http.StatusInternalServerError,
			in:      nil,
			wantStr: "500",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSON(rec, tc.code, tc.in)
			if rec.Code != tc.code {
				t.Fatalf("HTTP status: got %d, want %d", rec.Code, tc.code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v (raw=%q)", err, rec.Body.String())
			}
			gotCodeStr, _ := body["code"].(string)
			if gotCodeStr != tc.wantStr {
				t.Errorf("body[code] = %q, want %q (raw=%q)", gotCodeStr, tc.wantStr, rec.Body.String())
			}
			// status field is encoded as float64 by encoding/json
			gotStatus, ok := body["status"].(float64)
			if !ok || int(gotStatus) != tc.code {
				t.Errorf("body[status] = %v, want %d (raw=%q)", body["status"], tc.code, rec.Body.String())
			}
		})
	}
}

// TestWriteJSON_ErrorBody_PreservesExistingFields checks that the seam
// enrichment NEVER overwrites caller-supplied `status` / `code` / `error` /
// `detail` fields. Backward-compat is the contract.
func TestWriteJSON_ErrorBody_PreservesExistingFields(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusForbidden, map[string]any{
		"error":  "forbidden",
		"detail": "viewer cannot delete deployments",
		"status": 999, // caller intent wins
	})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := body["error"].(string); got != "forbidden" {
		t.Errorf("error preserved: got %q, want forbidden", got)
	}
	if got, _ := body["detail"].(string); got != "viewer cannot delete deployments" {
		t.Errorf("detail preserved: got %q", got)
	}
	if got, _ := body["status"].(float64); int(got) != 999 {
		t.Errorf("caller status overridden: got %v, want 999", body["status"])
	}
	// `code` was NOT supplied by the caller, so the seam should inject it.
	if got, _ := body["code"].(string); got != "403" {
		t.Errorf("code injected when absent: got %q, want 403", got)
	}
}

// TestWriteJSON_SuccessBody_NotEnriched verifies 2xx responses pass through
// untouched — domain payloads must not get HTTP fields injected.
func TestWriteJSON_SuccessBody_NotEnriched(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"hello": "world"})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := body["status"]; has {
		t.Errorf("2xx body must not be enriched with status field, got %v", body)
	}
	if _, has := body["code"]; has {
		t.Errorf("2xx body must not be enriched with code field, got %v", body)
	}
	if got, _ := body["hello"].(string); got != "world" {
		t.Errorf("payload preserved: got %v", body)
	}
}

// TestWriteJSON_ErrorBody_StructInput verifies the round-trip path used for
// struct inputs (catalyst-api uses a few typed response structs).
func TestWriteJSON_ErrorBody_StructInput(t *testing.T) {
	type errResp struct {
		Error  string `json:"error"`
		Detail string `json:"detail,omitempty"`
	}
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusForbidden, errResp{Error: "forbidden"})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := body["error"].(string); got != "forbidden" {
		t.Errorf("error preserved: got %q", got)
	}
	if got, _ := body["code"].(string); got != "403" {
		t.Errorf("code injected: got %q", got)
	}
	if got, _ := body["status"].(float64); int(got) != 403 {
		t.Errorf("status injected: got %v", body["status"])
	}
}
