package handlers

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// #5646 — the customer-facing vocabulary guard for the funnel timeline.
//
// WHAT THIS GUARD IS FOR, AND WHY IT IS NOT A GREP.
//
// The original defect was a string literal ("Creating tenant") and a grep would
// have caught it. The half a grep CANNOT catch — and the half that was still
// live on main when this guard was written — is the banned term arriving from a
// runtime KUBERNETES OBJECT NAME. `failStep(..., err.Error())` writes a raw Go
// error into the customer-visible `Message` field, those errors are built from
// object names like `tenant-<slug>-kubeconfig`, and
// ProvisioningTimeline.svelte:55 prints that field verbatim to a paying
// customer. No source string ever contains the offending sentence; it is
// assembled at runtime. Same class as #5435 (`deployment.apps/tenant`).
//
// So this guard asserts on the VALUE OF THE CUSTOMER-VISIBLE FIELD, and it
// builds its inputs from the REAL production helpers (vclusterKubeconfigSecretName,
// the real k8s error format, the real step-list builder) rather than from
// hand-written strings — if someone renames the secret or reshapes the error,
// the guard follows automatically instead of testing a stale copy.
//
// If the bug were present, could this go red? Yes — TestFailedStepMessage_*
// below fails on a passthrough (pre-fix) customerFacingMessage; that red run is
// pasted in the PR. And TestInternalIdentifiers_* is the CONTROL: it is green on
// BOTH trees, and it fails if the "fix" is done by renaming the internal object
// instead of the copy.
// ─────────────────────────────────────────────────────────────────────────────

// bannedProductTerm is the docs/GLOSSARY.md §Banned-terms entry this guard
// enforces on customer-facing copy: "tenant" → "Organization".
//
// Deliberately word-boundary anchored and case-insensitive so it also catches
// the spellings that would evade a naive grep for the original sentence:
// "Tenant", "TENANT", "tenants", "creating tenant", and the term embedded in a
// hyphenated object name (`tenant-uatco-kubeconfig`) — the last one is the
// whole point, since that is the spelling the runtime produces.
var bannedProductTerm = regexp.MustCompile(`(?i)tenant`)

// customerVisibleFields returns every string in a provisioning record that the
// funnel renders to the customer. Kept in one place so a new rendered field is
// added here rather than silently escaping the guard.
//
// Both fields are drawn by the funnel: LaunchingStep/ProvisioningTimeline print
// `Name` for every step and `Message` for any failed step.
func customerVisibleFields(steps []store.ProvisionStep) map[string]string {
	out := map[string]string{}
	for i, s := range steps {
		out[fmt.Sprintf("steps[%d].name", i)] = s.Name
		out[fmt.Sprintf("steps[%d].message", i)] = s.Message
	}
	return out
}

// realInternalErrors reproduces, using the production string formats and the
// production object-name builder, the errors that actually reach failStep /
// failProvision on a failed provision. Nothing here is invented copy: each entry
// cites the code path that emits it.
func realInternalErrors(slug string) []string {
	secret := vclusterKubeconfigSecretName(slug)

	// handlers.go:964 — the error k8sRequest returns on any >=400 response.
	k8sErr := func(method, path string, code int) string {
		return fmt.Sprintf("k8s %s %s: status %d: %s", method, path, code,
			`{"kind":"Status","message":"internal error"}`)
	}

	return []string{
		// mirrorVClusterKubeconfig PUT branch (handlers.go) wrapped by
		// `update mirror secret (%s): %w`, reaching the customer through
		// consumer.go's failStep on the "Provisioning vCluster" step.
		fmt.Sprintf("update mirror secret (flux-system): %s",
			k8sErr(http.MethodPut, "/api/v1/namespaces/flux-system/secrets/"+secret, 500)),

		// mirrorVClusterKubeconfig POST branch, same customer field.
		fmt.Sprintf("create mirror secret (%s): %s", slug,
			k8sErr(http.MethodPost, "/api/v1/namespaces/"+slug+"/secrets", 403)),

		// deleteVClusterKubeconfigMirror path, same object name.
		k8sErr(http.MethodDelete, "/api/v1/namespaces/flux-system/secrets/"+secret, 409),

		// consumer.go failProvision wrapper around the mirror error.
		fmt.Sprintf("mirror kubeconfig to flux-system: read source secret %s/vc-vcluster: %s",
			slug, k8sErr(http.MethodGet, "/api/v1/namespaces/"+slug+"/secrets/vc-vcluster", 404)),
	}
}

// TestFailedStepMessage_NoBannedTermFromRuntimeObjectNames is the RED half.
//
// It pushes the errors the real code paths produce through the production
// customer-copy boundary and asserts the customer-visible value is free of the
// banned term. With customerFacingMessage reduced to a passthrough (which is
// exactly what main did before this change) every case below fails.
func TestFailedStepMessage_NoBannedTermFromRuntimeObjectNames(t *testing.T) {
	const slug = "uatco" // the Org from the hw292 record that filed #5646

	for _, raw := range realInternalErrors(slug) {
		got := customerFacingMessage(raw)

		if loc := bannedProductTerm.FindStringIndex(got); loc != nil {
			t.Errorf(`banned term reached the customer-facing step message.
  internal error : %s
  rendered to customer: %s
                        %s^-- %q
  docs/GLOSSARY.md bans "tenant" on product surfaces; the correction is "Organization".
  ProvisioningTimeline.svelte prints step.message verbatim, so this string IS product copy.`,
				raw, got, strings.Repeat(" ", loc[0]), got[loc[0]:loc[1]])
		}

		if strings.TrimSpace(got) == "" {
			t.Errorf("customer message for %q was redacted to nothing — the customer needs a reason, not a blank", raw)
		}
	}
}

