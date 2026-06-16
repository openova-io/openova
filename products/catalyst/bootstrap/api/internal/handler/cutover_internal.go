// cutover_internal.go — In-cluster Self-Sovereignty Cutover trigger
// endpoint (issue #935 Bug 2).
//
// Why this file exists separately from cutover.go
// ────────────────────────────────────────────────
// The operator-facing cutover endpoint
// (POST /api/v1/sovereign/cutover/start, in cutover.go) lives INSIDE
// the RequireSession middleware group in cmd/api/main.go — it requires
// a valid HMAC-signed `catalyst_session` cookie minted by the
// /auth/handover handler. That gate is exactly right for human
// operators clicking "Achieve True Sovereignty" in the admin console.
//
// But issue #933 wired bp-self-sovereign-cutover (chart 0.1.16) with
// an auto-trigger Helm post-install Job that POSTs /cutover/start
// AUTOMATICALLY after the chart's ConfigMaps land — the founder rule
// is "handover is not done until cutover has run; no operator click
// required." That Job is in-cluster and has no human session cookie;
// when it hit /cutover/start on otech113 (2026-05-05) it got 401
// `unauthenticated` forever and the cutover never started.
//
// This file adds a SECOND endpoint:
//
//	POST /api/v1/internal/cutover/trigger
//
// …that lives OUTSIDE the RequireSession group and validates the
// caller via a Kubernetes TokenReview against the bearer token
// presented in `Authorization: Bearer <token>`. The auto-trigger Job
// mounts its ServiceAccount token at the standard projected-volume
// path (/var/run/secrets/kubernetes.io/serviceaccount/token) and
// passes it on the request. The endpoint then:
//
//  1. Calls authentication.k8s.io/v1 TokenReview to validate the
//     token bytes against the cluster's authentication chain. The
//     apiserver returns the authenticated user (typically
//     `system:serviceaccount:<ns>:<sa-name>`) plus a list of groups.
//     This is the ONLY supported way to verify a bearer token came
//     from a valid in-cluster ServiceAccount — manual JWT parsing
//     would skip rotation, audience checking, and revocation.
//
//  2. Checks the username matches the canonical cutover-runner SA:
//     `system:serviceaccount:<ns>:bp-self-sovereign-cutover-runner`
//     (override via CATALYST_INTERNAL_CUTOVER_SA_USERNAME for test
//     and per-Sovereign re-naming).
//
//  3. Delegates to the same engine logic as HandleCutoverStart —
//     idempotency, in-process running flag, and goroutine spawn are
//     shared. The two endpoints are alternative entry points to the
//     SAME state machine; the only difference is auth surface.
//
// Why not skip auth entirely for in-cluster callers
// ─────────────────────────────────────────────────
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) every
// privileged action MUST authenticate. A wide-open `/internal/*`
// surface that any pod in the cluster can reach is a foothold —
// e.g. a compromised side-car in any namespace could fire cutovers
// repeatedly. TokenReview narrows the surface to the SINGLE SA the
// chart authored for this purpose, which already has the RBAC to do
// the work via the Jobs the cutover engine stamps. Same identity,
// same blast radius — just expressed at the HTTP edge instead of
// only at the K8s API edge.
//
// Why not the operator-session cookie
// ───────────────────────────────────
// The auto-trigger Job has no browser, no cookie jar, and no SAML/OIDC
// flow. It's a `curlimages/curl` Pod with a projected SA token. Sharing
// a session-cookie path would either require minting a fake operator
// session (wrong abstraction; cookies represent humans) or routing
// through Keycloak's client_credentials grant (extra moving parts that
// fail on a freshly-bootstrapped Sovereign before Keycloak is fully
// healthy). TokenReview is the canonical K8s seam.
//
// Configuration
// ─────────────
// Every value is runtime-overridable per docs/INVIOLABLE-PRINCIPLES.md #4:
//
//	CATALYST_INTERNAL_CUTOVER_SA_USERNAME
//	  Required SA username (e.g.
//	  "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner").
//	  Default = system:serviceaccount:<CATALYST_CUTOVER_NAMESPACE>:bp-self-sovereign-cutover-runner.
//
//	CATALYST_INTERNAL_CUTOVER_SA_USERNAMES
//	  Optional comma-separated list of additional accepted SA usernames.
//	  Empty by default. Used for test fixtures and per-Sovereign
//	  re-naming. Both the singular and plural forms are honoured;
//	  either one matching is sufficient.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// Default suffix appended to the cutover namespace to compute the
// canonical service-account username for token-review validation.
// The chart helper `bp-self-sovereign-cutover.serviceAccountName`
// always produces `<chartName>-runner`, and chartName is fixed at
// `bp-self-sovereign-cutover`.
const defaultCutoverRunnerSAName = "bp-self-sovereign-cutover-runner"

