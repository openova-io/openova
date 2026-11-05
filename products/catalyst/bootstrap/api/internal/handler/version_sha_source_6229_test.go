package handler

// #6229 — /version must report the sha of the IMAGE, and say where it came from.
//
// Measured on the mothership 2026-08-13: catalyst-api was rolled from 6749aff
// to 4ebf34f with `kubectl set image`, and /api/v1/version kept answering
// sha=7468a69 — a May commit — because the handler preferred the Deployment's
// CATALYST_BUILD_SHA env literal over the binary's own ldflag, and a roll
// replaces the image but never the env. The same response carried
// buildTimeSource=process-start, which proved no ldflag was being stamped at
// all, so even the fallback could not have told the truth.
//
// Two things change, each pinned here:
//   - precedence: a STAMPED ldflag wins over the env literal, because only the
//     ldflag describes the image that is actually running;
//   - provenance: `shaSource` names which source produced the value, the
//     sibling of buildTimeSource (#5821), so an env-sourced sha is visibly
//     unable to answer "does this env carry fix X".

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getVersion(t *testing.T) VersionResponse {
	t.Helper()
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	h.HandleVersion(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got VersionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// A stamped ldflag beats the Deployment env literal — the roll case.
func TestVersion_StampedLdflagWinsOverStaleEnv(t *testing.T) {
	orig := buildSHA
	buildSHA = "4ebf34f" // what the Containerfile stamps for the image
	t.Cleanup(func() { buildSHA = orig })
	t.Setenv("CATALYST_BUILD_SHA", "7468a69") // the chart literal a roll never touches

	got := getVersion(t)
	if got.SHA != "4ebf34f" || got.GitSha != "4ebf34f" {
		t.Fatalf("sha = %q / gitSha = %q, want the ldflag 4ebf34f — the env literal describes the "+
			"Deployment template, not the image that is running (#6229)", got.SHA, got.GitSha)
	}
	if got.ShaSource != shaSourceLdflag {
		t.Fatalf("shaSource = %q, want %q", got.ShaSource, shaSourceLdflag)
	}
}

// An unstamped binary (ldflag left at the compiled-in "dev") still falls back
// to the env literal — and SAYS so.
func TestVersion_UnstampedBinaryFallsBackToEnvAndLabelsIt(t *testing.T) {
	orig := buildSHA
	buildSHA = defaultBuildSHA
	t.Cleanup(func() { buildSHA = orig })
	t.Setenv("CATALYST_BUILD_SHA", "77b1489")

	got := getVersion(t)
	if got.SHA != "77b1489" {
		t.Fatalf("sha = %q, want the env fallback 77b1489", got.SHA)
	}
	if got.ShaSource != shaSourceEnv {
		t.Fatalf("shaSource = %q, want %q — an env-sourced sha must be self-labelling, "+
			"otherwise it is indistinguishable from a stamped one (#6229)", got.ShaSource, shaSourceEnv)
	}
}

// Neither source set → the compiled-in default, labelled as such.
func TestVersion_NoSourceReportsDefault(t *testing.T) {
	orig := buildSHA
	buildSHA = defaultBuildSHA
	t.Cleanup(func() { buildSHA = orig })
	t.Setenv("CATALYST_BUILD_SHA", "")

	got := getVersion(t)
	if got.SHA != defaultBuildSHA {
		t.Fatalf("sha = %q, want %q", got.SHA, defaultBuildSHA)
	}
	if got.ShaSource != shaSourceDefault {
		t.Fatalf("shaSource = %q, want %q", got.ShaSource, shaSourceDefault)
	}
}

// The wire key must be present even when the value is the default, so a
// dashboard can pin against it — and the sha fields the matrix already
// asserts (sha, gitSha, buildTimeSource) stay in place as a control.
func TestVersion_ShaSourceIsAWireKey(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	h.HandleVersion(w, req)
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"sha", "gitSha", "buildTime", "buildTimeSource", "shaSource"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("wire key %q missing from %v", k, raw)
		}
	}
}

// resolveBuildSHA is the pure core — pin whitespace + the "dev" sentinel.
func TestResolveBuildSHA_Table(t *testing.T) {
	cases := []struct {
		ldflag, env, wantSha, wantSrc string
	}{
		{"abc1234", "def5678", "abc1234", shaSourceLdflag},
		{" abc1234\n", "def5678", "abc1234", shaSourceLdflag},
		{"dev", "def5678", "def5678", shaSourceEnv},
		{"", " def5678 ", "def5678", shaSourceEnv},
		{"", "", "dev", shaSourceDefault},
		{"dev", "   ", "dev", shaSourceDefault},
	}
	for _, c := range cases {
		sha, src := resolveBuildSHA(c.ldflag, c.env)
		if sha != c.wantSha || src != c.wantSrc {
			t.Errorf("resolveBuildSHA(%q,%q) = (%q,%q), want (%q,%q)",
				c.ldflag, c.env, sha, src, c.wantSha, c.wantSrc)
		}
	}
}
