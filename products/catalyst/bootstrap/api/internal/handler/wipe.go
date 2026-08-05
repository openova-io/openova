// wipe.go — pre-handover wipe surface (issue #318).
//
// When a provisioning attempt fails (mid-tofu-apply, mid-Phase-1, or just
// because the operator decided to abandon), the wizard's failed-state
// banner exposes a "Cancel & Wipe" button that POSTs here. This handler
// runs the canonical purge sequence:
//
//  1. Cancel any in-flight context (helmwatch informer, current Phase-0
//     runner) for this deployment.
//  2. Run `tofu destroy -auto-approve` against the per-deployment
//     workdir. Idempotent — re-runs on partial state are safe.
//  3. Run a Hetzner force-purge of any resources tagged with
//     `catalyst.openova.io/sovereign=<fqdn>` so anything tofu missed (or
//     anything created out-of-band) is removed. Belt + braces; tofu
//     destroy is the primary path, Hetzner API the safety net.
//  4. Release the PDM allocation row (pool subdomain only). Best-effort:
//     a PDM outage doesn't block local cleanup, the pool-domain-manager
//     operator can force-release later via `pdm-cli` (#319).
//  5. Delete the on-disk record + kubeconfig + tofu workdir.
//  6. Mark the in-memory Deployment Status="wiped" so subsequent GETs
//     return 410 Gone (per the founder's minimum-retention principle —
//     Catalyst-Zero retains nothing operational about a wiped
//     deployment).
//
// All progress streams as SSE events on the same channel as the original
// provisioning + Phase-1 watch, so the wizard's banner can render the
// purge live without a second stream.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (OpenTofu owns Phase 0): tofu
// destroy is the primary purge mechanism; the Hetzner direct-API call in
// step 3 is fallback ONLY for orphans tofu can't see (corrupt state,
// resources created out-of-band by a half-completed cloud-init, etc.).
// We never use the direct API for new resource creation.
//
// #5193 — the purge sequence above (steps 2-6) runs ASYNCHRONOUSLY in a
// detached goroutine (runWipePurge), NOT inside the HTTP request that
// triggered it. WipeDeployment itself only validates + flips
// Status="wiping" + launches the goroutine, then returns 202 Accepted
// immediately. This matters because the console wipe endpoint sits
// behind nginx with a 60s proxy-read-timeout: the old synchronous
// implementation ran `tofu destroy` bound to r.Context(), so nginx
// closing the client connection at 60s cancelled the context and
// SIGKILLed the destroy mid-teardown, stranding the record at
// status=wiping forever (hw268 incident). Poll GET
// /api/v1/deployments/{id} for status=wiped + the `lastWipeReport`
// field (same shape this file used to return synchronously), or GET
// .../events for the live SSE purge log.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"

	// Side-effect import — registers every cloud-provider adapter so
	// providers.Get(dep.Request.Provider) returns a usable instance.
	// Without this, the wipe handler's providers.Get() call returns
	// "unknown provider" in this package's test binary.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
)

// pdmErrNotFound returns the sentinel value to compare against PDM Release
// outcomes. Wrapped in a function so callers can use errors.Is without
// importing the pdm package at every call site.
func pdmErrNotFound() error { return pdm.ErrNotFound }

// wipeMinLifeProtectionEnv — issue #914. Env var override for the
// minimum-age threshold below which an externally-triggered wipe is
// REFUSED when the deployment is still in `phase1-watching`. Operator
// override is the `?force=true` query param on the wipe URL.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4, every threshold is a runtime
// knob. The default (DefaultWipeMinLifeProtection, 30m) is sized
// against the Phase-1 watcher's DefaultWatchTimeout (60m) so a
// converging Sovereign gets at least half the watch budget before any
// external wipe can destroy it. otech106 incident, 2026-05-05.
const wipeMinLifeProtectionEnv = "CATALYST_WIPE_MIN_LIFE_PROTECTION"

// DefaultWipeMinLifeProtection — production default for the
// minimum-life guard. Issue #914.
//
// 30 minutes covers the worst-observed bp-catalyst-platform install
// window (15m install timer × multiple retries) plus headroom for the
// 4-component install cascade (keycloak/openbao/powerdns/crossplane all
// in their 15m windows simultaneously, which is exactly the otech106
// snapshot). Below this age a still-converging deployment must be
// protected from accidental external wipes — operator can override via
// `?force=true` when they really mean it.
const DefaultWipeMinLifeProtection = 30 * time.Minute

// compileWipeMinLifeProtection — small parse helper. Returns
// DefaultWipeMinLifeProtection for empty / unparseable / non-positive
// input.
func compileWipeMinLifeProtection(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultWipeMinLifeProtection
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultWipeMinLifeProtection
	}
	return d
}

// wipeMinLifeProtectionFor returns the effective protection threshold
// for this Handler. Test override (h.wipeMinLifeProtection) wins;
// production reads env → default.
func (h *Handler) wipeMinLifeProtectionFor() time.Duration {
	if h.wipeMinLifeProtection > 0 {
		return h.wipeMinLifeProtection
	}
	return compileWipeMinLifeProtection(envOrEmpty(wipeMinLifeProtectionEnv))
}

// shouldRefuseWipe — pure decision function for issue #914's
// minimum-life guard. Returns true when the wipe MUST be refused with
// 409 because the deployment is still mid-converge.
//
// The four-input contract is intentional so tests can exercise each
// branch deterministically without standing up a Handler:
//
//   - status            — current dep.Status snapshot
//   - startedAt         — dep.StartedAt snapshot (zero = unknown)
//   - now               — wall-clock at decision time
//   - threshold         — minimum-life protection threshold
//   - force             — operator override flag (?force=true)
//
// Refuse iff status is `phase1-watching` AND startedAt is non-zero AND
// (now - startedAt) < threshold AND force is false. Every other shape
// falls through to allow the wipe — including a finished deployment, a
// failed deployment, a missing startedAt (legacy record from before the
// field was stamped — can't enforce a threshold we have no anchor for),
// or an explicit force override.
func shouldRefuseWipe(status string, startedAt, now time.Time, threshold time.Duration, force bool) bool {
	if force {
		return false
	}
	if status != "phase1-watching" {
		return false
	}
	if startedAt.IsZero() {
		// Legacy record — without an anchor we can't compute age.
		// Allow the wipe; the operator's intent is clearer than the
		// guard's heuristic.
		return false
	}
	age := now.Sub(startedAt)
	return age < threshold
}

// wipeRequest is the body of POST /api/v1/deployments/{id}/wipe.
//
// HetznerToken is required ONLY when the on-disk Deployment record's
// Request.HetznerToken has been GC'd (the field is intentionally cleared
// after writeTfvars per the credential-hygiene principle). The wizard
// re-prompts the operator for the token in the Cancel & Wipe modal, so
// the value survives just long enough to drive `tofu destroy` + the
// Hetzner orphan purge, then is forgotten.
//
// ObjectStorageAccessKey / ObjectStorageSecretKey / ObjectStorageRegion
// mirror the same pattern (issue #166): the on-disk Deployment record
// strips S3 credentials at Save() time per the credential-hygiene
// principle, so after a catalyst-api Pod restart `dep.Request` carries
// EMPTY S3 creds and the bucket purge in WipeDeployment silently
// skipped — leaking 10+ orphan buckets in the field (one per wiped
// provision back to prov #11, observed 2026-05-11). The wizard MUST
// re-prompt the operator for the same triplet it captured at provision
// time and pass it on the wipe body; the handler then prefers body
// values over `dep.Request` so the purge runs whether or not the Pod
// has rolled. Wire shape mirrors the existing HetznerToken sibling.
//
// TODO(#166-followup): consider Option B (per-deployment K8s Secret
// holding S3 creds, reaped on wipe) if a future security review
// objects to the operator re-prompt model. Option A is shipped today
// because it matches the canonical HetznerToken pattern and survives
// Pod restarts with zero extra storage.
type wipeRequest struct {
	HetznerToken           string `json:"hetznerToken"`
	ObjectStorageAccessKey string `json:"objectStorageAccessKey,omitempty"`
	ObjectStorageSecretKey string `json:"objectStorageSecretKey,omitempty"`
	ObjectStorageRegion    string `json:"objectStorageRegion,omitempty"`

	// Wave 5.130 (hw30 fix-forward 2026-05-27): typed Huawei creds.
	// Replaces the legacy `hetznerToken`/`objectStorageSecretKey`
	// overload that Wave 3 introduced for the Cancel & Wipe modal.
	// All three sources (typed body, legacy body, in-memory depReq,
	// per-deployment tfvars on PVC) feed buildWipeCredsRaw via
	// firstNonEmpty so the wipe handler always finds creds whether
	// the operator re-prompted via the wizard, the Pod has them in
	// memory, OR they're only on disk.
	HuaweiAccessKey string `json:"huaweiAccessKey,omitempty"`
	HuaweiSecretKey string `json:"huaweiSecretKey,omitempty"`
	HuaweiProjectID string `json:"huaweiProjectId,omitempty"`
	HuaweiRegion    string `json:"huaweiRegion,omitempty"`
}