const (
	envInternalCutoverSAUsername  = "CATALYST_INTERNAL_CUTOVER_SA_USERNAME"
	envInternalCutoverSAUsernames = "CATALYST_INTERNAL_CUTOVER_SA_USERNAMES"
)

// envRequireHandoverForCutover gates the in-cluster auto-trigger on
// handover completion. See requireHandoverForCutover.
const envRequireHandoverForCutover = "CATALYST_CUTOVER_REQUIRE_HANDOVER"

// requireHandoverForCutover reports whether the in-cluster auto-trigger
// must verify this Sovereign has been handed over before it starts the
// cutover engine. Default TRUE (founder principle: the cutover is a
// post-handover operation). Set CATALYST_CUTOVER_REQUIRE_HANDOVER=false
// only for test fixtures or a deliberate operator-forced demo cutover on
// a converged-but-not-yet-handed-over prov. Runtime-overridable per
// docs/PRINCIPLES.md #4.
func requireHandoverForCutover() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(envRequireHandoverForCutover)), "false")
}

// envFireCutoverOnHandover gates whether ReceiveTofuArchive fires the
// cutover engine immediately after sealing the Phase-0 archive. See
// fireCutoverOnHandover.
const envFireCutoverOnHandover = "CATALYST_FIRE_CUTOVER_ON_HANDOVER"

// fireCutoverOnHandover reports whether sealing the tofu-phase0-archive at
// handover should auto-fire the cutover engine (issue #933 / #3052). Default
// TRUE — the founder rule is "handover is not done until cutover has run; no
// operator click required", and the in-cluster auto-trigger Helm hook only
// fires once at bootstrap (where it correctly defers via the handover gate),
// so WITHOUT this nothing re-fires the engine post-handover and the cutover
// stays dormant forever. Set CATALYST_FIRE_CUTOVER_ON_HANDOVER=false to keep
// the cutover a deliberate operator action (the BSS "Achieve True
// Sovereignty" CTA still works). Runtime-overridable per docs/PRINCIPLES.md #4.
func fireCutoverOnHandover() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(envFireCutoverOnHandover)), "false")
}

// fireCutoverEngine dispatches to the test seam (spawnCutoverEngineFn) when
// one is wired, otherwise to the real h.spawnCutoverEngine. Every caller
// goes through here so production behaviour and test assertions share one
// entry point.
func (h *Handler) fireCutoverEngine(ctx context.Context, deps *cutoverDeps, source string) (cutoverSpawnResult, error) {
	if h.spawnCutoverEngineFn != nil {
		return h.spawnCutoverEngineFn(ctx, deps, source)
	}
	return h.spawnCutoverEngine(ctx, deps, source)
}

