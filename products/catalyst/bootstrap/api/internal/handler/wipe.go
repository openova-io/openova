// wipe.go — pre-handover wipe surface (issue #318).
//
// When a provisioning attempt fails (mid-tofu-apply, mid-Phase-1, or just
// because the operator decided to abandon), the wizard's failed-state
// banner exposes a "Cancel & Wipe" button that POSTs here. This handler
// runs the canonical purge sequence:
//
//   1. Cancel any in-flight context (helmwatch informer, current Phase-0
//      runner) for this deployment.
//   2. Run `tofu destroy -auto-approve` against the per-deployment
//      workdir. Idempotent — re-runs on partial state are safe.
//   3. Run a Hetzner force-purge of any resources tagged with
//      `catalyst.openova.io/sovereign=<fqdn>` so anything tofu missed (or
//      anything created out-of-band) is removed. Belt + braces; tofu
//      destroy is the primary path, Hetzner API the safety net.
//   4. Release the PDM allocation row (pool subdomain only). Best-effort:
//      a PDM outage doesn't block local cleanup, the pool-domain-manager
//      operator can force-release later via `pdm-cli` (#319).
//   5. Delete the on-disk record + kubeconfig + tofu workdir.
//   6. Mark the in-memory Deployment Status="wiped" so subsequent GETs
//      return 410 Gone (per the founder's minimum-retention principle —
//      Catalyst-Zero retains nothing operational about a wiped
//      deployment).
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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
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
}

// wipeResponse summarises what was actually purged. The wizard renders
// the counts in a "Wipe complete — N servers, M load balancers, …
// removed" success banner.
//
// NOTE (Wave 2 / Issue #1841): the `HetznerPurge` field name + the
// import of internal/hetzner above remain in this file pending Wave 3
// of the providers/ refactor. The seam (providers.CloudProvider) is
// in place + the credentials + bucket-naming handlers have migrated;
// migrating this orchestration-heavy handler requires either (a)
// moving steps 1+2+2b inside provider.Wipe (changes the wire shape
// because WipeResult.ProviderPurge is a generic map) or (b) splitting
// the provider interface into finer-grained methods. The right path
// is (a) coupled with a UI-side rename of `hetznerPurge` -> `providerPurge`,
// which lockstep-bumps with Huawei's concrete impl in Wave 3.
type wipeResponse struct {
	DeploymentID  string              `json:"deploymentId"`
	SovereignFQDN string              `json:"sovereignFQDN"`
	TofuDestroyed bool                `json:"tofuDestroyed"`
	HetznerPurge  hetzner.PurgeReport `json:"hetznerPurge"`
	PDMReleased   bool                `json:"pdmReleased"`
	LocalCleaned  bool                `json:"localCleaned"`
	Errors        []string            `json:"errors"`
	WipedAt       string              `json:"wipedAt"`
}