// wipeResponse summarises what was actually purged. The wizard renders
// the counts in a "Wipe complete — N servers, M load balancers, …
// removed" success banner.
//
// Wave 3 / Issue #2140 — wire shape renamed from `hetznerPurge` to
// `providerPurge` (per-resource-kind map) to match the cross-provider
// shape providers.WipeResult.ProviderPurge emits. The map is keyed
// by canonical resource-kind ("servers", "load_balancers",
// "networks", "firewalls", "ssh_keys", "s3_buckets", "volumes",
// "primary_ips", "floating_ips") so the UI can render the same
// banner regardless of underlying cloud — Hetzner emits its native
// kinds; Huawei emits ECS / EIP / SG / VPC under the same map keys
// ("servers" / "floating_ips" / "firewalls" / "networks"). The
// `provider` field surfaces which adapter ran the purge so the UI
// can label the banner correctly.
type wipeResponse struct {
	DeploymentID  string              `json:"deploymentId"`
	SovereignFQDN string              `json:"sovereignFQDN"`
	Provider      string              `json:"provider,omitempty"`
	TofuDestroyed bool                `json:"tofuDestroyed"`
	ProviderPurge map[string][]string `json:"providerPurge"`
	S3Buckets     []string            `json:"s3Buckets,omitempty"`
	PDMReleased   bool                `json:"pdmReleased"`
	LocalCleaned  bool                `json:"localCleaned"`
	Errors        []string            `json:"errors"`
	WipedAt       string              `json:"wipedAt"`

	// G103 (Refs #2670) — post-wipe orphan-verification surface.
	// VerifiedZeroOrphans == true means the provider ran a post-wipe
	// scan AND found zero catalyst-* resources (bastion-* excluded).
	// This is the canonical "wipe was complete" signal the operator
	// admin console + the G104 (Refs #2671) CI zero-touch gate read.
	//
	// When false, ResidualOrphans carries the per-resource-kind names
	// of every survivor so the operator (or the gate) can see exactly
	// what survived without re-scanning the cloud account.
	VerifiedZeroOrphans bool                `json:"verifiedZeroOrphans"`
	ResidualOrphans     map[string][]string `json:"residualOrphans,omitempty"`
}

// providerPurgeTotal sums every per-resource-kind slice in a
// providers.WipeResult.ProviderPurge map for the SSE log banner.
func providerPurgeTotal(p map[string][]string) int {
	n := 0
	for _, v := range p {
		n += len(v)
	}
	return n
}

// resolveProviderName returns the canonical provider name for a
// deployment. Walks the per-region Provider field (the canonical
// source on the existing wizard payload — every RegionSpec carries
// its own provider per docs/ARCHITECTURE.md). Defaults to "hetzner"
// for legacy records that pre-date the per-region payload.
func resolveProviderName(dep *Deployment) string {
	for _, r := range dep.Request.Regions {
		if r.Provider != "" {
			return strings.ToLower(strings.TrimSpace(r.Provider))
		}
	}
	return "hetzner"
}