// sovereignHandoverComplete reports whether THIS Sovereign has received
// the Phase-0 OpenTofu archive from the mothership — the durable,
// Sovereign-side signal that ownership has transferred. handover.go's
// ReceiveTofuArchive seals it at secret/catalyst/tofu-phase0-archive
// (KV-v2) on a successful handover POST; its ABSENCE means the Sovereign
// is still soft-tethered (pre-handover). A nil OpenBao client (e.g.
// Catalyst-Zero, which is never itself a cutover subject) reads as
// not-handed-over so the gate stays closed.
func (h *Handler) sovereignHandoverComplete(ctx context.Context) (bool, error) {
	if h.openbao == nil {
		return false, nil
	}
	data, err := h.openbao.GetKVv2(ctx, "secret", "catalyst/tofu-phase0-archive")
	if err != nil {
		if errors.Is(err, openbao.ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	return len(data) > 0, nil
}

// cutoverCompleteSecretPath is the KV-v2 path of the DURABLE, revert-immune
// sovereignty fact (#3667). It mirrors the handover marker
// (catalyst/tofu-phase0-archive) but records the FINAL state of the
// cutover state machine: once the 11 steps run green AND the 600s
// deny-egress hold holds, runCutover seals this in OpenBao in the SAME
// transaction that flips the status ConfigMap's cutoverComplete=true.
//
// Why OpenBao and not the status ConfigMap: the status ConfigMap is
// Helm/Flux-managed (the chart authors it at install with
// data.cutoverComplete:"false"). A routine `helm upgrade
// bp-self-sovereign-cutover` — chart pins land on running Sovereigns
// constantly — runs helm-controller's 3-way merge, re-asserts the
// template's "false" over the live "true", and re-arms the whole
// cutover (the auto-trigger Job re-fires the 600s hold). OpenBao is
// NOT chart-managed, so a chart upgrade cannot revert the seal. This
// is the single durable bit that makes cutoverComplete=true a TRUE,
// reconcile-immune proof of independence.
const cutoverCompleteSecretPath = "catalyst/cutover-complete"

// sovereignCutoverComplete reports whether THIS Sovereign has sealed the
// durable cutover-complete fact (#3667). Modeled EXACTLY on
// sovereignHandoverComplete: reads secret/catalyst/cutover-complete via
// KV-v2; ABSENCE (ErrSecretNotFound) ⇒ false; a nil OpenBao client
// (Catalyst-Zero, the orchestrator, is never itself a cutover subject)
// reads as not-complete so the gate stays closed and a fresh prov can
// still run its cutover.
func (h *Handler) sovereignCutoverComplete(ctx context.Context) (bool, error) {
	if h.openbao == nil {
		return false, nil
	}
	data, err := h.openbao.GetKVv2(ctx, "secret", cutoverCompleteSecretPath)
	if err != nil {
		if errors.Is(err, openbao.ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	return len(data) > 0, nil
}

// sealCutoverComplete writes the durable, revert-immune sovereignty fact
// (#3667). Called from runCutover's success tail in the SAME block that
// flips cutoverComplete=true in the status ConfigMap. The payload records
// the audit anchor (true first-run start + finish) so the seal alone is
// enough for HandleCutoverStatus to re-derive cutoverComplete=true even
// if a chart upgrade reverted the ConfigMap.
//
// A nil OpenBao client is a non-fatal no-op (the orchestrator path):
// the run still succeeds and the ConfigMap still carries the truth; only
// the durable seal is skipped, which is correct because Catalyst-Zero is
// never a cutover subject. The caller logs the skip.
func (h *Handler) sealCutoverComplete(ctx context.Context, startedAt, finishedAt string) error {
	if h.openbao == nil {
		return nil
	}
	return h.openbao.PutKVv2(ctx, "secret", cutoverCompleteSecretPath, map[string]any{
		"cutoverComplete":   "true",
		"cutoverStartedAt":  startedAt,
		"cutoverFinishedAt": finishedAt,
		"sealedAt":          time.Now().UTC().Format(time.RFC3339),
	})
}

// expectedInternalCutoverUsernames returns the set of acceptable SA
// usernames a TokenReview may resolve to. Order is:
//  1. Singular env override.
//  2. Plural env override (comma-split).
//  3. Default = system:serviceaccount:<ns>:<defaultCutoverRunnerSAName>.
//
// At least one must match the TokenReview-resolved username for the
// request to be accepted.
func expectedInternalCutoverUsernames(deps *cutoverDeps) []string {
	out := make([]string, 0, 3)
	if v := strings.TrimSpace(os.Getenv(envInternalCutoverSAUsername)); v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(os.Getenv(envInternalCutoverSAUsernames)); v != "" {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
	}
	// Default — always accepted unless the operator explicitly
	// excludes it via env override.
	defaultUser := fmt.Sprintf("system:serviceaccount:%s:%s", deps.ns, defaultCutoverRunnerSAName)
	out = append(out, defaultUser)
	return out
}

// validateInternalCutoverBearer runs a TokenReview against the
// cluster's authentication chain. Returns the resolved username on
// success or an error describing why the token was rejected.
//
// Production wires deps.core to a real kubernetes.Interface; tests
// inject a fake.NewSimpleClientset and prepend a reactor for the
// `tokenreviews` resource so the apiserver round-trip is mocked.
func validateInternalCutoverBearer(ctx context.Context, deps *cutoverDeps, bearer string) (string, error) {
	if bearer == "" {
		return "", fmt.Errorf("empty bearer token")
	}
	tr := &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{
			Token: bearer,
		},
	}
	resp, err := deps.core.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("token review API call failed: %w", err)
	}
	if !resp.Status.Authenticated {
		// resp.Status.Error is operator-actionable (e.g. "token expired").
		// Return verbatim so the auto-trigger Job can log it.
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

// HandleCutoverInternalTrigger handles
//
//	POST /api/v1/internal/cutover/trigger
//
// — the auto-trigger Job's entry into the cutover engine. Accepts a
// service-account bearer token in the Authorization header, validates
// it via TokenReview, and (on success) delegates to the same engine
// path as HandleCutoverStart.
//
// Request shape:
//   - Method: POST (no body required; the engine reads its inputs from
//     the cutover-step ConfigMaps in the cluster).
//   - Header: Authorization: Bearer <SA token bytes>.
//
// Response shape:
//   - 200 + cutoverStatusResponse on success (or idempotent no-op when
//     cutoverComplete=true).
//   - 401 on missing/invalid bearer.
//   - 403 on bearer that resolves to a non-cutover SA.
//   - 409 when a cutover is already in progress.
//   - 424 when no cutover-step ConfigMaps are found (chart not
//     installed).
//   - 502 on TokenReview API failure or status read failure.
//   - 503 when in-cluster config is unavailable.
func (h *Handler) HandleCutoverInternalTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error":  "method-not-allowed",
			"detail": "this endpoint accepts POST only",
		})
		return
	}

	deps, err := h.cutoverDepsFor()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cutover-unconfigured",
			"detail": err.Error(),
		})
		return
	}

	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-bearer",
			"detail": "Authorization: Bearer <serviceaccount-token> header is required",
		})
		return
	}

	user, err := validateInternalCutoverBearer(r.Context(), deps, bearer)
	if err != nil {
		// 502 here because the failure is on the apiserver side of the
		// TokenReview; the client did not necessarily mis-send. The
		// auto-trigger Job will retry on a non-2xx response per its
		// shell loop.
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "token-review-failed",
			"detail": err.Error(),
		})
		return
	}

	allowed := expectedInternalCutoverUsernames(deps)
	matched := false
	for _, u := range allowed {
		if user == u {
			matched = true
			break
		}
	}
	if !matched {
		h.log.Warn("internal cutover trigger: unauthorized SA",
			"user", user,
			"allowed", strings.Join(allowed, ","),
		)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "unauthorized-sa",
			"detail": fmt.Sprintf("token resolved to %q which is not an authorized cutover-runner SA", user),
		})
		return
	}

	// Reuse the operator-side engine path. The state machine is
	// identical regardless of which endpoint kicked it off. The
	// handover-completion gate lives inside runCutoverFromTrigger so an
	// already-complete cutover still returns its idempotent 200 first.
	h.runCutoverFromTrigger(w, r, deps, "internal")
}