// TestFailedStepMessage_KeepsTheDiagnostic checks the redaction stayed useful.
// A guard that passes by blanking every message would be worse than the bug, so
// the status code (the one genuinely actionable token) must survive.
func TestFailedStepMessage_KeepsTheDiagnostic(t *testing.T) {
	raw := fmt.Sprintf("update mirror secret (flux-system): k8s PUT /api/v1/namespaces/flux-system/secrets/%s: status 500: boom",
		vclusterKubeconfigSecretName("uatco"))

	got := customerFacingMessage(raw)
	if !strings.Contains(got, "500") {
		t.Errorf("redaction dropped the status code — customer message %q is no longer diagnostic", got)
	}
	if !strings.Contains(got, "mirror secret") {
		t.Errorf("redaction dropped the failing operation — customer message %q says nothing about what broke", got)
	}
}

// TestProvisionStepNames_NoBannedTerm covers the literal half of the issue: the
// step list the producer emits. It asserts on the names the funnel actually
// draws, so a future edit that reintroduces "Creating tenant" is caught here
// and not on a customer's screen.
func TestProvisionStepNames_NoBannedTerm(t *testing.T) {
	// Read from the PRODUCER, never transcribed (#5769). This list used to be
	// hand-copied into the test, which meant putting "Creating tenant" back into
	// consumer.go left this guard green — the guard could not detect a change to
	// the thing it was copying. buildProvisionSteps is the function
	// startProvisioning itself calls; the runtime-interpolated entries are fed
	// the same shapes the live hw292 catalog produces (dependency slugs verbatim,
	// app names as catalog display titles).
	emitted := buildProvisionSteps(
		[]string{"mysql"},
		[]string{"wp"},
		map[string]string{"wp": "WordPress"},
	)

	// Vacuity check: a guard that iterates an empty list passes trivially. If
	// the producer ever returns nothing, that is a defect in its own right and
	// must not read as "no banned terms found".
	if len(emitted) < 7 {
		t.Fatalf("buildProvisionSteps returned %d steps, want >= 7 — "+
			"a short or empty list makes the banned-term scan below vacuous", len(emitted))
	}

	for field, val := range customerVisibleFields(emitted) {
		if bannedProductTerm.MatchString(val) {
			t.Errorf("banned term in customer-visible %s = %q (docs/GLOSSARY.md: use \"Organization\")", field, val)
		}
	}
}

// TestInternalIdentifiers_AreNotRenamed is the CONTROL, and it must be GREEN ON
// BOTH TREES — before and after the fix.
//
// The legitimate internal uses of the word must survive untouched: the mirrored
// kubeconfig Secret is referenced by the generated apps-sync Kustomization and
// by every per-Org application HelmRelease already reconciling on provisioned
// Sovereigns. Fixing the customer's screen by renaming that object would strand
// them. This control fails if anyone "fixes" the banned term at the identifier
// layer instead of the copy layer — which is the wrong layer.
func TestInternalIdentifiers_AreNotRenamed(t *testing.T) {
	const slug = "uatco"

	got := vclusterKubeconfigSecretName(slug)
	if want := "tenant-" + slug + "-kubeconfig"; got != want {
		t.Fatalf(`the INTERNAL vCluster kubeconfig Secret name changed: got %q, want %q.
  This name is referenced by gitops.go's apps-sync Kustomization and by every
  per-Org application HelmRelease on every already-provisioned Sovereign.
  #5646 is a COPY defect: fix what the customer reads (customerFacingMessage),
  never the object identity.`, got, want)
	}

	// And the copy boundary must not corrupt an internal identifier by rewriting
	// it in place into a name that does not exist anywhere.
	if strings.Contains(customerFacingMessage("k8s GET /api/v1/namespaces/flux-system/secrets/"+got+": status 404: x"),
		"organization-"+slug+"-kubeconfig") {
		t.Error("customerFacingMessage invented a renamed object name; it must redact the internal reference, not rewrite it into a fiction")
	}
}

// TestBannedTermPattern_IsNotVacuous is the vacuity check. An absence-assertion
// reports clean both when the term is absent AND when the matcher stopped
// working. If bannedProductTerm is ever broken, every test above passes forever
// on nothing — so prove it still matches the spellings that actually shipped,
// and still ignores clean copy.
func TestBannedTermPattern_IsNotVacuous(t *testing.T) {
	mustMatch := []string{
		"Creating tenant",                                 // the #5646 headline string
		"Creating Tenant",                                 // case variant
		"Console (my tenants)",                            // the funnel header string
		"secrets/tenant-uatco-kubeconfig",                 // runtime object name (#5435 class)
		"helmrelease tenant-f4179walk/vcluster not ready", // the 2026-06-24 live sighting
	}
	for _, s := range mustMatch {
		if !bannedProductTerm.MatchString(s) {
			t.Errorf("banned-term matcher no longer matches %q — every assertion in this file would pass on nothing", s)
		}
	}

	mustNotMatch := []string{
		"Creating Organization",
		"Installing mysql (dependency)",
		"platform API returned status 500",
		"Provisioning vCluster",
	}
	for _, s := range mustNotMatch {
		if bannedProductTerm.MatchString(s) {
			t.Errorf("banned-term matcher is too broad: it flags clean copy %q and would fail every run", s)
		}
	}
}
