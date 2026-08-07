package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #5821 — /version must say WHERE buildTime came from.
//
// buildTime falls back to the process start time when neither the ldflag nor
// CATALYST_BUILD_TIME is set, and the key stays named `buildTime`. The response
// is then indistinguishable from a genuinely fresh build.
//
// Measured on hw292 2026-08-07: /version returned buildTime 2026-08-07T12:39:41Z
// on a binary whose SHA (fad88bd) was built five days earlier. Read at face
// value that says every fix merged that morning was already live. It was not —
// the pod had merely restarted. The UAT ledger's whole
// deploy-gated-vs-code-blocked split turns on that question, so a field that can
// be off by days without saying so is worse than one that is absent.
//
// The fallback itself is NOT the defect and is not removed: process start dates
// the current pod, which is how a restart loop gets spotted. The defect was the
// fallback being unlabelled.

func decodeVersion(t *testing.T) VersionResponse {
	t.Helper()
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.HandleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/version returned %d, want 200", rec.Code)
	}
	var got VersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /version: %v (body %s)", err, rec.Body.String())
	}
	return got
}

func TestVersion_BuildTimeSourceNamesTheFallback(t *testing.T) {
	// No env, and the ldflag is empty in a test binary → the fallback path.
	got := decodeVersion(t)

	if got.BuildTime == "" {
		t.Fatal("buildTime is empty — the endpoint must always return a parseable timestamp")
	}
	if got.BuildTimeSource != "process-start" {
		t.Fatalf("buildTimeSource = %q, want %q — with no ldflag and no env the timestamp "+
			"is the process start, and a caller that cannot tell will read a restarted pod "+
			"as a freshly built one (#5821)", got.BuildTimeSource, "process-start")
	}
	// The fallback VALUE is still the process start, not something invented.
	if got.BuildTime != processStartTime {
		t.Fatalf("buildTime = %q, want the process start %q — the fallback must report the "+
			"real start, not a synthesised timestamp", got.BuildTime, processStartTime)
	}
}

func TestVersion_BuildTimeSourceNamesTheEnvOverride(t *testing.T) {
	t.Setenv("CATALYST_BUILD_TIME", "2026-08-02T04:00:00Z")

	got := decodeVersion(t)
	if got.BuildTime != "2026-08-02T04:00:00Z" {
		t.Fatalf("buildTime = %q, want the env value", got.BuildTime)
	}
	if got.BuildTimeSource != "env" {
		t.Fatalf("buildTimeSource = %q, want %q", got.BuildTimeSource, "env")
	}
}

// Whitespace-only env must NOT count as a real value. This is the shape that
// makes the field lie most convincingly: the chart templates the env var, the
// value renders empty with a trailing newline, and an un-trimmed check reports
// source="env" for a timestamp that is blank or whitespace.
func TestVersion_BlankEnvFallsThroughAndSaysSo(t *testing.T) {
	t.Setenv("CATALYST_BUILD_TIME", "   ")

	got := decodeVersion(t)
	if got.BuildTimeSource != "process-start" {
		t.Fatalf("buildTimeSource = %q with a whitespace-only CATALYST_BUILD_TIME, want %q — "+
			"a blank env value must fall through, not be reported as a real build timestamp",
			got.BuildTimeSource, "process-start")
	}
	if got.BuildTime != processStartTime {
		t.Fatalf("buildTime = %q, want the process start %q", got.BuildTime, processStartTime)
	}
}

// The key must be present on the wire under its exact name. Callers (the UAT
// classifier, monitoring) read `buildTimeSource` verbatim; no omitempty, and
// never absent — an absent key is exactly the ambiguity this fixes.
func TestVersion_BuildTimeSourceKeyIsAlwaysOnTheWire(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.HandleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, present := m["buildTimeSource"]
	if !present {
		t.Fatalf(`/version carries no "buildTimeSource" key — a reader is back to guessing `+
			`whether buildTime is a link time or a restart time: %s`, rec.Body.String())
	}
	if s, _ := v.(string); s == "" {
		t.Fatalf(`"buildTimeSource" is empty — omitempty was added, or the resolution left it `+
			`unset; either way the key conveys nothing: %s`, rec.Body.String())
	}

	// Control: the pre-existing contract keys are untouched. version_test.go
	// owns that contract, but asserting it here too means a careless edit to
	// this response struct cannot pass on THIS file alone.
	for _, k := range []string{"service", "sha", "gitSha", "version", "buildTime", "go"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("/version lost the pre-existing key %q — removing keys is a contract break", k)
		}
	}
}