// WipeDeployment handles POST /api/v1/deployments/{id}/wipe.
//
// Response codes:
//   - 202 Accepted once validation passes and the purge goroutine has
//     been launched (#5193 — the actual tofu destroy + orphan sweep +
//     DNS/PDM/local cleanup runs ASYNCHRONOUSLY in runWipePurge, never
//     inside this request). Poll GET /api/v1/deployments/{id} for
//     status=wiped + the `lastWipeReport` field (the same shape this
//     endpoint used to return synchronously in a 200 body), or GET
//     .../events for the live SSE purge log.
//   - 400 Bad Request when the body cannot be parsed, or (huawei) no
//     credential source resolves (typed body, legacy body, in-memory
//     request, per-deployment PVC tfvars, or the huawei-operator-creds
//     env fallback — see the provHint switch below).
//   - 404 Not Found when the deployment id is unknown
//   - 409 Conflict if a purge goroutine is ALREADY running for this
//     deployment in this process (dep.wipeInFlight==true), OR (issue
//     #914) if the deployment is still in `phase1-watching` AND
//     younger than wipeMinLifeProtection (default 30m). The 409 body
//     for the latter carries `retryAfterSec` + `minLifeSec` so the
//     wizard can render a "wait N minutes" countdown. Operator
//     override: `?force=true` query param.
//
// The endpoint is idempotent, including re-wipe of a STRANDED `wiping`
// record (#5193): a Status=="wiping" row whose owning goroutine died
// with a prior catalyst-api process (Pod roll, or — pre-#5193 — nginx's
// 60s proxy-timeout killing a request-bound destroy) is NOT protected
// by the single-flight guard, because wipeInFlight is in-memory-only
// and always starts false on a freshly rehydrated Deployment. A fresh
// POST .../wipe on such a record simply resumes/re-runs the destroy.
func (h *Handler) WipeDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	// Issue #689 — ownership check before allowing the destructive wipe.
	// 404 (not 403) on mismatch so a hostile probe can't enumerate which
	// deployment ids exist by walking 403 vs 404 responses.
	if !h.checkOwnership(w, r, dep) {
		return
	}

	// Parse body — the wizard re-prompts for the Hetzner token because
	// catalyst-api intentionally GCs it from the in-memory Request after
	// writeTfvars. Without it tofu destroy + the orphan purge can't
	// authenticate.
	var body wipeRequest
	if err := decodeJSONBody(r, &body); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Wave 5.130 (hw30 fix-forward 2026-05-27): for Huawei deployments
	// the canonical creds resolution order is (1) typed body fields,
	// (2) in-memory depReq, (3) the per-deployment tofu.auto.tfvars.json
	// on PVC (which catalyst-api wrote during provision and survives
	// Pod restart unconditionally), (4) — #5193 — the `huawei-operator-
	// creds` Secret projected into the catalyst-api Pod as
	// CATALYST_HUAWEI_ACCESS_KEY/_SECRET_KEY/_PROJECT_ID/_REGION, the
	// SAME in-cluster fallback the janitor's discoverHuaweiCreds
	// (janitor.go:584) already uses for the project-wide orphan sweep.
	// If ALL FOUR sources are empty, THEN refuse the wipe with a typed
	// error. Otherwise let the wipe proceed and let buildWipeCredsRaw +
	// the provider adapter do the rest. For Hetzner the legacy
	// `hetznerToken` is still the only path until the wizard ships a
	// typed shape.
	//
	// #5193 root cause: a partial destroy (killed mid-teardown by the
	// nginx 60s proxy-timeout — see runWipePurge) deletes the per-
	// deployment tfvars off the PVC. If the catalyst-api Pod has also
	// rolled since (dep.Request.Huawei* GC'd from memory) and the
	// operator re-fires the wipe with an empty body, sources (1)-(3)
	// are ALL empty even though an environment the platform
	// created must always be wipeable — the operator-creds Secret is
	// right there in the catalyst ns. Without this fallback the wipe
	// 400s "credentials are required" forever, stranding the record at
	// status=wiping and blocking the one-environment-at-a-time gate.
	provHint := strings.ToLower(strings.TrimSpace(dep.Request.Provider))
	if provHint == "" {
		provHint = "hetzner"
	}
	switch provHint {
	case "huawei":
		// Try to harvest from PVC tfvars when body fields are empty.
		if strings.TrimSpace(body.HuaweiAccessKey) == "" || strings.TrimSpace(body.HuaweiSecretKey) == "" {
			if hw, ok := loadHuaweiCredsFromTfvars(filepath.Join(h.tofuWorkDir(), id)); ok {
				if strings.TrimSpace(body.HuaweiAccessKey) == "" {
					body.HuaweiAccessKey = hw.AccessKey
				}
				if strings.TrimSpace(body.HuaweiSecretKey) == "" {
					body.HuaweiSecretKey = hw.SecretKey
				}
				if strings.TrimSpace(body.HuaweiProjectID) == "" {
					body.HuaweiProjectID = hw.ProjectID
				}
				if strings.TrimSpace(body.HuaweiRegion) == "" {
					body.HuaweiRegion = hw.Region
				}
			}
		}
		// #5193 — operator-creds env fallback. Only fires when body +
		// tfvars + in-memory dep.Request are still empty for the
		// authenticating pair; body/tfvars-supplied values always win.
		if firstNonEmpty(body.HuaweiAccessKey, dep.Request.HuaweiAccessKey) == "" ||
			firstNonEmpty(body.HuaweiSecretKey, dep.Request.HuaweiSecretKey) == "" {
			if hw, ok := huaweiOperatorCredsFromEnv(); ok {
				if strings.TrimSpace(body.HuaweiAccessKey) == "" {
					body.HuaweiAccessKey = hw.AccessKey
				}
				if strings.TrimSpace(body.HuaweiSecretKey) == "" {
					body.HuaweiSecretKey = hw.SecretKey
				}
				if strings.TrimSpace(body.HuaweiProjectID) == "" {
					body.HuaweiProjectID = hw.ProjectID
				}
				if strings.TrimSpace(body.HuaweiRegion) == "" {
					body.HuaweiRegion = hw.Region
				}
			}
		}
		if firstNonEmpty(body.HuaweiAccessKey, body.HetznerToken, dep.Request.HuaweiAccessKey) == "" ||
			firstNonEmpty(body.HuaweiSecretKey, body.ObjectStorageSecretKey, dep.Request.HuaweiSecretKey) == "" {
			http.Error(w, "huawei credentials are required (huaweiAccessKey + huaweiSecretKey in body, or already on PVC tfvars, or CATALYST_HUAWEI_ACCESS_KEY/_SECRET_KEY env on catalyst-api)", http.StatusBadRequest)
			return
		}
	default:
		if strings.TrimSpace(body.HetznerToken) == "" {
			http.Error(w, "hetznerToken is required (re-prompt the operator before calling this endpoint)", http.StatusBadRequest)
			return
		}
	}

	// Issue #914 — minimum-life guard against externally-triggered
	// wipes that destroy still-converging Sovereigns.
	//
	// otech106.omani.works (2026-05-05) was 28/40 components installed,
	// 4 actively installing in their 15m install windows, when an
	// external POST /wipe at T+24m destroyed it. Same shape as B2 #910
	// (orchestrator marking deployment FAILED prematurely) but on the
	// WIPE path. Whatever path triggered it (stale browser tab,
	// decommission button on adjacent deployment, watchdog goroutine),
	// the result is data destruction without warning.
	//
	// Guard logic: when status is `phase1-watching` AND the deployment
	// is younger than wipeMinLifeProtection (default 30m, runtime-
	// configurable per docs/INVIOLABLE-PRINCIPLES.md #4), refuse the
	// wipe with 409 + a clear retry_after_sec hint. Operator can
	// override with `?force=true` query param when they really mean
	// it (after reading the wizard banner that says "this Sovereign is
	// still converging — wait N minutes or click Force Wipe").
	//
	// Audit log emitted unconditionally so future incidents have a
	// single grep target — see [WIPE-AUDIT] tag.
	force := r.URL.Query().Get("force") == "true"
	dep.mu.Lock()
	depStatus := dep.Status
	depStartedAt := dep.StartedAt
	depFQDN := dep.Request.SovereignFQDN
	dep.mu.Unlock()

	now := time.Now()
	age := now.Sub(depStartedAt)
	threshold := h.wipeMinLifeProtectionFor()
	refuse := shouldRefuseWipe(depStatus, depStartedAt, now, threshold, force)

	h.log.Info("[WIPE-AUDIT] wipe request received",
		"id", id,
		"sovereignFQDN", depFQDN,
		"status", depStatus,
		"startedAt", depStartedAt.UTC().Format(time.RFC3339),
		"ageSec", int(age.Seconds()),
		"thresholdSec", int(threshold.Seconds()),
		"refuse", refuse,
		"force", force,
		"remoteAddr", r.RemoteAddr,
	)

	if refuse {
		retryAfter := int((threshold - age).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":         "deployment is still converging — wipe refused to protect a mid-provision Sovereign",
			"deploymentId":  id,
			"sovereignFQDN": depFQDN,
			"status":        depStatus,
			"startedAt":     depStartedAt.UTC().Format(time.RFC3339),
			"ageSec":        int(age.Seconds()),
			"minLifeSec":    int(threshold.Seconds()),
			"retryAfterSec": retryAfter,
			"hint":          "wait for status to leave 'phase1-watching', or re-issue with ?force=true to override (only use force if you truly intend to destroy a still-converging Sovereign — see issue #914)",
		})
		return
	}

	// Single-flight guard — #5193 hardened for idempotent re-wipe.
	// Refuse ONLY when a goroutine in THIS process is genuinely running
	// the purge right now (wipeInFlight==true). A record whose Status
	// is "wiping" but wipeInFlight is false is a STRANDED wipe —
	// abandoned by a catalyst-api Pod roll mid-destroy, or (the
	// primary #5193 defect, fixed below by runWipePurge's detached
	// context) killed by nginx's 60s proxy-timeout tearing down a
	// request-bound destroy. Both used to leave the record un-wipeable
	// via the old status-string-only guard, which blocked the
	// one-environment-at-a-time fresh-prov gate forever. Re-firing the
	// wipe on a stranded record now simply resumes/re-runs the destroy
	// — idempotent, per the doc comment at the top of this file.
	dep.mu.Lock()
	if dep.wipeInFlight {
		dep.mu.Unlock()
		http.Error(w, "wipe already in progress for this deployment", http.StatusConflict)
		return
	}
	prevStatus := dep.Status
	dep.Status = "wiping"
	dep.wipeInFlight = true
	dep.mu.Unlock()

	// Re-open the events channel for the wipe phase. The previous one
	// (from provisioning) may have already been closed by the Phase-1
	// watch goroutine on its terminal exit (deployments.go:575) — Go
	// has no portable check-without-receive for `closed`, and a CLOSED
	// channel is non-nil, so the prior `if dep.eventsCh == nil` guard
	// silently kept a dead channel and the first emit() send panicked
	// with "send on closed channel" (wipe.go:156).
	//
	// Always replace the channel here. Any stragglers reading from the
	// old channel will see end-of-stream and exit naturally; the wipe
	// emit goroutine writes to the fresh channel.
	dep.mu.Lock()
	dep.eventsCh = make(chan provisioner.Event, 256)
	dep.mu.Unlock()

	// #5193 — respond immediately; the purge itself runs in a detached
	// goroutine (mirrors CreateDeployment's `go h.runProvisioning(dep)`
	// pattern, deployments.go:1498). This is the primary fix: the OLD
	// code ran the entire purge sequence synchronously inside this HTTP
	// request with `tofuCtx` rooted in `r.Context()`. nginx's 60s
	// proxy-timeout closes the client-facing connection mid-destroy;
	// Go's server then cancels r.Context() because the peer connection
	// closed, which cancelled tofuCtx and SIGKILLed the `tofu destroy`
	// subprocess partway through (region-a destroyed, region-b/EIPs/
	// VPCs/S3 left behind — the hw268 incident). runWipePurge below
	// roots every internal context in context.Background() instead, so
	// a slow or disconnected client can never abort an in-flight
	// destroy again. The wizard (or any client) polls
	// GET /api/v1/deployments/{id} — `status` flips wiping → wiped, and
	// `lastWipeReport` carries the same detail the old synchronous
	// response used to return directly in its body.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"deploymentId":  id,
		"sovereignFQDN": dep.Request.SovereignFQDN,
		"status":        "wiping",
		"message":       "wipe started; poll GET /api/v1/deployments/" + id + " for status=wiped, or GET .../events for the live purge log",
	})

	go h.runWipePurge(dep, id, body, prevStatus)
}

