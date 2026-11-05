// Package handler — version.go: build-info endpoint.
//
// QA-loop iter-2 Fix #17. The matrix expects /api/v1/version to return
// a JSON document carrying the running image's git SHA + chart
// version, so operators (and the QA matrix) have a single
// non-authenticated probe to confirm "what version is live right now"
// without curling /healthz (plain "ok") or scraping the Pod spec.
//
// This is the canonical pattern for every long-running daemon at
// OpenOva: every service exposes a small /version document under its
// own API root with no auth gate. The body fields are stable across
// releases — adding new keys is fine, removing keys is a contract
// break tested by version_test.go.
//
// The implementation prefers, in order:
//
//  1. The buildSHA ldflag (`go build -ldflags="-X .../handler.buildSHA=$SHA"`,
//     stamped by the Containerfile from the BUILD_SHA build-arg the
//     catalyst-build workflow passes). The ldflag describes THE BINARY,
//     so it is the truth about the image that is running (#6229).
//  2. CATALYST_BUILD_SHA env var — a literal the chart template carries
//     and CI bumps in lockstep with the image tag. It is a property of
//     the Deployment, not of the image: a `kubectl set image` roll
//     replaces the image and leaves the env untouched, so an env-first
//     answer kept reporting a May commit on an August image (measured on
//     the mothership 2026-08-13, #6229). It remains as the fallback for
//     binaries built without the ldflag.
//  3. CATALYST_BUILD_VERSION env var (semver — set by chart values
//     when a tagged release is rolled out), else the buildVersion ldflag.
//  4. The literal "dev" / "0.0.0" — surfaced when neither env nor
//     ldflags carries a value, so the response is always well-formed
//     instead of empty.
//
// Every freshness field names its provenance (`shaSource`,
// `buildTimeSource`) so a caller can tell a value that describes the
// image from one that describes the Deployment or the pod's own boot.
//
// The handler is registered on the public router (no session gate) so
// CI smoke tests + external monitoring can probe it without a cookie.
// It carries no privileged data.
package handler