// cutoverSpawnOutcome enumerates the terminal decisions spawnCutoverEngine
// can reach. The HTTP wrapper (runCutoverFromTrigger) maps each to the same
// status code + body it emitted before the engine-spawn tail was extracted;
// the async handover-seal path (ReceiveTofuArchive) only cares about
// started vs not-started and logs the rest.
type cutoverSpawnOutcome int

const (
	// cutoverSpawnStarted — a fresh engine goroutine was launched.
	cutoverSpawnStarted cutoverSpawnOutcome = iota
	// cutoverSpawnAlreadyComplete — cutoverComplete=true; idempotent no-op.
	cutoverSpawnAlreadyComplete
	// cutoverSpawnHandoverIncomplete — source="internal" + handover gate
	// closed (no tofu-phase0-archive). Benign defer on a fresh prov.
	cutoverSpawnHandoverIncomplete
	// cutoverSpawnNoSteps — no cutover-step ConfigMaps found.
	cutoverSpawnNoSteps
	// cutoverSpawnAlreadyRunning — another goroutine holds the run flag.
	cutoverSpawnAlreadyRunning
)

// cutoverSpawnResult carries the outcome of spawnCutoverEngine plus the
// data the HTTP layer needs to reconstruct its byte-identical responses
// (the status map for the already-complete / started snapshots, and the
// resolved step names + count). A non-nil err means a hard failure
// (status-read / handover-check / step-discovery) the caller surfaces as
// a 502; outcome is then meaningless.
type cutoverSpawnResult struct {
	outcome cutoverSpawnOutcome
	// status is the status ConfigMap snapshot read before the decision —
	// used to build the already-complete 200 body. For the started case it
	// is re-read post-spawn so cutoverStartedAt is reflected.
	status map[string]string
	// stepNames + totalSteps populate the started 200 snapshot.
	stepNames  []string
	totalSteps int
}