// runWipePurge runs the full destructive purge sequence for a wipe —
// tofu destroy, provider orphan sweep, DNS teardown, PDM release, and
// local-state cleanup — detached from the HTTP request that triggered
// it (#5193). WipeDeployment launches this in its own goroutine and
// responds 202 immediately; every context created in here is rooted in
// context.Background(), never r.Context(), so a client disconnect (the
// nginx 60s proxy-timeout, a dropped wizard tab, a catalyst-api Pod
// roll of some OTHER request) can never cancel this purge.
//
// Always clears dep.wipeInFlight on return (success or failure) so a
// stranded `wiping` record — one whose owning goroutine died with a
// prior catalyst-api process — is always re-wipeable via a fresh
// POST .../wipe; the single-flight guard in WipeDeployment only blocks
// a genuinely-running goroutine, never a bare status string left over
// from a dead one.
func (h *Handler) runWipePurge(dep *Deployment, id string, body wipeRequest, prevStatus string) {
	defer func() {
		dep.mu.Lock()
		dep.wipeInFlight = false
		dep.mu.Unlock()
	}()

	providerName := resolveProviderName(dep)
	report := wipeResponse{
		DeploymentID:  id,
		SovereignFQDN: dep.Request.SovereignFQDN,
		Provider:      providerName,
		ProviderPurge: map[string][]string{},
		WipedAt:       time.Now().UTC().Format(time.RFC3339),
	}

	emit := func(phase, level, msg string) {
		ev := provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   phase,
			Level:   level,
			Message: msg,
		}
		dep.recordEvent(ev)
		// Wave 5.135: defense-in-depth — recover from "send on
		// closed channel" if a concurrent close raced us. The
		// runProvisioning skip-close (deployments.go) is the
		// primary fix; this recover handles any other reaper
		// path that may have closed the channel.
		defer func() { _ = recover() }()
		dep.mu.Lock()
		ch := dep.eventsCh
		dep.mu.Unlock()
		if ch == nil {
			return
		}
		select {
		case ch <- ev:
		default:
		}
	}
	emit("wipe", "info", "Wipe initiated for "+dep.Request.SovereignFQDN+" (was: "+prevStatus+")")

	// Cancel the per-deployment helmwatch.Watcher BEFORE we destroy the
	// underlying apiserver. Issue #156: previously this site relied on
	// the watcher exiting "naturally" once `tofu destroy` removed the
	// apiserver — but the dynamicinformer's Reflector keeps reconnecting
	// against the cached CA bundle on the destroyed control-plane IP,
	// spamming `x509: certificate signed by unknown authority` hundreds
	// of times per second for hours. The leaked goroutines burn CPU,
	// hide real errors in catalyst-api logs, and (worst of all) hold
	// stale Indexer state that subsequent fresh provisions inherit
	// through the SSE event stream — exactly the "event stream stale at
	// T+18 min" symptom Fix #151 surfaced on prov #25.
	//
	// Watcher.Cancel is safe pre-Start, post-terminate, and twice in a
	// row; we read the pointer under dep.mu to race against the
	// phase1_watch goroutine clearing it on its terminal exit.
	dep.mu.Lock()
	live := dep.liveWatcher
	// #5600 — drop the cached LIVE region census too. It is derived from a
	// cluster that is about to be destroyed; a re-provision under the same
	// deployment id must re-read, never inherit the previous cluster's counts.
	invalidateLiveRegionCensusLocked(dep)
	dep.mu.Unlock()
	if live != nil {
		live.Cancel()
		emit("wipe", "info", "phase-1 watcher cancelled to stop reflector spam against the about-to-be-destroyed apiserver")
	}

	// Tear down the per-Sovereign k8scache informer set so its
	// reflectors don't keep retrying against the destroyed apiserver
	// either. Issue #156. Same canonical seam as Watcher.Cancel above:
	// stop the goroutines BEFORE Hetzner cleanup makes the IP a
	// sinkhole. Idempotent + nil-tolerant — production wires k8sCache
	// via main.go; tests building Handler{} directly leave it nil.
	//
	// #5285 — use RemoveDeployment (not RemoveCluster) so BOTH the bare
	// `<id>` primary AND every `<id>-<region>` secondary reflector set is
	// torn down. The bare-id RemoveCluster missed the secondary-region
	// clusters, which then kept 404/x509-flooding against their destroyed
	// apiservers. Then clear any terminal-failed quarantine for this id
	// (the kubeconfig files are removed later in this wipe, so nothing will
	// resurrect it) to keep the quarantine set bounded.
	if h.k8sCache != nil {
		h.k8sCache.RemoveDeployment(id)
		h.k8sCache.UnquarantineDeployment(id)
		emit("wipe", "info", "k8scache informer set removed for "+id)
	}

	// Steps 1-2b — dispatch the full purge sequence (tofu destroy +
	// orphan sweep + object-storage bucket purge) through the
	// providers.CloudProvider seam. The adapter owns ordering +
	// resource-kind enumeration; the handler stays cloud-agnostic.
	//
	// Wave 3 / Issue #2140: same shape Hetzner + Huawei both
	// implement. Future AWS / GCP / Azure adapters get the same
	// dispatch automatically — the handler does not change.
	//
	// Credential resolution mirrors the legacy single-provider path:
	// REQUEST BODY values win over the in-memory dep.Request fallback
	// (which itself survives a Pod restart only when the wizard didn't
	// re-prompt before submit). The provider-specific Creds map below
	// resolves both layers so adapter.Wipe() gets a clean union.
	wipeReq := dep.Request
	wipeReq.HetznerToken = body.HetznerToken

	prov := provisioner.New()
	// #5193 — rooted in context.Background(), NOT the (now long-since-
	// returned) HTTP request context. This is what lets `tofu destroy`
	// run to completion even though nginx's 60s proxy-timeout closes
	// the original client connection almost immediately (the handler
	// already responded 202 before this goroutine was even launched).
	tofuCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Wave 3 — the provider adapter owns tofu destroy + orphan sweep
	// + bucket purge end-to-end. The Wipe() method below returns a
	// WipeResult with per-resource-kind ProviderPurge map and the
	// list of removed S3 buckets; we project those into the legacy
	// wire-shape so the wizard's existing SSE+banner code reads them
	// without further change.
	cp, perr := providers.Get(providerName)
	if perr != nil {
		report.Errors = append(report.Errors, "providers.Get("+providerName+"): "+perr.Error())
		emit("wipe", "error", "no adapter registered for provider "+providerName+" — falling back to in-process tofu destroy only")
		// Best-effort fallback: tofu destroy still runs from the
		// shared provisioner.
		if err := prov.Destroy(tofuCtx, wipeReq, dep.eventsCh); err != nil {
			report.Errors = append(report.Errors, "tofu destroy: "+err.Error())
		} else {
			report.TofuDestroyed = true
		}
	} else {
		// #4677 — DRAIN-BEFORE-DESTROY. Before tofu destroy tears down the nodes,
		// delete the cluster's LoadBalancer Services + PVCs so the in-cluster
		// CCM/CSI controllers release their cloud resources (LBs, EIPs, EVS
		// volumes). Without this, `tofu destroy` orphans every CSI `pvc-<uuid>`
		// volume (they are not in tofu state) — the ~5.8TB-leak root cause.
		// Best-effort: an unreachable (already-dead) cluster no-ops and the wipe
		// proceeds; the post-destroy cloud-GC backstops by detached-status.
		if h.kubeconfigsDir != "" {
			if kcRaw, rerr := os.ReadFile(filepath.Join(h.kubeconfigsDir, id+".yaml")); rerr == nil {
				emit("wipe", "info", "drain-before-destroy: releasing CSI volumes + LoadBalancers before tofu destroy (#4677)")
				_ = drainClusterCloudResources(tofuCtx, kcRaw, func(msg string) {
					emit("wipe", "info", msg)
				})
			}
		}

		credsRaw := buildWipeCredsRaw(providerName, body, dep.Request)

		// #5135 / #5028 — WORKDIR-CRED FALLBACK for the post-destroy EVS
		// backstop. On the canonical body-less wipe (`{}`) AFTER a
		// catalyst-api Pod roll, dep.Request's Huawei AK/SK are gone (secrets
		// are GC'd from the in-memory Request post-writeTfvars and do not
		// survive a restart), so buildWipeCredsRaw yields empty
		// access_key/secret_key here even though providerName resolves to
		// "huawei" from the per-region spec. tofu destroy still authenticates
		// (the provider reads the workdir tfvars directly), but
		// huaweiSweepCredsFromRaw would return ok=false below and the #5028
		// EVS backstop would SILENTLY SKIP — stranding every CSI `pvc-*`
		// volume as status=available toward the HCS quota (413
		// VolumeLimitExceeded on the next fresh prov). Recover the creds from
		// the durable per-deployment tofu.auto.tfvars.json on the
		// catalyst-api-deployments PVC while the workdir still exists (tofu
		// destroy may remove it below). Only empty keys are filled —
		// body/request-provided values always win.
		if applyWorkdirEVSCredsFallback(providerName, credsRaw, filepath.Join(h.tofuWorkDir(), id)) {
			emit("wipe", "info", "huawei: #5028 EVS backstop creds recovered from workdir tofu.auto.tfvars.json (body/request creds empty after Pod roll) (#5135)")
		}

		wipeSpec := providers.WipeSpec{
			DeploymentID:  dep.ID,
			SovereignFQDN: dep.Request.SovereignFQDN,
			Creds:         providers.ProviderCreds{Raw: credsRaw},
		}
		wipeRes, werr := cp.Wipe(tofuCtx, wipeSpec, func(msg string) {
			emit("wipe", "info", providerName+": "+msg)
		})
		if werr != nil {
			report.Errors = append(report.Errors, providerName+" wipe: "+werr.Error())
		}
		if wipeRes != nil {
			report.TofuDestroyed = wipeRes.TofuDestroyed
			report.ProviderPurge = wipeRes.ProviderPurge
			report.S3Buckets = wipeRes.S3Buckets
			// G103 (Refs #2670) — propagate the post-wipe orphan-
			// verification result so the operator console + the G104
			// CI gate see whether the wipe contract was honoured.
			report.VerifiedZeroOrphans = wipeRes.VerifiedZeroOrphans
			report.ResidualOrphans = wipeRes.ResidualOrphans
			if len(wipeRes.Errors) > 0 {
				report.Errors = append(report.Errors, wipeRes.Errors...)
			}
		}

		// #5028 — POST-DESTROY EVS BACKSTOP. tofu destroy + the provider
		// orphan-purge remove IaC-declared infra, but NOT the CSI-provisioned
		// `pvc-*` EVS volumes (no dep name → invisible to the name-prefix
		// purge). The #4677 drain releases them only when the cluster was
		// still reachable; on an already-dead env it no-ops and the volumes
		// strand as status=available, filling the HCS 400-volume quota after
		// ~3 wipes and hard-blocking the next fresh prov's PVCs (413
		// VolumeLimitExceeded). This is the "post-destroy cloud-GC backstop"
		// wipe_drain.go promised: reap THIS dep's detached `pvc-*` volumes
		// (throttled + 429-backoff), protecting every OTHER live dep.
		if strings.EqualFold(providerName, "huawei") {
			if hp := h.huaweiProvider("post-wipe-EVS"); hp != nil {
				if creds, ok := huaweiSweepCredsFromRaw(credsRaw); ok {
					reaped := h.reapDeploymentEVSBackstop(tofuCtx, hp, id, creds, func(msg string) {
						emit("wipe", "info", "huawei: post-destroy EVS backstop: "+msg)
					})
					if reaped > 0 {
						emit("wipe", "info", "huawei: post-destroy EVS backstop reaped "+itoa(reaped)+" detached CSI volume(s) tofu destroy left orphaned (#5028)")
					}
				}
			}
		}
	}

	if providerPurgeTotal(report.ProviderPurge) > 0 {
		emit("wipe", "info", providerName+" orphan purge removed "+itoa(providerPurgeTotal(report.ProviderPurge))+" resource(s) across kinds: "+kindCountSummary(report.ProviderPurge))
	} else {
		emit("wipe", "info", providerName+" orphan purge: nothing to remove (clean account)")
	}

	// G103 (Refs #2670) — surface the post-wipe verification verdict
	// on the SSE stream so the operator + the G104 zero-touch gate see
	// the canonical contract signal alongside the purge totals.
	if report.VerifiedZeroOrphans {
		emit("wipe", "info", providerName+" G103 verification: zero orphans — wipe contract honoured")
	} else if residualTotal := providerPurgeTotal(report.ResidualOrphans); residualTotal > 0 {
		emit("wipe", "error", providerName+" G103 verification: "+itoa(residualTotal)+" catalyst-* resource(s) survived wipe (bastion-* excluded): "+kindCountSummary(report.ResidualOrphans))
	}

	// Step 2b — sovereign DNS teardown (#4732 item 7). The prov wrote the
	// canonical per-Sovereign A records (console/api/marketplace/… + apex)
	// into EVERY parent zone via upsertSovereignParentZoneRecords; nothing
	// deleted them on wipe, so a wiped env's `console.<fqdn>` kept
	// resolving a released EIP (verified live: console.hw217.omani.works
	// a full day post-wipe — the exact stale-record class #1505 was built
	// to prevent on the WRITE side). Best-effort + idempotent: a DNS
	// failure logs and never blocks the purge or record cleanup.
	{
		fqdn := dep.Request.SovereignFQDN
		parents := make([]string, 0, len(dep.Request.ParentDomains))
		for _, pd := range dep.Request.ParentDomains {
			if pd.Name != "" {
				parents = append(parents, pd.Name)
			}
		}
		if len(parents) == 0 && fqdn != "" {
			if idx := strings.IndexByte(fqdn, '.'); idx > 0 {
				parents = append(parents, fqdn[idx+1:])
			}
		}
		dnsCtx, dnsCancel := context.WithTimeout(context.Background(), 60*time.Second)
		for _, parent := range parents {
			// WithFallback mirrors the write-side 404 retry: a BYO Sovereign's
			// ParentDomains[0].Name is the FQDN itself, whose authoritative zone
			// is the tail label — so a DELETE against the sub-FQDN 404s and the
			// records (incl. console.<fqdn>) survive unless we retry the tail
			// (#4764). Surfaced at ERROR (not warn) so a genuinely leaked record
			// is a visible wipe error, not a silently-swallowed line.
			if err := h.deleteSovereignParentZoneRecordsWithFallback(dnsCtx, fqdn, parent); err != nil {
				report.Errors = append(report.Errors, "sovereign dns teardown ("+parent+"): "+err.Error())
				emit("wipe", "error", "sovereign DNS teardown FAILED for zone "+parent+" — stale records may linger and resolve a released EIP; re-run the wipe (idempotent) or delete them by hand: "+err.Error())
			} else {
				emit("wipe", "info", "sovereign DNS records deleted from parent zone "+parent)
			}
		}
		dnsCancel()
	}

	// Step 3 — PDM release (pool-subdomain only). Resolve pool + subdomain
	// from either the Deployment record (set during the reservation step)
	// or from the FQDN as a fallback.
	//
	// Refs #3728 — three hardening changes vs. the prior best-effort call:
	//   1. Guard h.pdm == nil (symmetric with ReleaseSubdomain at line ~840
	//      and the reserve path) — a wipe on a catalyst-api without
	//      POOL_DOMAIN_MANAGER_URL must not nil-deref, and must NOT silently
	//      forget the deployment with the pool row still active.
	//   2. ReleaseWithRetry on a BACKGROUND context — an `active`
	//      pool_allocations row never self-heals (the sweeper only reclaims
	//      expired *reserved* rows), so a single transient PDM failure
	//      orphans the subdomain permanently and 409s every re-fire. The
	//      release context is deliberately independent of r.Context() so a
	//      client disconnect after the long (≤30m) cloud purge can't starve
	//      it with `context canceled`.
	//   3. When the release still fails after retries, set pdmReleaseFailed
	//      so the finalize step SKIPS deleting the on-disk record + reaping
	//      the in-memory row — keeping ReleaseSubdomain (DELETE
	//      …/release-subdomain) a viable manual recovery instead of leaving
	//      an orphan no actor knows to clean up.
	poolDomain, subdomain := dep.pdmPoolDomain, dep.pdmSubdomain
	if poolDomain == "" || subdomain == "" {
		// Fallback: split sovereignFQDN at the first dot.
		if idx := strings.IndexByte(dep.Request.SovereignFQDN, '.'); idx > 0 {
			subdomain = dep.Request.SovereignFQDN[:idx]
			poolDomain = dep.Request.SovereignFQDN[idx+1:]
		}
	}
	pdmReleaseFailed := false
	if dep.Request.SovereignDomainMode == "pool" && poolDomain != "" && subdomain != "" {
		if h.pdm == nil {
			pdmReleaseFailed = true
			report.Errors = append(report.Errors, "pdm release skipped: pool-domain-manager client is not configured")
			emit("wipe", "warn", "PDM client not configured — pool allocation for "+subdomain+"."+poolDomain+" NOT released; on-disk record retained for retry via DELETE /release-subdomain")
		} else {
			// Background context — independent of the inbound HTTP request so
			// an operator-console disconnect during the cloud purge cannot
			// cancel the release. 90s covers up to 5 retry attempts of the
			// 15s-timeout PDM client.
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 90*time.Second)
			if err := h.pdm.ReleaseWithRetry(releaseCtx, poolDomain, subdomain, pdm.CommitRetryConfig{}); err != nil {
				pdmReleaseFailed = true
				report.Errors = append(report.Errors, "pdm release: "+err.Error())
				emit("wipe", "warn", "PDM release failed after retries — on-disk record retained for retry via DELETE /release-subdomain (or operator pdm-cli force-release): "+err.Error())
			} else {
				report.PDMReleased = true
				emit("wipe", "info", "PDM allocation released for "+subdomain+"."+poolDomain)
			}
			releaseCancel()
		}
	} else {
		emit("wipe", "info", "BYO or unresolvable pool — no PDM allocation to release")
	}

	// Step 4 — local state cleanup. Three things to clean:
	//   - kubeconfig file (mode 0600 per file)
	//   - tofu workdir (already removed by Destroy on success, but be
	//     defensive in case Destroy returned an error and left it)
	//   - on-disk deployment record JSON
	if h.kubeconfigsDir != "" {
		// Primary kubeconfig.
		kcPath := filepath.Join(h.kubeconfigsDir, id+".yaml")
		if err := os.Remove(kcPath); err != nil && !os.IsNotExist(err) {
			report.Errors = append(report.Errors, "remove kubeconfig: "+err.Error())
		} else if err == nil {
			emit("wipe", "info", "kubeconfig file removed: "+kcPath)
		}
		// Multi-region secondaries — <id>-<region>.yaml glob.
		if entries, derr := os.ReadDir(h.kubeconfigsDir); derr == nil {
			prefix := id + "-"
			for _, e := range entries {
				n := e.Name()
				if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".yaml") {
					p := filepath.Join(h.kubeconfigsDir, n)
					if rerr := os.Remove(p); rerr != nil && !os.IsNotExist(rerr) {
						report.Errors = append(report.Errors, "remove secondary kubeconfig "+n+": "+rerr.Error())
					} else if rerr == nil {
						emit("wipe", "info", "secondary kubeconfig file removed: "+p)
					}
				}
			}
		}
	}

	// Key the workdir by the deployment ID — the provisioner does the same
	// (provisioner.go:workdirKey()), so this matches what was actually
	// created at provision time. FQDN-keyed lookups would miss when two
	// reprovs of the same FQDN existed in sequence.
	tofuWorkDir := filepath.Join(prov.WorkDir, id)
	_ = deploymentSovereignName // retained for backwards-compat callers; unused on the wipe path now
	// Refs #5193 (item 3) — RETAIN the per-deployment tofu workdir (tfvars +
	// terraform.tfstate) unless the wipe is VERIFIED complete. Removing it
	// unconditionally here clobbered the exact state a retried wipe
	// re-destroys against after a PARTIAL destroy (the hw268 shape: region-a
	// tore down but region-b/EIPs/VPCs/OBS survived), leaving the env
	// un-wipeable via the clean tofu path — the retry then fell back to the
	// name-prefix orphan sweep only. provisioner.Destroy already PRESERVES the
	// workdir when `tofu destroy` errors (provisioner.go:2433 "operator may
	// want to inspect") and removes it itself on a clean destroy (2438); this
	// epilogue removal used to negate that preservation. Only reclaim the
	// workdir once BOTH convergence signals agree the wipe converged: tofu
	// destroy returned clean (TofuDestroyed) AND the provider's post-wipe
	// zero-orphan verify passed (VerifiedZeroOrphans). On a partial wipe the
	// workdir is left in place so the next (idempotent — see the file header +
	// the wipeInFlight re-wipe guard) wipe re-destroys against the preserved
	// tfstate and converges; that successful retry's epilogue reclaims it.
	if report.TofuDestroyed && report.VerifiedZeroOrphans {
		if err := os.RemoveAll(tofuWorkDir); err != nil {
			report.Errors = append(report.Errors, "remove tofu workdir: "+err.Error())
		}
	} else {
		emit("wipe", "warn", "tofu workdir RETAINED at "+tofuWorkDir+" — wipe not verified complete (tofuDestroyed="+boolStr(report.TofuDestroyed)+", verifiedZeroOrphans="+boolStr(report.VerifiedZeroOrphans)+"); a retried wipe re-destroys against the preserved tfstate until it converges (Refs #5193)")
	}

	// Refs #3728 — if the PDM pool release failed after retries, RETAIN the
	// on-disk record so the deployment is still loadable after a Pod
	// restart and DELETE /release-subdomain can drive the manual recovery.
	// Deleting it here is what historically stranded the orphan `active`
	// row with no actor left to release it.
	if h.store != nil && !pdmReleaseFailed {
		if err := h.store.Delete(id); err != nil {
			report.Errors = append(report.Errors, "store delete: "+err.Error())
		} else {
			report.LocalCleaned = true
			emit("wipe", "info", "deployment record removed from on-disk store")
		}
	} else if h.store == nil {
		report.LocalCleaned = true
	} else {
		emit("wipe", "info", "on-disk deployment record retained — PDM allocation still active; retry release via DELETE /api/v1/deployments/"+id+"/release-subdomain")
	}

	// Step 5 — finalize. Mark the deployment "wiped" in memory and close
	// the events channel so the SSE stream terminates with a clean
	// boundary. The next GET /events returns the full purge log; any
	// future GET on the deployment id returns 410 Gone (handled in
	// GetDeployment).
	dep.mu.Lock()
	dep.Status = "wiped"
	dep.FinishedAt = time.Now().UTC()
	dep.mu.Unlock()

	// Don't immediately remove from sync.Map — we want StreamLogs
	// reconnects within ~30s to see the final wipe summary frames. A
	// background goroutine reaps after a TTL.
	//
	// Refs #3728 — skip the reap when the PDM release failed: the
	// in-memory deployment (with its pdmPoolDomain/pdmSubdomain pointers)
	// must survive so ReleaseSubdomain can retry the orphaned pool
	// allocation. Reaping it here is what made the manual recovery 404.
	if !pdmReleaseFailed {
		go func() {
			time.Sleep(60 * time.Second)
			h.deployments.Delete(id)
		}()
	}

	emit("wipe", "info", "Wipe complete. Start a fresh deployment from /sovereign.")

	// Close the events channel so SSE consumers get a clean EOF after
	// replaying the purge log.
	//
	// Wave 5.156 (2026-05-27): hw33 wipe #2-of-3 hit `panic: close of
	// closed channel` here at line 661. Root cause: deployments.go:1801
	// runProvisioning's defer closed the same channel concurrently
	// because the Wave 5.135 skipClose guard only matched
	// Status=="wiping" — but wipe.go flips Status to "wiped" at line 643
	// BEFORE this close, so the runProvisioning defer saw "wiped" and
	// closed first. deployments.go:1799 fix extends skipClose to
	// "wiping"||"wiped". This wipe.go fix wraps the close in
	// func+recover for defense-in-depth.
	dep.mu.Lock()
	ch := dep.eventsCh
	dep.eventsCh = nil
	dep.mu.Unlock()
	if ch != nil {
		func() {
			defer func() { _ = recover() }()
			close(ch)
		}()
	}

	// #5193 — persist the final report so a poller (GET
	// /deployments/{id}, or a re-fired wipe checking whether the prior
	// attempt actually finished) sees the same detail the old
	// synchronous 200 response used to return directly in its HTTP
	// body. dep.Error carries a joined summary for the plain-text
	// surfaces (wizard FailureCard, `error` field) that don't unwrap
	// lastWipeReport; empty on a clean wipe.
	dep.mu.Lock()
	dep.lastWipeReport = &report
	if len(report.Errors) > 0 {
		dep.Error = "wipe completed with errors: " + strings.Join(report.Errors, "; ")
	} else {
		dep.Error = ""
	}
	dep.mu.Unlock()
	h.persistDeployment(dep)
}

