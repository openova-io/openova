// internal_auth.go — ServiceAccount authentication for the /api/v1/internal/*
// routes.
//
// Two in-cluster callers have no operator session and must still reach the
// sovereign-admin API:
//
//   - the bp-self-sovereign-cutover auto-trigger Job
//     (POST /api/v1/internal/cutover/trigger, #935);
//   - the chargeback application's collections Enforcer
//     (POST /api/v1/internal/organizations/{id}/suspend | /resume,
//     products/chargeback DESIGN.md §9, EPIC #6867).
//
// Both present a projected ServiceAccount token as `Authorization: Bearer`
// and the apiserver's TokenReview is the ONLY judge of it — never a
// hand-parsed JWT, which would skip rotation, audience checking and
// revocation. Every internal route is authenticated the same way (extract
// the bearer → TokenReview → allow-list) and differs ONLY in which
// ServiceAccounts it admits, so the allow-lists live in the one table below:
// a new caller is a row here, not a copy of the function.
//
// Why an allow-list and not "any authenticated ServiceAccount": per
// docs/PRINCIPLES.md #10 a wide-open `/internal/*` surface that any Pod in
// the cluster can reach is a foothold — a compromised side-car in any
// namespace could suspend every Organization. TokenReview + the table narrow
// each route to the identity that already carries the RBAC to do the work.
package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ── The table ─────────────────────────────────────────────────────────────

const (
	// defaultCutoverRunnerSAName is the SA the bp-self-sovereign-cutover
	// chart authors (`<chartName>-runner`, chartName fixed at
	// bp-self-sovereign-cutover). Its namespace is the cutover namespace.
	defaultCutoverRunnerSAName = "bp-self-sovereign-cutover-runner"

	// chargebackNamespace / chargebackSAName — the bp-chargeback Sovereign
	// placement (bootstrap-kit slot 13f: releaseName chargeback,
	// targetNamespace chargeback; the chart's serviceAccountName helper
	// yields the release name). The Deployment projects a ServiceAccount
	// token at /var/run/secrets/platform-api/token and the binary presents
	// it on every enforcement call.
	chargebackNamespace = "chargeback"
	chargebackSAName    = "chargeback"

	// Runtime overrides (docs/PRINCIPLES.md #4). The singular form names one
	// username, the plural a comma-separated list; either matching suffices.
	envInternalCutoverSAUsername     = "CATALYST_INTERNAL_CUTOVER_SA_USERNAME"
	envInternalCutoverSAUsernames    = "CATALYST_INTERNAL_CUTOVER_SA_USERNAMES"
	envInternalOrgSuspendSAUsername  = "CATALYST_INTERNAL_ORG_SUSPEND_SA_USERNAME"
	envInternalOrgSuspendSAUsernames = "CATALYST_INTERNAL_ORG_SUSPEND_SA_USERNAMES"
)

// internalSAAllowList names the ServiceAccounts one internal route admits.
type internalSAAllowList struct {
	// what the route is, for the refusal detail and the log line.
	what string
	// envSingular / envPlural are the runtime overrides.
	envSingular, envPlural string
	// defaults are always accepted.
	defaults []string
}

// cutoverTriggerCallers — POST /api/v1/internal/cutover/trigger admits the
// cutover runner in the cutover namespace.
func cutoverTriggerCallers(cutoverNS string) internalSAAllowList {
	return internalSAAllowList{
		what:        "cutover-runner",
		envSingular: envInternalCutoverSAUsername,
		envPlural:   envInternalCutoverSAUsernames,
		defaults:    []string{serviceAccountUsername(cutoverNS, defaultCutoverRunnerSAName)},
	}
}

// orgSuspendCallers — POST /api/v1/internal/organizations/{id}/suspend and
// /resume admit the chargeback application's ServiceAccount.
func orgSuspendCallers() internalSAAllowList {
	return internalSAAllowList{
		what:        "billing-enforcement",
		envSingular: envInternalOrgSuspendSAUsername,
		envPlural:   envInternalOrgSuspendSAUsernames,
		defaults:    []string{serviceAccountUsername(chargebackNamespace, chargebackSAName)},
	}
}

// serviceAccountUsername is the username a TokenReview resolves a
// ServiceAccount token to.
func serviceAccountUsername(namespace, name string) string {
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, name)
}

// usernames returns the acceptable TokenReview usernames, overrides first,
// defaults last. At least one must match for the request to be accepted.
func (l internalSAAllowList) usernames() []string {
	out := make([]string, 0, len(l.defaults)+2)
	if v := strings.TrimSpace(os.Getenv(l.envSingular)); l.envSingular != "" && v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(os.Getenv(l.envPlural)); l.envPlural != "" && v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return append(out, l.defaults...)
}

// admits reports whether the resolved username is on the list.
func (l internalSAAllowList) admits(user string) bool {
	for _, u := range l.usernames() {
		if user == u {
			return true
		}
	}
	return false
}

// ── The mechanism ─────────────────────────────────────────────────────────

// validateInternalBearer runs a TokenReview against the cluster's
// authentication chain. Returns the resolved username on success or an
// error describing why the token was rejected.
//
// Production wires core to a real kubernetes.Interface; tests inject a
// fake.NewSimpleClientset and prepend a reactor for the `tokenreviews`
// resource so the apiserver round-trip is mocked.
func validateInternalBearer(ctx context.Context, core kubernetes.Interface, bearer string) (string, error) {
	if bearer == "" {
		return "", fmt.Errorf("empty bearer token")
	}
	if core == nil {
		return "", fmt.Errorf("no cluster client to review the token with")
	}
	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: bearer}}
	resp, err := core.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("token review API call failed: %w", err)
	}
	if !resp.Status.Authenticated {
		// resp.Status.Error is operator-actionable (e.g. "token expired").
		// Returned verbatim so the caller can log it.
		detail := resp.Status.Error
		if detail == "" {
			detail = "token not authenticated by apiserver"
		}
		return "", fmt.Errorf("token review rejected: %s", detail)
	}
	user := strings.TrimSpace(resp.Status.User.Username)
	if user == "" {
		return "", fmt.Errorf("token review returned empty username")
	}
	return user, nil
}

// authenticateInternalCaller is the whole gate for an internal route: it
// extracts the bearer, reviews it and checks the allow-list, writing the
// refusal itself. It returns the resolved username and true when the
// request may proceed.
//
//   - 401 missing-bearer      — no `Authorization: Bearer` header.
//   - 502 token-review-failed — the apiserver rejected the token or the
//     TokenReview call failed; the failure is on the cluster side, so the
//     caller's retry loop is the right response.
//   - 403 unauthorized-sa     — authenticated, but not on this route's list.
func (h *Handler) authenticateInternalCaller(w http.ResponseWriter, r *http.Request, core kubernetes.Interface, allow internalSAAllowList) (string, bool) {
	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-bearer",
			"detail": "Authorization: Bearer <serviceaccount-token> header is required",
		})
		return "", false
	}
	user, err := validateInternalBearer(r.Context(), core, bearer)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "token-review-failed",
			"detail": err.Error(),
		})
		return "", false
	}
	if !allow.admits(user) {
		h.log.Warn("internal route: unauthorized ServiceAccount",
			"route", allow.what,
			"user", user,
			"allowed", strings.Join(allow.usernames(), ","),
		)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "unauthorized-sa",
			"detail": fmt.Sprintf("token resolved to %q which is not an authorized %s ServiceAccount", user, allow.what),
		})
		return "", false
	}
	return user, true
}