// spawnCutoverEngine is the w-less engine-spawn core shared by the
// operator endpoint (HandleCutoverStart), the in-cluster auto-trigger
// (HandleCutoverInternalTrigger, via runCutoverFromTrigger), AND the
// post-handover archive-seal path (ReceiveTofuArchive). It performs the
// SAME sequence the old runCutoverFromTrigger tail did — cutoverComplete
// idempotency short-circuit, the source-gated handover-completion check,
// step discovery, the in-process running-flag claim, and the detached
// engine goroutine — but returns a typed result instead of writing HTTP.
//
// The behaviour is byte-identical to the pre-extraction tail; the only
// new axis is `source`: the handover gate applies ONLY to
// source="internal" (the bootstrap-time Helm post-install hook that can
// fire before handover). source="operator" (human CTA behind
// RequireSession) and source="handover" (ReceiveTofuArchive, where the
// tofu-phase0-archive is sealed BY DEFINITION at the call site) are
// deliberate post-handover actions and skip the gate.
func (h *Handler) spawnCutoverEngine(ctx context.Context, deps *cutoverDeps, source string) (cutoverSpawnResult, error) {
	status, err := readCutoverStatus(ctx, deps)
	if err != nil {
		return cutoverSpawnResult{}, fmt.Errorf("status-read-failed: %w", err)
	}

	// Durable, revert-immune sovereignty gate (#3667). Check the sealed
	// OpenBao fact BEFORE the ConfigMap key. A routine `helm upgrade
	// bp-self-sovereign-cutover` re-asserts the chart template's
	// cutoverComplete:"false" over the live "true" (chart pins land on
	// running Sovereigns constantly) — so the CM alone would read "false"
	// and let the auto-trigger Job re-fire the whole cutover INCLUDING the
	// 600s deny-egress hold on a customer cluster. The seal cannot be
	// reverted by a chart upgrade, so once it exists we no-op regardless of
	// what the CM says. Backfill the CM so the UI re-derives the green state
	// on the next read. A status-read failure above is hard; a seal-read
	// failure here is treated as "not sealed" only on ErrSecretNotFound
	// (inside sovereignCutoverComplete) — any other error fails closed.
	if sealed, serr := h.sovereignCutoverComplete(ctx); serr != nil {
		return cutoverSpawnResult{}, fmt.Errorf("cutover-seal-check-failed: %w", serr)
	} else if sealed {
		bus := h.cutoverBusFor()
		h.publishCutoverEvent(bus, cutoverEvent{
			Phase:   cutoverPhaseAlreadyDone,
			Level:   "info",
			Message: fmt.Sprintf("cutover already complete (durable seal present); %s trigger is a no-op even if the status ConfigMap was reverted by a chart upgrade", source),
		})
		// Best-effort backfill so /status renders sovereign immediately.
		if status["cutoverComplete"] != "true" {
			_ = patchCutoverStatus(ctx, deps, map[string]string{"cutoverComplete": "true"})
			status["cutoverComplete"] = "true"
		}
		return cutoverSpawnResult{
			outcome:   cutoverSpawnAlreadyComplete,
			status:    status,
			stepNames: listStepNamesFromStatus(status),
		}, nil
	}

	if status["cutoverComplete"] == "true" {
		bus := h.cutoverBusFor()
		h.publishCutoverEvent(bus, cutoverEvent{
			Phase:   cutoverPhaseAlreadyDone,
			Level:   "info",
			Message: fmt.Sprintf("cutover already complete; %s trigger is a no-op", source),
		})
		return cutoverSpawnResult{
			outcome:   cutoverSpawnAlreadyComplete,
			status:    status,
			stepNames: listStepNamesFromStatus(status),
		}, nil
	}

	// Handover-completion gate (fresh-prov registry half-pivot).
	// ──────────────────────────────────────────────────────────
	// The in-cluster auto-trigger (source="internal") is a Helm
	// post-install hook on bp-self-sovereign-cutover, so it fires at
	// chart-INSTALL time — which on a fresh prov is bootstrap, long
	// before handover. Without this gate the cutover half-pivots the
	// registry (rewrites the ghcr-pull Secret to the local Harbor) while
	// Flux still pulls the catalog from GitHub (ghcr.io URLs) and the
	// local Harbor is not yet serving — breaking every FRESH chart pull
	// and wedging bootstrap-kit. Verified live hw93 2026-06-04: bp-kyverno
	// 1.3.4 stuck SourceNotReady "no auth config for 'ghcr.io' in
	// docker-registry Secret 'ghcr-pull'", cutover-egress-block-test
	// Failed, console 000.
	//
	// The cutover is a POST-handover operation. The durable Sovereign-side
	// handover marker is the tofu-phase0-archive secret, sealed in OpenBao
	// by ReceiveTofuArchive ONLY when the mothership POSTs the archive at
	// handover. Until it exists, refuse — which the auto-trigger Job treats
	// as benign (its case-* arm exits 0, so the chart install completes and
	// the HR goes Ready with NO engine start). Placed AFTER the
	// cutoverComplete short-circuit so an already-done cutover keeps its
	// idempotent 200.
	//
	// Only source="internal" is gated. The operator CTA path
	// (source="operator", behind RequireSession) and the handover-seal
	// path (source="handover", inside ReceiveTofuArchive where the archive
	// is sealed by definition) are deliberate post-handover actions — gating
	// them would be wrong: handover seals the archive THEN fires the engine,
	// so re-checking the gate it just satisfied is redundant, and a
	// read-after-write race against OpenBao could falsely defer it (issue
	// #933 / #3052 — the bug was that NOTHING fired the engine after the
	// seal, leaving the cutover dormant forever).
	if source == "internal" && requireHandoverForCutover() {
		handedOver, herr := h.sovereignHandoverComplete(ctx)
		if herr != nil {
			return cutoverSpawnResult{}, fmt.Errorf("handover-check-failed: %w", herr)
		}
		if !handedOver {
			h.log.Info("internal cutover trigger deferred: handover not complete",
				"detail", "tofu-phase0-archive absent in OpenBao — Sovereign not yet handed over; auto-trigger is benign pre-handover",
			)
			return cutoverSpawnResult{outcome: cutoverSpawnHandoverIncomplete}, nil
		}
	}

	steps, err := listCutoverSteps(ctx, deps)
	if err != nil {
		return cutoverSpawnResult{}, fmt.Errorf("step-discovery-failed: %w", err)
	}
	if len(steps) == 0 {
		return cutoverSpawnResult{outcome: cutoverSpawnNoSteps}, nil
	}

	bus := h.cutoverBusFor()
	if !bus.tryStartRun() {
		return cutoverSpawnResult{outcome: cutoverSpawnAlreadyRunning}, nil
	}

	// Run the engine with a context independent of the request so a
	// client disconnect doesn't cancel a multi-step cutover. The
	// cutoverStepTimeout on each step bounds the overall runtime.
	//
	// operatorRetry=false (#3379): the in-cluster auto-trigger
	// (source="internal") and the handover-seal path (source="handover") are
	// unattended — a GENUINE prior step failure must fail-closed, not
	// auto-loop. Only the operator-session CTA (HandleCutoverStart) sets
	// operatorRetry=true to re-run a genuinely-failed step. Transient
	// (DeadlineExceeded) failures still re-run via jobFailedTransiently.
	go h.runCutover(context.Background(), deps, steps, false)

	freshStatus, _ := readCutoverStatus(ctx, deps)
	stepNames := make([]string, 0, len(steps))
	for _, s := range steps {
		stepNames = append(stepNames, s.stepName)
	}
	return cutoverSpawnResult{
		outcome:    cutoverSpawnStarted,
		status:     freshStatus,
		stepNames:  stepNames,
		totalSteps: len(steps),
	}, nil
}