import (
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// buildSHA / buildVersion / chartVersion are populated by the build
// pipeline via -ldflags. Default values keep the response well-formed
// when running from `go run` during development.
//
// CI invocation:
//
//	go build -ldflags="\
//	  -X github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler.buildSHA=$GITHUB_SHA \
//	  -X github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler.buildVersion=$VERSION \
//	"
var (
	buildSHA     = "dev"
	buildVersion = "0.0.0"
	chartVersion = ""
	// buildTime — RFC3339 UTC timestamp of when the binary was linked.
	// CI sets this via -ldflags="-X .../handler.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)".
	// When the ldflag is unset the variable stays empty and the
	// handler falls back to processStartTime (set at package init)
	// so /version always returns a non-empty buildTime — the QA
	// matrix (TC-014) requires the key to be present + parseable.
	buildTime = ""
)

// processStartTime — captured once at package init so a /version
// caller without ldflag-injected buildTime still gets a stable
// timestamp instead of a per-request `time.Now()` (which would make
// scrape-based monitoring see the field flap every poll).
var processStartTime = time.Now().UTC().Format(time.RFC3339)

// VersionResponse — wire shape of GET /api/v1/version.
//
// The fields mirror what `kubectl get pod -o jsonpath` would tell an
// operator: the git SHA the image was built from, the semver tag, the
// chart version that mounted this Pod, and the Go runtime version.
//
// Adding new keys is non-breaking; existing keys MUST keep their JSON
// names so dashboards + the QA matrix can pin against them.
type VersionResponse struct {
	// Service — fixed identifier of which OpenOva service is responding.
	// Lets a single dashboard probe multiple /version endpoints across
	// services (catalyst-api, catalyst-application-controller, …) and
	// disambiguate them by `service`.
	Service string `json:"service"`

	// SHA — git commit the image was built from. Truthful resolution:
	// buildSHA ldflag > CATALYST_BUILD_SHA env var > "dev" (#6229).
	// Retained as the legacy key; new callers SHOULD prefer GitSha
	// (added qa-loop iter-6 to match the QA matrix TC-014 contract).
	SHA string `json:"sha"`

	// GitSha — alias of SHA stamped under the canonical
	// camelCase-with-explicit-vcs name. Always equals SHA; emitted as a
	// separate field rather than a remap so existing dashboards keyed
	// on "sha" keep working in lockstep with new callers keyed on
	// "gitSha". Required by qa-loop iter-6 TC-014.
	GitSha string `json:"gitSha"`

	// Version — semver of the release. Truthful resolution:
	// CATALYST_BUILD_VERSION env var > buildVersion ldflag > "0.0.0".
	Version string `json:"version"`

	// ChartVersion — the umbrella chart version (bp-catalyst-platform
	// or catalyst chart) responsible for this rollout. Set by the
	// chart's deployment template via env if known.
	ChartVersion string `json:"chartVersion,omitempty"`

	// BuildTime — RFC3339 UTC timestamp the binary was linked.
	// Resolution order: CATALYST_BUILD_TIME env > buildTime ldflag >
	// processStartTime (computed once at package init). Always
	// present + parseable per qa-loop iter-6 TC-014.
	BuildTime string `json:"buildTime"`

	// Go — runtime version (debug aid; e.g. "go1.26.0"). Useful when
	// chasing a regression that correlates with a Go upgrade.
	Go string `json:"go"`

	// BuildTimeSource — where BuildTime actually came from: "ldflag",
	// "env", or "process-start" (#5821).
	//
	// WHY THIS FIELD EXISTS. BuildTime silently falls back to the process
	// start time when neither the ldflag nor CATALYST_BUILD_TIME is set. The
	// key is still named `buildTime`, so the response is indistinguishable
	// from a genuinely fresh build — and the whole point of this endpoint is
	// answering "what version is live right now".
	//
	// Measured on hw292 2026-08-07: /version returned
	// buildTime 2026-08-07T12:39:41Z on a binary whose SHA (fad88bd) was built
	// five days earlier. Read at face value that says every fix merged that
	// morning was already live. It was not — the pod had merely restarted.
	// The UAT ledger's entire deploy-gated-vs-code-blocked split turns on
	// exactly this question, so a field that can be off by days without
	// saying so is worse than one that is absent.
	//
	// Reported rather than suppressed: process start is genuinely useful (it
	// dates the current pod, which is how you spot a restart loop). The defect
	// was never the fallback — it was the fallback being unlabelled.
	BuildTimeSource string `json:"buildTimeSource"`

	// ShaSource — where SHA actually came from: "ldflag" (stamped into
	// the binary at link time — describes the image), "env" (the chart's
	// CATALYST_BUILD_SHA literal — describes the Deployment template,
	// which a `kubectl set image` roll never updates) or "default" (the
	// compiled-in "dev"). The sibling of BuildTimeSource (#5821) for the
	// sha: a walker asking "does this env carry fix X" must be able to
	// see that an env-sourced answer cannot answer that question (#6229).
	ShaSource string `json:"shaSource"`
}

// shaSourceLdflag / shaSourceEnv / shaSourceDefault — the closed
// vocabulary of VersionResponse.ShaSource.
const (
	shaSourceLdflag  = "ldflag"
	shaSourceEnv     = "env"
	shaSourceDefault = "default"
)

// defaultBuildSHA is the compiled-in placeholder an unstamped binary
// carries. resolveBuildSHA treats it as "no ldflag".
const defaultBuildSHA = "dev"

// resolveBuildSHA returns the sha to report and which source produced
// it. ldflag is the linker-stamped value (buildSHA), env the trimmed
// CATALYST_BUILD_SHA. The ldflag wins whenever it is stamped, because it
// is the only one of the two that describes the running image (#6229).
func resolveBuildSHA(ldflag, env string) (sha, source string) {
	if v := strings.TrimSpace(ldflag); v != "" && v != defaultBuildSHA {
		return v, shaSourceLdflag
	}
	if v := strings.TrimSpace(env); v != "" {
		return v, shaSourceEnv
	}
	return defaultBuildSHA, shaSourceDefault
}

// HandleVersion — GET /api/v1/version
//
// Returns the running build's SHA + version. Always 200, always
// JSON. No auth gate (probe-friendly).
func (h *Handler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	sha, shaSource := resolveBuildSHA(buildSHA, os.Getenv("CATALYST_BUILD_SHA"))

	// Resolve BuildTime and record WHICH source won, so a caller can tell a
	// real link timestamp from the process-start fallback (#5821).
	bt := strings.TrimSpace(os.Getenv("CATALYST_BUILD_TIME"))
	btSource := "env"
	if bt == "" {
		bt, btSource = strings.TrimSpace(buildTime), "ldflag"
	}
	if bt == "" {
		bt, btSource = processStartTime, "process-start"
	}
	resp := VersionResponse{
		Service:         "catalyst-api",
		SHA:             sha,
		GitSha:          sha,
		Version:         envOrTrim("CATALYST_BUILD_VERSION", buildVersion),
		ChartVersion:    envOrTrim("CATALYST_CHART_VERSION", chartVersion),
		BuildTime:       bt,
		BuildTimeSource: btSource,
		ShaSource:       shaSource,
		Go:              runtime.Version(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// envOrTrim returns the trimmed value of `env` when non-empty, else
// `fallback`. Distinct from the marketplace_settings.go envOr helper
// (which returns the raw env value) because version-handler callers
// rely on trimming — env values injected via the K8s downward API
// frequently arrive with trailing newlines that would render as
// "abc1234\n" in JSON without the trim.
func envOrTrim(env, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return fallback
}