// WipeDeployment handles POST /api/v1/deployments/{id}/wipe.
//
// Response codes:
//   - 200 OK on full or partial success (errors in the body)
//   - 400 Bad Request when the body cannot be parsed
//   - 404 Not Found when the deployment id is unknown
//   - 409 Conflict if a wipe is already in progress for this deployment,
//     OR (issue #914) if the deployment is still in `phase1-watching`
//     AND younger than wipeMinLifeProtection (default 30m). The 409
//     body carries `retryAfterSec` + `minLifeSec` so the wizard can
//     render a "wait N minutes" countdown. Operator override:
//     `?force=true` query param.
//   - 500 on a fatal local-state error (workdir non-removable, etc.)
//
// The endpoint is idempotent: re-running on a partially-wiped deployment
// returns the same shape with empty deltas. The wizard treats a 200 with
// non-empty Errors as "investigate the log; some cleanup may be manual".
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
	if strings.TrimSpace(body.HetznerToken) == "" {
		http.Error(w, "hetznerToken is required (re-prompt the operator before calling this endpoint)", http.StatusBadRequest)
		return
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

	// Single-flight guard: if Status is already "wiping", refuse.
	dep.mu.Lock()
	if dep.Status == "wiping" {
		dep.mu.Unlock()
		http.Error(w, "wipe already in progress for this deployment", http.StatusConflict)
		return
	}
	prevStatus := dep.Status
	dep.Status = "wiping"
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

	report := wipeResponse{
		DeploymentID:  id,
		SovereignFQDN: dep.Request.SovereignFQDN,
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
		select {
		case dep.eventsCh <- ev:
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
	if h.k8sCache != nil {
		h.k8sCache.RemoveCluster(id)
		emit("wipe", "info", "k8scache informer set removed for "+id)
	}

	// Step 1 — tofu destroy. Pass the freshly-prompted Hetzner token via
	// the Request so writeTfvars renders it for the destroy run.
	wipeReq := dep.Request
	wipeReq.HetznerToken = body.HetznerToken

	prov := provisioner.New()
	tofuCtx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	if err := prov.Destroy(tofuCtx, wipeReq, dep.eventsCh); err != nil {
		report.Errors = append(report.Errors, "tofu destroy: "+err.Error())
		emit("wipe", "warn", "tofu destroy did not complete cleanly: "+err.Error()+" — falling back to direct Hetzner orphan purge")
	} else {
		report.TofuDestroyed = true
		emit("wipe", "info", "tofu destroy complete")
	}

	// Step 2 — Hetzner orphan purge (always runs as belt-and-braces, even
	// when tofu destroy succeeded — catches resources tofu didn't track,
	// e.g. a half-failed cloud-init that created a worker manually, or
	// resources the operator created in the same project for testing).
	purge, err := hetzner.Purge(tofuCtx, body.HetznerToken, dep.Request.SovereignFQDN, func(msg string) {
		emit("wipe", "info", "hetzner: "+msg)
	})
	report.HetznerPurge = purge
	if err != nil {
		report.Errors = append(report.Errors, "hetzner purge: "+err.Error())
	}

	// Step 2b — Hetzner Object Storage bucket purge (issue #706). tofu
	// destroy does NOT remove `minio_s3_bucket` resources because the
	// minio provider's force_destroy semantics intentionally fail when
	// the bucket holds objects. Run AFTER tofu destroy so we don't fight
	// tofu state, BEFORE the local-record cleanup so the UI banner can
	// surface the count + any error.
	//
	// Credential resolution order (issue #166):
	//
	//   1. Wipe REQUEST body (canonical, survives Pod restart) — this
	//      mirrors the existing HetznerToken-in-body pattern at the top
	//      of this handler. The wizard re-prompts the operator for the
	//      same triplet it captured at provision time and POSTs it on
	//      the Cancel & Wipe modal submit.
	//   2. In-memory dep.Request (the wizard captured them at provision
	//      time and they live there until the Pod restarts; on-disk
	//      records redact them per credential-hygiene principle). Kept
	//      as a fallback so wipes triggered immediately after a
	//      successful provision — without a re-prompt — still work.
	//
	// When BOTH are empty (Pod restarted AND wizard didn't re-prompt),
	// surface a HARD error in the response rather than silently leaving
	// the bucket behind. Bucket orphans cost real money + count against
	// the Hetzner project's S3-bucket quota; the pre-#166 silent-skip
	// leaked 10 buckets on omantel.biz alone (catalyst-omantel-biz-*,
	// one per wiped provision back to prov #11, manually purged via
	// boto3 with the same creds — confirming the creds work, the
	// handler just lacked them).
	//
	// SSE log hygiene (principle 19): we emit a structural notice that
	// the bucket-purge step ran with body-supplied vs in-memory creds,
	// but NEVER the access/secret key values themselves. Credentials
	// stay in transit-encrypted POST body → in-process variables →
	// Hetzner S3 SDK, never on the events channel or in logs.
	//
	// TODO(#166-followup): consider Option B (per-deployment K8s Secret
	// holding S3 creds, reaped on wipe) if a future security review
	// objects to the operator re-prompt model. Option A is shipped
	// today because it matches the canonical HetznerToken pattern and
	// survives Pod restarts with zero extra storage.
	accessKey := strings.TrimSpace(body.ObjectStorageAccessKey)
	secretKey := strings.TrimSpace(body.ObjectStorageSecretKey)
	region := strings.TrimSpace(body.ObjectStorageRegion)
	credsSource := "request-body"
	if accessKey == "" || secretKey == "" || region == "" {
		// Fall back to in-memory dep.Request — still useful when the
		// operator triggers wipe seconds after a successful provision
		// (no Pod restart in between).
		if accessKey == "" {
			accessKey = dep.Request.ObjectStorageAccessKey
		}
		if secretKey == "" {
			secretKey = dep.Request.ObjectStorageSecretKey
		}
		if region == "" {
			region = dep.Request.ObjectStorageRegion
		}
		credsSource = "in-memory-request-record"
	}
	if accessKey != "" && secretKey != "" && region != "" {
		emit("wipe", "info", "object-storage: bucket purge starting (creds source: "+credsSource+")")
		bucketCtx, bucketCancel := context.WithTimeout(r.Context(), 5*time.Minute)
		removed, berr := hetzner.PurgeBuckets(bucketCtx,
			hetzner.PurgeBucketsConfig{
				AccessKey: accessKey,
				SecretKey: secretKey,
				Region:    region,
			},
			dep.Request.SovereignFQDN,
			// Per Fix #111: deploymentID feeds the bucket-name suffix so
			// PurgeBuckets targets the same global-namespace bucket that
			// CreateDeployment provisioned. dep.ID (URL path lookup key)
			// IS the deployment ID; we prefer it over Request.DeploymentID
			// because the on-disk record's stamping flow lands the value
			// on req only after newID() runs in CreateDeployment, and
			// dep.ID is always present whether the record came from the
			// in-memory map or store.Load on Pod restart.
			dep.ID,
			func(msg string) { emit("wipe", "info", "object-storage: "+msg) },
		)
		bucketCancel()
		// Issue #153 — PurgeBuckets is now prefix-match: every bucket
		// matching `catalyst-<fqdn-slug>` (legacy) or
		// `catalyst-<fqdn-slug>-*` (Fix #111+ deployment-id-suffixed)
		// gets emptied + deleted. Pre-fix this only purged the ONE
		// bucket whose suffix matched the CURRENT deployment-id,
		// leaving every prior provision's bucket orphaned (4+ stale
		// catalyst-omantel-biz-* buckets observed live).
		//
		// Log the prefix actually swept so an operator inspecting the
		// SSE log can correlate with the `removed` count without
		// re-deriving the deterministic name. The S3Buckets slice gets
		// one entry per removed bucket (name not surfaced from the
		// purge layer; the SSE log already carries per-bucket detail).
		bucketPrefix := hetzner.BucketNamePrefixForSovereign(dep.Request.SovereignFQDN)
		for i := 0; i < removed; i++ {
			report.HetznerPurge.S3Buckets = append(report.HetznerPurge.S3Buckets, bucketPrefix+"-*")
		}
		if berr != nil {
			report.HetznerPurge.Errors = append(report.HetznerPurge.Errors,
				"object-storage bucket purge: "+berr.Error())
			report.Errors = append(report.Errors, "object-storage bucket purge: "+berr.Error())
		}
	} else {
		// Both sources empty. Surface as a hard error in the response
		// (NOT a silent warn) so the wizard banner forces the operator
		// to re-prompt + retry, instead of pretending the wipe is
		// complete while a bucket leaks. Issue #166 root cause was
		// exactly this silent-skip path.
		msg := "object-storage credentials not supplied on request body AND not retained on in-memory deployment record (Pod likely restarted) — bucket purge SKIPPED; re-issue the wipe with objectStorageAccessKey/SecretKey/Region in the body, or run the manual sweep from docs/feedback_idempotent_iac_purge.md"
		emit("wipe", "warn", msg)
		report.Errors = append(report.Errors, msg)
	}

	if report.HetznerPurge.Total() > 0 {
		emit("wipe", "info", "Hetzner orphan purge removed "+itoa(report.HetznerPurge.Total())+" resource(s) (servers: "+itoa(len(report.HetznerPurge.Servers))+", lbs: "+itoa(len(report.HetznerPurge.LoadBalancers))+", networks: "+itoa(len(report.HetznerPurge.Networks))+", firewalls: "+itoa(len(report.HetznerPurge.Firewalls))+", ssh-keys: "+itoa(len(report.HetznerPurge.SSHKeys))+", s3-buckets: "+itoa(len(report.HetznerPurge.S3Buckets))+")")
	} else if len(report.HetznerPurge.Errors) == 0 {
		emit("wipe", "info", "Hetzner orphan purge: nothing to remove (clean account)")
	}

	// Step 3 — PDM release (pool-subdomain only). Best-effort. Resolve pool
	// + subdomain from either the Deployment record (set during the
	// reservation step) or from the FQDN as a fallback.
	poolDomain, subdomain := dep.pdmPoolDomain, dep.pdmSubdomain
	if poolDomain == "" || subdomain == "" {
		// Fallback: split sovereignFQDN at the first dot.
		if idx := strings.IndexByte(dep.Request.SovereignFQDN, '.'); idx > 0 {
			subdomain = dep.Request.SovereignFQDN[:idx]
			poolDomain = dep.Request.SovereignFQDN[idx+1:]
		}
	}
	if dep.Request.SovereignDomainMode == "pool" && poolDomain != "" && subdomain != "" {
		releaseCtx, releaseCancel := context.WithTimeout(r.Context(), 30*time.Second)
		if err := h.pdm.Release(releaseCtx, poolDomain, subdomain); err != nil {
			report.Errors = append(report.Errors, "pdm release: "+err.Error())
			emit("wipe", "warn", "PDM release failed (operator must run pdm-cli force-release later): "+err.Error())
		} else {
			report.PDMReleased = true
			emit("wipe", "info", "PDM allocation released for "+subdomain+"."+poolDomain)
		}
		releaseCancel()
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
	if err := os.RemoveAll(tofuWorkDir); err != nil {
		report.Errors = append(report.Errors, "remove tofu workdir: "+err.Error())
	}

	if h.store != nil {
		if err := h.store.Delete(id); err != nil {
			report.Errors = append(report.Errors, "store delete: "+err.Error())
		} else {
			report.LocalCleaned = true
			emit("wipe", "info", "deployment record removed from on-disk store")
		}
	} else {
		report.LocalCleaned = true
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
	go func() {
		time.Sleep(60 * time.Second)
		h.deployments.Delete(id)
	}()

	emit("wipe", "info", "Wipe complete. Start a fresh deployment from /sovereign.")

	// Close the events channel so SSE consumers get a clean EOF after
	// replaying the purge log.
	dep.mu.Lock()
	if dep.eventsCh != nil {
		close(dep.eventsCh)
		dep.eventsCh = nil
	}
	dep.mu.Unlock()

	writeJSON(w, http.StatusOK, report)
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
	if status == "wiped" {
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

	releaseCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := h.pdm.Release(releaseCtx, poolDomain, subdomain)
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