// runCutoverFromTrigger is the shared engine-spawn path used by both
// HandleCutoverStart (operator-session) and HandleCutoverInternalTrigger
// (in-cluster SA token). The split keeps cutover.go's HTTP handler
// readable while making the auth-edge swap a single-call site change.
//
// It is now a thin HTTP adapter over spawnCutoverEngine: it maps the typed
// (cutoverSpawnResult, err) back to the EXACT status codes + bodies it
// produced before the engine-spawn core was extracted. Behaviour through
// HandleCutoverStart and HandleCutoverInternalTrigger is unchanged.
func (h *Handler) runCutoverFromTrigger(w http.ResponseWriter, r *http.Request, deps *cutoverDeps, source string) {
	res, err := h.fireCutoverEngine(r.Context(), deps, source)
	if err != nil {
		// Preserve the three distinct 502 error codes by inspecting the
		// wrapped sentinel prefix spawnCutoverEngine attached.
		msg := err.Error()
		code := "status-read-failed"
		switch {
		case strings.HasPrefix(msg, "handover-check-failed:"):
			code = "handover-check-failed"
		case strings.HasPrefix(msg, "step-discovery-failed:"):
			code = "step-discovery-failed"
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  code,
			"detail": errors.Unwrap(err).Error(),
		})
		return
	}

	switch res.outcome {
	case cutoverSpawnAlreadyComplete:
		resp := buildCutoverStatusResponseFromMap(res.status, res.stepNames)
		writeJSON(w, http.StatusOK, resp)
	case cutoverSpawnHandoverIncomplete:
		writeJSON(w, http.StatusTooEarly, map[string]string{
			"error":  "handover-incomplete",
			"detail": "cutover deferred: this Sovereign has not been handed over yet (tofu-phase0-archive not present in OpenBao). The cutover auto-fires post-handover; deferring is expected and benign on a fresh prov.",
		})
	case cutoverSpawnNoSteps:
		writeJSON(w, http.StatusFailedDependency, map[string]string{
			"error":  "no-steps-found",
			"detail": fmt.Sprintf("no cutover-step ConfigMaps found in namespace %q — bp-self-sovereign-cutover not installed?", deps.ns),
		})
	case cutoverSpawnAlreadyRunning:
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "cutover-in-progress",
			"detail": "a cutover is already running on this catalyst-api Pod",
		})
	default: // cutoverSpawnStarted
		resp := buildCutoverStatusResponseFromMap(res.status, res.stepNames)
		resp.TotalSteps = res.totalSteps
		writeJSON(w, http.StatusOK, resp)
	}
}