// deploymentSovereignName mirrors provisioner.Request.sovereignName() —
// dot-to-dash so the workdir lookup matches what Provision/Destroy use.
// Duplicated here (vs exported on the package) to keep that field
// internal to the provisioner; the handler only needs this for cleanup.
func deploymentSovereignName(fqdn string) string {
	return strings.ReplaceAll(fqdn, ".", "-")
}

// releaseSubdomainResponse is the wire shape of DELETE
// /api/v1/deployments/{id}/release-subdomain — a subdomain-only release
// path that does NOT require the HetznerToken (issue #489). The full
// Cancel & Wipe flow remains the canonical purge for live Hetzner
// resources; this endpoint is the narrower fix for the case where a
// failed-or-abandoned deployment locks a pool subdomain that an
// operator wants to retry under the SAME name.
type releaseSubdomainResponse struct {
	DeploymentID string `json:"deploymentId"`
	PoolDomain   string `json:"poolDomain"`
	Subdomain    string `json:"subdomain"`
	PDMReleased  bool   `json:"pdmReleased"`
	NoOp         string `json:"noOp,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ReleaseSubdomain handles DELETE /api/v1/deployments/{id}/release-subdomain.
//
// Issue #489 — each failed provision permanently consumed its subdomain
// because the only release seams were (a) runProvisioning's post-Phase-0
// failure path, dead after a Pod restart, and (b) the Cancel & Wipe flow
// which requires the operator to re-enter the HetznerToken. For franchise
// customers a retry of `acme.omani.works` after a failed `acme.omani.works`
// must NOT need a new subdomain or a HetznerToken roundtrip — the slot
// belongs to the customer, not to the failed attempt.
//
// This endpoint:
//
//   - Looks up the deployment by id.
//   - Refuses to release a deployment that is still in-flight (operator
//     must wait or wipe properly).
//   - Refuses to release a deployment whose AdoptedAt is set — that's a
//     production customer Sovereign, not an abandoned attempt.
//   - Calls PDM Release for the deployment's pdmPoolDomain + pdmSubdomain.
//   - Treats pdm.ErrNotFound as success (idempotent — a second call after
//     PDM has already released the slot is a no-op).
//   - Does NOT touch Hetzner (no destroy, no orphan purge), does NOT
//     delete the on-disk record, does NOT mark the deployment "wiped".
//     The operator can still inspect the failed deployment + run a
//     proper Cancel & Wipe later. The only mutation is on PDM.
//
// Response codes:
//
//	200 — release succeeded (or was a no-op)
//	404 — unknown deployment id
//	409 — deployment is still in-flight; cannot release while running
//	410 — deployment was already wiped
//	422 — deployment has been adopted by a customer; protected
//	502 — PDM call failed (operator can retry)
func (h *Handler) ReleaseSubdomain(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	dep.mu.Lock()
	status := dep.Status
	poolDomain := dep.pdmPoolDomain
	subdomain := dep.pdmSubdomain
	adopted := dep.AdoptedAt != nil
	dep.mu.Unlock()

	// Adopted deployments are customer-owned Sovereigns — never strip a
	// committed pool record from underneath an active customer.
	if adopted {
		writeJSON(w, http.StatusUnprocessableEntity, releaseSubdomainResponse{
			DeploymentID: id,
			PoolDomain:   poolDomain,
			Subdomain:    subdomain,
			Error:        "deployment has been adopted by a customer; the pool record protects the live Sovereign and cannot be released via this endpoint",
		})
		return
	}

	// Refuse to release a deployment that is still in-flight — the
	// runProvisioning goroutine may still call Commit. Operator must
	// wait for terminal state, or run the full Cancel & Wipe flow.
	if isInFlightStatus(status) {
		writeJSON(w, http.StatusConflict, releaseSubdomainResponse{
			DeploymentID: id,
			PoolDomain:   poolDomain,
			Subdomain:    subdomain,
			Error:        "deployment is still in-flight (status=" + status + ") — wait for terminal state or use POST /wipe",
		})
		return
	}
	// Refs #3728 — a "wiped" deployment normally has its PDM pointers
	// cleared (the wipe released the slot), so 410 is correct. BUT when the
	// wipe's pool release FAILED, the record is retained with its pointers
	// intact precisely so this endpoint can finish the release. In that
	// recovery case, fall through to the release instead of 410.
	if status == "wiped" && (poolDomain == "" || subdomain == "") {
		writeJSON(w, http.StatusGone, releaseSubdomainResponse{
			DeploymentID: id,
			PoolDomain:   poolDomain,
			Subdomain:    subdomain,
			Error:        "deployment already wiped",
		})
		return
	}

	// Fallback FQDN split for older records that committed without
	// stamping pdmPoolDomain/pdmSubdomain (the wipe path uses the same
	// fallback; keeping behaviour symmetric).
	if poolDomain == "" || subdomain == "" {
		if idx := strings.IndexByte(dep.Request.SovereignFQDN, '.'); idx > 0 {
			subdomain = dep.Request.SovereignFQDN[:idx]
			poolDomain = dep.Request.SovereignFQDN[idx+1:]
		}
	}

	// BYO deployments don't have a PDM allocation to release. Surface
	// that as a clean 200 no-op so wizard UI flows can call this
	// unconditionally.
	if dep.Request.SovereignDomainMode != "pool" || poolDomain == "" || subdomain == "" {
		writeJSON(w, http.StatusOK, releaseSubdomainResponse{
			DeploymentID: id,
			PoolDomain:   poolDomain,
			Subdomain:    subdomain,
			NoOp:         "no pool allocation to release (BYO or unresolvable pool)",
		})
		return
	}

	if h.pdm == nil {
		writeJSON(w, http.StatusServiceUnavailable, releaseSubdomainResponse{
			DeploymentID: id,
			PoolDomain:   poolDomain,
			Subdomain:    subdomain,
			Error:        "pool-domain-manager client is not configured",
		})
		return
	}

	// Refs #3728 — ReleaseWithRetry on a background context so this manual
	// recovery seam is as resilient as the wipe path (a single transient
	// PDM failure must not strand the operator's retry either).
	releaseCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err := h.pdm.ReleaseWithRetry(releaseCtx, poolDomain, subdomain, pdm.CommitRetryConfig{})
	if err != nil && !errors.Is(err, pdmErrNotFound()) {
		h.log.Warn("release-subdomain: pdm release failed",
			"id", id,
			"poolDomain", poolDomain,
			"subdomain", subdomain,
			"err", err,
		)
		writeJSON(w, http.StatusBadGateway, releaseSubdomainResponse{
			DeploymentID: id,
			PoolDomain:   poolDomain,
			Subdomain:    subdomain,
			Error:        "pdm release failed: " + err.Error(),
		})
		return
	}

	// Clear the cached PDM allocation pointers on the deployment so a
	// follow-up Cancel & Wipe doesn't try to release a slot we just
	// released (idempotent at PDM, but the cleared pointers also keep
	// /events output truthful — "no PDM allocation to release").
	dep.mu.Lock()
	dep.pdmPoolDomain = ""
	dep.pdmSubdomain = ""
	dep.pdmReservationToken = ""
	dep.mu.Unlock()
	h.persistDeployment(dep)

	h.log.Info("release-subdomain: pdm release complete",
		"id", id,
		"poolDomain", poolDomain,
		"subdomain", subdomain,
		"priorStatus", status,
	)

	writeJSON(w, http.StatusOK, releaseSubdomainResponse{
		DeploymentID: id,
		PoolDomain:   poolDomain,
		Subdomain:    subdomain,
		PDMReleased:  true,
	})
}

// buildWipeCredsRaw assembles the provider-specific credential bag
// passed to providers.CloudProvider.Wipe. Resolution order:
//
//  1. wipeRequest body fields (canonical, survive Pod restart — the
//     wizard re-prompts the operator on the Cancel & Wipe modal).
//  2. In-memory dep.Request fallback (still useful when the operator
//     triggers wipe seconds after a successful provision and no Pod
//     restart has happened in between).
//
// Per provider, the key names follow each adapter's documented
// ProviderCreds shape:
//   - hetzner: hcloud_token, hcloud_project_id, object_storage_*
//   - huawei:  access_key, secret_key, project_id, region
func buildWipeCredsRaw(providerName string, body wipeRequest, depReq provisioner.Request) map[string]string {
	out := map[string]string{}
	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case "huawei":
		// Wave 3 + Wave 5.130 (hw30 fix-forward 2026-05-27): legacy
		// `hetznerToken` overload kept for the wizard's existing
		// Cancel & Wipe modal, but PRIMARY path is now the typed
		// huawei{accessKey,secretKey,projectId,region} body fields
		// + a fallback to the in-memory dep.Request fields (set at
		// provision time, survive Pod restart only if the operator
		// re-submitted via the wizard). When all three sources are
		// empty, the wipe.go entry path reads from the per-deployment
		// tofu.auto.tfvars.json on the PVC (see
		// loadHuaweiCredsFromWorkdir). This makes the wipe truly
		// atomic: no need for the operator to re-prompt creds after
		// a Pod restart, because catalyst-api wrote them to disk
		// during provision.
		// #5193 — final fallback to the huawei-operator-creds projected env
		// (CATALYST_HUAWEI_*, api-deployment.yaml). WORST case a partial destroy
		// leaves behind: the per-deployment tofu.auto.tfvars.json is gone, the Pod
		// rolled (no in-memory depReq creds), and the wipe body carries none —
		// without this the bag is empty → the wipe 400s "huawei credentials are
		// required" and the record strands at status=wiping, blocking the
		// one-environment-at-a-time preflight for the next fire. catalyst-api's
		// OWN env still holds the operator creds, so a re-wipe can authenticate.
		// Precedence: body > depReq > operator env.
		out["access_key"] = firstNonEmpty(body.HuaweiAccessKey, body.HetznerToken, depReq.HuaweiAccessKey, os.Getenv("CATALYST_HUAWEI_ACCESS_KEY"))
		out["secret_key"] = firstNonEmpty(body.HuaweiSecretKey, body.ObjectStorageSecretKey, depReq.HuaweiSecretKey, os.Getenv("CATALYST_HUAWEI_SECRET_KEY"))
		out["project_id"] = firstNonEmpty(body.HuaweiProjectID, body.ObjectStorageAccessKey, depReq.HuaweiProjectID, os.Getenv("CATALYST_HUAWEI_PROJECT_ID"))
		out["region"] = firstNonEmpty(body.HuaweiRegion, body.ObjectStorageRegion, depReq.HuaweiRegion, os.Getenv("CATALYST_HUAWEI_REGION"))
		// Region defaults to me-east-215 ONLY when AK/SK/PID all resolved
		// (mirrors janitor.go:818). The truly-empty case stays region="" so the
		// EVS/orphan backstop reads "no creds" instead of scanning me-east-215
		// with a blank AK/SK.
		if out["region"] == "" && out["access_key"] != "" && out["secret_key"] != "" && out["project_id"] != "" {
			out["region"] = "me-east-215"
		}
	default:
		// hetzner (canonical) — same wire shape pre-Wave-3.
		token := strings.TrimSpace(body.HetznerToken)
		out["hcloud_token"] = token
		out["hcloud_project_id"] = strings.TrimSpace(depReq.HetznerProjectID)
		// Object-storage creds: body-supplied wins, in-memory dep.Request fallback.
		access := strings.TrimSpace(body.ObjectStorageAccessKey)
		if access == "" {
			access = depReq.ObjectStorageAccessKey
		}
		secret := strings.TrimSpace(body.ObjectStorageSecretKey)
		if secret == "" {
			secret = depReq.ObjectStorageSecretKey
		}
		region := strings.TrimSpace(body.ObjectStorageRegion)
		if region == "" {
			region = depReq.ObjectStorageRegion
		}
		out["object_storage_access_key"] = access
		out["object_storage_secret_key"] = secret
		out["object_storage_region"] = region
	}
	return out
}

// kindCountSummary returns a stable "kind=N, kind=N, ..." string for
// the SSE wipe-summary banner. Sorted by kind so the banner reads
// consistently across runs.
func kindCountSummary(p map[string][]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	// Stable order via simple insertion sort (avoid sort import bloat).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+itoa(len(p[k])))
	}
	return strings.Join(parts, ", ")
}

// decodeJSONBody is a thin error-wrapping helper for request bodies. Other
// handlers in this package use json.NewDecoder directly; we wrap to
// produce a consistent 400 message.
func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

// huaweiCreds is the typed extraction of Huawei IAM credentials from
// tofu.auto.tfvars.json. Used by the wipe handler's Pod-restart-safe
// fallback (Wave 5.130 hw30 fix-forward 2026-05-27).
type huaweiCreds struct {
	AccessKey string
	SecretKey string
	ProjectID string
	Region    string
}

// loadHuaweiCredsFromTfvars reads tofu.auto.tfvars.json from the given
// per-deployment workdir and returns the Huawei creds quartet. Returns
// (zero, false) on any IO/parse error — the caller falls back to the
// typed-error path that asks the operator to re-prompt via the wizard.
//
// The tfvars file is written by provisioner.writeTfvars during phase 0
// and contains the AK/SK/projectId/region. It persists on the
// catalyst-api-deployments PVC across Pod restarts.
func loadHuaweiCredsFromTfvars(workdir string) (huaweiCreds, bool) {
	path := filepath.Join(workdir, "tofu.auto.tfvars.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return huaweiCreds{}, false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return huaweiCreds{}, false
	}
	get := func(k string) string {
		if v, ok := raw[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	hw := huaweiCreds{
		AccessKey: get("huawei_access_key"),
		SecretKey: get("huawei_secret_key"),
		ProjectID: get("huawei_project_id"),
		Region:    get("huawei_region"),
	}
	if hw.AccessKey == "" || hw.SecretKey == "" {
		return huaweiCreds{}, false
	}
	return hw, true
}

// huaweiOperatorCredsFromEnv resolves the `huawei-operator-creds`
// Kubernetes Secret, projected into the catalyst-api Pod as
// CATALYST_HUAWEI_ACCESS_KEY / _SECRET_KEY / _PROJECT_ID / _REGION —
// the exact same in-cluster fallback discoverHuaweiCreds (janitor.go)
// already uses for the project-wide orphan sweep. Refs #5193: the
// wipe path lacked this fallback, so a wipe whose per-deployment
// tfvars were already destroyed by a partial prior destroy (and whose
// in-memory dep.Request was lost to a Pod roll) had nowhere left to
// turn and 400'd "credentials required" forever. An environment the
// platform created must always be wipeable from in-cluster creds.
//
// Returns (zero, false) when access_key, secret_key, or project_id is
// missing — region alone defaults to "me-east-215" (same default the
// janitor's discoverHuaweiCreds applies) when unset.
func huaweiOperatorCredsFromEnv() (huaweiCreds, bool) {
	hw := huaweiCreds{
		AccessKey: strings.TrimSpace(os.Getenv("CATALYST_HUAWEI_ACCESS_KEY")),
		SecretKey: strings.TrimSpace(os.Getenv("CATALYST_HUAWEI_SECRET_KEY")),
		ProjectID: strings.TrimSpace(os.Getenv("CATALYST_HUAWEI_PROJECT_ID")),
		Region:    strings.TrimSpace(os.Getenv("CATALYST_HUAWEI_REGION")),
	}
	if hw.AccessKey == "" || hw.SecretKey == "" || hw.ProjectID == "" {
		return huaweiCreds{}, false
	}
	if hw.Region == "" {
		hw.Region = "me-east-215"
	}
	return hw, true
}

// tofuWorkDir returns the root tofu workdir path. Defaults to
// /var/lib/catalyst/tofu but honors CATALYST_TOFU_WORKDIR for the
// same env override the provisioner uses.
func (h *Handler) tofuWorkDir() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_TOFU_WORKDIR")); v != "" {
		return v
	}
	return "/var/lib/catalyst/tofu"
}
