package handlers

import (
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// The customer-facing copy boundary for the funnel provisioning timeline.
//
// #5646. The `launching` page is the screen a PAYING CUSTOMER watches straight
// after checkout. Everything on it comes from one JSON payload
// (`GET /api/provisioning/tenant/<id>`), and the funnel renderers draw that
// payload verbatim — `ProvisioningTimeline.svelte` prints `step.message` into
// the page for any failed step with no filtering of its own. So whatever the
// provisioning service writes into a step IS product copy, and
// docs/GLOSSARY.md §Banned-terms governs it: "tenant" is banned, the
// correction is "Organization".
//
// The step NAMES were the visible half of that bug and are already literal
// strings under our control (consumer.go's step list). This file exists for
// the half a source grep cannot see:
//
//	failStep(..., err.Error())
//
// writes a RAW Go error into the customer-visible `Message` field, and those
// errors are built at runtime out of KUBERNETES OBJECT NAMES. The mirrored
// vCluster kubeconfig Secret is named `tenant-<slug>-kubeconfig`, so a failed
// mirror reaches the customer's screen as
//
//	update mirror secret (flux-system): k8s PUT /api/v1/namespaces/flux-system/
//	secrets/tenant-uatco-kubeconfig: status 500: ...
//
// — the banned term on a customer-facing screen, sourced from an object name,
// invisible to any grep for a banned string literal. That is the #5435 class
// (`deployment.apps/tenant` reaching the showback table the same way).
//
// The fix is at the PRESENTATION boundary, deliberately NOT at the identifier.
// `tenant-<slug>-kubeconfig` is referenced by generated Flux Kustomizations and
// per-Org HelmReleases (gitops.go, per_org_flux.go) on every Sovereign already
// provisioned; renaming it is a separate change with a far larger blast radius.
// The internal name stays exactly as it is — see vclusterKubeconfigSecretName —
// and only the copy shown to the customer is rewritten. The unabridged error
// is still logged by failStep for the sovereign-admin, so no diagnostic is lost.
// ─────────────────────────────────────────────────────────────────────────────

var (
	// k8sAPIErrorRE matches the error k8sRequest emits on a >=400 response:
	// `k8s <METHOD> <path>: status <code>`. The path carries the object name,
	// which is exactly what must not reach the customer. The status code is
	// genuinely useful, so it is kept.
	k8sAPIErrorRE = regexp.MustCompile(`k8s [A-Z]+ /\S*?: status (\d+)`)

	// k8sAPIPathRE catches any other bare API path that leaks through a
	// differently-shaped wrapper.
	k8sAPIPathRE = regexp.MustCompile(`/apis?/\S+`)

	// internalObjectRE matches an internal Kubernetes object name built on the
	// banned term — `tenant-<slug>-kubeconfig`, `tenant-<slug>-tls`,
	// `tenant-<slug>-apps`, `catalyst-tenant-<slug>`. Matched BEFORE the bare
	// word so the whole identifier is removed rather than half-rewritten into
	// a name that does not exist.
	internalObjectRE = regexp.MustCompile(`(?i)\b[a-z0-9-]*tenants?-[a-z0-9][a-z0-9.\-/]*`)

	// bannedWordRE matches the banned term as a standalone English word, which
	// is the form that reads as product copy.
	bannedWordRE = regexp.MustCompile(`(?i)\btenants\b`)

	bannedWordSingularRE = regexp.MustCompile(`(?i)\btenant\b`)

	// tidy-up passes so a redacted message still reads like a sentence.
	danglingSepRE = regexp.MustCompile(`\s*([:,])\s*([:,])`)
	multiSpaceRE  = regexp.MustCompile(`[ \t]{2,}`)
)

// customerFacingMessage turns an internal error string into copy that is safe
// to render on the funnel timeline: no Kubernetes plumbing, and no banned term.
//
// It is intentionally a pure string function so the guard can drive it with the
// error strings the real code paths produce. Order matters — the widest,
// most-specific redactions run first so that an identifier is removed whole
// instead of being partially rewritten into something that does not exist.
func customerFacingMessage(raw string) string {
	if raw == "" {
		return raw
	}

	out := k8sAPIErrorRE.ReplaceAllString(raw, "platform API returned status $1")
	out = k8sAPIPathRE.ReplaceAllString(out, "an internal resource")
	out = internalObjectRE.ReplaceAllString(out, "an internal resource")
	out = bannedWordRE.ReplaceAllString(out, "Organizations")
	out = bannedWordSingularRE.ReplaceAllString(out, "Organization")

	out = danglingSepRE.ReplaceAllString(out, "$1")
	out = multiSpaceRE.ReplaceAllString(out, " ")
	out = strings.TrimSpace(out)
	out = strings.TrimRight(out, ":, ")

	return out
}

// vclusterKubeconfigSecretName is the name of the mirrored vCluster kubeconfig
// Secret for an Organization.
//
// The `tenant-` prefix is an INTERNAL Kubernetes object name. It is referenced
// by the generated apps-sync Kustomization (gitops.go) and by every per-Org
// application HelmRelease already reconciling on every provisioned Sovereign,
// so it deliberately does NOT change here — renaming it would strand those
// references. It is centralised in one function so the customer-copy guard can
// assert against the real name instead of a hand-written copy of it, and so the
// mirror and its teardown can never drift apart.
func vclusterKubeconfigSecretName(slug string) string {
	return "tenant-" + slug + "-kubeconfig"
}
